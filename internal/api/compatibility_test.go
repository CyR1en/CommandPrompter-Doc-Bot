package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/chattokens"
)

type fakeCompatibilityTokens struct {
	grant chattokens.Grant
	err   error
}

func (service fakeCompatibilityTokens) Authenticate(context.Context, string) (chattokens.Grant, error) {
	return service.grant, service.err
}

type fakeScopedAgents struct {
	list       []agents.Agent
	selected   agents.Agent
	resolveErr error
}

func (service *fakeScopedAgents) ListReadyScoped(context.Context, []agents.AgentID) ([]agents.Agent, error) {
	return service.list, nil
}

func (service *fakeScopedAgents) ResolveReadyScoped(context.Context, []agents.AgentID, string) (agents.Agent, error) {
	return service.selected, service.resolveErr
}

type fakeCompatibilityEngine struct {
	result       agents.ExecuteResult
	err          error
	request      agents.ExecuteRequest
	afterExecute func()
}

func (engine *fakeCompatibilityEngine) Execute(
	ctx context.Context,
	request agents.ExecuteRequest,
	authorizer agents.Authorizer,
) (agents.ExecuteResult, error) {
	engine.request = request
	if err := authorizer.Authorize(ctx, agents.AuthorizationScope{
		AgentID: engineAgentID(testedAgentID), AgentKey: "docs", Origin: request.Origin, Subject: request.Subject,
	}); err != nil {
		return agents.ExecuteResult{}, err
	}
	if engine.afterExecute != nil {
		engine.afterExecute()
	}
	return engine.result, engine.err
}

const (
	testedAgentID = "10000000-0000-4000-8000-000000000001"
	testedTokenID = "20000000-0000-4000-8000-000000000002"
	testedRunID   = "30000000-0000-4000-8000-000000000003"
)

func TestCompatibilityModelsAndNonStreamingCompletion(t *testing.T) {
	handler, engine := compatibilityTestHandler(t, nil)

	unauthorized := compatibilityRequest(handler, http.MethodGet, compatibilityModelsPath, "", nil)
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") == "" ||
		!strings.Contains(unauthorized.Body.String(), `"code":"invalid_api_key"`) {
		t.Fatalf("unauthorized=%d headers=%v body=%s", unauthorized.Code, unauthorized.Header(), unauthorized.Body.String())
	}

	models := compatibilityRequest(handler, http.MethodGet, compatibilityModelsPath, "", map[string]string{"Authorization": "Bearer token"})
	if models.Code != http.StatusOK || models.Body.String() != "{\"object\":\"list\",\"data\":[{\"id\":\"agent:docs\",\"object\":\"model\",\"created\":1600000000,\"owned_by\":\"ref0\"}]}\n" {
		t.Fatalf("models=%d %s", models.Code, models.Body.String())
	}

	body := `{"model":"agent:docs","messages":[{"role":"system","content":"style"},{"role":"user","content":"question"}],"stream":false,"max_tokens":42}`
	response := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath, body, map[string]string{
		"Authorization": "Bearer token", "Content-Type": "application/json",
	})
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("completion=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var completion compatibilityCompletion
	if err := json.Unmarshal(response.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.ID != "chatcmpl-"+testedRunID || completion.Object != "chat.completion" ||
		completion.Ref0.RunID != testedRunID || completion.Ref0.Status != "answered" ||
		len(completion.Ref0.Citations) != 1 || completion.Choices[0].Message.Content != "verified" ||
		completion.Usage.TotalTokens != 12 {
		t.Fatalf("completion=%+v", completion)
	}
	if engine.request.Origin != agents.OriginHTTP || engine.request.Subject != "chat-token:"+testedTokenID ||
		engine.request.MaxTokens != 42 || len(engine.request.Messages) != 2 {
		t.Fatalf("engine request=%+v", engine.request)
	}
}

func TestCompatibilityStrictValidationAndModelNonEnumeration(t *testing.T) {
	handler, _ := compatibilityTestHandler(t, func(scoped *fakeScopedAgents) {
		scoped.resolveErr = agents.ErrChatModelUnavailable
	})
	headers := map[string]string{"Authorization": "Bearer token", "Content-Type": "application/json"}

	for _, model := range []string{"agent:unknown", "agent:inactive", "agent:unready"} {
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"question"}],"stream":false}`
		response := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath, body, headers)
		if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":{\"message\":\"The requested model is unavailable.\",\"type\":\"invalid_request_error\",\"param\":\"model\",\"code\":\"model_not_found\"}}\n" {
			t.Fatalf("model %q=%d %s", model, response.Code, response.Body.String())
		}
	}
	cases := []string{
		`{"model":"agent:docs","messages":[{"role":"assistant","content":"last"}],"stream":false}`,
		`{"model":"agent:docs","messages":[{"role":"user","content":[{"type":"text","text":"no"}]}],"stream":false}`,
		`{"model":"agent:docs","messages":[{"role":"user","content":"question"}],"stream":false,"conversation_id":"legacy"}`,
		`{"model":"agent:docs","messages":[{"role":"user","content":"question"}],"stream":false,"tools":[]}`,
		`{"model":"agent:docs","messages":[{"role":"user","content":"question"}],"stream":false,"stream_options":{"include_usage":true}}`,
	}
	for _, body := range cases {
		response := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath, body, headers)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"type":"invalid_request_error"`) {
			t.Fatalf("invalid body=%s response=%d %s", body, response.Code, response.Body.String())
		}
	}
	idempotent := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath,
		`{"model":"agent:docs","messages":[{"role":"user","content":"question"}],"stream":false}`,
		map[string]string{"Authorization": "Bearer token", "Content-Type": "application/json", "Idempotency-Key": "false-promise"})
	if idempotent.Code != http.StatusBadRequest || !strings.Contains(idempotent.Body.String(), "Idempotency-Key is not supported") {
		t.Fatalf("idempotency=%d %s", idempotent.Code, idempotent.Body.String())
	}
	unauthenticatedIdempotent := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath,
		`{"model":"agent:docs","messages":[{"role":"user","content":"question"}],"stream":false}`,
		map[string]string{"Content-Type": "application/json", "Idempotency-Key": "false-promise"})
	if unauthenticatedIdempotent.Code != http.StatusUnauthorized || !strings.Contains(unauthenticatedIdempotent.Body.String(), `"code":"invalid_api_key"`) {
		t.Fatalf("unauthenticated idempotency=%d %s", unauthenticatedIdempotent.Code, unauthenticatedIdempotent.Body.String())
	}
}

func TestCompatibilityBufferedSSEPreservesNativeErrorsAndFlusher(t *testing.T) {
	if openWebUICompatibilityVersion != "v0.11.3" {
		t.Fatalf("review the Open WebUI wire fixture before changing the supported version: %s", openWebUICompatibilityVersion)
	}
	handler, _ := compatibilityTestHandler(t, nil)
	body := `{"model":"agent:docs","messages":[{"role":"user","content":"question"}],"stream":true,"stream_options":{"include_usage":true}}`
	response := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath, body, map[string]string{
		"Authorization": "Bearer token", "Content-Type": "application/json",
	})
	if response.Code != http.StatusOK || !response.Flushed || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream=%d flushed=%v headers=%v body=%s", response.Code, response.Flushed, response.Header(), response.Body.String())
	}
	frames := strings.Split(strings.TrimSpace(response.Body.String()), "\n\n")
	if len(frames) != 5 || frames[len(frames)-1] != "data: [DONE]" ||
		!strings.Contains(frames[1], `"content":"verified"`) ||
		!strings.Contains(frames[3], `"x_ref0":{"run_id":"`+testedRunID+`"`) ||
		!strings.Contains(frames[3], `"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}`) {
		t.Fatalf("frames=%q", frames)
	}

	missing := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath,
		`{"model":"agent:missing","messages":[{"role":"user","content":"question"}],"stream":false}`,
		map[string]string{"Authorization": "Bearer token", "Content-Type": "application/json"})
	if missing.Code != http.StatusOK {
		// The default fake resolves every key; exercise native 404 via the /v1 fallback below.
		t.Fatalf("unexpected default fake result=%d", missing.Code)
	}
	notFound := compatibilityRequest(handler, http.MethodGet, "/v1/unknown", "", map[string]string{"Authorization": "Bearer token"})
	if notFound.Code != http.StatusNotFound || notFound.Header().Get("Content-Type") != "application/json" ||
		!strings.Contains(notFound.Body.String(), `"code":"not_found"`) {
		t.Fatalf("native 404=%d headers=%v body=%s", notFound.Code, notFound.Header(), notFound.Body.String())
	}
	method := compatibilityRequest(handler, http.MethodGet, compatibilityCompletionsPath, "", nil)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Content-Type") != "application/json" ||
		!strings.Contains(method.Body.String(), `"code":"method_not_allowed"`) {
		t.Fatalf("native 405=%d headers=%v body=%s", method.Code, method.Header(), method.Body.String())
	}
}

func TestCompatibilityPanicUsesOpenAIErrorEnvelope(t *testing.T) {
	handler := problemBoundary(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("untrusted panic detail")
	}))
	response := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath, "", nil)
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/json" ||
		response.Body.String() != "{\"error\":{\"message\":\"The request could not be completed.\",\"type\":\"server_error\",\"param\":null,\"code\":\"internal_error\"}}\n" ||
		strings.Contains(response.Body.String(), "untrusted panic detail") {
		t.Fatalf("panic=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func compatibilityTestHandler(t *testing.T, configure func(*fakeScopedAgents)) (http.Handler, *fakeCompatibilityEngine) {
	t.Helper()
	agentID := engineAgentID(testedAgentID)
	tokenID, err := chattokens.ParseID(testedTokenID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := chattokens.NewGrant(tokenID, []agents.AgentID{agentID})
	if err != nil {
		t.Fatal(err)
	}
	runID := engineRunID(testedRunID)
	scoped := &fakeScopedAgents{
		list:     []agents.Agent{{ID: agentID, Key: "docs", CreatedAt: time.Unix(1_600_000_000, 0)}},
		selected: agents.Agent{ID: agentID, Key: "docs"},
	}
	if configure != nil {
		configure(scoped)
	}
	engine := &fakeCompatibilityEngine{result: agents.ExecuteResult{
		RunID: runID, Status: agents.CompletionAnswered, Markdown: "verified",
		Citations: []agents.Citation{{ID: "c1", Label: "Docs", Resource: "wiki://page"}},
		Usage:     map[string]int{"input_tokens": 7, "output_tokens": 5},
	}}
	compatibility, err := newCompatibilityHandler(
		fakeCompatibilityTokens{grant: grant}, scoped, engine,
		func() time.Time { return time.Unix(1_700_000_000, 0) },
	)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	compatibility.register(mux)
	return instrumentRequests(problemBoundary(mux), newApplicationMetrics()), engine
}

func compatibilityRequest(handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func engineAgentID(raw string) agents.AgentID {
	id, _ := agents.ParseID(raw)
	return agents.AgentID(id)
}

func engineRunID(raw string) agents.RunID {
	id, _ := agents.ParseID(raw)
	return agents.RunID(id)
}

func TestCompatibilityRechecksTokenBeforeAnyDelivery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			tokenID, _ := chattokens.ParseID(testedTokenID)
			grant, _ := chattokens.NewGrant(tokenID, []agents.AgentID{engineAgentID(testedAgentID)})
			tokens := &mutableCompatibilityToken{grant: grant}
			engine := &fakeCompatibilityEngine{result: agents.ExecuteResult{Markdown: "must not escape"}, afterExecute: func() { tokens.revoked = true }}
			handler, err := newCompatibilityHandler(tokens, &fakeScopedAgents{selected: agents.Agent{ID: engineAgentID(testedAgentID), Key: "docs"}}, engine, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			handler.register(mux)
			response := compatibilityRequest(mux, http.MethodPost, compatibilityCompletionsPath, fmt.Sprintf(`{"model":"agent:docs","messages":[{"role":"user","content":"question"}],"stream":%t}`, stream), map[string]string{"Authorization": "Bearer token", "Content-Type": "application/json"})
			if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "must not escape") || strings.Contains(response.Body.String(), "data:") {
				t.Fatalf("revoked token delivery: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

type mutableCompatibilityToken struct {
	grant   chattokens.Grant
	revoked bool
}

func (token *mutableCompatibilityToken) Authenticate(context.Context, string) (chattokens.Grant, error) {
	if token.revoked {
		return chattokens.Grant{}, chattokens.ErrUnauthorized
	}
	return token.grant, nil
}
