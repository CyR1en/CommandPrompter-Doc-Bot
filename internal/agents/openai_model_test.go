package agents

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/safenet"
	"github.com/cyr1en/ref0/internal/security"
)

func TestOpenAIModelUsesCapturedCredentialAndStructuredAnswerTool(t *testing.T) {
	request := executableOpenAIRequest(t, ToolCalling, false)
	request.Capture.Model.ReasoningEffort = ReasoningLow
	request.Capture.Model.Profile.CurrentVersion.Settings.ReasoningTransport = providers.ReasoningEffort
	transport := &scriptedModelTransport{responses: []any{completionWithTool("final", "VerifiedAnswer", `{"status":"answered","spans":[{"markdown":"Grounded.","citation_ids":["c1_cite_known"]}]}`)}}
	secrets := &recordingModelSecrets{value: "provider-key"}
	model, err := NewOpenAIModelWithFactory(secrets, transport.factory)
	if err != nil {
		t.Fatal(err)
	}
	before := 0
	request.BeforeRequest = func(context.Context) error { before++; return nil }
	turn, err := model.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Draft == nil || turn.Draft.Status != DraftAnswered || len(turn.Draft.Spans) != 1 || len(turn.ToolCalls) != 0 {
		t.Fatalf("model turn = %#v", turn)
	}
	if got, want := turn.Usage, map[string]int{"model_calls": 1, "input_tokens": 13, "output_tokens": 7, "total_tokens": 20, "truncated_tool_results": 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
	if before != 1 || transport.closed != 1 || len(transport.payloads) != 1 || len(secrets.reads) != 1 {
		t.Fatalf("requests before=%d closed=%d payloads=%d secret_reads=%d", before, transport.closed, len(transport.payloads), len(secrets.reads))
	}
	credentialID := *request.Capture.Model.Endpoint.Configuration.CredentialID
	if secrets.reads[0].id != credentialID || secrets.reads[0].kind != credentials.ProviderAPIKey || secrets.reads[0].version != 3 {
		t.Fatalf("credential lease = %#v", secrets.reads[0])
	}
	if transport.headers[0]["Authorization"] != "Bearer provider-key" || transport.headers[0]["X-Tenant"] != "docs" ||
		transport.paths[0] != "/v1/chat/completions" || transport.timeouts[0] != 7*time.Second {
		t.Fatalf("provider request headers=%#v path=%q timeout=%s", transport.headers[0], transport.paths[0], transport.timeouts[0])
	}
	payload := transport.payloads[0]
	if payload["model"] != "agent-model" || payload["max_completion_tokens"] != 77 || payload["seed"] != float64(19) && payload["seed"] != 19 || payload["reasoning_effort"] != "low" {
		t.Fatalf("payload controls = %#v", payload)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != len(request.Tools)+1 || toolName(t, tools[len(tools)-1]) != "VerifiedAnswer" {
		t.Fatalf("payload tools = %#v", payload["tools"])
	}
}

func TestOpenAIModelUsesNativeStructuredOutputWithoutAnswerTool(t *testing.T) {
	request := executableOpenAIRequest(t, ToolCalling, true)
	request.Capture.Model.Endpoint.Configuration.CredentialID = nil
	request.Capture.Model.CapturedCredentialID = nil
	request.Capture.Model.CapturedCredentialVersion = nil
	response := completionWithContent(`{"status":"insufficient_evidence","spans":[]}`)
	transport := &scriptedModelTransport{responses: []any{response}}
	model, err := NewOpenAIModelWithFactory(&recordingModelSecrets{}, transport.factory)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := model.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Draft == nil || turn.Draft.Status != DraftInsufficientEvidence {
		t.Fatalf("model turn = %#v", turn)
	}
	payload := transport.payloads[0]
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) != len(request.Tools) {
		t.Fatalf("native tools = %#v", payload["tools"])
	}
	format, ok := payload["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("native response format = %#v", payload["response_format"])
	}
}

func TestOpenAIModelRetriesWithFreshnessAndExactCredentialLease(t *testing.T) {
	request := executableOpenAIRequest(t, SinglePass, false)
	request.Capture.Model.Profile.CurrentVersion.Settings.MaxRetries = 1
	transport := &scriptedModelTransport{
		errors:    []error{&safenet.RequestError{Code: safenet.Connection, Retryable: true}},
		responses: []any{completionWithContent("refused")},
	}
	secrets := &recordingModelSecrets{value: "provider-key"}
	model, err := NewOpenAIModelWithFactory(secrets, transport.factory)
	if err != nil {
		t.Fatal(err)
	}
	before := 0
	request.BeforeRequest = func(context.Context) error { before++; return nil }
	turn, err := model.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Draft == nil || turn.Draft.Status != DraftRefused || before != 2 || len(secrets.reads) != 2 || transport.closed != 2 {
		t.Fatalf("retry turn=%#v before=%d reads=%d closed=%d", turn, before, len(secrets.reads), transport.closed)
	}
	if secrets.reads[0] != secrets.reads[1] || secrets.reads[0].version != 3 {
		t.Fatalf("retry credential leases = %#v", secrets.reads)
	}
}

func TestOpenAIModelTruncatesOversizedToolResultInsideBoundedEnvelope(t *testing.T) {
	request := executableOpenAIRequest(t, ToolCalling, false)
	contextTokens, outputTokens := int32(4_000), int32(100)
	request.Capture.Model.Profile.CurrentVersion.Settings.ContextWindowTokens = &contextTokens
	request.Capture.Model.Profile.CurrentVersion.Settings.MaxOutputTokens = &outputTokens
	request.MaxOutputTokens = 100
	request.Messages = append(request.Messages,
		ModelMessage{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Name: "read_wiki_page", Arguments: `{"page_handle":"page_allowed"}`}}},
		ModelMessage{Role: "tool", ToolCallID: "call-1", Content: `{"text":"` + strings.Repeat("evidence ", 5_000) + `"}`},
	)
	transport := &scriptedModelTransport{responses: []any{completionWithTool("final", "VerifiedAnswer", `{"status":"insufficient_evidence","spans":[]}`)}}
	model, err := NewOpenAIModelWithFactory(&recordingModelSecrets{value: "provider-key"}, transport.factory)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := model.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Usage["truncated_tool_results"] != 1 {
		t.Fatalf("usage = %#v", turn.Usage)
	}
	messages := transport.payloads[0]["messages"].([]any)
	tool := messages[len(messages)-1].(map[string]any)
	content := tool["content"].(string)
	if !strings.Contains(content, `"truncated":true`) || !strings.Contains(content, `"original_bytes"`) {
		t.Fatalf("truncated tool result = %s", content)
	}
}

func TestEngineRunsConcreteOpenAIModelToolLoop(t *testing.T) {
	capture := executionCapture(t, ToolCalling)
	configureCapturedModelForOpenAI(&capture, false)
	capture.Model.Endpoint.Configuration.CredentialID = nil
	capture.Model.CapturedCredentialID = nil
	capture.Model.CapturedCredentialVersion = nil
	repository := &fakeExecutionRepository{capture: capture}
	transport := &scriptedModelTransport{}
	pagePattern := regexp.MustCompile(`page_[A-Za-z0-9_-]{20,}`)
	citationPattern := regexp.MustCompile(`c1_cite_[A-Za-z0-9_-]+`)
	transport.respond = func(call int, payload map[string]any) (any, error) {
		messages := payload["messages"].([]any)
		if call == 0 {
			content := messages[1].(map[string]any)["content"].(string)
			handle := pagePattern.FindString(content)
			if handle == "" {
				t.Fatalf("initial evidence omitted page handle: %s", content)
			}
			return completionWithTool("read-1", "read_wiki_page", `{"handle":"`+handle+`","start_line":1,"end_line":20}`), nil
		}
		content := messages[len(messages)-1].(map[string]any)["content"].(string)
		citation := citationPattern.FindString(content)
		if citation == "" {
			t.Fatalf("tool result omitted scoped citation: %s", content)
		}
		return completionWithTool("final", "VerifiedAnswer", `{"status":"answered","spans":[{"markdown":"Verified from the captured wiki.","citation_ids":["`+citation+`"]}]}`), nil
	}
	model, err := NewOpenAIModelWithFactory(&recordingModelSecrets{}, transport.factory)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(repository, staticDigester{}, model, EngineOptions{Clock: func() time.Time { return capture.CapturedAt }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Execute(context.Background(), validExecuteRequest(77), authorizerFunc(func(AuthorizationScope) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CompletionAnswered || !strings.HasPrefix(result.Markdown, "Verified from the captured wiki.") || len(result.Citations) != 1 || len(transport.payloads) != 2 {
		t.Fatalf("execution result=%#v payloads=%d", result, len(transport.payloads))
	}
	if repository.freshCalls != 5 || repository.securityFreshCalls != 1 {
		t.Fatalf("freshness checks = %d full, %d security", repository.freshCalls, repository.securityFreshCalls)
	}
	if len(repository.records) != 1 || !reflect.DeepEqual(repository.records[0].ToolCalls, []string{"initial_search_wiki", "read_wiki_page"}) {
		t.Fatalf("run receipt = %#v", repository.records)
	}
}

func TestAgentModelFailureClassification(t *testing.T) {
	tests := []struct {
		err  error
		want error
	}{
		{err: &safenet.RequestError{Code: safenet.HTTPStatus, HTTPStatus: 429}, want: ErrModelRateLimit},
		{err: &safenet.RequestError{Code: safenet.Timeout}, want: ErrModelTimeout},
		{err: context.DeadlineExceeded, want: ErrModelTimeout},
		{err: &safenet.RequestError{Code: safenet.PolicyDenied}, want: ErrModelProvider},
	}
	for _, test := range tests {
		if got := classifyAgentModelFailure(test.err); !errors.Is(got, test.want) {
			t.Fatalf("classify(%v) = %v, want %v", test.err, got, test.want)
		}
	}
}

type modelSecretRead struct {
	id      credentials.ID
	kind    credentials.Kind
	version int32
}

type recordingModelSecrets struct {
	value string
	reads []modelSecretRead
	err   error
}

func (reader *recordingModelSecrets) Read(_ context.Context, id credentials.ID, kind credentials.Kind, version int32) (*security.SecretValue, error) {
	reader.reads = append(reader.reads, modelSecretRead{id: id, kind: kind, version: version})
	if reader.err != nil {
		return nil, reader.err
	}
	return security.NewSecretValue(reader.value)
}

type scriptedModelTransport struct {
	responses []any
	errors    []error
	respond   func(int, map[string]any) (any, error)
	payloads  []map[string]any
	paths     []string
	headers   []map[string]string
	timeouts  []time.Duration
	closed    int
}

func (transport *scriptedModelTransport) factory(configuration providers.Configuration, headers map[string]string, timeout time.Duration) (ModelNetworkClient, error) {
	clone := make(map[string]string, len(headers))
	for key, value := range headers {
		clone[key] = value
	}
	transport.headers = append(transport.headers, clone)
	transport.timeouts = append(transport.timeouts, timeout)
	return &scriptedModelClient{transport: transport}, nil
}

type scriptedModelClient struct {
	transport *scriptedModelTransport
}

func (client *scriptedModelClient) PostJSON(_ context.Context, path string, raw any) (any, int, error) {
	payload, ok := raw.(map[string]any)
	if !ok {
		return nil, 0, errors.New("unexpected payload")
	}
	call := len(client.transport.payloads)
	client.transport.payloads = append(client.transport.payloads, payload)
	client.transport.paths = append(client.transport.paths, path)
	if client.transport.respond != nil {
		response, err := client.transport.respond(call, payload)
		return response, 200, err
	}
	if len(client.transport.errors) != 0 {
		err := client.transport.errors[0]
		client.transport.errors = client.transport.errors[1:]
		return nil, 0, err
	}
	if len(client.transport.responses) == 0 {
		return nil, 0, errors.New("unexpected provider request")
	}
	response := client.transport.responses[0]
	client.transport.responses = client.transport.responses[1:]
	return response, 200, nil
}

func (client *scriptedModelClient) CloseIdleConnections() {
	client.transport.closed++
}

func executableOpenAIRequest(t *testing.T, mode AnswerMode, nativeOutput bool) ModelRequest {
	t.Helper()
	capture := executionCapture(t, mode)
	configureCapturedModelForOpenAI(&capture, nativeOutput)
	request := ModelRequest{
		Capture: capture,
		Messages: []ModelMessage{
			{Role: "system", Content: "platform policy"},
			{Role: "user", Content: `{"untrusted_transcript":[{"role":"USER","content":"Question?"}]}`},
		},
		Tools: []ToolDefinition{{
			Name: "read_wiki_page", Description: "Read captured wiki evidence.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{"page_handle": map[string]any{"type": "string"}}, "required": []any{"page_handle"}, "additionalProperties": false},
		}},
		MaxOutputTokens: 77,
		BeforeRequest:   func(context.Context) error { return nil },
	}
	if mode == SinglePass {
		request.Tools = nil
	}
	return request
}

func configureCapturedModelForOpenAI(capture *ExecutionCapture, nativeOutput bool) {
	credentialID := credentials.ID{12}
	capturedCredentialID := CredentialID(credentialID)
	capturedCredentialVersion := int32(3)
	settings := &capture.Model.Profile.CurrentVersion.Settings
	settings.TimeoutSeconds = 7
	settings.MaxRetries = 0
	settings.ExtraBody = map[string]any{"seed": 19}
	settings.SupportsStructuredOutput = &nativeOutput
	capture.Model.Endpoint.Configuration = providers.Configuration{
		BaseURL: "https://provider.example", CredentialID: &credentialID,
		Headers: providers.NonSecretHeaders{"X-Tenant": "docs"}, ChatCompletionsPath: "/v1/chat/completions",
	}
	capture.Model.Endpoint.ConfigurationVersion = 1
	capture.Model.Endpoint.Health = providers.Healthy
	capture.Model.Profile.ModelID = "agent-model"
	capture.Model.CapturedCredentialID = &capturedCredentialID
	capture.Model.CapturedCredentialVersion = &capturedCredentialVersion
}

func completionWithContent(content string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		"usage":   map[string]any{"prompt_tokens": float64(13), "completion_tokens": float64(7), "total_tokens": float64(20)},
	}
}

func completionWithTool(id, name, arguments string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{
			"content": nil,
			"tool_calls": []any{map[string]any{
				"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments},
			}},
		}}},
		"usage": map[string]any{"prompt_tokens": float64(13), "completion_tokens": float64(7), "total_tokens": float64(20)},
	}
}

func toolName(t *testing.T, raw any) string {
	t.Helper()
	tool, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("tool = %#v", raw)
	}
	function, ok := tool["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool function = %#v", tool)
	}
	name, _ := function["name"].(string)
	return name
}

func TestModelRetryWaitIsCancellableAndReleasesAdmission(t *testing.T) {
	request := executableOpenAIRequest(t, SinglePass, false)
	request.Capture.Model.Profile.CurrentVersion.Settings.MaxRetries = 1
	transport := &scriptedModelTransport{errors: []error{&safenet.RequestError{Code: safenet.HTTPStatus, HTTPStatus: 429, Retryable: true, RetryHeaders: map[string]string{"retry-after": "60"}}}}
	model, err := NewOpenAIModelWithFactory(&recordingModelSecrets{value: "key"}, transport.factory)
	if err != nil {
		t.Fatal(err)
	}
	admitted, released := 0, 0
	model.admit = func(context.Context, providers.ProfileID, time.Duration) (func(), error) {
		admitted++
		return func() { released++ }, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = model.Complete(ctx, request)
	if !errors.Is(err, context.DeadlineExceeded) || len(transport.payloads) != 1 || admitted != 1 || released != 1 {
		t.Fatalf("cancelled retry: %v calls=%d admitted=%d released=%d", err, len(transport.payloads), admitted, released)
	}
}
