package capsule

import (
	"errors"
	"fmt"

	"github.com/cyr1en/ref0/internal/modelbudget"
)

type OpenAIAttempt struct {
	State AttemptState
	Usage Usage

	modelID          string
	samplingOptions  map[string]any
	reasoningOptions map[string]any
	contextWindow    int
	maxOutputTokens  int
	maxModelRequests int
	responseLimits   ResponseLimits
	messages         []map[string]any
	tools            []Tool
	outputSchema     map[string]any
	validators       map[string]*compiledSchema
	outputValidator  *compiledSchema
	pending          *PendingToolCall
	nextTurn         int
	currentTurn      int
}

func NewOpenAIAttempt(modelID, systemPrompt, prompt string, tools []Tool, outputSchema map[string]any,
	samplingOptions, reasoningOptions map[string]any, contextWindow, maxOutputTokens, maxModelRequests int, limits ResponseLimits,
) (*OpenAIAttempt, error) {
	if contextWindow <= 0 || maxOutputTokens <= 0 || maxModelRequests < 1 || maxModelRequests > 1_000 {
		return nil, errors.New("model request budget is invalid")
	}
	sampling, err := validateSamplingOptions(samplingOptions)
	if err != nil {
		return nil, err
	}
	reasoning, err := normalizeJSONObject(reasoningOptions, limits.MaxBytes, complexityLimits{limits.MaxDepth, limits.MaxStringBytes, limits.MaxKeys})
	if err != nil || len(reasoning) > 1 {
		return nil, errors.New("provider reasoning options are invalid")
	}
	for name := range reasoning {
		if name == "" {
			return nil, errors.New("provider reasoning options are invalid")
		}
		if _, denied := samplingNames[name]; denied {
			return nil, errors.New("provider reasoning options are invalid")
		}
		if _, denied := hostRequestNames[name]; denied {
			return nil, errors.New("provider reasoning options are invalid")
		}
	}
	outputValidator, err := compileSchema(outputSchema)
	if err != nil {
		return nil, err
	}
	validators := make(map[string]*compiledSchema, len(tools)+1)
	seen := map[string]struct{}{}
	clonedTools := make([]Tool, len(tools))
	for index, tool := range tools {
		if !validToolName(tool.Name) || tool.Name == "submit_result" || tool.Handler == nil {
			return nil, errors.New("capsule granted tool names are invalid")
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return nil, errors.New("capsule granted tool names are invalid")
		}
		seen[tool.Name] = struct{}{}
		validator, err := compileSchema(tool.Parameters)
		if err != nil {
			return nil, err
		}
		cloned, err := normalizeJSONObject(tool.Parameters, limits.MaxBytes, complexityLimits{limits.MaxDepth, limits.MaxStringBytes, limits.MaxKeys})
		if err != nil {
			return nil, err
		}
		tool.Parameters = cloned
		clonedTools[index] = tool
		validators[tool.Name] = validator
	}
	output, err := normalizeJSONObject(outputSchema, limits.MaxBytes, complexityLimits{limits.MaxDepth, limits.MaxStringBytes, limits.MaxKeys})
	if err != nil {
		return nil, err
	}
	validators["submit_result"] = outputValidator
	return &OpenAIAttempt{
		State: ReadyModel, modelID: modelID, samplingOptions: sampling, reasoningOptions: reasoning,
		contextWindow: contextWindow, maxOutputTokens: maxOutputTokens, maxModelRequests: maxModelRequests, responseLimits: limits,
		messages: []map[string]any{{"role": "system", "content": systemPrompt}, {"role": "user", "content": prompt}},
		tools:    clonedTools, outputSchema: output, validators: validators, outputValidator: outputValidator, nextTurn: 1,
	}, nil
}

func (attempt *OpenAIAttempt) BeginModel(turn int) ([]byte, error) {
	if turn > attempt.maxModelRequests {
		return nil, errors.New("model request exceeds configured budget")
	}
	if attempt.State != ReadyModel || turn != attempt.nextTurn {
		return nil, errors.New("model request is out of order")
	}
	attempt.State, attempt.currentTurn, attempt.nextTurn = ModelInFlight, turn, turn+1
	encoded, err := attempt.modelRequest(turn, attempt.messages)
	if err != nil || len(encoded) > attempt.responseLimits.MaxBytes ||
		!modelbudget.Fits(encoded, attempt.contextWindow, attempt.maxOutputTokens, modelbudget.DefaultSafetyTokens) {
		return nil, errors.New("provider request body exceeds model context")
	}
	return encoded, nil
}

func (attempt *OpenAIAttempt) modelRequest(turn int, transcript []map[string]any) ([]byte, error) {
	remaining := attempt.maxModelRequests - turn + 1
	messages := make([]any, 0, len(transcript)+1)
	for _, message := range transcript {
		messages = append(messages, message)
	}
	messages = append(messages, map[string]any{
		"role": "user",
		"content": fmt.Sprintf(
			"Host model-request budget: %d request(s), including this one, remain. Inspect only what is essential and leave enough time to call submit_result with one complete valid result. On the final request, call submit_result and do not request a source tool.",
			remaining,
		),
	})
	tools := make([]any, 0, len(attempt.tools)+1)
	for _, tool := range attempt.tools {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters, "strict": true,
			},
		})
	}
	tools = append(tools, map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "submit_result", "description": "Validate and submit the final structured result.",
			"parameters": attempt.outputSchema, "strict": true,
		},
	})
	toolChoice := any("required")
	if turn == attempt.maxModelRequests {
		toolChoice = map[string]any{"type": "function", "function": map[string]any{"name": "submit_result"}}
	}
	body := map[string]any{
		"model": attempt.modelID, "messages": messages, "stream": true,
		"stream_options": map[string]any{"include_usage": true}, "n": 1,
		"tools": tools, "tool_choice": toolChoice, "parallel_tool_calls": false,
		"max_tokens": attempt.maxOutputTokens,
	}
	for name, value := range attempt.samplingOptions {
		body[name] = value
	}
	for name, value := range attempt.reasoningOptions {
		body[name] = value
	}
	return canonicalJSON(body)
}

func (attempt *OpenAIAttempt) AcceptModel(body []byte) ([]byte, error) {
	if attempt.State != ModelInFlight {
		return nil, errors.New("provider response is out of turn")
	}
	accepted, err := ParseOpenAISSE(body, attempt.modelID, attempt.validators, attempt.responseLimits)
	if err != nil {
		return nil, err
	}
	attempt.Usage = attempt.Usage.Add(accepted.Usage)
	if accepted.Pending == nil {
		return nil, errors.New("provider response omitted the required tool call")
	}
	if attempt.currentTurn == attempt.maxModelRequests && accepted.Pending.Name != "submit_result" {
		return nil, errors.New("provider final model turn did not submit result")
	}
	attempt.messages = append(attempt.messages, accepted.AssistantMessage)
	attempt.pending = accepted.Pending
	if accepted.Pending.Name == "submit_result" {
		attempt.State = AwaitComplete
	} else {
		attempt.State = AwaitTool
	}
	return accepted.NormalizedSSE, nil
}

func (attempt *OpenAIAttempt) ValidateToolCall(providerCallID, name any, arguments any) (*PendingToolCall, error) {
	if attempt.State != AwaitTool || attempt.pending == nil || providerCallID != attempt.pending.CallID ||
		name != attempt.pending.Name || !sameJSON(arguments, attempt.pending.Arguments) {
		return nil, errors.New("capsule tool call does not match provider call")
	}
	return attempt.pending, nil
}

func (attempt *OpenAIAttempt) AcceptToolResult(result any) (string, any, error) {
	if attempt.State != AwaitTool || attempt.pending == nil {
		return "", nil, errors.New("tool result is out of turn")
	}
	cloned, err := cloneJSON(result, complexityLimits{attempt.responseLimits.MaxDepth, attempt.responseLimits.MaxStringBytes, attempt.responseLimits.MaxKeys})
	if err != nil {
		return "", nil, err
	}
	encoded, err := canonicalJSON(cloned)
	if err != nil {
		return "", nil, err
	}
	message := map[string]any{
		"role": "tool", "tool_call_id": attempt.pending.CallID, "content": string(encoded),
	}
	candidate := append(append([]map[string]any(nil), attempt.messages...), message)
	if !attempt.transcriptFits(candidate) {
		bounded, truncateErr := modelbudget.TruncateResult(encoded, func(value map[string]any) bool {
			content, marshalErr := canonicalJSON(value)
			if marshalErr != nil {
				return false
			}
			candidate[len(candidate)-1] = map[string]any{
				"role": "tool", "tool_call_id": attempt.pending.CallID, "content": string(content),
			}
			return attempt.transcriptFits(candidate)
		})
		if truncateErr != nil {
			return "", nil, truncateErr
		}
		cloned = bounded
		encoded, _ = canonicalJSON(bounded)
		candidate[len(candidate)-1] = map[string]any{
			"role": "tool", "tool_call_id": attempt.pending.CallID, "content": string(encoded),
		}
		attempt.Usage.TruncatedToolResults++
	}
	attempt.messages = candidate
	attempt.pending, attempt.State = nil, ReadyModel
	return string(encoded), cloned, nil
}

func (attempt *OpenAIAttempt) transcriptFits(messages []map[string]any) bool {
	encoded, err := attempt.modelRequest(attempt.nextTurn, messages)
	return err == nil && len(encoded) <= attempt.responseLimits.MaxBytes &&
		modelbudget.Fits(encoded, attempt.contextWindow, attempt.maxOutputTokens, modelbudget.DefaultSafetyTokens)
}

func (attempt *OpenAIAttempt) AcceptComplete(output any) (map[string]any, error) {
	object, ok := output.(map[string]any)
	if attempt.State != AwaitComplete || attempt.pending == nil || attempt.pending.Name != "submit_result" ||
		!ok || !sameJSON(object, attempt.pending.Arguments) || !attempt.outputValidator.valid(object) {
		return nil, errors.New("capsule completion does not match provider submission")
	}
	result := attempt.pending.Arguments
	attempt.pending, attempt.State = nil, Terminal
	return result, nil
}

func (attempt *OpenAIAttempt) Fail(cancelled bool) {
	if cancelled {
		attempt.State = Cancelled
	} else {
		attempt.State = Failed
	}
}

func (attempt *OpenAIAttempt) Messages() []map[string]any {
	result := make([]map[string]any, 0, len(attempt.messages))
	for _, message := range attempt.messages {
		cloned, _ := cloneJSON(message, complexityLimits{attempt.responseLimits.MaxDepth, attempt.responseLimits.MaxStringBytes, attempt.responseLimits.MaxKeys})
		result = append(result, cloned.(map[string]any))
	}
	return result
}

func validToolName(value string) bool {
	return value != "" && len([]byte(value)) <= 128 && validOperationID(value)
}
