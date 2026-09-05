package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/chattokens"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

func TestChatAccessTokenCreateResolvesMetadataBeforeIssueAndReplaysWithoutSecret(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	sessions := &fakeSessionService{session: authenticated.Session}
	agentID := mustAgentID(t, "10000000-0000-4000-8000-000000000001")
	knowledgeBaseID := agents.KnowledgeBaseID(mustAgentID(t, "20000000-0000-4000-8000-000000000001"))
	tokenID, err := chattokens.ParseID("30000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(24 * time.Hour)
	secret, err := security.NewSecretValue("ref0_chat_test-secret-once")
	if err != nil {
		t.Fatal(err)
	}
	value := chattokens.Token{
		ID: tokenID, Prefix: "ref0_chat_test", Label: "Open WebUI", AgentIDs: []agents.AgentID{agentID},
		CreatedByOperator: authenticated.Session.Operator.ID, CreatedAt: createdAt, ExpiresAt: expiresAt,
	}
	description := agents.ScopeDescription{
		AgentID: agentID, AgentKey: "documentation", KnowledgeBaseIDs: []agents.KnowledgeBaseID{knowledgeBaseID},
		EffectiveAccess: agents.Restricted, Ready: true,
	}
	scopes := &fakeAgentScopeService{descriptions: []agents.ScopeDescription{description}}
	tokens := &fakeChatTokenRouteService{issued: chattokens.Issued{Token: value, Secret: secret}}
	handler := chatTokenRoutesTestHandler(t, sessions, tokens, scopes)
	headers := map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()), csrfHeaderName: authenticated.CSRFToken,
		"Idempotency-Key": "issue-open-webui",
	}
	body := `{"label":"Open WebUI","agent_ids":["` + agentID.String() + `"],"expires_at":"` + expiresAt.Format(time.RFC3339Nano) + `"}`

	scopes.err = errors.New("scope-metadata-unavailable")
	failed := authRequest(t, handler, http.MethodPost, chatTokensPath, body, headers)
	if failed.Code != http.StatusInternalServerError || tokens.createCalls != 0 {
		t.Fatalf("metadata failure=%d %s create calls=%d", failed.Code, failed.Body.String(), tokens.createCalls)
	}

	scopes.err = nil
	created := authRequest(t, handler, http.MethodPost, chatTokensPath, body, headers)
	if created.Code != http.StatusCreated || created.Header().Get("Cache-Control") != "no-store" || tokens.createCalls != 1 {
		t.Fatalf("created=%d headers=%v body=%s calls=%d", created.Code, created.Header(), created.Body.String(), tokens.createCalls)
	}
	var createdBody issuedChatTokenResponse
	if err = json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	if createdBody.Secret != secret.Reveal() || len(createdBody.AgentScopes) != 1 ||
		createdBody.AgentScopes[0].AgentKey != description.AgentKey || !createdBody.AgentScopes[0].Ready ||
		createdBody.AgentScopes[0].EffectiveAccess != "restricted" ||
		len(createdBody.AgentScopes[0].KnowledgeBaseIDs) != 1 || createdBody.AgentScopes[0].KnowledgeBaseIDs[0] != knowledgeBaseID.String() {
		t.Fatalf("created token response=%#v", createdBody)
	}

	tokens.issued.Secret = nil
	tokens.createErr = chattokens.ErrSecretAlreadyIssued
	replayed := authRequest(t, handler, http.MethodPost, chatTokensPath, body, headers)
	if replayed.Code != http.StatusConflict || replayed.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("replay=%d headers=%v body=%s", replayed.Code, replayed.Header(), replayed.Body.String())
	}
	var replay ChatTokenReplayProblem
	if err = json.Unmarshal(replayed.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Code != "secret_already_issued" || replay.Token.ID != value.ID.String() ||
		len(replay.Token.AgentScopes) != 1 || replay.Token.AgentScopes[0].AgentKey != description.AgentKey ||
		containsJSONField(replayed.Body.Bytes(), "secret") {
		t.Fatalf("replay problem=%#v body=%s", replay, replayed.Body.String())
	}
}

func TestChatAccessTokenListUsesBoundedSummariesWithoutResolvingAgentScopes(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	firstID, _ := chattokens.ParseID("30000000-0000-4000-8000-000000000001")
	secondID, _ := chattokens.ParseID("30000000-0000-4000-8000-000000000002")
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	tokens := &fakeChatTokenRouteService{page: chattokens.Page{Summaries: []chattokens.Summary{
		{ID: firstID, Prefix: "first", Label: "First", AgentCount: 2048, CreatedByOperator: authenticated.Session.Operator.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: secondID, Prefix: "second", Label: "Second", AgentCount: 1, CreatedByOperator: authenticated.Session.Operator.ID, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)},
	}}}
	scopes := &fakeAgentScopeService{err: errors.New("catalog must not be called")}
	handler := chatTokenRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, tokens, scopes)
	response := authRequest(t, handler, http.MethodGet, chatTokensPath, "", map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()),
	})
	if response.Code != http.StatusOK || scopes.calls != 0 ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"agent_count":2048`)) ||
		bytes.Contains(response.Body.Bytes(), []byte(`"agent_ids"`)) || bytes.Contains(response.Body.Bytes(), []byte(`"agent_scopes"`)) {
		t.Fatalf("list=%d %s scope calls=%d received=%v", response.Code, response.Body.String(), scopes.calls, scopes.received)
	}
}

func TestChatAccessTokenRevokeReturnsCommittedSummaryWithoutCatalogLookup(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	tokenID, _ := chattokens.ParseID("30000000-0000-4000-8000-000000000001")
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	tokens := &fakeChatTokenRouteService{revoked: chattokens.Summary{
		ID: tokenID, Prefix: "ref0_chat_test", Label: "Open WebUI", AgentCount: 2,
		CreatedByOperator: authenticated.Session.Operator.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour), RevokedAt: &now,
	}}
	scopes := &fakeAgentScopeService{err: errors.New("catalog unavailable after commit")}
	handler := chatTokenRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, tokens, scopes)
	response := authRequest(t, handler, http.MethodDelete, chatTokensPath+"/"+tokenID.String(), "", map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()), csrfHeaderName: authenticated.CSRFToken,
		"Idempotency-Key": "revoke-open-webui",
	})
	if response.Code != http.StatusOK || scopes.calls != 0 || tokens.revokeCalls != 1 ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"agent_count":2`)) ||
		bytes.Contains(response.Body.Bytes(), []byte(`"agent_scopes"`)) {
		t.Fatalf("revoke=%d %s scope calls=%d revoke calls=%d", response.Code, response.Body.String(), scopes.calls, tokens.revokeCalls)
	}
}

func TestChatAccessTokenScopePreviewUsesCanonicalCurrentEffectiveScopeWithoutIssuing(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	firstAgent := mustAgentID(t, "10000000-0000-4000-8000-000000000001")
	secondAgent := mustAgentID(t, "10000000-0000-4000-8000-000000000002")
	firstKnowledge := agents.KnowledgeBaseID(mustAgentID(t, "20000000-0000-4000-8000-000000000001"))
	secondKnowledge := agents.KnowledgeBaseID(mustAgentID(t, "20000000-0000-4000-8000-000000000002"))
	thirdKnowledge := agents.KnowledgeBaseID(mustAgentID(t, "20000000-0000-4000-8000-000000000003"))
	scopes := &fakeAgentScopeService{descriptions: []agents.ScopeDescription{
		{
			AgentID: secondAgent, AgentKey: "second", EffectiveAccess: agents.Public, Ready: true,
			KnowledgeBaseIDs: []agents.KnowledgeBaseID{thirdKnowledge, secondKnowledge},
		},
		{
			AgentID: firstAgent, AgentKey: "first", EffectiveAccess: agents.Restricted, Ready: false,
			KnowledgeBaseIDs: []agents.KnowledgeBaseID{secondKnowledge, firstKnowledge},
		},
	}}
	tokens := &fakeChatTokenRouteService{}
	handler := chatTokenRoutesTestHandler(
		t, &fakeSessionService{session: authenticated.Session}, tokens, scopes,
	)
	body := `{"agent_ids":["` + secondAgent.String() + `","` + firstAgent.String() + `"]}`
	response := authRequest(t, handler, http.MethodPost, chatTokenPreviewPath, body, map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()),
	})
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || tokens.createCalls != 0 {
		t.Fatalf("preview=%d headers=%v body=%s token calls=%d", response.Code, response.Header(), response.Body.String(), tokens.createCalls)
	}
	var preview previewChatTokenScopesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preview.AgentIDs, []string{firstAgent.String(), secondAgent.String()}) ||
		len(preview.AgentScopes) != 2 || preview.AgentScopes[0].AgentID != firstAgent.String() ||
		!reflect.DeepEqual(preview.AgentScopes[0].KnowledgeBaseIDs, []string{secondKnowledge.String(), firstKnowledge.String()}) ||
		preview.AgentScopes[1].AgentID != secondAgent.String() ||
		!reflect.DeepEqual(preview.AgentScopes[1].KnowledgeBaseIDs, []string{thirdKnowledge.String(), secondKnowledge.String()}) ||
		!reflect.DeepEqual(preview.KnowledgeBaseIDs, []string{firstKnowledge.String(), secondKnowledge.String(), thirdKnowledge.String()}) ||
		preview.EffectiveAccess != "restricted" || preview.Ready {
		t.Fatalf("preview response=%#v", preview)
	}
	if !reflect.DeepEqual(scopes.received, []agents.AgentID{firstAgent, secondAgent}) {
		t.Fatalf("DescribeScopes received=%v", scopes.received)
	}
}

func TestChatAccessTokenScopePreviewRejectsInvalidMissingAndUnauthenticatedRequests(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	validID := "10000000-0000-4000-8000-000000000001"
	scopes := &fakeAgentScopeService{}
	handler := chatTokenRoutesTestHandler(
		t, &fakeSessionService{session: authenticated.Session}, &fakeChatTokenRouteService{}, scopes,
	)

	unauthenticated := authRequest(t, handler, http.MethodPost, chatTokenPreviewPath, `{"agent_ids":["`+validID+`"]}`, nil)
	if unauthenticated.Code != http.StatusUnauthorized || scopes.calls != 0 {
		t.Fatalf("unauthenticated preview=%d %s calls=%d", unauthenticated.Code, unauthenticated.Body.String(), scopes.calls)
	}

	headers := map[string]string{"Cookie": sessionCookie(authenticated.Token.Reveal())}
	for _, body := range []string{
		`{"agent_ids":[]}`,
		`{"agent_ids":["00000000-0000-0000-0000-000000000000"]}`,
		`{"agent_ids":["` + validID + `","` + validID + `"]}`,
		`{"agent_ids":["10000000-0000-4000-8000-00000000000A"]}`,
	} {
		response := authRequest(t, handler, http.MethodPost, chatTokenPreviewPath, body, headers)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid preview=%d %s body=%s", response.Code, response.Body.String(), body)
		}
	}
	if scopes.calls != 0 {
		t.Fatalf("invalid previews reached catalog %d times", scopes.calls)
	}

	scopes.err = agents.ErrNotFound
	missing := authRequest(t, handler, http.MethodPost, chatTokenPreviewPath, `{"agent_ids":["`+validID+`"]}`, headers)
	if missing.Code != http.StatusNotFound || problemDetail(t, missing) != "Agent resource not found." {
		t.Fatalf("missing preview=%d %s", missing.Code, missing.Body.String())
	}
}

func TestChatAccessTokenCreateRejectsInvalidAgentScopesBeforeCatalog(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	tokens := &fakeChatTokenRouteService{}
	scopes := &fakeAgentScopeService{}
	handler := chatTokenRoutesTestHandler(
		t, &fakeSessionService{session: authenticated.Session}, tokens, scopes,
	)
	headers := map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()), csrfHeaderName: authenticated.CSRFToken,
		"Idempotency-Key": "invalid-scopes",
	}
	validID := "10000000-0000-4000-8000-000000000001"
	overLimit := make([]string, chattokens.MaxAgentScopesPerToken+1)
	for index := range overLimit {
		overLimit[index] = validID
	}
	tests := []struct {
		name string
		ids  []string
	}{
		{name: "empty", ids: []string{}},
		{name: "zero UUID", ids: []string{"00000000-0000-0000-0000-000000000000"}},
		{name: "duplicate", ids: []string{validID, validID}},
		{name: "over limit", ids: overLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]any{
				"label": "Invalid", "agent_ids": test.ids,
				"expires_at": time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatal(err)
			}
			response := authRequest(t, handler, http.MethodPost, chatTokensPath, string(encoded), headers)
			if response.Code != http.StatusUnprocessableEntity || scopes.calls != 0 || tokens.createCalls != 0 {
				t.Fatalf(
					"invalid scopes=%d %s metadata calls=%d create calls=%d",
					response.Code, response.Body.String(), scopes.calls, tokens.createCalls,
				)
			}
		})
	}
}

func TestAgentMutationCandidateNotReadyProblemPreservesTransactionalSnapshot(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	agentID := mustAgentID(t, "10000000-0000-4000-8000-000000000001")
	knowledgeBaseID := agents.KnowledgeBaseID(mustAgentID(t, "20000000-0000-4000-8000-000000000001"))
	readiness := agents.Readiness{
		Ready: false, EffectiveAccess: agents.Restricted,
		Issues: []agents.ReadinessIssue{{Code: agents.IssueKnowledgeBaseUnpublished, KnowledgeBaseID: &knowledgeBaseID}},
	}
	service := &fakeAgentRouteService{
		replaceErr:   fakeAgentNotReadyError{readiness: readiness},
		lifecycleErr: fakeAgentNotReadyError{readiness: readiness},
	}
	handler := agentRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service)
	headers := map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()), csrfHeaderName: authenticated.CSRFToken,
		"Idempotency-Key": "candidate-not-ready",
	}
	configuration := `{"display_name":"Documentation","description":"","response_language":"English","identity_instructions":"Answer from documentation.","model_profile_id":"30000000-0000-4000-8000-000000000001","reasoning_effort":"none","answer_mode":"single_pass","behavioral_instructions":"","evidence_access":"wiki_only","refusal_markdown":"Unable to answer.","max_tool_calls":0,"max_answer_tokens":256,"knowledge_base_ids":["` + knowledgeBaseID.String() + `"]}`
	tests := []struct {
		name, method, path, body string
	}{
		{
			name: "configuration", method: http.MethodPut,
			path: agentsPath + "/" + agentID.String() + "/configuration",
			body: `{"expected_version":4,"configuration":` + configuration + `}`,
		},
		{
			name: "lifecycle", method: http.MethodPatch,
			path: agentsPath + "/" + agentID.String() + "/lifecycle",
			body: `{"expected_version":4,"lifecycle":"active"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := authRequest(t, handler, test.method, test.path, test.body, headers)
			if response.Code != http.StatusConflict || response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("candidate response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			var problem AgentCandidateNotReadyProblem
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if problem.Code != "candidate_not_ready" || problem.Readiness.Ready ||
				problem.Readiness.EffectiveAccess != "restricted" || len(problem.Readiness.Issues) != 1 ||
				problem.Readiness.Issues[0].Code != "knowledge_base_unpublished" ||
				problem.Readiness.Issues[0].KnowledgeBaseID == nil || *problem.Readiness.Issues[0].KnowledgeBaseID != knowledgeBaseID.String() {
				t.Fatalf("candidate problem=%#v", problem)
			}
		})
	}

	service.replaceErr = agents.ErrConflict
	stale := authRequest(t, handler, http.MethodPut, tests[0].path, tests[0].body, headers)
	if stale.Code != http.StatusConflict || bytes.Contains(stale.Body.Bytes(), []byte("candidate_not_ready")) ||
		problemDetail(t, stale) != "Agent state conflicts with the request." {
		t.Fatalf("stale conflict was mislabeled=%d %s", stale.Code, stale.Body.String())
	}
}

func TestAgentVersionCursorRoundTripAndRejectsMalformedValues(t *testing.T) {
	versionID := agents.VersionID(mustAgentID(t, "40000000-0000-4000-8000-000000000001"))
	want := agents.VersionPageCursor{VersionNumber: 17, VersionID: versionID}
	encoded := encodeAgentVersionCursor(want)
	got, err := decodeAgentVersionCursor(&encoded)
	if err != nil || got == nil || *got != want {
		t.Fatalf("version cursor=%#v err=%v", got, err)
	}
	for _, malformed := range []string{
		"not-base64",
		"eyJ2ZXJzaW9uX251bWJlciI6MCwiaWQiOiI0MDAwMDAwMC0wMDAwLTQwMDAtODAwMC0wMDAwMDAwMDAwMDEifQ",
		"eyJ2ZXJzaW9uX251bWJlciI6MTcsImlkIjoiNDAwMDAwMDAtMDAwMC00MDAwLTgwMDAtMDAwMDAwMDAwMDAxIiwiZXh0cmEiOnRydWV9",
	} {
		if decoded, decodeErr := decodeAgentVersionCursor(&malformed); decodeErr == nil || decoded != nil {
			t.Fatalf("malformed version cursor %q decoded as %#v err=%v", malformed, decoded, decodeErr)
		}
	}
}

func containsJSONField(raw []byte, field string) bool {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	_, exists := value[field]
	return exists
}

func mustAgentID(t *testing.T, raw string) agents.AgentID {
	t.Helper()
	id, err := agents.ParseID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return agents.AgentID(id)
}

type fakeAgentScopeService struct {
	descriptions []agents.ScopeDescription
	err          error
	calls        int
	received     []agents.AgentID
}

func (service *fakeAgentScopeService) DescribeScopes(_ context.Context, ids []agents.AgentID) ([]agents.ScopeDescription, error) {
	service.calls++
	service.received = append([]agents.AgentID(nil), ids...)
	return append([]agents.ScopeDescription(nil), service.descriptions...), service.err
}

type fakeChatTokenRouteService struct {
	page        chattokens.Page
	issued      chattokens.Issued
	revoked     chattokens.Summary
	createErr   error
	createCalls int
	revokeCalls int
}

type fakeAgentNotReadyError struct{ readiness agents.Readiness }

func (err fakeAgentNotReadyError) Error() string { return "candidate is not ready" }
func (err fakeAgentNotReadyError) Is(target error) bool {
	return target == agents.ErrNotReady || target == agents.ErrConflict
}
func (err fakeAgentNotReadyError) Readiness() agents.Readiness { return err.readiness }

type fakeAgentRouteService struct {
	replaceErr   error
	lifecycleErr error
}

func (*fakeAgentRouteService) DescribeScopes(context.Context, []agents.AgentID) ([]agents.ScopeDescription, error) {
	return nil, nil
}
func (*fakeAgentRouteService) ListPage(context.Context, *agents.PageCursor, int) (agents.Page, error) {
	return agents.Page{}, nil
}
func (*fakeAgentRouteService) Get(context.Context, agents.AgentID) (agents.Agent, error) {
	return agents.Agent{}, agents.ErrNotFound
}
func (*fakeAgentRouteService) Create(context.Context, agents.CreateCommand, auth.OperatorID, string) (agents.Agent, error) {
	return agents.Agent{}, nil
}
func (service *fakeAgentRouteService) ReplaceConfiguration(context.Context, agents.ReplaceConfigurationCommand, auth.OperatorID, string) (agents.Agent, error) {
	return agents.Agent{}, service.replaceErr
}
func (service *fakeAgentRouteService) SetLifecycle(context.Context, agents.SetLifecycleCommand, auth.OperatorID, string) (agents.Agent, error) {
	return agents.Agent{}, service.lifecycleErr
}
func (*fakeAgentRouteService) ListVersions(context.Context, agents.AgentID, *agents.VersionPageCursor, int) (agents.VersionPage, error) {
	return agents.VersionPage{}, nil
}
func (*fakeAgentRouteService) GetVersion(context.Context, agents.AgentID, agents.VersionID) (agents.Version, error) {
	return agents.Version{}, nil
}
func (*fakeAgentRouteService) EvaluateReadiness(context.Context, agents.AgentID) (agents.Readiness, error) {
	return agents.Readiness{}, nil
}
func (*fakeAgentRouteService) ListRuns(context.Context, agents.AgentID, *agents.RunPageCursor, int) (agents.RunPage, error) {
	return agents.RunPage{}, nil
}
func (*fakeAgentRouteService) GetRun(context.Context, agents.AgentID, agents.RunID) (agents.RunDetail, error) {
	return agents.RunDetail{}, nil
}

func (service *fakeChatTokenRouteService) List(context.Context, *chattokens.PageCursor, int) (chattokens.Page, error) {
	return service.page, nil
}

func (service *fakeChatTokenRouteService) Create(
	_ context.Context,
	_ chattokens.CreateCommand,
	_ auth.OperatorID,
	_ string,
) (chattokens.Issued, error) {
	service.createCalls++
	return service.issued, service.createErr
}

func (service *fakeChatTokenRouteService) Revoke(context.Context, chattokens.ID, auth.OperatorID, string) (chattokens.Summary, error) {
	service.revokeCalls++
	return service.revoked, nil
}

func chatTokenRoutesTestHandler(
	t *testing.T,
	sessions auth.SessionService,
	tokens chatTokenService,
	scopes agentScopeService,
) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ref0 test", "test")
	config.CreateHooks = nil
	config.Transformers = nil
	api := humago.New(mux, config)
	registerChatTokens(api, sessions, tokens, scopes)
	return problemBoundary(mux)
}

func agentRoutesTestHandler(t *testing.T, sessions auth.SessionService, service agentService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ref0 test", "test")
	config.CreateHooks = nil
	config.Transformers = nil
	api := humago.New(mux, config)
	registerAgents(api, sessions, service)
	return problemBoundary(mux)
}

var _ agentScopeService = (*fakeAgentScopeService)(nil)
var _ chatTokenService = (*fakeChatTokenRouteService)(nil)
var _ agentService = (*fakeAgentRouteService)(nil)
