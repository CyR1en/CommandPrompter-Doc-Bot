package capsule

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/safenet"
	"github.com/cyr1en/ref0/internal/security"
)

func testBinding() Binding {
	return Binding{
		ModelID: "custom-model", BaseURL: "https://provider.example/v1", ChatCompletionsPath: "chat/completions",
		Headers: map[string]string{"x-tenant": "tenant-a"}, BodyOptions: map[string]any{"temperature": 0},
		ContextWindow: 8_192, MaxOutputTokens: 1_024, ReasoningEffort: providers.EffortNone,
		ReasoningOptions: map[string]any{}, Timeout: 5 * time.Second, Credential: &CredentialReference{ID: credentials.ID{15: 1}, SecretVersion: 3},
		CapsuleRuntimeRevision: RuntimeRevision, Limits: DefaultLimits(), NetworkPolicy: safenet.Policy{},
	}
}

type fakeSecretReader struct {
	read func(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error)
}

func (reader fakeSecretReader) Read(ctx context.Context, id credentials.ID, kind credentials.Kind, version int32) (*security.SecretValue, error) {
	return reader.read(ctx, id, kind, version)
}

type fakeNetworkClient struct {
	mu        sync.Mutex
	responses []safenet.Response
	errors    []error
	headers   []map[string]string
	bodies    [][]byte
}

func (client *fakeNetworkClient) Exchange(_ context.Context, method, path string, headers map[string]string, body []byte, maximum int) (safenet.Response, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if method != http.MethodPost || path != "chat/completions" || maximum != 1_048_576 {
		return safenet.Response{}, errors.New("unexpected request")
	}
	client.headers = append(client.headers, cloneStringMap(headers))
	client.bodies = append(client.bodies, append([]byte(nil), body...))
	index := len(client.bodies) - 1
	var err error
	if index < len(client.errors) {
		err = client.errors[index]
	}
	if err != nil {
		return safenet.Response{}, err
	}
	return client.responses[index], nil
}

func (*fakeNetworkClient) CloseIdleConnections() {}

func cloneStringMap(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for name, item := range value {
		result[name] = item
	}
	return result
}

func startedPool(t *testing.T) *SlotPool {
	t.Helper()
	slot, _ := NewSlot("test", "/run/test/capsule.sock")
	pool, err := NewSlotPool([]Slot{slot})
	if err != nil || pool.start(nil) != nil {
		t.Fatal(err)
	}
	return pool
}

func validSecret(t *testing.T) *security.SecretValue {
	t.Helper()
	secret, err := security.NewSecretValue("provider-secret-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestSessionOwnsCredentialRequestTranscriptToolAndCompletion(t *testing.T) {
	pool := startedPool(t)
	readerCalledBeforeLease := false
	secrets := fakeSecretReader{read: func(_ context.Context, id credentials.ID, kind credentials.Kind, version int32) (*security.SecretValue, error) {
		pool.mu.Lock()
		readerCalledBeforeLease = len(pool.leased) == 0 && len(pool.free) == 1
		pool.mu.Unlock()
		if id != (credentials.ID{15: 1}) || kind != credentials.ProviderAPIKey || version != 3 {
			t.Fatal("credential capture mismatch")
		}
		return validSecret(t), nil
	}}
	network := &fakeNetworkClient{responses: []safenet.Response{
		{Status: 200, Headers: map[string]string{"content-type": "text/event-stream; charset=utf-8"}, Body: toolSSE(t, "lookup", `{"id":7}`, "source-call", []any{})},
		{Status: 200, Headers: map[string]string{"content-type": "text/event-stream"}, Body: toolSSE(t, "submit_result", `{"answer":"done"}`, "submit-call", []any{})},
	}}
	factory, err := NewFactory(testBinding(), Planner, pool, secrets, FactoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	factory.client = func(Binding) (networkClient, error) { return network, nil }
	host, child := net.Pipe()
	factory.dial = func(context.Context, string) (net.Conn, error) { return host, nil }
	serverError := make(chan error, 1)
	go func() {
		transport := newWire(child, DefaultLimits())
		start, err := transport.receive()
		if err != nil {
			serverError <- err
			return
		}
		serialized, _ := canonicalJSON(start)
		if stringContainsAny(string(serialized), "provider-secret-sentinel", "tenant-a", "provider.example") {
			serverError <- errors.New("host authority leaked into capsule start")
			return
		}
		provider, providerOK := start["provider"].(map[string]any)
		timeout, timeoutOK := jsonInteger(provider["timeout_ms"])
		if !providerOK || !timeoutOK || timeout != testBinding().Timeout.Milliseconds() {
			serverError <- errors.New("capsule start changed the configured model timeout")
			return
		}
		if err = transport.send(map[string]any{"type": "model_request", "id": "model-1", "turn": 1}); err != nil {
			serverError <- err
			return
		}
		if _, err = transport.receive(); err != nil {
			serverError <- err
			return
		}
		if err = transport.send(map[string]any{
			"type": "tool_call", "id": "tool-1", "provider_call_id": "source-call", "name": "lookup", "arguments": map[string]any{"id": 7},
		}); err != nil {
			serverError <- err
			return
		}
		toolResult, err := transport.receive()
		if err != nil || toolResult["content"] != `{"found":7}` {
			serverError <- errors.New("tool result mismatch")
			return
		}
		if err = transport.send(map[string]any{"type": "model_request", "id": "model-2", "turn": 2}); err != nil {
			serverError <- err
			return
		}
		if _, err = transport.receive(); err != nil {
			serverError <- err
			return
		}
		if err = transport.send(map[string]any{"type": "complete", "output": map[string]any{"answer": "done"}}); err != nil {
			serverError <- err
			return
		}
		_ = child.Close()
		serverError <- nil
	}()
	session, err := factory.NewSession(Planner, "system", []Tool{{
		Name: "lookup", Description: "Look up.", Parameters: testLookupSchema,
		Handler: func(_ context.Context, arguments map[string]any) (any, error) {
			if !sameJSON(arguments, map[string]any{"id": 7}) {
				t.Fatal("tool arguments mismatch")
			}
			return map[string]any{"found": 7}, nil
		},
	}}, testOutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := session.Invoke(context.Background(), "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
	if !readerCalledBeforeLease || invocation.Output["answer"] != "done" || invocation.Usage != (Usage{ModelCalls: 2, InputTokens: 20, OutputTokens: 8, TotalTokens: 28}) {
		t.Fatalf("unexpected invocation: before=%v result=%#v", readerCalledBeforeLease, invocation)
	}
	if len(network.headers) != 2 || network.headers[0]["authorization"] != "Bearer provider-secret-sentinel" || network.headers[0]["accept"] != "text/event-stream" {
		t.Fatalf("host provider headers mismatch: %#v", network.headers)
	}
	first, _, _ := parseStrictJSON(network.bodies[0], complexityLimits{32, 65_536, 10_000})
	second, _, _ := parseStrictJSON(network.bodies[1], complexityLimits{32, 65_536, 10_000})
	if len(first.(map[string]any)["messages"].([]any)) != 3 || len(second.(map[string]any)["messages"].([]any)) != 5 {
		t.Fatal("host transcript was not replayed exactly")
	}
}

func TestCredentialFailurePrecedesSlotAndSocket(t *testing.T) {
	pool := startedPool(t)
	factory, err := NewFactory(testBinding(), Planner, pool, fakeSecretReader{read: func(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error) {
		return nil, errors.New("credential sentinel")
	}}, FactoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	factory.client = func(Binding) (networkClient, error) { return &fakeNetworkClient{}, nil }
	factory.dial = func(context.Context, string) (net.Conn, error) { opened = true; return nil, errors.New("opened") }
	session, err := factory.NewSession(Planner, "system", nil, testOutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Invoke(context.Background(), "prompt")
	if err == nil || err.Error() != "capsule credential resolution failed safely" || opened {
		t.Fatalf("credential failure was not fenced: %v opened=%v", err, opened)
	}
	pool.mu.Lock()
	leasing := len(pool.leased)
	free := len(pool.free)
	pool.mu.Unlock()
	if leasing != 0 || free != 1 {
		t.Fatalf("credential failure occupied slot: leased=%d free=%d", leasing, free)
	}
}

func TestProviderRetriesHonorBoundedRetryAfter(t *testing.T) {
	factory, err := NewFactory(func() Binding { value := testBinding(); value.MaxRetries = 4; return value }(), Planner, startedPool(t),
		fakeSecretReader{read: func(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error) {
			return validSecret(t), nil
		}}, FactoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	network := &fakeNetworkClient{responses: []safenet.Response{
		{Status: 408}, {Status: 409}, {Status: 429, Headers: map[string]string{"retry-after": "7"}}, {Status: 503}, {Status: 200},
	}}
	delays := []time.Duration{}
	factory.sleep = func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil }
	response, err := factory.exchange(context.Background(), network, []byte(`{}`), "secret")
	if err != nil || response.Status != 200 {
		t.Fatal(err)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 7 * time.Second, 4 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("retry delays: %#v", delays)
	}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("retry delays: %#v", delays)
		}
	}
	if _, err := safenet.RetryDelay(map[string]string{"retry-after": "61"}, 0, time.Now()); err == nil {
		t.Fatal("excessive Retry-After was accepted")
	}
}

func TestCompileBindingFencesEveryCapturedProviderVersion(t *testing.T) {
	trueValue := true
	contextWindow, output := int32(8_192), int32(1_024)
	endpoint := providers.Endpoint{
		ID: providers.EndpointID{15: 1}, Lifecycle: providers.Active, ConfigurationVersion: 7,
		Configuration: providers.Configuration{
			BaseURL: "https://provider.example/v1", CredentialID: func() *credentials.ID { value := credentials.ID{15: 2}; return &value }(),
			Headers: providers.NonSecretHeaders{"x-tenant": "tenant-a"}, ChatCompletionsPath: "chat/completions",
		},
	}
	profile := providers.Profile{
		ID: providers.ProfileID{15: 3}, EndpointID: endpoint.ID, ModelID: "custom-model", Availability: providers.Available,
		CurrentVersion: providers.ProfileVersion{
			ID: providers.ProfileVersionID{15: 4}, VersionNumber: 5, ConfigurationVersion: 7,
			Settings: providers.Settings{
				Transport: providers.ChatCompletions, ContextWindowTokens: &contextWindow, MaxOutputTokens: &output,
				SupportsStreaming: &trueValue, SupportsTools: &trueValue, SupportsStructuredOutput: &trueValue, SupportsTemperature: &trueValue,
				ReasoningTransport: providers.ReasoningEffort, TimeoutSeconds: 60, MaxRetries: 2, MaxConcurrentTasks: 1, ExtraBody: map[string]any{"temperature": 0},
			},
		},
	}
	credentialVersion := int32(9)
	captured := ProviderCapture{
		Role: providers.DocumentationPlanner, ProfileID: profile.ID, ProfileVersionID: profile.CurrentVersion.ID,
		ProfileVersion: 5, EndpointID: endpoint.ID, EndpointConfigurationVersion: 7,
		CredentialVersion: &credentialVersion, ReasoningEffort: providers.EffortHigh,
	}
	binding, role, err := CompileBinding(captured, profile, endpoint)
	if err != nil || role != Planner || binding.ReasoningOptions["reasoning_effort"] != "high" || binding.Credential.SecretVersion != 9 || binding.Timeout != 60*time.Second {
		t.Fatalf("valid captured binding failed: %#v %s %v", binding, role, err)
	}
	profile.CurrentVersion.Settings.TimeoutSeconds = 1
	minimum, _, err := CompileBinding(captured, profile, endpoint)
	if err != nil || minimum.Timeout != time.Second {
		t.Fatalf("minimum timeout binding=%#v err=%v", minimum, err)
	}
	for _, timeout := range []int32{0, 61} {
		profile.CurrentVersion.Settings.TimeoutSeconds = timeout
		if _, _, err := CompileBinding(captured, profile, endpoint); !errors.Is(err, ErrBinding) {
			t.Fatalf("out-of-range timeout %d was accepted", timeout)
		}
	}
	profile.CurrentVersion.Settings.TimeoutSeconds = 60
	stale := captured
	stale.EndpointConfigurationVersion++
	if _, _, err := CompileBinding(stale, profile, endpoint); !errors.Is(err, ErrBinding) {
		t.Fatal("stale captured provider binding was accepted")
	}
}

func stringContainsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
