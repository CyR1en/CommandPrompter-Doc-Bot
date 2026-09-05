package agents

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/modelbudget"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/safenet"
	"github.com/cyr1en/ref0/internal/security"
)

var (
	ErrModelProvider   = errors.New("agent model provider failed")
	ErrModelRateLimit  = errors.New("agent model provider rate limited the request")
	ErrModelTimeout    = errors.New("agent model provider timed out")
	ErrModelValidation = errors.New("agent model response is invalid")
)

var agentCitationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]{0,511}$`)
var agentJSONFencePattern = regexp.MustCompile("(?is)^```(?:json)?[ \\t]*\\r?\\n(.*?)\\r?\\n?```[ \\t]*$")

type ModelSecretReader interface {
	Read(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error)
}

type ModelNetworkClient interface {
	PostJSON(context.Context, string, any) (any, int, error)
	CloseIdleConnections()
}

type ModelClientFactory func(providers.Configuration, map[string]string, time.Duration) (ModelNetworkClient, error)

type OpenAIModelOptions struct {
	Resolver  safenet.Resolver
	TLSConfig *tls.Config
}

type OpenAIModel struct {
	secrets ModelSecretReader
	client  ModelClientFactory
	admit   func(context.Context, providers.ProfileID, time.Duration) (func(), error)
}

func NewOpenAIModel(secrets ModelSecretReader, options OpenAIModelOptions) (*OpenAIModel, error) {
	if secrets == nil {
		return nil, errors.New("agent model secret reader is required")
	}
	factory := func(configuration providers.Configuration, headers map[string]string, timeout time.Duration) (ModelNetworkClient, error) {
		return safenet.NewClient(configuration.BaseURL, safenet.Policy{
			AllowPrivateAddresses: configuration.AllowPrivateNetwork,
			AllowPlainHTTP:        configuration.AllowHTTP,
		}, safenet.ClientOptions{Headers: headers, Resolver: options.Resolver, TLSConfig: options.TLSConfig, Timeout: timeout})
	}
	return &OpenAIModel{secrets: secrets, client: factory}, nil
}

func NewOpenAIModelWithFactory(secrets ModelSecretReader, factory ModelClientFactory) (*OpenAIModel, error) {
	if secrets == nil || factory == nil {
		return nil, errors.New("agent model dependencies are incomplete")
	}
	return &OpenAIModel{secrets: secrets, client: factory}, nil
}

func (model *OpenAIModel) Complete(ctx context.Context, request ModelRequest) (ModelTurn, error) {
	if err := validateModelRequest(request); err != nil {
		return ModelTurn{}, err
	}
	messages, err := openAIMessages(request.Messages)
	if err != nil {
		return ModelTurn{}, err
	}
	payload, messages, truncated, err := fitOpenAIPayload(request, messages)
	if err != nil {
		return ModelTurn{}, err
	}
	response, err := model.complete(ctx, request, payload)
	if err != nil {
		return ModelTurn{}, err
	}
	message, usage, err := openAICompletionMessage(response)
	if err != nil {
		return ModelTurn{}, err
	}
	usage["model_calls"] = 1
	usage["truncated_tool_results"] = truncated
	calls, err := parseOpenAIToolCalls(message)
	if err != nil {
		return ModelTurn{Usage: usage}, err
	}
	nativeOutput := supportsAgentNativeOutput(request.Capture.Model.Profile.CurrentVersion.Settings)
	if len(calls) == 0 {
		content, _ := message["content"].(string)
		if request.Capture.Model.AnswerMode != SinglePass && !nativeOutput {
			return ModelTurn{Usage: usage}, fmt.Errorf("%w: structured answer tool was not used", ErrModelValidation)
		}
		draft, parseErr := parseAnswerDraftContent(content)
		if parseErr != nil {
			return ModelTurn{Usage: usage}, parseErr
		}
		return ModelTurn{Draft: &draft, Usage: usage}, nil
	}
	for _, call := range calls {
		if call.Name != "VerifiedAnswer" {
			continue
		}
		if len(calls) != 1 || nativeOutput {
			return ModelTurn{Usage: usage}, fmt.Errorf("%w: structured answer is mixed with evidence tools", ErrModelValidation)
		}
		draft, parseErr := parseAnswerDraft([]byte(call.Arguments))
		if parseErr != nil {
			return ModelTurn{Usage: usage}, parseErr
		}
		return ModelTurn{Draft: &draft, Usage: usage}, nil
	}
	if request.Capture.Model.AnswerMode != ToolCalling {
		return ModelTurn{Usage: usage}, fmt.Errorf("%w: single-pass model requested tools", ErrModelValidation)
	}
	return ModelTurn{ToolCalls: calls, Usage: usage}, nil
}

func validateModelRequest(request ModelRequest) error {
	model := request.Capture.Model
	settings := model.Profile.CurrentVersion.Settings
	if request.BeforeRequest == nil || len(request.Messages) < 2 || request.MaxOutputTokens < 1 ||
		model.Endpoint.ID == (providers.EndpointID{}) || model.Endpoint.Lifecycle != providers.Active || model.Endpoint.Health != providers.Healthy ||
		model.Profile.ID == (providers.ProfileID{}) || model.Profile.EndpointID != model.Endpoint.ID || model.Profile.Availability == providers.Unavailable ||
		model.ProfileVersionID != ModelProfileVersionID(model.Profile.CurrentVersion.ID) || model.ProfileVersionNumber != model.Profile.CurrentVersion.VersionNumber ||
		settings.Transport != providers.ChatCompletions || model.Profile.CurrentVersion.ConfigurationVersion != model.Endpoint.ConfigurationVersion ||
		settings.ContextWindowTokens == nil || *settings.ContextWindowTokens <= 0 || settings.MaxOutputTokens == nil || *settings.MaxOutputTokens <= 0 ||
		request.MaxOutputTokens > int(*settings.MaxOutputTokens) || request.MaxOutputTokens > int(request.Capture.Agent.CurrentVersion.Configuration.MaxAnswerTokens) ||
		settings.TimeoutSeconds < providers.MinModelTimeoutSeconds || settings.TimeoutSeconds > providers.MaxModelTimeoutSeconds ||
		settings.MaxRetries < 0 || settings.MaxRetries > 10 || model.AnswerMode != request.Capture.Agent.CurrentVersion.Configuration.AnswerMode {
		return fmt.Errorf("%w: captured model is not executable", ErrModelProvider)
	}
	if model.AnswerMode == ToolCalling && (settings.SupportsTools == nil || !*settings.SupportsTools || len(request.Tools) == 0) ||
		model.AnswerMode == SinglePass && len(request.Tools) != 0 {
		return fmt.Errorf("%w: model tools do not match answer mode", ErrModelProvider)
	}
	credentialID := model.Endpoint.Configuration.CredentialID
	if (credentialID == nil) != (model.CapturedCredentialID == nil) ||
		(model.CapturedCredentialID == nil) != (model.CapturedCredentialVersion == nil) {
		return fmt.Errorf("%w: credential capture does not match endpoint", ErrModelProvider)
	}
	if credentialID != nil && [16]byte(*credentialID) != [16]byte(*model.CapturedCredentialID) {
		return fmt.Errorf("%w: credential capture does not match endpoint", ErrModelProvider)
	}
	return nil
}

func openAIMessages(values []ModelMessage) ([]any, error) {
	result := make([]any, 0, len(values))
	for _, value := range values {
		switch value.Role {
		case "system", "user":
			if value.Content == "" || value.ToolCallID != "" || len(value.ToolCalls) != 0 {
				return nil, fmt.Errorf("%w: model transcript is invalid", ErrModelValidation)
			}
			result = append(result, map[string]any{"role": value.Role, "content": value.Content})
		case "assistant":
			if len(value.ToolCalls) == 0 {
				return nil, fmt.Errorf("%w: assistant tool turn is invalid", ErrModelValidation)
			}
			calls := make([]any, len(value.ToolCalls))
			for index, call := range value.ToolCalls {
				if call.ID == "" || call.Name == "" || len(call.Arguments) > safenet.MaxBodyBytes {
					return nil, fmt.Errorf("%w: assistant tool turn is invalid", ErrModelValidation)
				}
				calls[index] = map[string]any{
					"id": call.ID, "type": "function",
					"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
				}
			}
			result = append(result, map[string]any{"role": "assistant", "content": nil, "tool_calls": calls})
		case "tool":
			if value.ToolCallID == "" || value.Content == "" || len(value.ToolCalls) != 0 {
				return nil, fmt.Errorf("%w: tool result turn is invalid", ErrModelValidation)
			}
			result = append(result, map[string]any{"role": "tool", "tool_call_id": value.ToolCallID, "content": value.Content})
		default:
			return nil, fmt.Errorf("%w: model transcript role is invalid", ErrModelValidation)
		}
	}
	return result, nil
}

func fitOpenAIPayload(request ModelRequest, messages []any) (map[string]any, []any, int, error) {
	working := cloneOpenAIMessages(messages)
	payload, err := openAIPayload(request, working)
	if err != nil {
		return nil, nil, 0, err
	}
	if openAIPayloadFits(request, payload) {
		return payload, working, 0, nil
	}
	truncated := 0
	for index := len(working) - 1; index >= 0; index-- {
		message, ok := working[index].(map[string]any)
		if !ok || message["role"] != "tool" {
			continue
		}
		content, _ := message["content"].(string)
		var raw map[string]any
		if json.Unmarshal([]byte(content), &raw) != nil {
			continue
		}
		bounded, truncateErr := modelbudget.TruncateResult([]byte(content), func(value map[string]any) bool {
			encoded, marshalErr := marshalAgentJSON(value)
			if marshalErr != nil {
				return false
			}
			message["content"] = string(encoded)
			candidate, payloadErr := openAIPayload(request, working)
			return payloadErr == nil && openAIPayloadFits(request, candidate)
		})
		if truncateErr != nil {
			continue
		}
		encoded, _ := marshalAgentJSON(bounded)
		message["content"] = string(encoded)
		truncated++
		payload, err = openAIPayload(request, working)
		if err == nil && openAIPayloadFits(request, payload) {
			return payload, working, truncated, nil
		}
	}
	return nil, nil, truncated, fmt.Errorf("%w: model payload exceeds context or byte limits", ErrModelValidation)
}

func openAIPayload(request ModelRequest, messages []any) (map[string]any, error) {
	settings := request.Capture.Model.Profile.CurrentVersion.Settings
	payload := make(map[string]any, len(settings.ExtraBody)+8)
	for key, value := range settings.ExtraBody {
		payload[key] = value
	}
	payload["model"] = request.Capture.Model.Profile.ModelID
	payload["messages"] = messages
	payload["max_completion_tokens"] = request.MaxOutputTokens
	if err := applyAgentReasoning(payload, settings, request.Capture.Model.ReasoningEffort); err != nil {
		return nil, err
	}
	if request.Capture.Model.AnswerMode == ToolCalling {
		tools := make([]any, 0, len(request.Tools)+1)
		for _, tool := range request.Tools {
			tools = append(tools, map[string]any{
				"type": "function", "function": map[string]any{
					"name": tool.Name, "description": tool.Description, "parameters": tool.Schema,
				},
			})
		}
		if supportsAgentNativeOutput(settings) {
			payload["response_format"] = map[string]any{
				"type": "json_schema", "json_schema": map[string]any{
					"name": "VerifiedAnswer", "strict": true, "schema": agentAnswerSchema(),
				},
			}
		} else {
			tools = append(tools, map[string]any{
				"type": "function", "function": map[string]any{
					"name": "VerifiedAnswer", "description": "Submit the final verified answer.", "parameters": agentAnswerSchema(),
				},
			})
		}
		payload["tools"] = tools
	}
	return payload, nil
}

func openAIPayloadFits(request ModelRequest, payload map[string]any) bool {
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > safenet.MaxBodyBytes {
		return false
	}
	contextWindow := request.Capture.Model.Profile.CurrentVersion.Settings.ContextWindowTokens
	return contextWindow != nil && modelbudget.Fits(encoded, int(*contextWindow), request.MaxOutputTokens, defaultSafetyTokens)
}

func (model *OpenAIModel) complete(ctx context.Context, request ModelRequest, payload map[string]any) (any, error) {
	settings := request.Capture.Model.Profile.CurrentVersion.Settings
	attempts := int(settings.MaxRetries) + 1
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		headers := make(map[string]string, len(request.Capture.Model.Endpoint.Configuration.Headers)+1)
		for key, value := range request.Capture.Model.Endpoint.Configuration.Headers {
			headers[key] = value
		}
		credentialID := request.Capture.Model.Endpoint.Configuration.CredentialID
		if credentialID != nil {
			secret, err := model.secrets.Read(ctx, *credentialID, credentials.ProviderAPIKey, *request.Capture.Model.CapturedCredentialVersion)
			if err != nil {
				return nil, fmt.Errorf("%w: credential is unavailable", ErrModelProvider)
			}
			headers["Authorization"] = "Bearer " + secret.Reveal()
		}
		timeout := time.Duration(settings.TimeoutSeconds) * time.Second
		client, err := model.client(request.Capture.Model.Endpoint.Configuration, headers, timeout)
		if err != nil {
			return nil, fmt.Errorf("%w: provider configuration is invalid", ErrModelProvider)
		}
		value, callErr := model.call(ctx, request, client, payload, timeout)
		client.CloseIdleConnections()
		if callErr == nil {
			return value, nil
		}
		if errors.Is(callErr, ErrModelRateLimit) {
			return nil, callErr
		}
		last = classifyAgentModelFailure(callErr)
		var network *safenet.RequestError
		if !errors.As(callErr, &network) || !network.Retryable || attempt+1 == attempts {
			break
		}
		delay, delayErr := safenet.RetryDelay(network.RetryHeaders, attempt, time.Now())
		if delayErr != nil {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if last == nil {
		last = ErrModelProvider
	}
	return nil, last
}

func (model *OpenAIModel) call(ctx context.Context, request ModelRequest, client ModelNetworkClient, payload map[string]any, timeout time.Duration) (any, error) {
	if model.admit != nil {
		release, err := model.admit(ctx, request.Capture.Model.Profile.ID, timeout)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := request.BeforeRequest(callContext); err != nil {
		return nil, err
	}
	value, _, err := client.PostJSON(callContext, request.Capture.Model.Endpoint.Configuration.ChatCompletionsPath, payload)
	return value, err
}

func classifyAgentModelFailure(err error) error {
	var request *safenet.RequestError
	if errors.As(err, &request) {
		if request.HTTPStatus == 429 {
			return ErrModelRateLimit
		}
		if request.Code == safenet.Timeout {
			return ErrModelTimeout
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrModelTimeout
	}
	return ErrModelProvider
}

func applyAgentReasoning(payload map[string]any, settings providers.Settings, effort ReasoningEffort) error {
	if effort == ReasoningNone {
		return nil
	}
	switch settings.ReasoningTransport {
	case providers.ReasoningEffort:
		payload["reasoning_effort"] = strings.ToLower(string(effort))
	case providers.CustomReasoning:
		if settings.ReasoningMapping == nil {
			return fmt.Errorf("%w: reasoning effort is not mapped", ErrModelProvider)
		}
		value, exists := settings.ReasoningMapping.Values[strings.ToLower(string(effort))]
		if !exists {
			return fmt.Errorf("%w: reasoning effort is not mapped", ErrModelProvider)
		}
		payload[settings.ReasoningMapping.Field] = value
	default:
		return fmt.Errorf("%w: reasoning effort is unsupported", ErrModelProvider)
	}
	return nil
}

func openAICompletionMessage(response any) (map[string]any, map[string]int, error) {
	object, ok := response.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%w: completion object is invalid", ErrModelProvider)
	}
	choices, ok := object["choices"].([]any)
	if !ok || len(choices) != 1 {
		return nil, nil, fmt.Errorf("%w: completion choices are invalid", ErrModelProvider)
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%w: completion choice is invalid", ErrModelProvider)
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("%w: completion message is invalid", ErrModelProvider)
	}
	usage := map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	if raw, ok := object["usage"].(map[string]any); ok {
		usage["input_tokens"] = nonnegativeAgentJSONInt(raw["prompt_tokens"])
		usage["output_tokens"] = nonnegativeAgentJSONInt(raw["completion_tokens"])
		usage["total_tokens"] = nonnegativeAgentJSONInt(raw["total_tokens"])
	}
	return message, usage, nil
}

func nonnegativeAgentJSONInt(value any) int {
	switch selected := value.(type) {
	case json.Number:
		parsed, err := selected.Int64()
		if err == nil && parsed >= 0 && int64(int(parsed)) == parsed {
			return int(parsed)
		}
	case float64:
		if selected >= 0 && selected == float64(int(selected)) {
			return int(selected)
		}
	case int:
		if selected >= 0 {
			return selected
		}
	}
	return 0
}

func parseOpenAIToolCalls(message map[string]any) ([]ToolCall, error) {
	raw, exists := message["tool_calls"]
	if !exists || raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > MaxToolCalls {
		return nil, fmt.Errorf("%w: tool calls are invalid", ErrModelValidation)
	}
	result := make([]ToolCall, 0, len(values))
	for _, value := range values {
		call, callOK := value.(map[string]any)
		function, functionOK := call["function"].(map[string]any)
		id, idOK := call["id"].(string)
		name, nameOK := function["name"].(string)
		arguments, argumentsOK := function["arguments"].(string)
		if !callOK || !functionOK || !idOK || !nameOK || !argumentsOK || id == "" || name == "" || len(arguments) > safenet.MaxBodyBytes {
			return nil, fmt.Errorf("%w: tool call is invalid", ErrModelValidation)
		}
		result = append(result, ToolCall{ID: id, Name: name, Arguments: arguments})
	}
	return result, nil
}

func parseAnswerDraftContent(content string) (AnswerDraft, error) {
	content = strings.TrimSpace(content)
	if content == "insufficient_evidence" {
		return AnswerDraft{Status: DraftInsufficientEvidence, Spans: []DraftSpan{}}, nil
	}
	if content == "refused" {
		return AnswerDraft{Status: DraftRefused, Spans: []DraftSpan{}}, nil
	}
	if match := agentJSONFencePattern.FindStringSubmatch(content); match != nil {
		content = strings.TrimSpace(match[1])
	}
	return parseAnswerDraft([]byte(content))
}

func parseAnswerDraft(content []byte) (AnswerDraft, error) {
	if len(content) == 0 || len(content) > maxAnswerBytes {
		return AnswerDraft{}, fmt.Errorf("%w: answer output exceeds its byte limit", ErrModelValidation)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var draft AnswerDraft
	if err := decoder.Decode(&draft); err != nil || requireAgentJSONEOF(decoder) != nil || draft.Spans == nil || len(draft.Spans) > 200 {
		return AnswerDraft{}, fmt.Errorf("%w: answer output is invalid", ErrModelValidation)
	}
	if draft.Status != DraftAnswered && draft.Status != DraftRefused && draft.Status != DraftInsufficientEvidence {
		return AnswerDraft{}, fmt.Errorf("%w: answer status is invalid", ErrModelValidation)
	}
	total := 0
	for _, span := range draft.Spans {
		if len(span.CitationIDs) > 100 {
			return AnswerDraft{}, fmt.Errorf("%w: answer span is invalid", ErrModelValidation)
		}
		for _, id := range span.CitationIDs {
			if !agentCitationIDPattern.MatchString(id) {
				return AnswerDraft{}, fmt.Errorf("%w: answer citation is invalid", ErrModelValidation)
			}
		}
		total += len([]byte(span.Markdown))
		if total > maxAnswerBytes {
			return AnswerDraft{}, fmt.Errorf("%w: answer output exceeds its byte limit", ErrModelValidation)
		}
	}
	if draft.Status == DraftAnswered && len(draft.Spans) == 0 {
		return AnswerDraft{}, fmt.Errorf("%w: answered output has no spans", ErrModelValidation)
	}
	return draft, nil
}

func requireAgentJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON has trailing content")
		}
		return err
	}
	return nil
}

func supportsAgentNativeOutput(settings providers.Settings) bool {
	return settings.SupportsStructuredOutput != nil && *settings.SupportsStructuredOutput
}

func agentAnswerSchema() map[string]any {
	return map[string]any{
		"type": "object", "properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []any{"answered", "refused", "insufficient_evidence"}},
			"spans": map[string]any{"type": "array", "maxItems": 200, "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"markdown":     map[string]any{"type": "string"},
					"citation_ids": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "maxLength": 512}},
				}, "required": []any{"markdown", "citation_ids"}, "additionalProperties": false,
			}},
		}, "required": []any{"status", "spans"}, "additionalProperties": false,
	}
}

func cloneOpenAIMessages(values []any) []any {
	result := make([]any, len(values))
	for index, value := range values {
		message, ok := value.(map[string]any)
		if !ok {
			result[index] = value
			continue
		}
		clone := make(map[string]any, len(message))
		for key, item := range message {
			clone[key] = item
		}
		result[index] = clone
	}
	return result
}
