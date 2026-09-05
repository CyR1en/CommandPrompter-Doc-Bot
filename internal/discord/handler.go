package discord

import (
	"context"
	"errors"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/worker"
)

type ExecutionService interface {
	AssertExecution(context.Context, jobs.Command, jobs.Permit) (Connection, *Binding, error)
	CompleteIdentity(context.Context, ConnectionID, Identity, jobs.Permit) error
	CompleteRefresh(context.Context, ConnectionID, Identity, []ServerSnapshot, jobs.Permit) error
	FailExecution(context.Context, ConnectionID, string, jobs.Permit, *BindingID) error
	FailCommandRegistration(context.Context, ConnectionID, Snowflake, jobs.Permit, *BindingID, []BindingCapture) error
	ListBindings(context.Context) ([]Binding, error)
}

type TokenReader interface {
	Read(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error)
}

type RESTAPI interface {
	ValidateToken(context.Context, string) (Identity, error)
	RefreshServers(context.Context, string, Snowflake) ([]ServerSnapshot, error)
	SendTestMessage(context.Context, string, Snowflake, string) (Snowflake, error)
	RegisterAskCommand(context.Context, string, Snowflake, Snowflake) (Snowflake, error)
	Close()
}

type RESTFactory func() (RESTAPI, error)

func WorkerHandlers(service ExecutionService, secrets TokenReader, factory RESTFactory) (worker.Registry, error) {
	if service == nil || secrets == nil {
		return nil, errors.New("Discord handler dependencies are incomplete")
	}
	if factory == nil {
		factory = func() (RESTAPI, error) { return NewRESTClient(RESTOptions{}) }
	}
	handler := &discordWorkerHandler{service: service, secrets: secrets, factory: factory}
	return worker.Registry{jobs.RefreshDiscord: handler.execute}, nil
}

type discordWorkerHandler struct {
	service ExecutionService
	secrets TokenReader
	factory RESTFactory
}

func (handler *discordWorkerHandler) execute(
	ctx context.Context,
	command jobs.Command,
	permit jobs.Permit,
) (map[string]any, error) {
	if command.Type != jobs.RefreshDiscord {
		return nil, errors.New("Discord job is invalid")
	}
	action, ok := command.Payload["action"].(string)
	if !ok || !validDiscordAction(action) {
		return nil, errors.New("Discord job action is invalid")
	}
	connection, binding, err := handler.service.AssertExecution(ctx, command, permit)
	if err != nil {
		return nil, err
	}
	client, err := handler.factory()
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("Discord REST client is unavailable")
	}
	defer client.Close()

	leasedToken := func() (string, error) {
		current, _, assertErr := handler.service.AssertExecution(ctx, command, permit)
		if assertErr != nil {
			return "", assertErr
		}
		secret, readErr := handler.secrets.Read(
			ctx, current.CredentialID, credentials.DiscordBotToken, current.CredentialVersion,
		)
		if readErr != nil {
			return "", readErr
		}
		if secret == nil {
			return "", credentials.ErrSecretUnavailable
		}
		return secret.Reveal(), nil
	}

	token, err := leasedToken()
	if err != nil {
		return handler.handleExecutionFailure(ctx, action, connection, binding, permit, err)
	}
	identity, err := client.ValidateToken(ctx, token)
	if err != nil {
		return handler.handleExecutionFailure(ctx, action, connection, binding, permit, err)
	}

	switch action {
	case "validate":
		if _, _, err = handler.service.AssertExecution(ctx, command, permit); err != nil {
			return nil, err
		}
		if err = handler.service.CompleteIdentity(ctx, connection.ID, identity, permit); err != nil {
			return nil, err
		}
		return map[string]any{
			"connection_id":  connection.ID.String(),
			"application_id": string(identity.ApplicationID),
			"bot_user_id":    string(identity.BotUserID),
		}, nil

	case "refresh":
		token, err = leasedToken()
		if err != nil {
			return handler.handleExecutionFailure(ctx, action, connection, binding, permit, err)
		}
		snapshots, refreshErr := client.RefreshServers(ctx, token, identity.BotUserID)
		if refreshErr != nil {
			return handler.handleExecutionFailure(ctx, action, connection, binding, permit, refreshErr)
		}
		if _, _, err = handler.service.AssertExecution(ctx, command, permit); err != nil {
			return nil, err
		}
		if err = handler.service.CompleteRefresh(ctx, connection.ID, identity, snapshots, permit); err != nil {
			return nil, err
		}
		configured, err := handler.service.ListBindings(ctx)
		if err != nil {
			return nil, err
		}
		registered := make(map[Snowflake]struct{})
		failed := make(map[Snowflake]struct{})
		serverBindings := make(map[Snowflake][]BindingCapture)
		for _, candidate := range configured {
			if candidate.ConnectionID == connection.ID && candidate.Enabled && candidate.HasTrigger(TriggerSlashCommand) {
				serverBindings[candidate.ServerID] = append(serverBindings[candidate.ServerID], BindingCapture{
					ID: candidate.ID, Version: candidate.Version,
				})
			}
		}
		for _, candidate := range configured {
			if candidate.ConnectionID != connection.ID || !candidate.Enabled || !candidate.HasTrigger(TriggerSlashCommand) {
				continue
			}
			if _, exists := registered[candidate.ServerID]; exists {
				continue
			}
			if _, exists := failed[candidate.ServerID]; exists {
				continue
			}
			token, err = leasedToken()
			if err != nil {
				return handler.handleExecutionFailure(ctx, action, connection, binding, permit, err)
			}
			_, registrationErr := client.RegisterAskCommand(ctx, token, identity.ApplicationID, candidate.ServerID)
			if registrationErr != nil {
				var apiFailure *APIError
				if !errors.As(registrationErr, &apiFailure) {
					return nil, registrationErr
				}
				if err = handler.service.FailCommandRegistration(
					ctx, connection.ID, candidate.ServerID, permit, nil, serverBindings[candidate.ServerID],
				); err != nil {
					return nil, err
				}
				failed[candidate.ServerID] = struct{}{}
				continue
			}
			registered[candidate.ServerID] = struct{}{}
		}
		return map[string]any{
			"connection_id":              connection.ID.String(),
			"server_count":               len(snapshots),
			"registered_server_count":    len(registered),
			"registration_failure_count": len(failed),
		}, nil

	case "register_command":
		if binding == nil || !binding.HasTrigger(TriggerSlashCommand) {
			return nil, errors.New("Discord command-registration job has no slash binding")
		}
		token, err = leasedToken()
		if err != nil {
			return handler.handleExecutionFailure(ctx, action, connection, binding, permit, err)
		}
		commandID, registrationErr := client.RegisterAskCommand(
			ctx, token, identity.ApplicationID, binding.ServerID,
		)
		if registrationErr != nil {
			return handler.handleExecutionFailure(ctx, action, connection, binding, permit, registrationErr)
		}
		return map[string]any{"binding_id": binding.ID.String(), "command_id": string(commandID)}, nil

	case "test_message":
		if binding == nil {
			return nil, errors.New("Discord test-message job has no binding")
		}
		channelID := binding.ListenChannelID
		if binding.ReplyPolicy == ReplySelectedChannel {
			if binding.ReplyChannelID == nil {
				return nil, errors.New("Discord test-message job has no reply channel")
			}
			channelID = *binding.ReplyChannelID
		}
		token, err = leasedToken()
		if err != nil {
			return handler.handleExecutionFailure(ctx, action, connection, binding, permit, err)
		}
		messageID, messageErr := client.SendTestMessage(
			ctx, token, channelID, "ref0 connection test succeeded.",
		)
		if messageErr != nil {
			return handler.handleExecutionFailure(ctx, action, connection, binding, permit, messageErr)
		}
		return map[string]any{"binding_id": binding.ID.String(), "message_id": string(messageID)}, nil
	default:
		return nil, errors.New("Discord job action is invalid")
	}
}

func (handler *discordWorkerHandler) handleExecutionFailure(
	ctx context.Context,
	action string,
	connection Connection,
	binding *Binding,
	permit jobs.Permit,
	failure error,
) (map[string]any, error) {
	var apiFailure *APIError
	safe := ""
	if errors.As(failure, &apiFailure) {
		safe = apiFailure.Error()
	} else if errors.Is(failure, credentials.ErrSecretUnavailable) {
		safe = "Discord credential is unavailable."
	} else {
		return nil, failure
	}
	if action == "register_command" && binding != nil {
		id := binding.ID
		if err := handler.service.FailCommandRegistration(ctx, connection.ID, binding.ServerID, permit, &id,
			[]BindingCapture{{ID: binding.ID, Version: binding.Version}}); err != nil {
			return nil, err
		}
	} else {
		var bindingID *BindingID
		if binding != nil {
			id := binding.ID
			bindingID = &id
		}
		if err := handler.service.FailExecution(ctx, connection.ID, safe, permit, bindingID); err != nil {
			return nil, err
		}
	}
	retryable := safe == "Discord API is unavailable." ||
		safe == "Discord API rate limit was reached." ||
		safe == "Discord API request failed."
	code := "discord_api:rejected"
	if retryable {
		code = "discord_api:unavailable"
	}
	return nil, &worker.HandlerFailure{
		SanitizedError: code,
		Retryable:      retryable && action != "register_command",
	}
}

func validDiscordAction(action string) bool {
	switch action {
	case "validate", "refresh", "register_command", "test_message":
		return true
	default:
		return false
	}
}
