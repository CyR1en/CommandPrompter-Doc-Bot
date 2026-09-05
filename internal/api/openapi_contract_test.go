package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/cyr1en/ref0/internal/chattokens"
	"github.com/prometheus/client_golang/prometheus"
)

const openAPIOutputEnvironment = "REF0_OPENAPI_OUTPUT"

const pythonControlPlaneTopology = `
GET /api/v1/agents list_agents_api_v1_agents_get
POST /api/v1/agents create_agent_api_v1_agents_post
GET /api/v1/agents/{agent_id} get_agent_api_v1_agents__agent_id__get
PUT /api/v1/agents/{agent_id}/configuration replace_agent_configuration_api_v1_agents__agent_id__configuration_put
PATCH /api/v1/agents/{agent_id}/lifecycle set_agent_lifecycle_api_v1_agents__agent_id__lifecycle_patch
GET /api/v1/agents/{agent_id}/readiness get_agent_readiness_api_v1_agents__agent_id__readiness_get
GET /api/v1/agents/{agent_id}/runs list_agent_runs_api_v1_agents__agent_id__runs_get
GET /api/v1/agents/{agent_id}/runs/{run_id} get_agent_run_api_v1_agents__agent_id__runs__run_id__get
GET /api/v1/agents/{agent_id}/versions list_agent_versions_api_v1_agents__agent_id__versions_get
GET /api/v1/agents/{agent_id}/versions/{version_id} get_agent_version_api_v1_agents__agent_id__versions__version_id__get
POST /api/v1/auth/bootstrap bootstrap_api_v1_auth_bootstrap_post
POST /api/v1/auth/login login_api_v1_auth_login_post
POST /api/v1/auth/logout logout_api_v1_auth_logout_post
GET /api/v1/auth/session current_session_api_v1_auth_session_get
GET /api/v1/chat-access-tokens list_chat_access_tokens_api_v1_chat_access_tokens_get
POST /api/v1/chat-access-tokens create_chat_access_token_api_v1_chat_access_tokens_post
POST /api/v1/chat-access-tokens/preview preview_chat_access_token_scopes_api_v1_chat_access_tokens_preview_post
DELETE /api/v1/chat-access-tokens/{token_id} revoke_chat_access_token_api_v1_chat_access_tokens__token_id__delete
GET /api/v1/credentials list_credentials_api_v1_credentials_get
POST /api/v1/credentials create_credential_api_v1_credentials_post
GET /api/v1/credentials/{credential_id} get_credential_api_v1_credentials__credential_id__get
POST /api/v1/credentials/{credential_id}/rotate rotate_credential_api_v1_credentials__credential_id__rotate_post
GET /api/v1/discord/bindings bindings_api_v1_discord_bindings_get
POST /api/v1/discord/bindings create_binding_api_v1_discord_bindings_post
GET /api/v1/discord/bindings/{binding_id} binding_api_v1_discord_bindings__binding_id__get
DELETE /api/v1/discord/bindings/{binding_id} delete_binding_api_v1_discord_bindings__binding_id__delete
PATCH /api/v1/discord/bindings/{binding_id} update_binding_api_v1_discord_bindings__binding_id__patch
POST /api/v1/discord/bindings/{binding_id}/test-message test_binding_api_v1_discord_bindings__binding_id__test_message_post
POST /api/v1/discord/bindings/{binding_id}/validate validate_binding_api_v1_discord_bindings__binding_id__validate_post
GET /api/v1/discord/connections list_connections_api_v1_discord_connections_get
POST /api/v1/discord/connections create_connection_api_v1_discord_connections_post
GET /api/v1/discord/connections/{connection_id} get_connection_api_v1_discord_connections__connection_id__get
PATCH /api/v1/discord/connections/{connection_id} update_connection_api_v1_discord_connections__connection_id__patch
POST /api/v1/discord/connections/{connection_id}/installation-url installation_api_v1_discord_connections__connection_id__installation_url_post
POST /api/v1/discord/connections/{connection_id}/refresh refresh_connection_api_v1_discord_connections__connection_id__refresh_post
POST /api/v1/discord/connections/{connection_id}/rotate-token rotate_connection_api_v1_discord_connections__connection_id__rotate_token_post
GET /api/v1/discord/connections/{connection_id}/servers servers_api_v1_discord_connections__connection_id__servers_get
GET /api/v1/discord/connections/{connection_id}/servers/{server_id}/channels channels_api_v1_discord_connections__connection_id__servers__server_id__channels_get
GET /api/v1/discord/connections/{connection_id}/servers/{server_id}/roles roles_api_v1_discord_connections__connection_id__servers__server_id__roles_get
POST /api/v1/discord/connections/{connection_id}/validate validate_connection_api_v1_discord_connections__connection_id__validate_post
GET /api/v1/events events_api_v1_events_get
GET /api/v1/jobs list_jobs_api_v1_jobs_get
GET /api/v1/jobs/{job_id} get_job_api_v1_jobs__job_id__get
POST /api/v1/jobs/{job_id}/cancel cancel_job_api_v1_jobs__job_id__cancel_post
GET /api/v1/knowledge-bases list_knowledge_bases_api_v1_knowledge_bases_get
POST /api/v1/knowledge-bases create_knowledge_base_api_v1_knowledge_bases_post
GET /api/v1/knowledge-bases/{knowledge_base_id} get_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__get
DELETE /api/v1/knowledge-bases/{knowledge_base_id} delete_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__delete
PATCH /api/v1/knowledge-bases/{knowledge_base_id} update_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__patch
POST /api/v1/knowledge-bases/{knowledge_base_id}/generate generate_documentation_api_v1_knowledge_bases__knowledge_base_id__generate_post
GET /api/v1/knowledge-bases/{knowledge_base_id}/model-assignments list_model_assignments_api_v1_knowledge_bases__knowledge_base_id__model_assignments_get
PUT /api/v1/knowledge-bases/{knowledge_base_id}/model-assignments/{role} put_model_assignment_api_v1_knowledge_bases__knowledge_base_id__model_assignments__role__put
POST /api/v1/knowledge-bases/{knowledge_base_id}/restore restore_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__restore_post
GET /api/v1/knowledge-bases/{knowledge_base_id}/wiki get_wiki_api_v1_knowledge_bases__knowledge_base_id__wiki_get
GET /api/v1/knowledge-bases/{knowledge_base_id}/wiki/evidence get_wiki_evidence
GET /api/v1/knowledge-bases/{knowledge_base_id}/wiki/export export_wiki_api_v1_knowledge_bases__knowledge_base_id__wiki_export_get
GET /api/v1/knowledge-bases/{knowledge_base_id}/wiki/versions list_wiki_versions_api_v1_knowledge_bases__knowledge_base_id__wiki_versions_get
GET /api/v1/model-profiles list_model_profiles_api_v1_model_profiles_get
POST /api/v1/model-profiles create_model_profile_api_v1_model_profiles_post
GET /api/v1/model-profiles/{profile_id} get_model_profile_api_v1_model_profiles__profile_id__get
PATCH /api/v1/model-profiles/{profile_id} edit_model_profile_api_v1_model_profiles__profile_id__patch
GET /api/v1/overview operational_overview_api_v1_overview_get
GET /api/v1/provider-endpoints list_provider_endpoints_api_v1_provider_endpoints_get
POST /api/v1/provider-endpoints create_provider_endpoint_api_v1_provider_endpoints_post
GET /api/v1/provider-endpoints/{endpoint_id} get_provider_endpoint_api_v1_provider_endpoints__endpoint_id__get
PATCH /api/v1/provider-endpoints/{endpoint_id} update_provider_endpoint_api_v1_provider_endpoints__endpoint_id__patch
POST /api/v1/provider-endpoints/{endpoint_id}/discover schedule_provider_discovery_api_v1_provider_endpoints__endpoint_id__discover_post
POST /api/v1/provider-endpoints/{endpoint_id}/probe schedule_provider_probe_api_v1_provider_endpoints__endpoint_id__probe_post
GET /api/v1/runs list_documentation_runs_api_v1_runs_get
GET /api/v1/runs/{run_id} get_documentation_run_api_v1_runs__run_id__get
GET /api/v1/settings/export export_configuration_api_v1_settings_export_get
GET /api/v1/sources list_sources_api_v1_sources_get
POST /api/v1/sources/repositories create_repository_source_api_v1_sources_repositories_post
POST /api/v1/sources/websites create_website_source_api_v1_sources_websites_post
GET /api/v1/sources/{source_id} get_source_api_v1_sources__source_id__get
PATCH /api/v1/sources/{source_id} update_source_api_v1_sources__source_id__patch
POST /api/v1/sources/{source_id}/lifecycle change_source_lifecycle_api_v1_sources__source_id__lifecycle_post
GET /api/v1/sources/{source_id}/revisions list_source_revisions_api_v1_sources__source_id__revisions_get
POST /api/v1/sources/{source_id}/sync request_source_sync_api_v1_sources__source_id__sync_post
GET /api/v1/sources/{source_id}/syncs list_source_syncs_api_v1_sources__source_id__syncs_get
POST /api/v1/sources/{source_id}/validate request_source_validation_api_v1_sources__source_id__validate_post
GET /health/live live_health_live_get
GET /health/ready ready_health_ready_get
`

func TestControlPlaneOpenAPIContract(t *testing.T) {
	document := controlPlaneOpenAPIDocument(t)
	assertControlPlaneTopology(t, document)
	assertControlPlaneSemantics(t, document)

	committedContent, err := os.ReadFile(repositoryPath(t, "frontend", "openapi.json"))
	if err != nil {
		t.Fatalf("read committed frontend OpenAPI: %v", err)
	}
	var committed map[string]any
	if err := json.Unmarshal(committedContent, &committed); err != nil {
		t.Fatalf("decode committed frontend OpenAPI: %v", err)
	}
	if !reflect.DeepEqual(committed, document) {
		t.Fatalf(
			"frontend/openapi.json is stale: committed=%s live=%s; run npm run generate:api in frontend",
			documentDigest(committed),
			documentDigest(document),
		)
	}
}

func TestRemovedLegacyControlPlaneRoutesReturnNotFound(t *testing.T) {
	handler := controlPlaneContractHandler(t)
	for _, legacy := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/conversations/00000000-0000-4000-8000-000000000001"},
		{method: http.MethodPost, path: "/api/v1/knowledge-bases/00000000-0000-4000-8000-000000000001/chat"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(legacy.method, legacy.path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d %s", legacy.method, legacy.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestWriteControlPlaneOpenAPI(t *testing.T) {
	output := os.Getenv(openAPIOutputEnvironment)
	if output == "" {
		t.Skip(openAPIOutputEnvironment + " is not set")
	}

	document := controlPlaneOpenAPIDocument(t)
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("encode OpenAPI document: %v", err)
	}
	encoded = append(encoded, '\n')
	if err := writeOpenAPIAtomically(output, encoded); err != nil {
		t.Fatalf("write OpenAPI document to %s: %v", output, err)
	}
}

func assertControlPlaneTopology(t *testing.T, document map[string]any) {
	t.Helper()
	info := openAPIObject(t, document["info"], "info")
	if info["title"] != "ref0 control plane" || info["version"] != "0.1.0" {
		t.Fatalf("OpenAPI info = %v", info)
	}

	expected := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(pythonControlPlaneTopology), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 3 {
			t.Fatalf("invalid topology fixture line %q", line)
		}
		expected[parts[0]+" "+parts[1]] = parts[2]
	}
	paths := openAPIObject(t, document["paths"], "paths")
	actual := map[string]string{}
	for path, rawItem := range paths {
		item := openAPIObject(t, rawItem, "path "+path)
		for _, method := range []string{"get", "put", "post", "delete", "patch"} {
			rawOperation, exists := item[method]
			if !exists {
				continue
			}
			operation := openAPIObject(t, rawOperation, strings.ToUpper(method)+" "+path)
			operationID, ok := operation["operationId"].(string)
			if !ok || operationID == "" {
				t.Fatalf("operation ID missing for %s %s", method, path)
			}
			if _, hasSecurity := operation["security"]; hasSecurity {
				t.Fatalf("unexpected operation-level security drift for %s %s", method, path)
			}
			actual[strings.ToUpper(method)+" "+path] = operationID
		}
	}
	if len(paths) != 67 || len(actual) != 83 || paths["/metrics"] != nil {
		t.Fatalf("OpenAPI topology has %d paths and %d operations (metrics present=%v)", len(paths), len(actual), paths["/metrics"] != nil)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("OpenAPI topology drift:\n%s", topologyDifference(expected, actual))
	}
	components := openAPIObject(t, document["components"], "components")
	if _, hasSchemes := components["securitySchemes"]; hasSchemes {
		t.Fatal("securitySchemes drifted from the cookie-parameter contract")
	}
}

func assertControlPlaneSemantics(t *testing.T, document map[string]any) {
	t.Helper()
	schemas := openAPIObject(t, openAPIObject(t, document["components"], "components")["schemas"], "schemas")

	updateProvider := openAPIObject(t, schemas["UpdateProviderEndpointRequest"], "UpdateProviderEndpointRequest")
	assertProperties(t, updateProvider, "display_name", "base_url", "credential_id", "headers", "chat_completions_path", "responses_path", "models_path", "allow_http", "allow_private_network", "expected_version", "lifecycle")
	assertRequired(t, updateProvider, "display_name", "base_url", "expected_version")

	deletion := openAPIObject(t, schemas["DeleteKnowledgeBaseResponse"], "DeleteKnowledgeBaseResponse")
	assertProperties(t, deletion, "id", "name", "access", "lifecycle", "instructions", "language", "published_wiki_id", "archived_at", "delete_requested_at", "purge_after", "deleted_at", "created_at", "updated_at", "version", "job_id")
	assertRequired(t, deletion, "id", "job_id")

	export := openAPIObject(t, schemas["ConfigurationExport"], "ConfigurationExport")
	for _, field := range []string{"redacted_fields", "credentials", "knowledge_bases", "sources", "providers", "models", "model_assignments", "agents", "discord_connections", "discord_bindings"} {
		assertNonNullableArray(t, openAPIProperty(t, export, field), "ConfigurationExport."+field)
	}
	sourceItems := openAPIObject(t, openAPIProperty(t, export, "sources")["items"], "ConfigurationExport.sources.items")
	if choices, ok := sourceItems["anyOf"].([]any); !ok || len(choices) != 2 {
		t.Fatalf("configuration source union = %v", sourceItems)
	}

	for schemaName, fields := range map[string][]string{
		"DocumentationRunResponse": {"sources", "models", "pages"},
		"OperationalOverview":      {"unhealthy_sources", "failed_jobs", "knowledge_base_issues", "provider_errors", "agent_failures"},
		"WikiResponse":             {"pages"},
	} {
		schema := openAPIObject(t, schemas[schemaName], schemaName)
		for _, field := range fields {
			assertNonNullableArray(t, openAPIProperty(t, schema, field), schemaName+"."+field)
		}
	}
	assertNullable(t, openAPIProperty(t, openAPIObject(t, schemas["ProbeRunResponse"], "ProbeRunResponse"), "findings"), "ProbeRunResponse.findings")

	assertEnum(t, openAPIProperty(t, openAPIObject(t, schemas["JobResponse"], "JobResponse"), "status"), []string{"pending", "leased", "succeeded", "retry_wait", "failed", "cancel_requested", "cancelled"}, "JobResponse.status")
	assertEnum(t, openAPIProperty(t, openAPIObject(t, schemas["DiscoveryRunResponse"], "DiscoveryRunResponse"), "status"), []string{"pending", "running", "succeeded", "failed", "superseded"}, "DiscoveryRunResponse.status")
	assertEnum(t, openAPIProperty(t, openAPIObject(t, schemas["ModelAssignmentResponse"], "ModelAssignmentResponse"), "role"), []string{"documentation_planner", "documentation_writer"}, "ModelAssignmentResponse.role")
	for _, assertion := range []struct {
		schema, field string
		values        []string
	}{
		{schema: "AgentConfigurationResponse", field: "reasoning_effort", values: []string{"none", "minimal", "low", "medium", "high", "max"}},
		{schema: "AgentConfigurationResponse", field: "answer_mode", values: []string{"tool_calling", "single_pass"}},
		{schema: "AgentConfigurationResponse", field: "evidence_access", values: []string{"wiki_only", "wiki_and_source"}},
		{schema: "AgentResponse", field: "lifecycle", values: []string{"draft", "active", "archived"}},
		{schema: "AgentReadinessResponse", field: "effective_access", values: []string{"public", "restricted"}},
		{schema: "ReadinessIssueResponse", field: "code", values: []string{"model_unavailable", "endpoint_unavailable", "credential_unavailable", "model_configuration_stale", "model_limits_unknown", "model_capability_missing", "reasoning_unsupported", "knowledge_base_missing", "knowledge_base_inactive", "knowledge_base_unpublished"}},
		{schema: "RunSummaryResponse", field: "outcome", values: []string{"answered", "refused", "insufficient_evidence", "failed"}},
		{schema: "RunDetailResponse", field: "outcome", values: []string{"answered", "refused", "insufficient_evidence", "failed"}},
		{schema: "RunDetailResponse", field: "effective_access", values: []string{"public", "restricted"}},
		{schema: "RunKnowledgeBaseResponse", field: "access_policy", values: []string{"public", "restricted"}},
		{schema: "ChatTokenAgentScopeResponse", field: "effective_access", values: []string{"public", "restricted"}},
		{schema: "AgentConfiguration", field: "lifecycle", values: []string{"draft", "active", "archived"}},
		{schema: "AgentConfiguration", field: "reasoning_effort", values: []string{"none", "minimal", "low", "medium", "high", "max"}},
		{schema: "AgentConfiguration", field: "answer_mode", values: []string{"tool_calling", "single_pass"}},
		{schema: "AgentConfiguration", field: "evidence_access", values: []string{"wiki_only", "wiki_and_source"}},
	} {
		schema := openAPIObject(t, schemas[assertion.schema], assertion.schema)
		assertEnum(t, openAPIProperty(t, schema, assertion.field), assertion.values, assertion.schema+"."+assertion.field)
	}
	agentExport := openAPIObject(t, schemas["AgentConfiguration"], "AgentConfiguration")
	assertNonNullableArray(t, openAPIProperty(t, agentExport, "knowledge_base_ids"), "AgentConfiguration.knowledge_base_ids")
	discordBindingExport := openAPIObject(t, schemas["DiscordBindingConfiguration"], "DiscordBindingConfiguration")
	discordTriggerItems := openAPIObject(t, openAPIProperty(t, discordBindingExport, "triggers")["items"], "DiscordBindingConfiguration.triggers.items")
	assertEnum(t, discordTriggerItems, []string{"mention", "slash_command"}, "DiscordBindingConfiguration.triggers.items")
	issuedToken := openAPIObject(t, schemas["IssuedChatTokenResponse"], "IssuedChatTokenResponse")
	if secret := openAPIProperty(t, issuedToken, "secret"); secret["readOnly"] != true || secret["writeOnly"] == true {
		t.Fatalf("IssuedChatTokenResponse.secret is not response-only: %v", secret)
	}
	assertNonNullableArray(t, openAPIProperty(t, issuedToken, "agent_scopes"), "IssuedChatTokenResponse.agent_scopes")
	versionPage := openAPIObject(t, schemas["ListAgentVersionsResponse"], "ListAgentVersionsResponse")
	assertNonNullableArray(t, openAPIProperty(t, versionPage, "items"), "ListAgentVersionsResponse.items")
	assertProperties(t, openAPIObject(t, schemas["RunDetailResponse"], "RunDetailResponse"),
		"id", "agent_id", "agent_version_id", "origin", "outcome", "created_at", "completed_at")
	replayProblem := openAPIObject(t, schemas["ChatTokenReplayProblem"], "ChatTokenReplayProblem")
	assertProperties(t, replayProblem, "code", "token")
	assertEnum(t, openAPIProperty(t, replayProblem, "code"), []string{"secret_already_issued"}, "ChatTokenReplayProblem.code")
	candidateProblem := openAPIObject(t, schemas["AgentCandidateNotReadyProblem"], "AgentCandidateNotReadyProblem")
	assertProperties(t, candidateProblem, "code", "readiness")
	assertEnum(t, openAPIProperty(t, candidateProblem, "code"), []string{"candidate_not_ready"}, "AgentCandidateNotReadyProblem.code")
	chatTokenSummary := openAPIObject(t, schemas["ChatTokenSummaryResponse"], "ChatTokenSummaryResponse")
	assertProperties(t, chatTokenSummary, "id", "prefix", "label", "agent_count", "created_at", "expires_at")
	if _, exists := openAPIPropertyNames(chatTokenSummary)["agent_ids"]; exists {
		t.Fatalf("ChatTokenSummaryResponse unexpectedly expands agent_ids: %v", chatTokenSummary)
	}
	createTokenRequest := openAPIObject(t, schemas["CreateChatTokenRequest"], "CreateChatTokenRequest")
	if agentIDs := openAPIProperty(t, createTokenRequest, "agent_ids"); agentIDs["minItems"] != float64(1) || agentIDs["maxItems"] != float64(chattokens.MaxAgentScopesPerToken) || agentIDs["uniqueItems"] != true {
		t.Fatalf("CreateChatTokenRequest.agent_ids bounds = %v", agentIDs)
	} else if items := openAPIObject(t, agentIDs["items"], "CreateChatTokenRequest.agent_ids.items"); items["format"] != "uuid" {
		t.Fatalf("CreateChatTokenRequest.agent_ids item format = %v", items)
	}
	previewRequest := openAPIObject(t, schemas["PreviewChatTokenScopesRequest"], "PreviewChatTokenScopesRequest")
	previewAgentIDs := openAPIProperty(t, previewRequest, "agent_ids")
	if previewAgentIDs["minItems"] != float64(1) || previewAgentIDs["maxItems"] != float64(chattokens.MaxAgentScopesPerToken) || previewAgentIDs["uniqueItems"] != true {
		t.Fatalf("PreviewChatTokenScopesRequest.agent_ids bounds = %v", previewAgentIDs)
	} else if items := openAPIObject(t, previewAgentIDs["items"], "PreviewChatTokenScopesRequest.agent_ids.items"); items["format"] != "uuid" {
		t.Fatalf("PreviewChatTokenScopesRequest.agent_ids item format = %v", items)
	}
	previewResponse := openAPIObject(t, schemas["PreviewChatTokenScopesResponse"], "PreviewChatTokenScopesResponse")
	for _, field := range []string{"agent_ids", "agent_scopes", "knowledge_base_ids"} {
		assertNonNullableArray(t, openAPIProperty(t, previewResponse, field), "PreviewChatTokenScopesResponse."+field)
	}
	assertEnum(t, openAPIProperty(t, previewResponse, "effective_access"), []string{"public", "restricted"}, "PreviewChatTokenScopesResponse.effective_access")
	createAgentRequest := openAPIObject(t, schemas["CreateAgentRequest"], "CreateAgentRequest")
	if key := openAPIProperty(t, createAgentRequest, "key"); key["maxLength"] != float64(64) {
		t.Fatalf("CreateAgentRequest.key maxLength = %v", key)
	}

	paths := openAPIObject(t, document["paths"], "paths")
	assertResponseMediaType(t, paths, eventStreamPath, "get", "200", "text/event-stream")
	assertResponseMediaType(t, paths, "/api/v1/knowledge-bases/{knowledge_base_id}/wiki/export", "get", "200", "application/zip")
	assertRequiredHeader(t, paths, "/api/v1/jobs/{job_id}/cancel", "post", "Idempotency-Key", 1, 255)
	for _, mutation := range []struct{ path, method string }{
		{agentsPath, "post"},
		{agentsPath + "/{agent_id}/configuration", "put"},
		{agentsPath + "/{agent_id}/lifecycle", "patch"},
		{chatTokensPath, "post"},
		{chatTokensPath + "/{token_id}", "delete"},
	} {
		assertRequiredHeader(t, paths, mutation.path, mutation.method, "Idempotency-Key", 1, 255)
	}
	assertHeaderAbsent(t, paths, chatTokenPreviewPath, "post", "Idempotency-Key")
	assertHeaderAbsent(t, paths, chatTokenPreviewPath, "post", csrfHeaderName)
	createToken := openAPIObject(t, openAPIObject(t, paths[chatTokensPath], chatTokensPath)["post"], "create token")
	createTokenResponses := openAPIObject(t, createToken["responses"], "create token responses")
	replayResponse := openAPIObject(t, createTokenResponses["409"], "create token replay response")
	replayContent := openAPIObject(t, replayResponse["content"], "create token replay content")
	replayMedia := openAPIObject(t, replayContent["application/problem+json"], "create token replay media")
	replaySchema := openAPIObject(t, replayMedia["schema"], "create token replay schema")
	choices, ok := replaySchema["oneOf"].([]any)
	if !ok || len(choices) != 2 {
		t.Fatalf("create token 409 union = %v", replaySchema)
	}
	references := map[string]bool{}
	for _, choice := range choices {
		reference, _ := openAPIObject(t, choice, "create token 409 choice")["$ref"].(string)
		references[reference] = true
	}
	if !references["#/components/schemas/ChatTokenReplayProblem"] || !references["#/components/schemas/ErrorModel"] {
		t.Fatalf("create token 409 references = %v", references)
	}
	for _, mutation := range []struct{ path, method string }{
		{agentsPath + "/{agent_id}/configuration", "put"},
		{agentsPath + "/{agent_id}/lifecycle", "patch"},
	} {
		assertProblemUnion(t, paths, mutation.path, mutation.method, "409", "#/components/schemas/AgentCandidateNotReadyProblem")
	}
	assertGenericProblemResponses(t, paths)
}

func assertProblemUnion(t *testing.T, paths map[string]any, path, method, status, specialized string) {
	t.Helper()
	operation := openAPIObject(t, openAPIObject(t, paths[path], path)[method], method+" "+path)
	responses := openAPIObject(t, operation["responses"], method+" "+path+" responses")
	response := openAPIObject(t, responses[status], method+" "+path+" "+status)
	content := openAPIObject(t, response["content"], method+" "+path+" content")
	media := openAPIObject(t, content["application/problem+json"], method+" "+path+" media")
	schema := openAPIObject(t, media["schema"], method+" "+path+" schema")
	choices, ok := schema["oneOf"].([]any)
	if !ok || len(choices) != 2 {
		t.Fatalf("%s %s %s union = %v", method, path, status, schema)
	}
	references := map[string]bool{}
	for _, choice := range choices {
		reference, _ := openAPIObject(t, choice, method+" "+path+" choice")["$ref"].(string)
		references[reference] = true
	}
	if !references[specialized] || !references["#/components/schemas/ErrorModel"] {
		t.Fatalf("%s %s %s references = %v", method, path, status, references)
	}
}

func openAPIObject(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, not an object", label, value)
	}
	return object
}

func openAPIProperty(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	properties := openAPIObject(t, schema["properties"], "properties")
	return openAPIObject(t, properties[name], "property "+name)
}

func openAPIPropertyNames(schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	return properties
}

func assertProperties(t *testing.T, schema map[string]any, names ...string) {
	t.Helper()
	properties := openAPIObject(t, schema["properties"], "properties")
	for _, name := range names {
		if properties[name] == nil {
			t.Fatalf("schema property %q missing from %v", name, properties)
		}
	}
}

func assertRequired(t *testing.T, schema map[string]any, names ...string) {
	t.Helper()
	required := stringSet(schema["required"])
	for _, name := range names {
		if !required[name] {
			t.Fatalf("schema field %q is not required: %v", name, schema["required"])
		}
	}
}

func assertNonNullableArray(t *testing.T, schema map[string]any, label string) {
	t.Helper()
	if schema["type"] != "array" || schemaAllowsNull(schema) {
		t.Fatalf("%s is not a non-null array: %v", label, schema)
	}
}

func assertNullable(t *testing.T, schema map[string]any, label string) {
	t.Helper()
	if !schemaAllowsNull(schema) {
		t.Fatalf("%s does not accept null: %v", label, schema)
	}
}

func schemaAllowsNull(schema map[string]any) bool {
	if types, ok := schema["type"].([]any); ok {
		for _, value := range types {
			if value == "null" {
				return true
			}
		}
	}
	for _, union := range []string{"anyOf", "oneOf"} {
		if choices, ok := schema[union].([]any); ok {
			for _, choice := range choices {
				if candidate, ok := choice.(map[string]any); ok && candidate["type"] == "null" {
					return true
				}
			}
		}
	}
	if reference, ok := schema["$ref"].(string); ok {
		return reference == "#/components/schemas/WikiPageResponse"
	}
	return false
}

func assertEnum(t *testing.T, schema map[string]any, expected []string, label string) {
	t.Helper()
	values, ok := schema["enum"].([]any)
	if !ok {
		t.Fatalf("%s has no enum: %v", label, schema)
	}
	actual := make([]string, len(values))
	for index, value := range values {
		actual[index], _ = value.(string)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s enum = %v, want %v", label, actual, expected)
	}
}

func assertResponseMediaType(t *testing.T, paths map[string]any, path, method, status, mediaType string) {
	t.Helper()
	operation := openAPIObject(t, openAPIObject(t, paths[path], path)[method], method+" "+path)
	responses := openAPIObject(t, operation["responses"], "responses")
	response := openAPIObject(t, responses[status], status+" response")
	content := openAPIObject(t, response["content"], status+" response content")
	if content[mediaType] == nil || len(content) != 1 {
		t.Fatalf("%s %s response media types = %v", method, path, content)
	}
}

func assertRequiredHeader(t *testing.T, paths map[string]any, path, method, name string, minLength, maxLength float64) {
	t.Helper()
	operation := openAPIObject(t, openAPIObject(t, paths[path], path)[method], method+" "+path)
	parameters, ok := operation["parameters"].([]any)
	if !ok {
		t.Fatalf("parameters absent for %s %s", method, path)
	}
	for _, raw := range parameters {
		parameter := openAPIObject(t, raw, "parameter")
		if parameter["in"] == "header" && parameter["name"] == name {
			schema := openAPIObject(t, parameter["schema"], name+" schema")
			if parameter["required"] != true || schema["minLength"] != minLength || schema["maxLength"] != maxLength {
				t.Fatalf("%s header contract = %v", name, parameter)
			}
			return
		}
	}
	t.Fatalf("header %s is absent from %s %s", name, method, path)
}

func assertHeaderAbsent(t *testing.T, paths map[string]any, path, method, name string) {
	t.Helper()
	operation := openAPIObject(t, openAPIObject(t, paths[path], path)[method], method+" "+path)
	parameters, _ := operation["parameters"].([]any)
	for _, raw := range parameters {
		parameter := openAPIObject(t, raw, "parameter")
		if parameter["in"] == "header" && parameter["name"] == name {
			t.Fatalf("header %s must be absent from %s %s", name, method, path)
		}
	}
}

func assertStatuses(t *testing.T, paths map[string]any, path, method string, expected ...string) {
	t.Helper()
	operation := openAPIObject(t, openAPIObject(t, paths[path], path)[method], method+" "+path)
	responses := openAPIObject(t, operation["responses"], "responses")
	actual := make([]string, 0, len(responses))
	for status := range responses {
		actual = append(actual, status)
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s %s statuses = %v, want %v", method, path, actual, expected)
	}
}

func assertGenericProblemResponses(t *testing.T, paths map[string]any) {
	t.Helper()
	for path, rawItem := range paths {
		item := openAPIObject(t, rawItem, path)
		for _, method := range []string{"get", "put", "post", "delete", "patch"} {
			rawOperation, exists := item[method]
			if !exists {
				continue
			}
			operation := openAPIObject(t, rawOperation, method+" "+path)
			responses := openAPIObject(t, operation["responses"], "responses")
			for status, rawResponse := range responses {
				if strings.HasPrefix(status, "2") {
					continue
				}
				if status == "409" && ((path == chatTokensPath && method == "post") ||
					(path == agentsPath+"/{agent_id}/configuration" && method == "put") ||
					(path == agentsPath+"/{agent_id}/lifecycle" && method == "patch")) {
					continue
				}
				response := openAPIObject(t, rawResponse, status+" response")
				content := openAPIObject(t, response["content"], status+" response content")
				problem := openAPIObject(t, content["application/problem+json"], status+" problem media")
				schema := openAPIObject(t, problem["schema"], status+" problem schema")
				if schema["$ref"] != "#/components/schemas/ErrorModel" || len(content) != 1 {
					t.Fatalf("%s %s %s error boundary = %v", method, path, status, response)
				}
			}
		}
	}
}

func stringSet(value any) map[string]bool {
	result := map[string]bool{}
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if text, ok := item.(string); ok {
				result[text] = true
			}
		}
	}
	return result
}

func topologyDifference(expected, actual map[string]string) string {
	keys := make([]string, 0, len(expected)+len(actual))
	seen := map[string]bool{}
	for key := range expected {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range actual {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var result strings.Builder
	for _, key := range keys {
		if expected[key] != actual[key] {
			fmt.Fprintf(&result, "%s: want %q, got %q\n", key, expected[key], actual[key])
		}
	}
	return result.String()
}

func documentDigest(document map[string]any) string {
	encoded, _ := json.Marshal(document)
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:])
}

func writeOpenAPIAtomically(path string, content []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ref0-openapi-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o644); err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporaryPath, path)
}

func controlPlaneOpenAPIDocument(t *testing.T) map[string]any {
	t.Helper()
	response := request(t, controlPlaneContractHandler(t), "/openapi.json")
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI status = %d body=%s", response.Code, response.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode OpenAPI document: %v", err)
	}
	return document
}

func controlPlaneContractHandler(t *testing.T) http.Handler {
	t.Helper()
	sessions := &fakeSessionService{}
	routes := controlPlaneRoutes{
		sessions:          sessions,
		credentials:       &fakeCredentialRouteService{},
		knowledgeBases:    &fakeKnowledgeBaseRouteService{},
		providers:         &fakeProviderService{},
		sources:           &fakeSourceRouteService{},
		discord:           &fakeDiscordService{},
		jobs:              inertJobService{},
		documentation:     &fakeDocumentationService{},
		documentationJobs: inertJobService{},
	}
	registry := prometheus.NewRegistry()
	handler, err := newHandler(
		Config{version: "0.1.0"},
		fixedReadiness(readinessResult{
			database:      true,
			migrations:    true,
			dataDirectory: true,
			masterKey:     true,
		}),
		sessions,
		inertEventReader{},
		inertJobService{},
		inertOperationsService{},
		registry,
		registry,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		routes.register,
	)
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}
	return handler
}

func repositoryPath(t *testing.T, elements ...string) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	return filepath.Join(append([]string{directory, "..", ".."}, elements...)...)
}
