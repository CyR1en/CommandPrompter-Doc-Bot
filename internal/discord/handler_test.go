package discord

import (
	"context"
	"errors"
	"testing"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/worker"
)

type fakeDiscordExecutionService struct {
	connection Connection
	binding    *Binding
	bindings   []Binding
	assertErr  error
	assertions int

	identityCompletions        int
	refreshCompletions         int
	failedExecution            string
	failedBindingID            *BindingID
	failedRegistrations        []Snowflake
	failedRegistrationBindings [][]BindingCapture
}

func (service *fakeDiscordExecutionService) AssertExecution(context.Context, jobs.Command, jobs.Permit) (Connection, *Binding, error) {
	service.assertions++
	if service.assertErr != nil {
		return Connection{}, nil, service.assertErr
	}
	return service.connection, service.binding, nil
}

func (service *fakeDiscordExecutionService) CompleteIdentity(context.Context, ConnectionID, Identity, jobs.Permit) error {
	service.identityCompletions++
	return nil
}

func (service *fakeDiscordExecutionService) CompleteRefresh(context.Context, ConnectionID, Identity, []ServerSnapshot, jobs.Permit) error {
	service.refreshCompletions++
	return nil
}

func (service *fakeDiscordExecutionService) FailExecution(_ context.Context, _ ConnectionID, message string, _ jobs.Permit, bindingID *BindingID) error {
	service.failedExecution = message
	service.failedBindingID = bindingID
	return nil
}

func (service *fakeDiscordExecutionService) FailCommandRegistration(
	_ context.Context, _ ConnectionID, serverID Snowflake, _ jobs.Permit, _ *BindingID, captures []BindingCapture,
) error {
	service.failedRegistrations = append(service.failedRegistrations, serverID)
	service.failedRegistrationBindings = append(service.failedRegistrationBindings, append([]BindingCapture(nil), captures...))
	return nil
}

func (service *fakeDiscordExecutionService) ListBindings(context.Context) ([]Binding, error) {
	return append([]Binding(nil), service.bindings...), nil
}

type fakeDiscordTokenReader struct {
	secret *security.SecretValue
	err    error
	calls  int
}

func (reader *fakeDiscordTokenReader) Read(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error) {
	reader.calls++
	return reader.secret, reader.err
}

type fakeDiscordREST struct {
	identity        Identity
	snapshots       []ServerSnapshot
	validateErr     error
	refreshErr      error
	registrationErr map[Snowflake]error
	testErr         error
	registrations   []Snowflake
	testChannel     Snowflake
	testContent     string
	closed          bool
}

func (client *fakeDiscordREST) ValidateToken(context.Context, string) (Identity, error) {
	return client.identity, client.validateErr
}

func (client *fakeDiscordREST) RefreshServers(context.Context, string, Snowflake) ([]ServerSnapshot, error) {
	return client.snapshots, client.refreshErr
}

func (client *fakeDiscordREST) SendTestMessage(_ context.Context, _ string, channel Snowflake, content string) (Snowflake, error) {
	client.testChannel, client.testContent = channel, content
	return "900", client.testErr
}

func (client *fakeDiscordREST) RegisterAskCommand(_ context.Context, _ string, _ Snowflake, server Snowflake) (Snowflake, error) {
	client.registrations = append(client.registrations, server)
	return "901", client.registrationErr[server]
}

func (client *fakeDiscordREST) Close() { client.closed = true }

func TestDiscordWorkerValidateAndSelectedChannelMessageAreFenced(t *testing.T) {
	secret, _ := security.NewSecretValue("write-only-discord-token")
	connection := discordHandlerConnection()
	binding := discordHandlerBinding(connection.ID)
	reply := Snowflake("777")
	binding.ReplyPolicy, binding.ReplyChannelID = ReplySelectedChannel, &reply
	for _, test := range []struct {
		name   string
		action string
		bind   *Binding
	}{
		{name: "validate", action: "validate"},
		{name: "test message", action: "test_message", bind: &binding},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDiscordExecutionService{connection: connection, binding: test.bind}
			reader := &fakeDiscordTokenReader{secret: secret}
			client := &fakeDiscordREST{identity: Identity{ApplicationID: "800", BotUserID: "801", Username: "ref0"}}
			registry, err := WorkerHandlers(service, reader, func() (RESTAPI, error) { return client, nil })
			if err != nil {
				t.Fatal(err)
			}
			command := discordHandlerCommand(test.action, connection, test.bind)
			result, err := registry[jobs.RefreshDiscord](context.Background(), command, jobs.Permit{WorkerID: "worker", LeaseGeneration: 1})
			if err != nil {
				t.Fatal(err)
			}
			if reader.calls < 1 || service.assertions < 2 || !client.closed {
				t.Fatalf("reader=%d assertions=%d closed=%v", reader.calls, service.assertions, client.closed)
			}
			if test.action == "validate" {
				if service.identityCompletions != 1 || result["application_id"] != "800" {
					t.Fatalf("result=%v completions=%d", result, service.identityCompletions)
				}
			} else if client.testChannel != reply || client.testContent != "ref0 connection test succeeded." || result["message_id"] != "900" {
				t.Fatalf("result=%v channel=%s content=%q", result, client.testChannel, client.testContent)
			}
		})
	}
}

func TestDiscordWorkerRefreshRegistersEachServerOnceAndQuarantinesFailures(t *testing.T) {
	secret, _ := security.NewSecretValue("write-only-discord-token")
	connection := discordHandlerConnection()
	first := discordHandlerBinding(connection.ID)
	first.ServerID, first.Enabled, first.Triggers = "100", true, []TriggerType{TriggerMention, TriggerSlashCommand}
	second := first
	second.ID = BindingID{3}
	third := first
	third.ID, third.ServerID = BindingID{4}, "200"
	client := &fakeDiscordREST{
		identity:        Identity{ApplicationID: "800", BotUserID: "801", Username: "ref0"},
		snapshots:       []ServerSnapshot{{Server: ServerMetadata{ID: "100", Name: "one"}}},
		registrationErr: map[Snowflake]error{"200": apiError("Discord API rate limit was reached.")},
	}
	service := &fakeDiscordExecutionService{connection: connection, bindings: []Binding{first, second, third}}
	registry, err := WorkerHandlers(service, &fakeDiscordTokenReader{secret: secret}, func() (RESTAPI, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry[jobs.RefreshDiscord](context.Background(), discordHandlerCommand("refresh", connection, nil), jobs.Permit{WorkerID: "worker", LeaseGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if service.refreshCompletions != 1 || len(client.registrations) != 2 ||
		client.registrations[0] != "100" || client.registrations[1] != "200" ||
		len(service.failedRegistrations) != 1 || service.failedRegistrations[0] != "200" ||
		result["registered_server_count"] != 1 || result["registration_failure_count"] != 1 {
		t.Fatalf("result=%v registrations=%v failed=%v", result, client.registrations, service.failedRegistrations)
	}
	if captures := service.failedRegistrationBindings; len(captures) != 1 || len(captures[0]) != 1 ||
		captures[0][0] != (BindingCapture{ID: third.ID, Version: third.Version}) {
		t.Fatalf("registration captures=%+v", captures)
	}
}

func TestDiscordWorkerMapsOnlySafeFailuresAndPreservesStalePermit(t *testing.T) {
	connection := discordHandlerConnection()
	binding := discordHandlerBinding(connection.ID)
	secret, _ := security.NewSecretValue("write-only-discord-token")
	tests := []struct {
		name         string
		action       string
		binding      *Binding
		reader       *fakeDiscordTokenReader
		client       *fakeDiscordREST
		wantSafe     string
		wantCode     string
		wantRetry    bool
		registration bool
	}{
		{
			name: "rate retry", action: "test_message", binding: &binding,
			reader:   &fakeDiscordTokenReader{secret: secret},
			client:   &fakeDiscordREST{identity: Identity{ApplicationID: "800", BotUserID: "801", Username: "ref0"}, testErr: apiError("Discord API rate limit was reached.")},
			wantSafe: "Discord API rate limit was reached.", wantCode: "discord_api:unavailable", wantRetry: true,
		},
		{
			name: "credential rejection", action: "validate",
			reader: &fakeDiscordTokenReader{err: credentials.ErrSecretUnavailable},
			client: &fakeDiscordREST{}, wantSafe: "Discord credential is unavailable.", wantCode: "discord_api:rejected",
		},
		{
			name: "registration never retries", action: "register_command", binding: &binding,
			reader:   &fakeDiscordTokenReader{secret: secret},
			client:   &fakeDiscordREST{identity: Identity{ApplicationID: "800", BotUserID: "801", Username: "ref0"}, registrationErr: map[Snowflake]error{"100": apiError("Discord API is unavailable.")}},
			wantCode: "discord_api:unavailable", registration: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeDiscordExecutionService{connection: connection, binding: test.binding}
			registry, err := WorkerHandlers(service, test.reader, func() (RESTAPI, error) { return test.client, nil })
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry[jobs.RefreshDiscord](context.Background(), discordHandlerCommand(test.action, connection, test.binding), jobs.Permit{WorkerID: "worker", LeaseGeneration: 1})
			var failure *worker.HandlerFailure
			if !errors.As(err, &failure) || failure.SanitizedError != test.wantCode || failure.Retryable != test.wantRetry {
				t.Fatalf("failure=%#v err=%v", failure, err)
			}
			if test.registration {
				if len(service.failedRegistrations) != 1 || service.failedExecution != "" {
					t.Fatalf("registration=%v execution=%q", service.failedRegistrations, service.failedExecution)
				}
				if captures := service.failedRegistrationBindings; len(captures) != 1 || len(captures[0]) != 1 ||
					captures[0][0] != (BindingCapture{ID: binding.ID, Version: binding.Version}) {
					t.Fatalf("registration captures=%+v", captures)
				}
			} else if service.failedExecution != test.wantSafe {
				t.Fatalf("safe failure = %q", service.failedExecution)
			}
		})
	}

	staleService := &fakeDiscordExecutionService{connection: connection, assertErr: jobs.ErrStalePermit}
	registry, _ := WorkerHandlers(staleService, &fakeDiscordTokenReader{secret: secret}, func() (RESTAPI, error) { return &fakeDiscordREST{}, nil })
	_, err := registry[jobs.RefreshDiscord](context.Background(), discordHandlerCommand("validate", connection, nil), jobs.Permit{WorkerID: "worker", LeaseGeneration: 1})
	if !errors.Is(err, jobs.ErrStalePermit) || staleService.failedExecution != "" {
		t.Fatalf("stale err=%v failed=%q", err, staleService.failedExecution)
	}
}

func discordHandlerConnection() Connection {
	return Connection{
		ID: ConnectionID{1}, CredentialID: credentials.ID{2}, CredentialVersion: 7,
		Version: 3, Lifecycle: ConnectionEnabled, State: StateConnecting,
	}
}

func discordHandlerBinding(connectionID ConnectionID) Binding {
	return Binding{
		ID: BindingID{2}, ConnectionID: connectionID, ServerID: "100", ListenChannelID: "500",
		Triggers: []TriggerType{TriggerMention, TriggerSlashCommand}, ReplyPolicy: ReplySameChannel, Version: 5,
	}
}

func discordHandlerCommand(action string, connection Connection, binding *Binding) jobs.Command {
	payload := map[string]any{
		"action": action, "connection_id": connection.ID.String(), "connection_version": connection.Version,
		"credential_id": connection.CredentialID.String(), "credential_version": connection.CredentialVersion,
	}
	target := jobs.UUID(connection.ID)
	if binding != nil {
		payload["binding_id"] = binding.ID.String()
		payload["binding_version"] = binding.Version
		target = jobs.UUID(binding.ID)
	}
	return jobs.Command{Type: jobs.RefreshDiscord, TargetType: "discord", TargetID: target, Payload: payload, OperationKey: "test"}
}
