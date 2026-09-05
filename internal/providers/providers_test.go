package providers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/safenet"
	"github.com/cyr1en/ref0/internal/security"
)

func TestCanonicalJSONGoldenVector(t *testing.T) {
	value := map[string]any{
		"nested":        map[string]any{"z": "é", "a": 1},
		"emoji":         "🥔",
		"float":         1.0,
		"integer":       1,
		"small_fixed":   1e-4,
		"small_exp":     1e-5,
		"large_fixed":   1e15,
		"large_exp":     1e16,
		"negative_zero": math.Copysign(0, -1),
		"html":          "<>&",
	}
	encoded, err := pythonCanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"emoji":"\ud83e\udd54","float":1.0,"html":"<>&","integer":1,"large_exp":1e+16,"large_fixed":1000000000000000.0,"negative_zero":-0.0,"nested":{"a":1,"z":"\u00e9"},"small_exp":1e-05,"small_fixed":0.0001}`
	if string(encoded) != want {
		t.Fatalf("canonical JSON=%s want=%s", encoded, want)
	}
}

func TestConfigurationAndSettingsOracleVectors(t *testing.T) {
	responses := "responses"
	configuration, err := (Configuration{
		DisplayName: "Primary", DisplayKey: "primary", BaseURL: "https://models.example/",
		Headers: NonSecretHeaders{"X-Tenant": "docs"}, ChatCompletionsPath: "chat/completions",
		ResponsesPath: &responses, ModelsPath: "models",
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if configuration.BaseURL != "https://models.example/v1" {
		t.Fatalf("normalized base=%q", configuration.BaseURL)
	}
	invalidConfigurations := []Configuration{
		{DisplayName: "Primary", DisplayKey: "primary", BaseURL: "http://127.0.0.1/v1", ChatCompletionsPath: "chat/completions", ModelsPath: "models"},
		{DisplayName: "Primary", DisplayKey: "primary", BaseURL: "https://models.example/v1", Headers: NonSecretHeaders{"Authorization": "sentinel"}, ChatCompletionsPath: "chat/completions", ModelsPath: "models"},
		{DisplayName: "Primary", DisplayKey: "primary", BaseURL: "https://models.example/v1", Headers: NonSecretHeaders{"X-Key": "a", "x-key": "b"}, ChatCompletionsPath: "chat/completions", ModelsPath: "models"},
		{DisplayName: "Primary", DisplayKey: "primary", BaseURL: "https://models.example/v1", ChatCompletionsPath: "../chat", ModelsPath: "models"},
	}
	for index, invalid := range invalidConfigurations {
		if _, err := invalid.Normalize(); err == nil {
			t.Fatalf("invalid configuration %d was admitted", index)
		}
	}

	settings := validTestSettings()
	settings.SupportsStreaming = nil
	settings.ReasoningMapping = nil
	settings.MetadataOrigin = testOrigins(OriginDiscovered)
	manual, err := NormalizeManualSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if manual.MetadataOrigin["model_id"] != OriginOperator ||
		manual.MetadataOrigin["supports_streaming"] != OriginUnknown ||
		manual.MetadataOrigin["reasoning_mapping"] != OriginUnknown ||
		manual.MetadataOrigin["max_output_tokens"] != OriginOperator {
		t.Fatalf("manual origins=%v", manual.MetadataOrigin)
	}
	settings.ExtraBody = map[string]any{"model": "secret-sentinel"}
	if _, err := settings.Normalize(); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("reserved body error=%v", err)
	}
}

func TestModelTimeoutValidationUsesTheSharedInclusiveRange(t *testing.T) {
	for _, timeout := range []int32{MinModelTimeoutSeconds, MaxModelTimeoutSeconds} {
		settings := validTestSettings()
		settings.TimeoutSeconds = timeout
		if _, err := settings.Normalize(); err != nil {
			t.Fatalf("boundary timeout %d rejected: %v", timeout, err)
		}
	}
	for _, timeout := range []int32{MinModelTimeoutSeconds - 1, MaxModelTimeoutSeconds + 1} {
		settings := validTestSettings()
		settings.TimeoutSeconds = timeout
		if _, err := settings.Normalize(); err == nil {
			t.Fatalf("out-of-range timeout %d accepted", timeout)
		}
	}
}

func TestModelConcurrencyValidationUsesTheInclusiveQueueRange(t *testing.T) {
	for _, limit := range []int32{MinModelConcurrentTasks, MaxModelConcurrentTasks} {
		settings := validTestSettings()
		settings.MaxConcurrentTasks = limit
		if _, err := settings.Normalize(); err != nil {
			t.Fatalf("boundary concurrency %d rejected: %v", limit, err)
		}
	}
	for _, limit := range []int32{MinModelConcurrentTasks - 1, MaxModelConcurrentTasks + 1} {
		settings := validTestSettings()
		settings.MaxConcurrentTasks = limit
		if _, err := settings.Normalize(); err == nil {
			t.Fatalf("out-of-range concurrency %d accepted", limit)
		}
	}
}

func TestCaptureLimitUsesPythonJSONDumpSize(t *testing.T) {
	payload := map[string]any{"a": strings.Repeat("x", maxProbeCapture-8)}
	compact, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) != maxProbeCapture {
		t.Fatalf("compact size=%d", len(compact))
	}
	command := CompleteProbe{
		Findings:    &ProbeFindings{ChatSucceeded: boolPointer(true)},
		RawResponse: payload,
	}
	if err := command.validate(); err == nil {
		t.Fatal("Python json.dumps separator overhead was not enforced")
	}
}

func TestProbeMergePreservesOperatorEvidence(t *testing.T) {
	settings := validTestSettings()
	settings.SupportsStreaming = boolPointer(false)
	settings.SupportsTools = boolPointer(false)
	settings.MetadataOrigin["supports_streaming"] = OriginOperator
	settings.MetadataOrigin["supports_tools"] = OriginUnknown
	merged, err := MergeProbeFindings(settings, ProbeFindings{
		SupportsStreaming: boolPointer(true), SupportsTools: boolPointer(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if *merged.SupportsStreaming || !*merged.SupportsTools || merged.MetadataOrigin["supports_tools"] != OriginProbed {
		t.Fatalf("merged=%+v origins=%v", merged, merged.MetadataOrigin)
	}
}

func TestProviderJSONSanitizationAndSSEOracleVectors(t *testing.T) {
	secret := "provider-secret-sentinel"
	safe, err := sanitizeProviderJSON(map[string]any{
		"api_key": "unrelated", "message": "prefix " + secret,
		"nested": []any{map[string]any{"password": "other", "ok": true}},
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	rendered := mustJSON(t, safe)
	if strings.Contains(rendered, secret) || !strings.Contains(rendered, `"api_key":"[REDACTED]"`) ||
		!strings.Contains(rendered, `"password":"[REDACTED]"`) {
		t.Fatalf("unsafe sanitized value=%s", rendered)
	}
	if _, err := sanitizeProviderJSON(map[string]any{
		secret: "one", redacted: "two",
	}, secret); err == nil {
		t.Fatal("redacted key collision was admitted")
	}
	succeeded, chunks := streamEvidence([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n"))
	if !succeeded || chunks != 1 {
		t.Fatalf("SSE succeeded=%v chunks=%d", succeeded, chunks)
	}
}

func TestExecutionDiscoveryRedactsAndClassifies(t *testing.T) {
	credentialID := credentials.ID{1}
	reader := fakeSecrets{secret: "provider-secret-sentinel"}
	client := &fakeNetworkClient{getValue: map[string]any{
		"data": []any{map[string]any{"id": "model-a", "api_key": "another-value"}},
		"echo": "provider-secret-sentinel",
	}, getStatus: 200}
	execution, err := NewExecutionWithFactory(reader, func(Configuration, map[string]string) (NetworkClient, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	endpoint := testEndpoint(&credentialID)
	version := int32(1)
	run := DiscoveryRun{ID: DiscoveryRunID{2}, EndpointID: endpoint.ID, CapturedConfigurationVersion: 1, CapturedCredentialVersion: &version}
	completed, err := execution.Discover(context.Background(), endpoint, run)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(completed.ModelIDs, []string{"model-a"}) || completed.RawResponse["echo"] != redacted {
		t.Fatalf("completion=%+v", completed)
	}
	models := completed.RawResponse["data"].([]any)
	if models[0].(map[string]any)["api_key"] != redacted {
		t.Fatalf("secret-shaped response field was persisted: %+v", completed.RawResponse)
	}
	client.getError = &safenet.RequestError{Code: safenet.Timeout, Retryable: true}
	failed, err := execution.Discover(context.Background(), endpoint, run)
	if err != nil || failed.SanitizedError != "provider_discovery:timeout" || !failed.Retryable {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
}

func TestExecutionProbePayloadsAndRetryClassification(t *testing.T) {
	reader := fakeSecrets{}
	client := &fakeNetworkClient{postValues: []any{
		map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "OK"}}}},
		map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": providerProbeTool, "arguments": `{"status":"ok"}`}}}}}}},
	}}
	execution, err := NewExecutionWithFactory(reader, func(Configuration, map[string]string) (NetworkClient, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	endpoint := testEndpoint(nil)
	settings := validTestSettings()
	profile := Profile{ID: ProfileID{3}, EndpointID: endpoint.ID, ModelID: "model-a", CurrentVersion: ProfileVersion{
		ID: ProfileVersionID{4}, ConfigurationVersion: 1, Settings: settings,
	}}
	run := ProbeRun{ID: ProbeRunID{5}, ProfileID: profile.ID, CapturedConfigurationVersion: 1,
		CapturedProfileVersionID: profile.CurrentVersion.ID, SelectedChecks: []ProbeCheck{ProbeChat, ProbeTools}}
	completed, err := execution.Probe(context.Background(), endpoint, profile, run)
	if err != nil || completed.Findings == nil || !*completed.Findings.ChatSucceeded || !*completed.Findings.SupportsTools {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	client.postIndex = 0
	client.postValues = nil
	client.postError = &safenet.RequestError{Code: safenet.HTTPStatus, Retryable: true, HTTPStatus: 503}
	run.SelectedChecks = []ProbeCheck{ProbeChat}
	failed, err := execution.Probe(context.Background(), endpoint, profile, run)
	if err != nil || failed.SanitizedError != "provider_probe:http_status" || !failed.Retryable {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
}

func TestHandlersValidateTargetsAndExposeOnlySanitizedFailures(t *testing.T) {
	endpoint := testEndpoint(nil)
	discovery := DiscoveryRun{
		ID: DiscoveryRunID{7}, EndpointID: endpoint.ID, JobID: jobs.JobID{8},
		Status: CaptureRunning,
	}
	safeError := "provider_discovery:http_status"
	store := &fakeCaptureStore{
		discovery: discovery,
		endpoint:  endpoint,
		completedDiscovery: DiscoveryRun{
			ID: discovery.ID, EndpointID: endpoint.ID, JobID: discovery.JobID,
			Status: CaptureFailed, SanitizedError: &safeError,
		},
		probe: ProbeRun{ID: ProbeRunID{9}, ProfileID: ProfileID{10}, Status: CaptureSuperseded},
	}
	executor := &fakeCaptureExecutor{
		discovery: CompleteDiscovery{RunID: discovery.ID, SanitizedError: safeError, Retryable: true},
	}
	handlers, err := NewHandlers(store, executor)
	if err != nil {
		t.Fatal(err)
	}
	permit := jobs.Permit{JobID: discovery.JobID, WorkerID: "worker-a", LeaseGeneration: 1}
	command := jobs.Command{
		Type: jobs.DiscoverEndpoint, TargetType: "provider_endpoint", TargetID: jobs.UUID(endpoint.ID),
		Payload: map[string]any{"discovery_run_id": discovery.ID.String()},
	}
	_, err = handlers.Discover(context.Background(), command, permit)
	var failure *HandlerFailure
	if !errors.As(err, &failure) || failure.SanitizedError != safeError || !failure.Retryable ||
		strings.Contains(err.Error(), endpoint.Configuration.BaseURL) {
		t.Fatalf("handler failure=%#v", err)
	}

	command.Payload["unexpected"] = true
	if _, err := handlers.Discover(context.Background(), command, permit); err == nil {
		t.Fatal("job with an expanded payload was admitted")
	}
	probeCommand := jobs.Command{
		Type: jobs.ProbeModel, TargetType: "model_profile", TargetID: jobs.UUID(store.probe.ProfileID),
		Payload: map[string]any{"probe_run_id": store.probe.ID.String()},
	}
	result, err := handlers.Probe(context.Background(), probeCommand, permit)
	if err != nil || result["status"] != "superseded" || executor.probeCalls != 0 {
		t.Fatalf("terminal probe result=%v err=%v calls=%d", result, err, executor.probeCalls)
	}
}

type fakeCaptureStore struct {
	discovery          DiscoveryRun
	completedDiscovery DiscoveryRun
	probe              ProbeRun
	completedProbe     ProbeRun
	endpoint           Endpoint
	profile            Profile
}

func (store *fakeCaptureStore) BeginDiscovery(context.Context, DiscoveryRunID, jobs.Permit) (DiscoveryRun, error) {
	return store.discovery, nil
}

func (store *fakeCaptureStore) CompleteDiscovery(context.Context, CompleteDiscovery, jobs.Permit) (DiscoveryRun, error) {
	return store.completedDiscovery, nil
}

func (store *fakeCaptureStore) GetEndpoint(context.Context, EndpointID) (Endpoint, error) {
	return store.endpoint, nil
}

func (store *fakeCaptureStore) BeginProbe(context.Context, ProbeRunID, jobs.Permit) (ProbeRun, error) {
	return store.probe, nil
}

func (store *fakeCaptureStore) CompleteProbe(context.Context, CompleteProbe, jobs.Permit) (ProbeRun, error) {
	return store.completedProbe, nil
}

func (store *fakeCaptureStore) GetProfile(context.Context, ProfileID) (Profile, error) {
	return store.profile, nil
}

type fakeCaptureExecutor struct {
	discovery     CompleteDiscovery
	probe         CompleteProbe
	discoverCalls int
	probeCalls    int
}

func (execution *fakeCaptureExecutor) Discover(context.Context, Endpoint, DiscoveryRun) (CompleteDiscovery, error) {
	execution.discoverCalls++
	return execution.discovery, nil
}

func (execution *fakeCaptureExecutor) Probe(context.Context, Endpoint, Profile, ProbeRun) (CompleteProbe, error) {
	execution.probeCalls++
	return execution.probe, nil
}

type fakeSecrets struct {
	secret string
	err    error
}

func (reader fakeSecrets) Read(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error) {
	if reader.err != nil {
		return nil, reader.err
	}
	return security.NewSecretValue(reader.secret)
}

type fakeNetworkClient struct {
	getValue    any
	getStatus   int
	getError    error
	postValues  []any
	postIndex   int
	postError   error
	exchange    safenet.Response
	exchangeErr error
}

func (client *fakeNetworkClient) GetJSON(context.Context, string) (any, int, error) {
	return client.getValue, client.getStatus, client.getError
}

func (client *fakeNetworkClient) PostJSON(context.Context, string, any) (any, int, error) {
	if client.postError != nil {
		return nil, 0, client.postError
	}
	if client.postIndex >= len(client.postValues) {
		return nil, 0, errors.New("unexpected POST")
	}
	value := client.postValues[client.postIndex]
	client.postIndex++
	return value, 200, nil
}

func (client *fakeNetworkClient) Exchange(context.Context, string, string, map[string]string, []byte, int) (safenet.Response, error) {
	return client.exchange, client.exchangeErr
}

func (*fakeNetworkClient) CloseIdleConnections() {}

func validTestSettings() Settings {
	contextTokens, outputTokens := int32(16_000), int32(2_000)
	return Settings{
		Transport: ChatCompletions, ContextWindowTokens: &contextTokens, MaxOutputTokens: &outputTokens,
		SupportsStreaming: boolPointer(true), SupportsTools: boolPointer(true),
		SupportsStructuredOutput: boolPointer(true), SupportsTemperature: boolPointer(true),
		ReasoningTransport: ReasoningEffort, TimeoutSeconds: 45, MaxRetries: 2,
		MaxConcurrentTasks: 1,
		ExtraBody:          map[string]any{"seed": 7}, MetadataOrigin: testOrigins(OriginOperator),
	}
}

func testOrigins(origin MetadataOrigin) map[string]MetadataOrigin {
	values := make(map[string]MetadataOrigin, len(metadataFields))
	for _, field := range metadataFields {
		values[field] = origin
	}
	return values
}

func testEndpoint(credentialID *credentials.ID) Endpoint {
	responses := "responses"
	return Endpoint{ID: EndpointID{1}, Configuration: Configuration{
		DisplayName: "Primary", DisplayKey: "primary", BaseURL: "https://models.example/v1",
		CredentialID: credentialID, Headers: NonSecretHeaders{}, ChatCompletionsPath: "chat/completions",
		ResponsesPath: &responses, ModelsPath: "models",
	}, Lifecycle: Active, Version: 1, ConfigurationVersion: 1, Health: Unknown}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
