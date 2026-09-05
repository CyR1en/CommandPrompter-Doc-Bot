package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	discorddomain "github.com/cyr1en/ref0/internal/discord"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type fakeDiscordService struct {
	connection        discorddomain.Connection
	binding           discorddomain.Binding
	servers           []discorddomain.Server
	channels          []discorddomain.Channel
	roles             []discorddomain.Role
	jobID             jobs.JobID
	err               error
	actor             discorddomain.ActorID
	key               string
	createdConnection discorddomain.CreateConnection
	updatedConnection discorddomain.UpdateConnection
	rotatedToken      discorddomain.RotateToken
	createdBinding    discorddomain.CreateBinding
	updatedBinding    discorddomain.UpdateBinding
	lastExpected      int32
	lastAction        string
}

func (service *fakeDiscordService) ListConnections(context.Context) ([]discorddomain.Connection, error) {
	return []discorddomain.Connection{service.connection}, service.err
}

func (service *fakeDiscordService) GetConnection(context.Context, discorddomain.ConnectionID) (discorddomain.Connection, error) {
	return service.connection, service.err
}

func (service *fakeDiscordService) CreateConnection(_ context.Context, command discorddomain.CreateConnection, actor discorddomain.ActorID, key string) (discorddomain.Connection, error) {
	service.createdConnection, service.actor, service.key = command, actor, key
	return service.connection, service.err
}

func (service *fakeDiscordService) UpdateConnection(_ context.Context, command discorddomain.UpdateConnection, actor discorddomain.ActorID, key string) (discorddomain.Connection, error) {
	service.updatedConnection, service.actor, service.key = command, actor, key
	value := service.connection
	value.DisplayName, value.Lifecycle, value.Version = command.DisplayName, command.Lifecycle, value.Version+1
	return value, service.err
}

func (service *fakeDiscordService) RotateConnectionToken(_ context.Context, command discorddomain.RotateToken, actor discorddomain.ActorID, key string) (discorddomain.Connection, error) {
	service.rotatedToken, service.actor, service.key = command, actor, key
	return service.connection, service.err
}

func (service *fakeDiscordService) RequestConnectionValidation(_ context.Context, _ discorddomain.ConnectionID, expected int32, actor discorddomain.ActorID, key string) (jobs.JobID, error) {
	service.lastExpected, service.actor, service.key, service.lastAction = expected, actor, key, "validate"
	return service.jobID, service.err
}

func (service *fakeDiscordService) RequestConnectionRefresh(_ context.Context, _ discorddomain.ConnectionID, expected int32, actor discorddomain.ActorID, key string) (jobs.JobID, error) {
	service.lastExpected, service.actor, service.key, service.lastAction = expected, actor, key, "refresh"
	return service.jobID, service.err
}

func (service *fakeDiscordService) InstallationURL(context.Context, discorddomain.ConnectionID, bool) (string, error) {
	return "https://discord.com/oauth2/authorize?safe=true", service.err
}

func (service *fakeDiscordService) ListServers(context.Context, discorddomain.ConnectionID) ([]discorddomain.Server, error) {
	return service.servers, service.err
}

func (service *fakeDiscordService) ListChannels(context.Context, discorddomain.ConnectionID, discorddomain.Snowflake) ([]discorddomain.Channel, error) {
	return service.channels, service.err
}

func (service *fakeDiscordService) ListRoles(context.Context, discorddomain.ConnectionID, discorddomain.Snowflake) ([]discorddomain.Role, error) {
	return service.roles, service.err
}

func (service *fakeDiscordService) ListBindings(context.Context) ([]discorddomain.Binding, error) {
	return []discorddomain.Binding{service.binding}, service.err
}

func (service *fakeDiscordService) GetBinding(context.Context, discorddomain.BindingID) (discorddomain.Binding, error) {
	return service.binding, service.err
}

func (service *fakeDiscordService) CreateBinding(_ context.Context, command discorddomain.CreateBinding, actor discorddomain.ActorID, key string) (discorddomain.Binding, error) {
	service.createdBinding, service.actor, service.key = command, actor, key
	value := service.binding
	applyFakeBindingConfiguration(&value, command.Configuration)
	value.Enabled = command.Enabled
	return value, service.err
}

func (service *fakeDiscordService) UpdateBinding(_ context.Context, command discorddomain.UpdateBinding, actor discorddomain.ActorID, key string) (discorddomain.Binding, error) {
	service.updatedBinding, service.actor, service.key = command, actor, key
	value := service.binding
	applyFakeBindingConfiguration(&value, command.Configuration)
	value.Enabled, value.Version = command.Enabled, value.Version+1
	return value, service.err
}

func (service *fakeDiscordService) DeleteBinding(_ context.Context, _ discorddomain.BindingID, expected int32, actor discorddomain.ActorID, key string) error {
	service.lastExpected, service.actor, service.key, service.lastAction = expected, actor, key, "delete_binding"
	return service.err
}

func (service *fakeDiscordService) ValidateBinding(_ context.Context, _ discorddomain.BindingID, expected int32, actor discorddomain.ActorID, key string) (discorddomain.Binding, error) {
	service.lastExpected, service.actor, service.key, service.lastAction = expected, actor, key, "validate_binding"
	return service.binding, service.err
}

func (service *fakeDiscordService) RequestTestMessage(_ context.Context, _ discorddomain.BindingID, expected int32, actor discorddomain.ActorID, key string) (jobs.JobID, error) {
	service.lastExpected, service.actor, service.key, service.lastAction = expected, actor, key, "test_message"
	return service.jobID, service.err
}

type fakeDiscordJobReader struct {
	value jobs.Snapshot
	err   error
}

func (reader *fakeDiscordJobReader) Get(context.Context, jobs.JobID) (jobs.Snapshot, error) {
	return reader.value, reader.err
}

func TestDiscordRoutesPreserveAuthenticationMutationAndPatchContracts(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service, reader := discordRouteFixtures(t)
	handler := discordRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service, reader)
	cookie := sessionCookie(authenticated.Token.Reveal())
	csrf := authenticated.CSRFToken

	unauthorized := authRequest(t, handler, http.MethodGet, connectionsPath, "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}

	createBody := `{"display_name":"Docs bot","credential_id":"` + service.connection.CredentialID.String() + `"}`
	missingCSRF := authRequest(t, handler, http.MethodPost, connectionsPath, createBody,
		map[string]string{"Cookie": cookie, "Idempotency-Key": "discord-create"})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF=%d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	created := authRequest(t, handler, http.MethodPost, connectionsPath, createBody, map[string]string{
		"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": " discord-create ",
	})
	if created.Code != http.StatusCreated || service.createdConnection.DisplayName != "Docs bot" ||
		service.key != "discord-create" || service.actor != discorddomain.ActorID(authenticated.Session.Operator.ID) ||
		strings.Contains(created.Body.String(), "credential_version") ||
		!strings.Contains(created.Body.String(), `"state":"connecting"`) {
		t.Fatalf("created=%d %s command=%+v actor=%s key=%q", created.Code, created.Body.String(), service.createdConnection, service.actor, service.key)
	}

	connectionPath := connectionsPath + "/" + service.connection.ID.String()
	updated := authRequest(t, handler, http.MethodPatch, connectionPath,
		`{"expected_version":1,"lifecycle":"disabled"}`, map[string]string{
			"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "discord-update",
		})
	if updated.Code != http.StatusOK || service.updatedConnection.DisplayName != service.connection.DisplayName ||
		service.updatedConnection.Lifecycle != discorddomain.ConnectionDisabled {
		t.Fatalf("updated=%d %s command=%+v", updated.Code, updated.Body.String(), service.updatedConnection)
	}
	unknown := authRequest(t, handler, http.MethodPost, connectionsPath,
		`{"display_name":"Docs bot","credential_id":"`+service.connection.CredentialID.String()+`","token":"secret-sentinel"}`,
		map[string]string{"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "unknown"})
	if unknown.Code != http.StatusUnprocessableEntity || strings.Contains(unknown.Body.String(), "secret-sentinel") {
		t.Fatalf("unknown=%d %s", unknown.Code, unknown.Body.String())
	}
}

func TestDiscordBindingDefaultsAndExplicitReplyChannelClear(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service, reader := discordRouteFixtures(t)
	handler := discordRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service, reader)
	headers := map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()), csrfHeaderName: authenticated.CSRFToken,
		"Idempotency-Key": "binding-create",
	}
	createBody := `{"connection_id":"` + service.connection.ID.String() + `","server_id":"123",` +
		`"listen_channel_id":"456","agent_id":"` + service.binding.AgentID.String() + `",` +
		`"triggers":["mention"],"reply_policy":"same_channel"}`
	created := authRequest(t, handler, http.MethodPost, bindingsPath, createBody, headers)
	if created.Code != http.StatusCreated || service.createdBinding.Enabled ||
		service.createdBinding.Configuration.RatePolicy != discorddomain.DefaultRatePolicy() ||
		service.createdBinding.Configuration.AllowedRoleIDs == nil || service.createdBinding.Configuration.AllowedUserIDs == nil {
		t.Fatalf("created=%d %s command=%+v", created.Code, created.Body.String(), service.createdBinding)
	}

	headers["Idempotency-Key"] = "binding-clear-reply"
	bindingPath := bindingsPath + "/" + service.binding.ID.String()
	updated := authRequest(t, handler, http.MethodPatch, bindingPath,
		`{"expected_version":1,"reply_policy":"same_channel","reply_channel_id":null}`, headers)
	if updated.Code != http.StatusOK || service.updatedBinding.Configuration.ReplyChannelID != nil ||
		service.updatedBinding.Configuration.ServerID != service.binding.ServerID ||
		service.updatedBinding.Configuration.AllowedRoleIDs[0] != service.binding.AllowedRoleIDs[0] {
		t.Fatalf("updated=%d %s command=%+v", updated.Code, updated.Body.String(), service.updatedBinding)
	}

	headers["Idempotency-Key"] = "binding-null-list"
	nullList := authRequest(t, handler, http.MethodPost, bindingsPath,
		strings.TrimSuffix(createBody, "}")+`,"allowed_role_ids":null}`, headers)
	if nullList.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null list=%d %s", nullList.Code, nullList.Body.String())
	}
}

func TestDiscordDirectoryJobsAndErrorsAreMappedWithoutPrivateDetails(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service, reader := discordRouteFixtures(t)
	handler := discordRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service, reader)
	cookie := sessionCookie(authenticated.Token.Reveal())
	connectionID := service.connection.ID.String()

	channels := authRequest(t, handler, http.MethodGet,
		connectionsPath+"/"+connectionID+"/servers/123/channels", "", map[string]string{"Cookie": cookie})
	if channels.Code != http.StatusOK || !strings.Contains(channels.Body.String(), `"permission_status":"ready"`) ||
		!strings.Contains(channels.Body.String(), `"viewer_role_ids":["789"]`) {
		t.Fatalf("channels=%d %s", channels.Code, channels.Body.String())
	}

	headers := map[string]string{"Cookie": cookie, csrfHeaderName: authenticated.CSRFToken, "Idempotency-Key": "validate"}
	validated := authRequest(t, handler, http.MethodPost,
		connectionsPath+"/"+connectionID+"/validate", `{"expected_version":1}`, headers)
	if validated.Code != http.StatusAccepted || service.lastAction != "validate" ||
		!strings.Contains(validated.Body.String(), `"job_type":"refresh_discord"`) {
		t.Fatalf("validated=%d %s action=%q", validated.Code, validated.Body.String(), service.lastAction)
	}

	headers["Idempotency-Key"] = "install"
	nullThreads := authRequest(t, handler, http.MethodPost,
		connectionsPath+"/"+connectionID+"/installation-url", `{"threads":null}`, headers)
	if nullThreads.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null threads=%d %s", nullThreads.Code, nullThreads.Body.String())
	}
	headers["Idempotency-Key"] = "install-threads"
	installation := authRequest(t, handler, http.MethodPost,
		connectionsPath+"/"+connectionID+"/installation-url", `{"threads":true}`, headers)
	wantPermissions := int64(discorddomain.BasePermissions | discorddomain.ThreadPermissions)
	if installation.Code != http.StatusOK || !strings.Contains(installation.Body.String(), `"permissions":`+strconv.FormatInt(wantPermissions, 10)) ||
		!strings.Contains(installation.Body.String(), `"scopes":["bot","applications.commands"]`) {
		t.Fatalf("installation=%d %s", installation.Code, installation.Body.String())
	}

	service.err = discorddomain.ErrNotFound
	missing := authRequest(t, handler, http.MethodGet, connectionsPath+"/"+connectionID, "", map[string]string{"Cookie": cookie})
	if missing.Code != http.StatusNotFound || problemDetail(t, missing) != "Discord resource not found." {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
	service.err = idempotency.ErrConflict
	headers["Idempotency-Key"] = "conflict"
	conflict := authRequest(t, handler, http.MethodPatch, connectionsPath+"/"+connectionID, `{"expected_version":1,"lifecycle":"disabled"}`, headers)
	if conflict.Code != http.StatusConflict || problemDetail(t, conflict) != "Idempotency key conflicts with a different request." {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	service.err = errors.New("database-private-sentinel")
	internal := authRequest(t, handler, http.MethodGet, connectionsPath, "", map[string]string{"Cookie": cookie})
	if internal.Code != http.StatusInternalServerError || strings.Contains(internal.Body.String(), "database-private-sentinel") {
		t.Fatalf("internal=%d %s", internal.Code, internal.Body.String())
	}
}

func TestDiscordThreadPermissionStatusRequiresThreadPermissions(t *testing.T) {
	channel := discorddomain.Channel{
		ChannelType: 11, EffectiveBotPermissions: discorddomain.BasePermissions,
	}
	if status := newDiscordChannelResponse(channel).PermissionStatus; status != "missing" {
		t.Fatalf("base-only thread status = %q", status)
	}
	channel.EffectiveBotPermissions |= discorddomain.PermissionSendMessagesInThread
	if status := newDiscordChannelResponse(channel).PermissionStatus; status != "ready" {
		t.Fatalf("thread-capable status = %q", status)
	}
}

func TestDiscordOpenAPIExposesTheExactEighteenOperationIDs(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service, reader := discordRouteFixtures(t)
	handler := discordRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service, reader)
	document := discordOpenAPIDocument(t, handler)
	paths := document["paths"].(map[string]any)
	want := map[string]map[string]string{
		"/api/v1/discord/connections": {
			"get": "list_connections_api_v1_discord_connections_get", "post": "create_connection_api_v1_discord_connections_post",
		},
		"/api/v1/discord/connections/{connection_id}": {
			"get": "get_connection_api_v1_discord_connections__connection_id__get", "patch": "update_connection_api_v1_discord_connections__connection_id__patch",
		},
		"/api/v1/discord/connections/{connection_id}/validate": {
			"post": "validate_connection_api_v1_discord_connections__connection_id__validate_post",
		},
		"/api/v1/discord/connections/{connection_id}/refresh": {
			"post": "refresh_connection_api_v1_discord_connections__connection_id__refresh_post",
		},
		"/api/v1/discord/connections/{connection_id}/rotate-token": {
			"post": "rotate_connection_api_v1_discord_connections__connection_id__rotate_token_post",
		},
		"/api/v1/discord/connections/{connection_id}/installation-url": {
			"post": "installation_api_v1_discord_connections__connection_id__installation_url_post",
		},
		"/api/v1/discord/connections/{connection_id}/servers": {
			"get": "servers_api_v1_discord_connections__connection_id__servers_get",
		},
		"/api/v1/discord/connections/{connection_id}/servers/{server_id}/channels": {
			"get": "channels_api_v1_discord_connections__connection_id__servers__server_id__channels_get",
		},
		"/api/v1/discord/connections/{connection_id}/servers/{server_id}/roles": {
			"get": "roles_api_v1_discord_connections__connection_id__servers__server_id__roles_get",
		},
		"/api/v1/discord/bindings": {
			"get": "bindings_api_v1_discord_bindings_get", "post": "create_binding_api_v1_discord_bindings_post",
		},
		"/api/v1/discord/bindings/{binding_id}": {
			"get": "binding_api_v1_discord_bindings__binding_id__get", "patch": "update_binding_api_v1_discord_bindings__binding_id__patch", "delete": "delete_binding_api_v1_discord_bindings__binding_id__delete",
		},
		"/api/v1/discord/bindings/{binding_id}/validate": {
			"post": "validate_binding_api_v1_discord_bindings__binding_id__validate_post",
		},
		"/api/v1/discord/bindings/{binding_id}/test-message": {
			"post": "test_binding_api_v1_discord_bindings__binding_id__test_message_post",
		},
	}
	count := 0
	for path, methods := range want {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing OpenAPI path %s", path)
		}
		for method, operationID := range methods {
			operation, ok := item[method].(map[string]any)
			if !ok || operation["operationId"] != operationID {
				t.Fatalf("operation %s %s=%v", method, path, operation["operationId"])
			}
			count++
		}
	}
	if count != 18 {
		t.Fatalf("operation count=%d", count)
	}
	createConnectionOperation := paths["/api/v1/discord/connections"].(map[string]any)["post"].(map[string]any)
	parameters := createConnectionOperation["parameters"].([]any)
	seenIdempotency, seenCSRF := false, false
	for _, raw := range parameters {
		parameter := raw.(map[string]any)
		switch parameter["name"] {
		case "Idempotency-Key":
			seenIdempotency = parameter["required"] == true
		case "X-CSRF-Token":
			seenCSRF = true
		case "Content-Type":
			t.Fatalf("raw-body implementation header escaped into OpenAPI: %v", parameter)
		}
	}
	if !seenIdempotency || !seenCSRF {
		t.Fatalf("write headers=%v", parameters)
	}
	requestBody := createConnectionOperation["requestBody"].(map[string]any)
	if requestBody["required"] != true {
		t.Fatalf("request body=%v", requestBody)
	}
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{
		"CreateDiscordConnectionRequest", "UpdateDiscordConnectionRequest", "ExpectedDiscordVersionRequest",
		"RotateDiscordTokenRequest", "DiscordInstallationRequest", "CreateDiscordBindingRequest",
		"UpdateDiscordBindingRequest", "DiscordConnectionResponse", "DiscordServerResponse",
		"DiscordChannelResponse", "DiscordRoleResponse", "DiscordBindingResponse", "DiscordInstallationResponse",
	} {
		if components[name] == nil {
			t.Fatalf("missing OpenAPI schema %s", name)
		}
	}
	createBindingSchema := components["CreateDiscordBindingRequest"].(map[string]any)
	createBindingProperties := createBindingSchema["properties"].(map[string]any)
	for _, field := range []string{"connection_id", "server_id", "listen_channel_id", "agent_id", "triggers", "reply_policy", "enabled"} {
		if createBindingProperties[field] == nil {
			t.Fatalf("CreateDiscordBindingRequest is missing %s: %v", field, createBindingSchema)
		}
	}
	for _, removed := range []string{"knowledge_base_id", "trigger_policy"} {
		if createBindingProperties[removed] != nil {
			t.Fatalf("CreateDiscordBindingRequest retained %s: %v", removed, createBindingSchema)
		}
	}
	if discordSchemaIncludesNull(createBindingProperties["triggers"].(map[string]any)) {
		t.Fatalf("required triggers are nullable: %v", createBindingProperties["triggers"])
	}
	for _, schemaName := range []string{
		"CreateDiscordBindingRequest", "UpdateDiscordBindingRequest", "DiscordBindingResponse",
	} {
		properties := components[schemaName].(map[string]any)["properties"].(map[string]any)
		triggerItems := properties["triggers"].(map[string]any)["items"].(map[string]any)
		if values, ok := triggerItems["enum"].([]any); !ok || len(values) != 2 || values[0] != "mention" || values[1] != "slash_command" {
			t.Fatalf("%s trigger item enum=%v", schemaName, triggerItems)
		}
	}
	expectedProperties := components["ExpectedDiscordVersionRequest"].(map[string]any)["properties"].(map[string]any)
	expectedVersion := expectedProperties["expected_version"].(map[string]any)
	if expectedVersion["exclusiveMinimum"] != float64(0) || expectedVersion["format"] != nil {
		t.Fatalf("expected_version schema=%v", expectedVersion)
	}
	updateProperties := components["UpdateDiscordBindingRequest"].(map[string]any)["properties"].(map[string]any)
	if !discordSchemaIncludesNull(updateProperties["connection_id"].(map[string]any)) ||
		!discordSchemaIncludesNull(updateProperties["allowed_role_ids"].(map[string]any)) ||
		!discordSchemaIncludesNull(updateProperties["enabled"].(map[string]any)) {
		t.Fatalf("nullable update schema=%v", updateProperties)
	}
}

func discordRoutesTestHandler(t *testing.T, sessions auth.SessionService, service DiscordService, reader DiscordJobReader) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ref0 test", "test")
	config.CreateHooks, config.Transformers = nil, nil
	api := humago.New(mux, config)
	RegisterDiscordRoutes(api, sessions, service, reader)
	return problemBoundary(mux)
}

func discordOpenAPIDocument(t *testing.T, handler http.Handler) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI response=%d %s", response.Code, response.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func discordSchemaIncludesNull(schema map[string]any) bool {
	types, ok := schema["type"].([]any)
	if !ok {
		return false
	}
	for _, value := range types {
		if value == "null" {
			return true
		}
	}
	return false
}

func discordRouteFixtures(t *testing.T) (*fakeDiscordService, *fakeDiscordJobReader) {
	t.Helper()
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	connectionID := discorddomain.ConnectionID{1}
	credentialID := credentials.ID{2}
	applicationID, botID := discorddomain.Snowflake("777"), discorddomain.Snowflake("778")
	username := "ref0"
	connection := discorddomain.Connection{
		ID: connectionID, DisplayName: "Docs bot", CredentialID: credentialID, CredentialVersion: 7,
		ApplicationID: &applicationID, BotUserID: &botID, BotUsername: &username,
		Lifecycle: discorddomain.ConnectionEnabled, State: discorddomain.StateConnecting,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	replyID := discorddomain.Snowflake("457")
	binding := discorddomain.Binding{
		ID: discorddomain.BindingID{3}, ConnectionID: connectionID, ServerID: "123", ListenChannelID: "456",
		AgentID: agents.AgentID{4}, Triggers: []discorddomain.TriggerType{discorddomain.TriggerMention},
		ReplyPolicy: discorddomain.ReplySelectedChannel, ReplyChannelID: &replyID,
		AllowedRoleIDs: []discorddomain.Snowflake{"789"}, AllowedUserIDs: []discorddomain.Snowflake{},
		RatePolicy: discorddomain.DefaultRatePolicy(), Enabled: true, Health: discorddomain.BindingHealthy,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	service := &fakeDiscordService{
		connection: connection, binding: binding, jobID: jobs.JobID{9},
		servers: []discorddomain.Server{{ConnectionID: connectionID, ServerID: "123", Name: "Docs", Owner: true, RefreshedAt: now}},
		channels: []discorddomain.Channel{{
			ConnectionID: connectionID, ServerID: "123", ChannelID: "456", Name: "support",
			ChannelType: 0, EffectiveBotPermissions: discorddomain.BasePermissions,
			ViewerRoleIDs: []discorddomain.Snowflake{"789"}, ViewerUserIDs: []discorddomain.Snowflake{}, RefreshedAt: now,
		}},
		roles: []discorddomain.Role{{ConnectionID: connectionID, ServerID: "123", RoleID: "789", Name: "reader", RefreshedAt: now}},
	}
	job := jobs.Snapshot{
		ID: service.jobID, Type: jobs.RefreshDiscord, TargetType: "discord_connection", TargetID: jobs.UUID(connectionID),
		Status: jobs.Pending, MaxAttempts: 3, Result: map[string]any{}, CreatedAt: now, UpdatedAt: now,
	}
	return service, &fakeDiscordJobReader{value: job}
}

func applyFakeBindingConfiguration(value *discorddomain.Binding, configuration discorddomain.BindingConfiguration) {
	value.ConnectionID, value.ServerID, value.ListenChannelID = configuration.ConnectionID, configuration.ServerID, configuration.ListenChannelID
	value.AgentID, value.Triggers, value.ReplyPolicy = configuration.AgentID, configuration.Triggers, configuration.ReplyPolicy
	value.ReplyChannelID, value.AllowedRoleIDs, value.AllowedUserIDs = configuration.ReplyChannelID, configuration.AllowedRoleIDs, configuration.AllowedUserIDs
	value.RatePolicy = configuration.RatePolicy
}

var _ DiscordService = (*fakeDiscordService)(nil)
var _ DiscordJobReader = (*fakeDiscordJobReader)(nil)
