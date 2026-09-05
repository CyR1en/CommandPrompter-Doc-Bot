package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	providerdomain "github.com/cyr1en/ref0/internal/providers"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type fakeProviderService struct {
	endpoint      providerdomain.Endpoint
	profile       providerdomain.Profile
	discovery     providerdomain.DiscoveryRun
	probe         providerdomain.ProbeRun
	assignment    providerdomain.Assignment
	error         error
	actor         providerdomain.ActorID
	key           string
	configuration providerdomain.Configuration
	checks        []providerdomain.ProbeCheck
}

func (service *fakeProviderService) ListEndpoints(context.Context) ([]providerdomain.Endpoint, error) {
	return []providerdomain.Endpoint{service.endpoint}, service.error
}
func (service *fakeProviderService) GetEndpoint(context.Context, providerdomain.EndpointID) (providerdomain.Endpoint, error) {
	return service.endpoint, service.error
}
func (service *fakeProviderService) CreateEndpoint(_ context.Context, command providerdomain.CreateEndpoint, actor providerdomain.ActorID, key string) (providerdomain.Endpoint, error) {
	service.actor, service.key, service.configuration = actor, key, command.Configuration
	return service.endpoint, service.error
}
func (service *fakeProviderService) UpdateEndpoint(_ context.Context, command providerdomain.UpdateEndpoint, actor providerdomain.ActorID, key string) (providerdomain.Endpoint, error) {
	service.actor, service.key, service.configuration = actor, key, command.Configuration
	return service.endpoint, service.error
}
func (service *fakeProviderService) ScheduleDiscovery(context.Context, providerdomain.ScheduleDiscovery, providerdomain.ActorID, string) (providerdomain.DiscoveryRun, error) {
	return service.discovery, service.error
}
func (service *fakeProviderService) ListProfiles(context.Context, *providerdomain.EndpointID) ([]providerdomain.Profile, error) {
	return []providerdomain.Profile{service.profile}, service.error
}
func (service *fakeProviderService) GetProfile(context.Context, providerdomain.ProfileID) (providerdomain.Profile, error) {
	return service.profile, service.error
}
func (service *fakeProviderService) CreateProfile(context.Context, providerdomain.CreateProfile, providerdomain.ActorID, string) (providerdomain.Profile, error) {
	return service.profile, service.error
}
func (service *fakeProviderService) EditProfile(context.Context, providerdomain.EditProfile, providerdomain.ActorID, string) (providerdomain.Profile, error) {
	return service.profile, service.error
}
func (service *fakeProviderService) ScheduleProbe(_ context.Context, command providerdomain.ScheduleProbe, _ providerdomain.ActorID, _ string) (providerdomain.ProbeRun, error) {
	service.checks = command.SelectedChecks
	return service.probe, service.error
}
func (service *fakeProviderService) ListAssignments(context.Context, providerdomain.KnowledgeBaseID) ([]providerdomain.Assignment, error) {
	return []providerdomain.Assignment{service.assignment}, service.error
}
func (service *fakeProviderService) Assign(context.Context, providerdomain.AssignModel, providerdomain.ActorID, string) (providerdomain.Assignment, error) {
	return service.assignment, service.error
}

func TestProviderRoutesAuthenticateValidateAndPreservePublicContract(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	sessions := &fakeSessionService{session: authenticated.Session}
	service := providerRouteService(t)
	handler := providerRoutesTestHandler(t, sessions, service)
	cookie := sessionCookie(authenticated.Token.Reveal())
	csrf := authenticated.CSRFToken

	unauthorized := authRequest(t, handler, http.MethodGet, providerEndpointsPath, "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	listed := authRequest(t, handler, http.MethodGet, providerEndpointsPath, "", map[string]string{"Cookie": cookie})
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), service.endpoint.ID.String()) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	got := authRequest(t, handler, http.MethodGet, providerEndpointsPath+"/"+service.endpoint.ID.String(), "", map[string]string{"Cookie": cookie})
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), service.endpoint.ID.String()) {
		t.Fatalf("get=%d %s", got.Code, got.Body.String())
	}

	body := `{"display_name":" Primary models ","base_url":"https://models.example/","credential_id":"` +
		service.endpoint.Configuration.CredentialID.String() + `","headers":{"X-Tenant":"docs"}}`
	missingCSRF := authRequest(t, handler, http.MethodPost, providerEndpointsPath, body,
		map[string]string{"Cookie": cookie, "Idempotency-Key": "provider-create"})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF=%d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	created := authRequest(t, handler, http.MethodPost, providerEndpointsPath, body, map[string]string{
		"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": " provider-create ",
	})
	if created.Code != http.StatusCreated || service.configuration.DisplayName != "Primary models" ||
		service.configuration.DisplayKey != "primary models" || service.configuration.BaseURL != "https://models.example/v1" ||
		service.key != "provider-create" || service.actor != providerdomain.ActorID(authenticated.Session.Operator.ID) {
		t.Fatalf("created=%d %s config=%+v key=%q actor=%s", created.Code, created.Body.String(), service.configuration, service.key, service.actor)
	}

	secret := "header-secret-sentinel"
	badHeader := authRequest(t, handler, http.MethodPost, providerEndpointsPath,
		`{"display_name":"Unsafe","base_url":"https://models.example/v1","headers":{"Authorization":"`+secret+`"}}`,
		map[string]string{"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "bad-header"})
	if badHeader.Code != http.StatusUnprocessableEntity || strings.Contains(badHeader.Body.String(), secret) {
		t.Fatalf("bad header=%d %s", badHeader.Code, badHeader.Body.String())
	}
	malformedSecret := "malformed-secret-sentinel"
	malformed := authRequest(t, handler, http.MethodPost, providerEndpointsPath,
		`{"display_name":"Unsafe","base_url":"https://models.example/v1","headers":{"Authorization":"`+malformedSecret+`"},"allow_http":"false"}`,
		map[string]string{"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "malformed-secret"})
	if malformed.Code != http.StatusUnprocessableEntity || strings.Contains(malformed.Body.String(), malformedSecret) {
		t.Fatalf("malformed secret=%d %s", malformed.Code, malformed.Body.String())
	}

	probePath := providerEndpointsPath + "/" + service.endpoint.ID.String() + "/probe"
	noConsent := authRequest(t, handler, http.MethodPost, probePath,
		`{"profile_id":"`+service.profile.ID.String()+`","expected_version":1,"selected_checks":["chat"],"acknowledge_cost":false}`,
		map[string]string{"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "probe-false"})
	if noConsent.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no consent=%d %s", noConsent.Code, noConsent.Body.String())
	}
	probe := authRequest(t, handler, http.MethodPost, probePath,
		`{"profile_id":"`+service.profile.ID.String()+`","expected_version":1,"selected_checks":["chat","tools"],"acknowledge_cost":true}`,
		map[string]string{"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "probe"})
	if probe.Code != http.StatusAccepted || !strings.Contains(probe.Body.String(), `"selected_checks":["chat","tools"]`) ||
		!reflect.DeepEqual(service.checks, []providerdomain.ProbeCheck{providerdomain.ProbeChat, providerdomain.ProbeTools}) {
		t.Fatalf("probe=%d %s checks=%v", probe.Code, probe.Body.String(), service.checks)
	}
}

func TestProviderRoutesExposeAllTwelveOpenAPIOperationsAndGenericErrors(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service := providerRouteService(t)
	handler := providerRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service)
	document := openAPIDocument(t, handler)
	paths := document["paths"].(map[string]any)
	want := map[string][]string{
		"/api/v1/provider-endpoints":                                           {"get", "post"},
		"/api/v1/provider-endpoints/{endpoint_id}":                             {"get", "patch"},
		"/api/v1/provider-endpoints/{endpoint_id}/discover":                    {"post"},
		"/api/v1/provider-endpoints/{endpoint_id}/probe":                       {"post"},
		"/api/v1/model-profiles":                                               {"get", "post"},
		"/api/v1/model-profiles/{profile_id}":                                  {"get", "patch"},
		"/api/v1/knowledge-bases/{knowledge_base_id}/model-assignments":        {"get"},
		"/api/v1/knowledge-bases/{knowledge_base_id}/model-assignments/{role}": {"put"},
	}
	count := 0
	for path, methods := range want {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing OpenAPI path %s", path)
		}
		for _, method := range methods {
			if _, exists := item[method]; !exists {
				t.Fatalf("missing OpenAPI operation %s %s", method, path)
			}
			count++
		}
	}
	if count != 12 {
		t.Fatalf("operation count=%d", count)
	}
	writeOperations := []struct {
		path   string
		method string
	}{
		{providerEndpointsPath, "post"},
		{providerEndpointsPath + "/{endpoint_id}", "patch"},
		{providerEndpointsPath + "/{endpoint_id}/discover", "post"},
		{providerEndpointsPath + "/{endpoint_id}/probe", "post"},
		{modelProfilesPath, "post"},
		{modelProfilesPath + "/{profile_id}", "patch"},
		{"/api/v1/knowledge-bases/{knowledge_base_id}/model-assignments/{role}", "put"},
	}
	for _, operation := range writeOperations {
		_ = documentedProviderRequestSchema(t, document, operation.path, operation.method)
	}
	probeSchema := documentedProviderRequestSchema(t, document, providerEndpointsPath+"/{endpoint_id}/probe", "post")
	properties := probeSchema["properties"].(map[string]any)
	checks := properties["selected_checks"].(map[string]any)
	items := checks["items"].(map[string]any)
	if checks["uniqueItems"] != true || len(items["enum"].([]any)) != 4 {
		t.Fatalf("probe checks schema=%v", checks)
	}
	if properties["acknowledge_cost"].(map[string]any)["const"] != true {
		t.Fatalf("probe consent schema=%v", properties["acknowledge_cost"])
	}

	cookie := sessionCookie(authenticated.Token.Reveal())
	service.error = providerdomain.ErrNotFound
	missing := authRequest(t, handler, http.MethodGet, providerEndpointsPath+"/"+service.endpoint.ID.String(), "", map[string]string{"Cookie": cookie})
	if missing.Code != http.StatusNotFound || problemDetail(t, missing) != "Provider resource not found." {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
	service.error = idempotency.ErrConflict
	csrf := authenticated.CSRFToken
	conflict := authRequest(t, handler, http.MethodPost, providerEndpointsPath,
		`{"display_name":"Primary","base_url":"https://models.example/v1"}`,
		map[string]string{"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "conflict"})
	if conflict.Code != http.StatusConflict || problemDetail(t, conflict) != "Idempotency key conflicts with a different request." {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	service.error = errors.New("database included private detail")
	internal := authRequest(t, handler, http.MethodGet, providerEndpointsPath, "", map[string]string{"Cookie": cookie})
	if internal.Code != http.StatusInternalServerError || strings.Contains(internal.Body.String(), "database included") {
		t.Fatalf("internal=%d %s", internal.Code, internal.Body.String())
	}
}

func providerRoutesTestHandler(t *testing.T, sessions auth.SessionService, service ProviderService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ref0 test", "test")
	config.CreateHooks, config.Transformers = nil, nil
	api := humago.New(mux, config)
	RegisterProviderRoutes(api, sessions, service)
	return problemBoundary(mux)
}

func providerRouteService(t *testing.T) *fakeProviderService {
	t.Helper()
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	credentialID := credentials.ID{9}
	responses := "responses"
	origins := map[string]providerdomain.MetadataOrigin{}
	for _, field := range []string{"model_id", "transport", "context_window_tokens", "max_output_tokens", "supports_streaming", "supports_tools", "supports_structured_output", "supports_temperature", "reasoning_transport", "reasoning_mapping", "timeout_seconds", "max_retries", "max_concurrent_tasks", "extra_body"} {
		origins[field] = providerdomain.OriginOperator
	}
	contextTokens, outputTokens, truth := int32(16_000), int32(2_000), true
	settings := providerdomain.Settings{
		Transport: providerdomain.ChatCompletions, ContextWindowTokens: &contextTokens,
		MaxOutputTokens: &outputTokens, SupportsStreaming: &truth, SupportsTools: &truth,
		SupportsStructuredOutput: &truth, SupportsTemperature: &truth,
		ReasoningTransport: providerdomain.ReasoningEffort, TimeoutSeconds: 45, MaxRetries: 2,
		MaxConcurrentTasks: 2,
		ExtraBody:          map[string]any{"seed": 7}, MetadataOrigin: origins,
	}
	endpoint := providerdomain.Endpoint{
		ID: providerdomain.EndpointID{1}, Configuration: providerdomain.Configuration{
			DisplayName: "Primary", DisplayKey: "primary", BaseURL: "https://models.example/v1",
			CredentialID: &credentialID, Headers: map[string]string{"X-Tenant": "docs"},
			ChatCompletionsPath: "chat/completions", ResponsesPath: &responses, ModelsPath: "models",
		}, Lifecycle: providerdomain.Active, Health: providerdomain.Unknown,
		Version: 1, ConfigurationVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	profile := providerdomain.Profile{
		ID: providerdomain.ProfileID{2}, EndpointID: endpoint.ID, ModelID: "manual-model",
		Availability: providerdomain.Manual, Version: 1, CreatedAt: now, UpdatedAt: now,
		CurrentVersion: providerdomain.ProfileVersion{ID: providerdomain.ProfileVersionID{3},
			ProfileID: providerdomain.ProfileID{2}, VersionNumber: 1, ConfigurationVersion: 1,
			Settings: settings, Source: providerdomain.VersionOperator, CreatedAt: now},
	}
	actor := providerdomain.ActorID{4}
	discovery := providerdomain.DiscoveryRun{ID: providerdomain.DiscoveryRunID{5}, EndpointID: endpoint.ID,
		JobID: jobs.JobID{6}, CapturedConfigurationVersion: 1, RequestedByActorID: actor,
		Status: providerdomain.CapturePending, ModelIDs: []string{}, CreatedAt: now}
	probe := providerdomain.ProbeRun{ID: providerdomain.ProbeRunID{7}, ProfileID: profile.ID,
		JobID: jobs.JobID{8}, CapturedConfigurationVersion: 1, CapturedProfileVersionID: profile.CurrentVersion.ID,
		RequestedByActorID: actor, SelectedChecks: []providerdomain.ProbeCheck{providerdomain.ProbeChat, providerdomain.ProbeTools},
		AcknowledgeCost: true, Status: providerdomain.CapturePending, CreatedAt: now}
	assignment := providerdomain.Assignment{ID: providerdomain.AssignmentID{10}, KnowledgeBaseID: providerdomain.KnowledgeBaseID{11},
		Role: providerdomain.DocumentationPlanner, ProfileID: profile.ID, ReasoningEffort: providerdomain.EffortNone,
		AnswerMode: providerdomain.ToolCalling, Version: 1, CreatedAt: now, UpdatedAt: now}
	return &fakeProviderService{endpoint: endpoint, profile: profile, discovery: discovery, probe: probe, assignment: assignment}
}

var _ ProviderService = (*fakeProviderService)(nil)

func documentedProviderRequestSchema(t *testing.T, document map[string]any, path, method string) map[string]any {
	t.Helper()
	paths := document["paths"].(map[string]any)
	operation := paths[path].(map[string]any)[method].(map[string]any)
	body := operation["requestBody"].(map[string]any)
	if body["required"] != true {
		t.Fatalf("request body is not documented as required for %s %s", method, path)
	}
	for _, value := range operation["parameters"].([]any) {
		parameter := value.(map[string]any)
		if strings.EqualFold(parameter["name"].(string), "Content-Type") {
			t.Fatalf("content type leaked into documented parameters for %s %s", method, path)
		}
	}
	schema := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if reference, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(reference, "#/components/schemas/")
		return document["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)
	}
	return schema
}
