package capsule

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

type ResponseLimits struct {
	MaxBytes, MaxEvents, MaxLineBytes int
	MaxDepth, MaxStringBytes, MaxKeys int
}

type PendingToolCall struct {
	CallID    string
	Name      string
	Arguments map[string]any
}

type AcceptedResponse struct {
	NormalizedSSE    []byte
	AssistantMessage map[string]any
	Pending          *PendingToolCall
	Usage            Usage
}

func ParseOpenAISSE(body []byte, modelID string, validators map[string]*compiledSchema, limits ResponseLimits) (AcceptedResponse, error) {
	if len(body) == 0 || len(body) > limits.MaxBytes {
		return AcceptedResponse{}, errors.New("provider SSE size is invalid")
	}
	if !bytes.Equal(bytes.ToValidUTF8(body, []byte("\x00")), body) {
		return AcceptedResponse{}, errors.New("provider SSE is not UTF-8")
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r\n", "\n"), "\r", "\n")
	if !strings.HasSuffix(text, "\n\n") {
		return AcceptedResponse{}, errors.New("provider SSE is truncated")
	}
	rawEvents := strings.Split(strings.TrimSuffix(text, "\n\n"), "\n\n")
	events := make([]string, 0, len(rawEvents))
	for _, event := range rawEvents {
		if event != "" {
			events = append(events, event)
		}
	}
	if len(events) < 1 || len(events) > limits.MaxEvents {
		return AcceptedResponse{}, errors.New("provider SSE event count is invalid")
	}

	var content, reasoning, argumentParts []string
	var callID, callName string
	sawTool := false
	var finishReason *string
	var usage *Usage
	done := false
	totalKeys := 0
	complexity := complexityLimits{limits.MaxDepth, limits.MaxStringBytes, limits.MaxKeys}
	for eventIndex, event := range events {
		lines := strings.Split(event, "\n")
		for _, line := range lines {
			if len([]byte(line)) > limits.MaxLineBytes {
				return AcceptedResponse{}, errors.New("provider SSE line exceeds limit")
			}
		}
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "data:") {
			return AcceptedResponse{}, errors.New("provider SSE field is invalid")
		}
		data := strings.TrimPrefix(lines[0], "data:")
		data = strings.TrimPrefix(data, " ")
		if data == "[DONE]" {
			if done || eventIndex != len(events)-1 || finishReason == nil || usage == nil {
				return AcceptedResponse{}, errors.New("provider SSE terminator is invalid")
			}
			done = true
			continue
		}
		if done {
			return AcceptedResponse{}, errors.New("provider SSE has trailing data")
		}
		decoded, keys, err := parseStrictJSON([]byte(data), complexity)
		if err != nil {
			return AcceptedResponse{}, errors.New("provider SSE JSON is invalid")
		}
		totalKeys += keys
		if totalKeys > limits.MaxKeys {
			return AcceptedResponse{}, errors.New("provider SSE key count exceeds limit")
		}
		chunk, err := closedObject(decoded, set("id", "object", "created", "model", "choices", "usage", "system_fingerprint", "service_tier"), "chunk")
		if err != nil {
			return AcceptedResponse{}, err
		}
		if model, exists := chunk["model"]; exists && model != modelID {
			return AcceptedResponse{}, errors.New("provider response model does not match binding")
		}
		if object, exists := chunk["object"]; exists && object != "chat.completion.chunk" {
			return AcceptedResponse{}, errors.New("provider response object is invalid")
		}
		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) > 1 {
			return AcceptedResponse{}, errors.New("provider choices are invalid")
		}
		if rawUsage, exists := chunk["usage"]; exists {
			if usage != nil {
				return AcceptedResponse{}, errors.New("provider usage is duplicated")
			}
			parsed, err := parseUsage(rawUsage)
			if err != nil {
				return AcceptedResponse{}, err
			}
			usage = &parsed
		}
		if len(choices) == 0 {
			if _, exists := chunk["usage"]; !exists || finishReason == nil {
				return AcceptedResponse{}, errors.New("provider empty choice event is invalid")
			}
			continue
		}
		if finishReason != nil {
			choice, err := closedObject(choices[0], set("index", "delta", "finish_reason", "logprobs"), "choice")
			if err != nil {
				return AcceptedResponse{}, err
			}
			delta, err := closedObject(choice["delta"], set("role", "content", "tool_calls"), "delta")
			index, indexOK := jsonInteger(choice["index"])
			if err != nil || indexOK == false || index != 0 || len(delta) != 0 || choice["finish_reason"] != nil || choice["logprobs"] != nil {
				return AcceptedResponse{}, errors.New("provider emitted choices after finish")
			}
			if _, exists := chunk["usage"]; !exists {
				return AcceptedResponse{}, errors.New("provider emitted choices after finish")
			}
			continue
		}
		choice, err := closedObject(choices[0], set("index", "delta", "finish_reason", "logprobs"), "choice")
		if err != nil {
			return AcceptedResponse{}, err
		}
		index, ok := jsonInteger(choice["index"])
		if !ok || index != 0 || choice["logprobs"] != nil {
			return AcceptedResponse{}, errors.New("provider choice index or logprobs is invalid")
		}
		delta, err := closedObject(choice["delta"], set("role", "content", "reasoning_content", "tool_calls"), "delta")
		if err != nil {
			return AcceptedResponse{}, err
		}
		if role, exists := delta["role"]; exists && role != "assistant" {
			return AcceptedResponse{}, errors.New("provider response role is invalid")
		}
		if raw, exists := delta["reasoning_content"]; exists && raw != nil {
			value, ok := raw.(string)
			if !ok {
				return AcceptedResponse{}, errors.New("provider reasoning content is invalid")
			}
			if value != "" {
				reasoning = append(reasoning, value)
			}
		}
		if raw, exists := delta["content"]; exists && raw != nil {
			value, ok := raw.(string)
			if !ok {
				return AcceptedResponse{}, errors.New("provider assistant content is invalid")
			}
			if sawTool && value != "" {
				return AcceptedResponse{}, errors.New("provider mixed content and tool call")
			}
			content = append(content, value)
		}
		if raw, exists := delta["tool_calls"]; exists {
			sawTool = true
			calls, ok := raw.([]any)
			if !ok || len(calls) != 1 || anyNonempty(content) {
				return AcceptedResponse{}, errors.New("provider tool calls are invalid")
			}
			call, err := closedObject(calls[0], set("index", "id", "type", "function"), "tool call")
			if err != nil {
				return AcceptedResponse{}, err
			}
			callIndex, ok := jsonInteger(call["index"])
			if !ok || callIndex != 0 || call["type"] != nil && call["type"] != "function" {
				return AcceptedResponse{}, errors.New("provider tool call is invalid")
			}
			if rawID, exists := call["id"]; exists {
				candidate, ok := rawID.(string)
				if !ok || !validOperationID(candidate) || callID != "" && candidate != callID {
					return AcceptedResponse{}, errors.New("provider tool-call ID is invalid")
				}
				callID = candidate
			}
			function, err := closedObject(call["function"], set("name", "arguments"), "tool function")
			if err != nil {
				return AcceptedResponse{}, err
			}
			if rawName, exists := function["name"]; exists {
				candidate, ok := rawName.(string)
				if !ok || callName != "" && candidate != callName {
					return AcceptedResponse{}, errors.New("provider tool name is invalid")
				}
				callName = candidate
			}
			if rawArguments, exists := function["arguments"]; exists {
				part, ok := rawArguments.(string)
				if !ok {
					return AcceptedResponse{}, errors.New("provider tool arguments are invalid")
				}
				argumentParts = append(argumentParts, part)
			}
		}
		if rawFinish, exists := choice["finish_reason"]; exists && rawFinish != nil {
			value, ok := rawFinish.(string)
			if !ok || value != "stop" && value != "tool_calls" {
				return AcceptedResponse{}, errors.New("provider finish reason is invalid")
			}
			finishReason = &value
		}
	}
	if !done || usage == nil || finishReason == nil {
		return AcceptedResponse{}, errors.New("provider SSE is incomplete")
	}

	var pending *PendingToolCall
	var assistant, delta map[string]any
	if sawTool {
		if *finishReason != "tool_calls" || callName == "" || len(argumentParts) == 0 {
			return AcceptedResponse{}, errors.New("provider tool call is incomplete")
		}
		validator := validators[callName]
		if validator == nil {
			return AcceptedResponse{}, errors.New("provider requested an ungranted tool")
		}
		parsed, _, err := parseStrictJSON([]byte(strings.Join(argumentParts, "")), complexity)
		arguments, ok := parsed.(map[string]any)
		if err != nil || !ok || !validator.valid(arguments) {
			return AcceptedResponse{}, errors.New("provider tool arguments failed schema validation")
		}
		canonical, _ := canonicalJSON(arguments)
		pending = &PendingToolCall{CallID: callID, Name: callName, Arguments: arguments}
		toolCall := map[string]any{
			"id": callID, "type": "function",
			"function": map[string]any{"name": callName, "arguments": string(canonical)},
		}
		assistant = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{toolCall}}
		delta = map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"index": 0, "id": callID, "type": "function",
				"function": map[string]any{"name": callName, "arguments": string(canonical)},
			}},
		}
	} else {
		if *finishReason != "stop" {
			return AcceptedResponse{}, errors.New("provider finish reason does not match content")
		}
		joined := strings.Join(content, "")
		assistant = map[string]any{"role": "assistant", "content": joined}
		delta = map[string]any{"role": "assistant", "content": joined}
	}
	if joined := strings.Join(reasoning, ""); joined != "" {
		assistant["reasoning_content"] = joined
		delta["reasoning_content"] = joined
	}
	normalizedChunks := []map[string]any{
		{
			"id": "host-normalized", "object": "chat.completion.chunk", "model": modelID,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
		},
		{
			"id": "host-normalized", "object": "chat.completion.chunk", "model": modelID,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": *finishReason}},
		},
		{
			"id": "host-normalized", "object": "chat.completion.chunk", "model": modelID,
			"choices": []any{},
			"usage": map[string]any{
				"prompt_tokens": usage.InputTokens, "completion_tokens": usage.OutputTokens, "total_tokens": usage.TotalTokens,
			},
		},
	}
	var normalized bytes.Buffer
	for _, chunk := range normalizedChunks {
		encoded, err := canonicalJSON(chunk)
		if err != nil {
			return AcceptedResponse{}, err
		}
		normalized.WriteString("data: ")
		normalized.Write(encoded)
		normalized.WriteString("\n\n")
	}
	normalized.WriteString("data: [DONE]\n\n")
	if normalized.Len() > limits.MaxBytes {
		return AcceptedResponse{}, errors.New("normalized provider SSE exceeds limit")
	}
	return AcceptedResponse{NormalizedSSE: normalized.Bytes(), AssistantMessage: assistant, Pending: pending, Usage: *usage}, nil
}

func parseUsage(value any) (Usage, error) {
	object, err := closedObject(value, set("prompt_tokens", "completion_tokens", "completion_tokens_details", "total_tokens"), "usage")
	if err != nil {
		return Usage{}, err
	}
	if raw, exists := object["completion_tokens_details"]; exists {
		details, err := closedObject(raw, set("accepted_prediction_tokens", "audio_tokens", "reasoning_tokens", "rejected_prediction_tokens"), "completion token details")
		if err != nil {
			return Usage{}, err
		}
		for name, value := range details {
			if value != nil {
				if _, err := numberInt(value, strings.ReplaceAll(name, "_", " ")); err != nil {
					return Usage{}, err
				}
			}
		}
	}
	input, err := numberInt(object["prompt_tokens"], "input tokens")
	if err != nil {
		return Usage{}, err
	}
	output, err := numberInt(object["completion_tokens"], "output tokens")
	if err != nil {
		return Usage{}, err
	}
	total, err := numberInt(object["total_tokens"], "total tokens")
	if err != nil {
		return Usage{}, err
	}
	if total != input+output {
		return Usage{}, errors.New("provider usage totals are incoherent")
	}
	return Usage{ModelCalls: 1, InputTokens: input, OutputTokens: output, TotalTokens: total}, nil
}

func closedObject(value any, allowed map[string]struct{}, label string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("provider %s is invalid", label)
	}
	for name := range object {
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("provider %s is invalid", label)
		}
	}
	return object, nil
}

func set(names ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}

func anyNonempty(values []string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}
