package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	discorddomain "github.com/cyr1en/ref0/internal/discord"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/danielgtaylor/huma/v2"
)

const (
	discordPath      = "/api/v1/discord"
	connectionsPath  = discordPath + "/connections"
	bindingsPath     = discordPath + "/bindings"
	discordBodyLimit = 1 << 20
)

// DiscordService is the complete control-plane dependency for the Discord API.
// Gateway and worker behavior deliberately stay outside this HTTP adapter.
type DiscordService interface {
	ListConnections(context.Context) ([]discorddomain.Connection, error)
	GetConnection(context.Context, discorddomain.ConnectionID) (discorddomain.Connection, error)
	CreateConnection(context.Context, discorddomain.CreateConnection, discorddomain.ActorID, string) (discorddomain.Connection, error)
	UpdateConnection(context.Context, discorddomain.UpdateConnection, discorddomain.ActorID, string) (discorddomain.Connection, error)
	RotateConnectionToken(context.Context, discorddomain.RotateToken, discorddomain.ActorID, string) (discorddomain.Connection, error)
	RequestConnectionValidation(context.Context, discorddomain.ConnectionID, int32, discorddomain.ActorID, string) (jobs.JobID, error)
	RequestConnectionRefresh(context.Context, discorddomain.ConnectionID, int32, discorddomain.ActorID, string) (jobs.JobID, error)
	InstallationURL(context.Context, discorddomain.ConnectionID, bool) (string, error)
	ListServers(context.Context, discorddomain.ConnectionID) ([]discorddomain.Server, error)
	ListChannels(context.Context, discorddomain.ConnectionID, discorddomain.Snowflake) ([]discorddomain.Channel, error)
	ListRoles(context.Context, discorddomain.ConnectionID, discorddomain.Snowflake) ([]discorddomain.Role, error)
	ListBindings(context.Context) ([]discorddomain.Binding, error)
	GetBinding(context.Context, discorddomain.BindingID) (discorddomain.Binding, error)
	CreateBinding(context.Context, discorddomain.CreateBinding, discorddomain.ActorID, string) (discorddomain.Binding, error)
	UpdateBinding(context.Context, discorddomain.UpdateBinding, discorddomain.ActorID, string) (discorddomain.Binding, error)
	DeleteBinding(context.Context, discorddomain.BindingID, int32, discorddomain.ActorID, string) error
	ValidateBinding(context.Context, discorddomain.BindingID, int32, discorddomain.ActorID, string) (discorddomain.Binding, error)
	RequestTestMessage(context.Context, discorddomain.BindingID, int32, discorddomain.ActorID, string) (jobs.JobID, error)
}

type DiscordJobReader interface {
	Get(context.Context, jobs.JobID) (jobs.Snapshot, error)
}

var _ DiscordService = (*discorddomain.Store)(nil)
var _ DiscordJobReader = (*jobs.Service)(nil)

type createDiscordConnectionRequest struct {
	DisplayName  string `json:"display_name" minLength:"1" maxLength:"255"`
	CredentialID string `json:"credential_id" format:"uuid"`
}

type updateDiscordConnectionRequest struct {
	ExpectedVersion int     `json:"expected_version" exclusiveMinimum:"0"`
	DisplayName     *string `json:"display_name,omitempty" minLength:"1" maxLength:"255" nullable:"true"`
	Lifecycle       *string `json:"lifecycle,omitempty" enum:"enabled,disabled" nullable:"true"`
}

type expectedDiscordVersionRequest struct {
	ExpectedVersion int `json:"expected_version" exclusiveMinimum:"0"`
}

type rotateDiscordTokenRequest struct {
	ExpectedVersion int    `json:"expected_version" exclusiveMinimum:"0"`
	CredentialID    string `json:"credential_id" format:"uuid"`
}

type discordInstallationRequest struct {
	Threads bool `json:"threads,omitempty" default:"false"`
}

type discordBindingConfigurationRequest struct {
	ConnectionID      string   `json:"connection_id" format:"uuid"`
	ServerID          string   `json:"server_id" pattern:"^[1-9][0-9]{0,19}$"`
	ListenChannelID   string   `json:"listen_channel_id" pattern:"^[1-9][0-9]{0,19}$"`
	AgentID           string   `json:"agent_id" format:"uuid"`
	Triggers          []string `json:"triggers" enum:"mention,slash_command" minItems:"1" maxItems:"2" uniqueItems:"true" nullable:"false"`
	ReplyPolicy       string   `json:"reply_policy" enum:"same_channel,thread,selected_channel"`
	ReplyChannelID    *string  `json:"reply_channel_id,omitempty" pattern:"^[1-9][0-9]{0,19}$" nullable:"true"`
	AllowedRoleIDs    []string `json:"allowed_role_ids,omitempty" default:"[]" maxItems:"100" nullable:"false"`
	AllowedUserIDs    []string `json:"allowed_user_ids,omitempty" default:"[]" maxItems:"100" nullable:"false"`
	RateRequests      int      `json:"rate_requests,omitempty" default:"5" minimum:"1" maximum:"100"`
	RateWindowSeconds int      `json:"rate_window_seconds,omitempty" default:"60" minimum:"1" maximum:"86400"`
}

type createDiscordBindingRequest struct {
	ConnectionID      string   `json:"connection_id" format:"uuid"`
	ServerID          string   `json:"server_id" pattern:"^[1-9][0-9]{0,19}$"`
	ListenChannelID   string   `json:"listen_channel_id" pattern:"^[1-9][0-9]{0,19}$"`
	AgentID           string   `json:"agent_id" format:"uuid"`
	Triggers          []string `json:"triggers" enum:"mention,slash_command" minItems:"1" maxItems:"2" uniqueItems:"true" nullable:"false"`
	ReplyPolicy       string   `json:"reply_policy" enum:"same_channel,thread,selected_channel"`
	ReplyChannelID    *string  `json:"reply_channel_id,omitempty" pattern:"^[1-9][0-9]{0,19}$" nullable:"true"`
	AllowedRoleIDs    []string `json:"allowed_role_ids,omitempty" default:"[]" maxItems:"100" nullable:"false"`
	AllowedUserIDs    []string `json:"allowed_user_ids,omitempty" default:"[]" maxItems:"100" nullable:"false"`
	RateRequests      int      `json:"rate_requests,omitempty" default:"5" minimum:"1" maximum:"100"`
	RateWindowSeconds int      `json:"rate_window_seconds,omitempty" default:"60" minimum:"1" maximum:"86400"`
	Enabled           bool     `json:"enabled,omitempty" default:"false"`
}

type updateDiscordBindingRequest struct {
	ExpectedVersion   int       `json:"expected_version" exclusiveMinimum:"0"`
	ConnectionID      *string   `json:"connection_id,omitempty" format:"uuid" nullable:"true"`
	ServerID          *string   `json:"server_id,omitempty" pattern:"^[1-9][0-9]{0,19}$" nullable:"true"`
	ListenChannelID   *string   `json:"listen_channel_id,omitempty" pattern:"^[1-9][0-9]{0,19}$" nullable:"true"`
	AgentID           *string   `json:"agent_id,omitempty" format:"uuid" nullable:"true"`
	Triggers          *[]string `json:"triggers,omitempty" enum:"mention,slash_command" minItems:"1" maxItems:"2" uniqueItems:"true" nullable:"true"`
	ReplyPolicy       *string   `json:"reply_policy,omitempty" enum:"same_channel,thread,selected_channel" nullable:"true"`
	ReplyChannelID    *string   `json:"reply_channel_id,omitempty" pattern:"^[1-9][0-9]{0,19}$" nullable:"true"`
	AllowedRoleIDs    *[]string `json:"allowed_role_ids,omitempty" maxItems:"100" nullable:"true"`
	AllowedUserIDs    *[]string `json:"allowed_user_ids,omitempty" maxItems:"100" nullable:"true"`
	RateRequests      *int      `json:"rate_requests,omitempty" minimum:"1" maximum:"100" nullable:"true"`
	RateWindowSeconds *int      `json:"rate_window_seconds,omitempty" minimum:"1" maximum:"86400" nullable:"true"`
	Enabled           *bool     `json:"enabled,omitempty" nullable:"true"`
	replyChannelSet   bool
}

type discordReadInput struct {
	SessionCookie string `cookie:"ref0_session"`
}

type discordWriteInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type discordConnectionReadInput struct {
	SessionCookie string `cookie:"ref0_session"`
	ConnectionID  string `path:"connection_id" format:"uuid"`
}

type discordConnectionWriteInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	ConnectionID   string `path:"connection_id" format:"uuid"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type discordDirectoryInput struct {
	SessionCookie string `cookie:"ref0_session"`
	ConnectionID  string `path:"connection_id" format:"uuid"`
	ServerID      string `path:"server_id"`
}

type discordBindingReadInput struct {
	SessionCookie string `cookie:"ref0_session"`
	BindingID     string `path:"binding_id" format:"uuid"`
}

type discordBindingWriteInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	BindingID      string `path:"binding_id" format:"uuid"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type discordConnectionResponse struct {
	ID               string     `json:"id" format:"uuid"`
	DisplayName      string     `json:"display_name"`
	CredentialID     string     `json:"credential_id" format:"uuid"`
	ApplicationID    *string    `json:"application_id"`
	BotUserID        *string    `json:"bot_user_id"`
	BotUsername      *string    `json:"bot_username"`
	AvatarHash       *string    `json:"avatar_hash"`
	Lifecycle        string     `json:"lifecycle" enum:"enabled,disabled"`
	State            string     `json:"state" enum:"disabled,connecting,ready,degraded"`
	GatewayLatencyMS *int       `json:"gateway_latency_ms"`
	LastHeartbeatAt  *time.Time `json:"last_heartbeat_at"`
	LastEventAt      *time.Time `json:"last_event_at"`
	SanitizedError   *string    `json:"sanitized_error"`
	Version          int        `json:"version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type discordServerResponse struct {
	ConnectionID string    `json:"connection_id" format:"uuid"`
	ServerID     string    `json:"server_id"`
	Name         string    `json:"name"`
	IconHash     *string   `json:"icon_hash"`
	Owner        bool      `json:"owner"`
	RefreshedAt  time.Time `json:"refreshed_at"`
}

type discordChannelResponse struct {
	ConnectionID            string    `json:"connection_id" format:"uuid"`
	ServerID                string    `json:"server_id"`
	ChannelID               string    `json:"channel_id"`
	ParentID                *string   `json:"parent_id"`
	Name                    string    `json:"name"`
	ChannelType             int       `json:"channel_type"`
	Position                int       `json:"position"`
	EffectiveBotPermissions int       `json:"effective_bot_permissions"`
	EveryoneCanView         bool      `json:"everyone_can_view"`
	ViewerRoleIDs           []string  `json:"viewer_role_ids" nullable:"false"`
	ViewerUserIDs           []string  `json:"viewer_user_ids" nullable:"false"`
	PermissionStatus        string    `json:"permission_status" enum:"ready,missing"`
	RefreshedAt             time.Time `json:"refreshed_at"`
}

type discordRoleResponse struct {
	ConnectionID string    `json:"connection_id" format:"uuid"`
	ServerID     string    `json:"server_id"`
	RoleID       string    `json:"role_id"`
	Name         string    `json:"name"`
	Position     int       `json:"position"`
	RefreshedAt  time.Time `json:"refreshed_at"`
}

type discordBindingResponse struct {
	ID                string    `json:"id" format:"uuid"`
	ConnectionID      string    `json:"connection_id" format:"uuid"`
	ServerID          string    `json:"server_id"`
	ListenChannelID   string    `json:"listen_channel_id"`
	AgentID           string    `json:"agent_id" format:"uuid"`
	Triggers          []string  `json:"triggers" enum:"mention,slash_command" nullable:"false"`
	ReplyPolicy       string    `json:"reply_policy" enum:"same_channel,thread,selected_channel"`
	ReplyChannelID    *string   `json:"reply_channel_id"`
	AllowedRoleIDs    []string  `json:"allowed_role_ids" nullable:"false"`
	AllowedUserIDs    []string  `json:"allowed_user_ids" nullable:"false"`
	RateRequests      int       `json:"rate_requests"`
	RateWindowSeconds int       `json:"rate_window_seconds"`
	Enabled           bool      `json:"enabled"`
	Health            string    `json:"health" enum:"draft,healthy,unhealthy"`
	SanitizedError    *string   `json:"sanitized_error"`
	Version           int       `json:"version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type discordInstallationResponse struct {
	URL         string   `json:"url"`
	Permissions int      `json:"permissions"`
	Scopes      []string `json:"scopes" nullable:"false"`
}

type discordConnectionsOutput struct {
	Body []discordConnectionResponse `nameHint:"DiscordConnectionResponse" nullable:"false"`
}

type discordConnectionOutput struct {
	Status int
	Body   discordConnectionResponse `nameHint:"DiscordConnectionResponse"`
}

type discordServersOutput struct {
	Body []discordServerResponse `nameHint:"DiscordServerResponse" nullable:"false"`
}

type discordChannelsOutput struct {
	Body []discordChannelResponse `nameHint:"DiscordChannelResponse" nullable:"false"`
}

type discordRolesOutput struct {
	Body []discordRoleResponse `nameHint:"DiscordRoleResponse" nullable:"false"`
}

type discordBindingsOutput struct {
	Body []discordBindingResponse `nameHint:"DiscordBindingResponse" nullable:"false"`
}

type discordBindingOutput struct {
	Status int
	Body   discordBindingResponse `nameHint:"DiscordBindingResponse"`
}

type discordInstallationOutput struct {
	Body discordInstallationResponse `nameHint:"DiscordInstallationResponse"`
}

type discordJobOutput struct {
	Status int
	Body   jobResponse `nameHint:"JobResponse"`
}

type discordNoContentOutput struct{ Status int }

// RegisterDiscordRoutes installs all 19 Discord control-plane operations.
func RegisterDiscordRoutes(api huma.API, sessions auth.SessionService, service DiscordService, jobReader DiscordJobReader) {
	registerDiscordConnectionRoutes(api, sessions, service, jobReader)
	registerDiscordDirectoryRoutes(api, sessions, service)
	registerDiscordBindingRoutes(api, sessions, service, jobReader)
	normalizeDiscordOpenAPI(api)
}

func registerDiscordConnectionRoutes(api huma.API, sessions auth.SessionService, service DiscordService, jobReader DiscordJobReader) {
	huma.Register(api, discordOperation("list_connections_api_v1_discord_connections_get", http.MethodGet, connectionsPath, "List Connections", 0),
		func(ctx context.Context, input *discordReadInput) (*discordConnectionsOutput, error) {
			if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, connectionsPath); err != nil {
				return nil, err
			}
			values, err := service.ListConnections(ctx)
			if err != nil {
				return nil, discordProblem(connectionsPath, err)
			}
			output := &discordConnectionsOutput{Body: make([]discordConnectionResponse, len(values))}
			for index, value := range values {
				output.Body[index] = newDiscordConnectionResponse(value)
			}
			return output, nil
		})

	huma.Register(api, discordOperation("create_connection_api_v1_discord_connections_post", http.MethodPost, connectionsPath, "Create Connection", http.StatusCreated),
		func(ctx context.Context, input *discordWriteInput) (*discordConnectionOutput, error) {
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, connectionsPath)
			if err != nil {
				return nil, err
			}
			var body createDiscordConnectionRequest
			if !decodeDiscordRequest(input.RawBody, input.ContentType, &body) {
				return nil, validationProblem(connectionsPath)
			}
			credentialID, credentialErr := credentials.ParseID(body.CredentialID)
			command := discorddomain.CreateConnection{DisplayName: body.DisplayName, CredentialID: credentialID}
			if credentialErr != nil || discorddomain.ValidateCreateConnection(command) != nil {
				return nil, validationProblem(connectionsPath)
			}
			value, err := service.CreateConnection(ctx, command, discorddomain.ActorID(session.Operator.ID), requestKey)
			if err != nil {
				return nil, discordProblem(connectionsPath, err)
			}
			return &discordConnectionOutput{Status: http.StatusCreated, Body: newDiscordConnectionResponse(value)}, nil
		})
	documentDiscordRequest(api, connectionsPath, http.MethodPost, reflect.TypeFor[createDiscordConnectionRequest](), "CreateDiscordConnectionRequest")

	const connectionPath = connectionsPath + "/{connection_id}"
	huma.Register(api, discordOperation("get_connection_api_v1_discord_connections__connection_id__get", http.MethodGet, connectionPath, "Get Connection", 0),
		func(ctx context.Context, input *discordConnectionReadInput) (*discordConnectionOutput, error) {
			instance := discordConnectionInstance(connectionPath, input.ConnectionID)
			if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseConnectionID(input.ConnectionID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			value, err := service.GetConnection(ctx, id)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			return &discordConnectionOutput{Body: newDiscordConnectionResponse(value)}, nil
		})

	huma.Register(api, discordOperation("update_connection_api_v1_discord_connections__connection_id__patch", http.MethodPatch, connectionPath, "Update Connection", 0),
		func(ctx context.Context, input *discordConnectionWriteInput) (*discordConnectionOutput, error) {
			instance := discordConnectionInstance(connectionPath, input.ConnectionID)
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, instance)
			if err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseConnectionID(input.ConnectionID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			var body updateDiscordConnectionRequest
			if !decodeDiscordRequest(input.RawBody, input.ContentType, &body) {
				return nil, validationProblem(instance)
			}
			expectedVersion, versionOK := discordVersion(body.ExpectedVersion)
			if !versionOK ||
				body.DisplayName != nil && (len([]rune(*body.DisplayName)) < 1 || len([]rune(*body.DisplayName)) > 255) {
				return nil, validationProblem(instance)
			}
			current, err := service.GetConnection(ctx, id)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			displayName := current.DisplayName
			if body.DisplayName != nil {
				displayName = *body.DisplayName
			}
			lifecycle := current.Lifecycle
			if body.Lifecycle != nil {
				lifecycle, err = discordConnectionLifecycle(*body.Lifecycle)
				if err != nil {
					return nil, validationProblem(instance)
				}
			}
			command := discorddomain.UpdateConnection{ConnectionID: id, ExpectedVersion: expectedVersion, DisplayName: displayName, Lifecycle: lifecycle}
			if discorddomain.ValidateUpdateConnection(command) != nil {
				return nil, validationProblem(instance)
			}
			value, err := service.UpdateConnection(ctx, command, discorddomain.ActorID(session.Operator.ID), requestKey)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			return &discordConnectionOutput{Body: newDiscordConnectionResponse(value)}, nil
		})
	documentDiscordRequest(api, connectionPath, http.MethodPatch, reflect.TypeFor[updateDiscordConnectionRequest](), "UpdateDiscordConnectionRequest")

	registerDiscordConnectionJob(api, sessions, service, jobReader, "validate", "validate_connection_api_v1_discord_connections__connection_id__validate_post", "Validate Connection")
	registerDiscordConnectionJob(api, sessions, service, jobReader, "refresh", "refresh_connection_api_v1_discord_connections__connection_id__refresh_post", "Refresh Connection")

	const rotatePath = connectionsPath + "/{connection_id}/rotate-token"
	huma.Register(api, discordOperation("rotate_connection_api_v1_discord_connections__connection_id__rotate_token_post", http.MethodPost, rotatePath, "Rotate Connection", 0),
		func(ctx context.Context, input *discordConnectionWriteInput) (*discordConnectionOutput, error) {
			instance := discordConnectionInstance(rotatePath, input.ConnectionID)
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, instance)
			if err != nil {
				return nil, err
			}
			connectionID, err := discorddomain.ParseConnectionID(input.ConnectionID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			var body rotateDiscordTokenRequest
			if !decodeDiscordRequest(input.RawBody, input.ContentType, &body) {
				return nil, validationProblem(instance)
			}
			expectedVersion, ok := discordVersion(body.ExpectedVersion)
			if !ok {
				return nil, validationProblem(instance)
			}
			credentialID, err := credentials.ParseID(body.CredentialID)
			if err != nil {
				return nil, validationProblem(instance)
			}
			command := discorddomain.RotateToken{ConnectionID: connectionID, ExpectedVersion: expectedVersion, CredentialID: credentialID}
			value, err := service.RotateConnectionToken(ctx, command, discorddomain.ActorID(session.Operator.ID), requestKey)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			return &discordConnectionOutput{Body: newDiscordConnectionResponse(value)}, nil
		})
	documentDiscordRequest(api, rotatePath, http.MethodPost, reflect.TypeFor[rotateDiscordTokenRequest](), "RotateDiscordTokenRequest")

	const installationPath = connectionsPath + "/{connection_id}/installation-url"
	huma.Register(api, discordOperation("installation_api_v1_discord_connections__connection_id__installation_url_post", http.MethodPost, installationPath, "Installation", 0),
		func(ctx context.Context, input *discordConnectionWriteInput) (*discordInstallationOutput, error) {
			instance := discordConnectionInstance(installationPath, input.ConnectionID)
			if _, _, _, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, instance); err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseConnectionID(input.ConnectionID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			var body discordInstallationRequest
			if !decodeDiscordRequest(input.RawBody, input.ContentType, &body) || discordJSONNull(input.RawBody, "threads") {
				return nil, validationProblem(instance)
			}
			url, err := service.InstallationURL(ctx, id, body.Threads)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			permissions := discorddomain.BasePermissions
			if body.Threads {
				permissions |= discorddomain.ThreadPermissions
			}
			return &discordInstallationOutput{Body: discordInstallationResponse{
				URL: url, Permissions: int(permissions), Scopes: []string{"bot", "applications.commands"},
			}}, nil
		})
	documentDiscordRequest(api, installationPath, http.MethodPost, reflect.TypeFor[discordInstallationRequest](), "DiscordInstallationRequest")
}

func registerDiscordConnectionJob(api huma.API, sessions auth.SessionService, service DiscordService, jobReader DiscordJobReader, action, operationID, summary string) {
	path := connectionsPath + "/{connection_id}/" + action
	huma.Register(api, discordOperation(operationID, http.MethodPost, path, summary, http.StatusAccepted),
		func(ctx context.Context, input *discordConnectionWriteInput) (*discordJobOutput, error) {
			instance := discordConnectionInstance(path, input.ConnectionID)
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, instance)
			if err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseConnectionID(input.ConnectionID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			expectedVersion, ok := discordExpectedVersion(input.RawBody, input.ContentType)
			if !ok {
				return nil, validationProblem(instance)
			}
			var jobID jobs.JobID
			if action == "validate" {
				jobID, err = service.RequestConnectionValidation(ctx, id, expectedVersion, discorddomain.ActorID(session.Operator.ID), requestKey)
			} else {
				jobID, err = service.RequestConnectionRefresh(ctx, id, expectedVersion, discorddomain.ActorID(session.Operator.ID), requestKey)
			}
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			job, err := jobReader.Get(ctx, jobID)
			if err != nil {
				return nil, jobProblem(instance, err)
			}
			return &discordJobOutput{Status: http.StatusAccepted, Body: newJobResponse(job)}, nil
		})
	documentDiscordRequest(api, path, http.MethodPost, reflect.TypeFor[expectedDiscordVersionRequest](), "ExpectedDiscordVersionRequest")
}

func registerDiscordDirectoryRoutes(api huma.API, sessions auth.SessionService, service DiscordService) {
	const serversPath = connectionsPath + "/{connection_id}/servers"
	huma.Register(api, discordOperation("servers_api_v1_discord_connections__connection_id__servers_get", http.MethodGet, serversPath, "Servers", 0),
		func(ctx context.Context, input *discordConnectionReadInput) (*discordServersOutput, error) {
			instance := discordConnectionInstance(serversPath, input.ConnectionID)
			if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
				return nil, err
			}
			connectionID, err := discorddomain.ParseConnectionID(input.ConnectionID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			values, err := service.ListServers(ctx, connectionID)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			output := &discordServersOutput{Body: make([]discordServerResponse, len(values))}
			for index, value := range values {
				output.Body[index] = newDiscordServerResponse(value)
			}
			return output, nil
		})

	const channelsPath = connectionsPath + "/{connection_id}/servers/{server_id}/channels"
	huma.Register(api, discordOperation("channels_api_v1_discord_connections__connection_id__servers__server_id__channels_get", http.MethodGet, channelsPath, "Channels", 0),
		func(ctx context.Context, input *discordDirectoryInput) (*discordChannelsOutput, error) {
			instance := discordDirectoryInstance(channelsPath, input.ConnectionID, input.ServerID)
			if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
				return nil, err
			}
			connectionID, serverID, err := discordDirectoryIDs(input.ConnectionID, input.ServerID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			values, err := service.ListChannels(ctx, connectionID, serverID)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			output := &discordChannelsOutput{Body: make([]discordChannelResponse, len(values))}
			for index, value := range values {
				output.Body[index] = newDiscordChannelResponse(value)
			}
			return output, nil
		})

	const rolesPath = connectionsPath + "/{connection_id}/servers/{server_id}/roles"
	huma.Register(api, discordOperation("roles_api_v1_discord_connections__connection_id__servers__server_id__roles_get", http.MethodGet, rolesPath, "Roles", 0),
		func(ctx context.Context, input *discordDirectoryInput) (*discordRolesOutput, error) {
			instance := discordDirectoryInstance(rolesPath, input.ConnectionID, input.ServerID)
			if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
				return nil, err
			}
			connectionID, serverID, err := discordDirectoryIDs(input.ConnectionID, input.ServerID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			values, err := service.ListRoles(ctx, connectionID, serverID)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			output := &discordRolesOutput{Body: make([]discordRoleResponse, len(values))}
			for index, value := range values {
				output.Body[index] = newDiscordRoleResponse(value)
			}
			return output, nil
		})
}

func registerDiscordBindingRoutes(api huma.API, sessions auth.SessionService, service DiscordService, jobReader DiscordJobReader) {
	huma.Register(api, discordOperation("bindings_api_v1_discord_bindings_get", http.MethodGet, bindingsPath, "Bindings", 0),
		func(ctx context.Context, input *discordReadInput) (*discordBindingsOutput, error) {
			if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, bindingsPath); err != nil {
				return nil, err
			}
			values, err := service.ListBindings(ctx)
			if err != nil {
				return nil, discordProblem(bindingsPath, err)
			}
			output := &discordBindingsOutput{Body: make([]discordBindingResponse, len(values))}
			for index, value := range values {
				output.Body[index] = newDiscordBindingResponse(value)
			}
			return output, nil
		})

	huma.Register(api, discordOperation("create_binding_api_v1_discord_bindings_post", http.MethodPost, bindingsPath, "Create Binding", http.StatusCreated),
		func(ctx context.Context, input *discordWriteInput) (*discordBindingOutput, error) {
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, bindingsPath)
			if err != nil {
				return nil, err
			}
			command, ok := createDiscordBindingCommand(input.RawBody, input.ContentType)
			if !ok {
				return nil, validationProblem(bindingsPath)
			}
			value, err := service.CreateBinding(ctx, command, discorddomain.ActorID(session.Operator.ID), requestKey)
			if err != nil {
				return nil, discordProblem(bindingsPath, err)
			}
			return &discordBindingOutput{Status: http.StatusCreated, Body: newDiscordBindingResponse(value)}, nil
		})
	documentDiscordRequest(api, bindingsPath, http.MethodPost, reflect.TypeFor[createDiscordBindingRequest](), "CreateDiscordBindingRequest")

	const bindingPath = bindingsPath + "/{binding_id}"
	huma.Register(api, discordOperation("binding_api_v1_discord_bindings__binding_id__get", http.MethodGet, bindingPath, "Binding", 0),
		func(ctx context.Context, input *discordBindingReadInput) (*discordBindingOutput, error) {
			instance := discordBindingInstance(bindingPath, input.BindingID)
			if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseBindingID(input.BindingID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			value, err := service.GetBinding(ctx, id)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			return &discordBindingOutput{Body: newDiscordBindingResponse(value)}, nil
		})

	huma.Register(api, discordOperation("update_binding_api_v1_discord_bindings__binding_id__patch", http.MethodPatch, bindingPath, "Update Binding", 0),
		func(ctx context.Context, input *discordBindingWriteInput) (*discordBindingOutput, error) {
			instance := discordBindingInstance(bindingPath, input.BindingID)
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, instance)
			if err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseBindingID(input.BindingID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			body, ok := updateDiscordBindingBody(input.RawBody, input.ContentType)
			if !ok {
				return nil, validationProblem(instance)
			}
			current, err := service.GetBinding(ctx, id)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			command, ok := updateDiscordBindingCommand(id, current, body)
			if !ok {
				return nil, validationProblem(instance)
			}
			value, err := service.UpdateBinding(ctx, command, discorddomain.ActorID(session.Operator.ID), requestKey)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			return &discordBindingOutput{Body: newDiscordBindingResponse(value)}, nil
		})
	documentDiscordRequest(api, bindingPath, http.MethodPatch, reflect.TypeFor[updateDiscordBindingRequest](), "UpdateDiscordBindingRequest")

	huma.Register(api, discordOperation("delete_binding_api_v1_discord_bindings__binding_id__delete", http.MethodDelete, bindingPath, "Delete Binding", http.StatusNoContent),
		func(ctx context.Context, input *discordBindingWriteInput) (*discordNoContentOutput, error) {
			instance := discordBindingInstance(bindingPath, input.BindingID)
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, instance)
			if err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseBindingID(input.BindingID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			expectedVersion, ok := discordExpectedVersion(input.RawBody, input.ContentType)
			if !ok {
				return nil, validationProblem(instance)
			}
			if err = service.DeleteBinding(ctx, id, expectedVersion, discorddomain.ActorID(session.Operator.ID), requestKey); err != nil {
				return nil, discordProblem(instance, err)
			}
			return &discordNoContentOutput{Status: http.StatusNoContent}, nil
		})
	documentDiscordRequest(api, bindingPath, http.MethodDelete, reflect.TypeFor[expectedDiscordVersionRequest](), "ExpectedDiscordVersionRequest")

	const validatePath = bindingsPath + "/{binding_id}/validate"
	huma.Register(api, discordOperation("validate_binding_api_v1_discord_bindings__binding_id__validate_post", http.MethodPost, validatePath, "Validate Binding", 0),
		func(ctx context.Context, input *discordBindingWriteInput) (*discordBindingOutput, error) {
			instance := discordBindingInstance(validatePath, input.BindingID)
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, instance)
			if err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseBindingID(input.BindingID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			expectedVersion, ok := discordExpectedVersion(input.RawBody, input.ContentType)
			if !ok {
				return nil, validationProblem(instance)
			}
			value, err := service.ValidateBinding(ctx, id, expectedVersion, discorddomain.ActorID(session.Operator.ID), requestKey)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			return &discordBindingOutput{Body: newDiscordBindingResponse(value)}, nil
		})
	documentDiscordRequest(api, validatePath, http.MethodPost, reflect.TypeFor[expectedDiscordVersionRequest](), "ExpectedDiscordVersionRequest")

	const testPath = bindingsPath + "/{binding_id}/test-message"
	huma.Register(api, discordOperation("test_binding_api_v1_discord_bindings__binding_id__test_message_post", http.MethodPost, testPath, "Test Binding", http.StatusAccepted),
		func(ctx context.Context, input *discordBindingWriteInput) (*discordJobOutput, error) {
			instance := discordBindingInstance(testPath, input.BindingID)
			_, session, requestKey, err := authenticateDiscordWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, input.IdempotencyKey, instance)
			if err != nil {
				return nil, err
			}
			id, err := discorddomain.ParseBindingID(input.BindingID)
			if err != nil {
				return nil, parameterValidationProblem(instance, "path")
			}
			expectedVersion, ok := discordExpectedVersion(input.RawBody, input.ContentType)
			if !ok {
				return nil, validationProblem(instance)
			}
			jobID, err := service.RequestTestMessage(ctx, id, expectedVersion, discorddomain.ActorID(session.Operator.ID), requestKey)
			if err != nil {
				return nil, discordProblem(instance, err)
			}
			job, err := jobReader.Get(ctx, jobID)
			if err != nil {
				return nil, jobProblem(instance, err)
			}
			return &discordJobOutput{Status: http.StatusAccepted, Body: newJobResponse(job)}, nil
		})
	documentDiscordRequest(api, testPath, http.MethodPost, reflect.TypeFor[expectedDiscordVersionRequest](), "ExpectedDiscordVersionRequest")
}

func discordOperation(operationID, method, path, summary string, status int) huma.Operation {
	operation := huma.Operation{
		OperationID: operationID, Method: method, Path: path, Summary: summary,
		Tags: []string{"discord"}, MaxBodyBytes: discordBodyLimit,
	}
	if status != 0 {
		operation.DefaultStatus = status
	}
	if method == http.MethodGet {
		operation.Errors = []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity}
	} else {
		operation.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity}
		operation.SkipValidateBody = true
	}
	return operation
}

func authenticateDiscordWrite(ctx context.Context, sessions auth.SessionService, cookie, csrf, rawKey, instance string) (auth.SessionToken, auth.OperatorSession, string, error) {
	token, session, err := RequireAuthenticatedWrite(ctx, sessions, cookie, csrf, instance)
	if err != nil {
		return auth.SessionToken{}, auth.OperatorSession{}, "", err
	}
	requestKey, err := requiredIdempotencyKey(rawKey, instance)
	return token, session, requestKey, err
}

func decodeDiscordRequest(content []byte, contentType string, destination any) bool {
	trimmed := bytes.TrimSpace(content)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' &&
		isJSONContentType(contentType) && decodeSecretRequest(content, destination)
}

func discordExpectedVersion(content []byte, contentType string) (int32, bool) {
	var body expectedDiscordVersionRequest
	if !decodeDiscordRequest(content, contentType, &body) {
		return 0, false
	}
	return discordVersion(body.ExpectedVersion)
}

func discordVersion(value int) (int32, bool) {
	if value <= 0 || value > math.MaxInt32 {
		return 0, false
	}
	return int32(value), true
}

func createDiscordBindingCommand(content []byte, contentType string) (discorddomain.CreateBinding, bool) {
	body := createDiscordBindingRequest{
		AllowedRoleIDs: []string{}, AllowedUserIDs: []string{}, RateRequests: 5, RateWindowSeconds: 60,
	}
	if !decodeDiscordRequest(content, contentType, &body) ||
		discordJSONNull(content, "enabled", "triggers", "allowed_role_ids", "allowed_user_ids") {
		return discorddomain.CreateBinding{}, false
	}
	configuration, ok := discordBindingConfiguration(discordBindingConfigurationRequest{
		ConnectionID: body.ConnectionID, ServerID: body.ServerID, ListenChannelID: body.ListenChannelID,
		AgentID: body.AgentID, Triggers: body.Triggers, ReplyPolicy: body.ReplyPolicy,
		ReplyChannelID: body.ReplyChannelID, AllowedRoleIDs: body.AllowedRoleIDs, AllowedUserIDs: body.AllowedUserIDs,
		RateRequests: body.RateRequests, RateWindowSeconds: body.RateWindowSeconds,
	})
	if !ok {
		return discorddomain.CreateBinding{}, false
	}
	command := discorddomain.CreateBinding{Configuration: configuration, Enabled: body.Enabled}
	return command, discorddomain.ValidateCreateBinding(command) == nil
}

func updateDiscordBindingBody(content []byte, contentType string) (updateDiscordBindingRequest, bool) {
	var body updateDiscordBindingRequest
	if !decodeDiscordRequest(content, contentType, &body) {
		return updateDiscordBindingRequest{}, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(content, &fields) != nil {
		return updateDiscordBindingRequest{}, false
	}
	_, body.replyChannelSet = fields["reply_channel_id"]
	return body, true
}

func updateDiscordBindingCommand(id discorddomain.BindingID, current discorddomain.Binding, body updateDiscordBindingRequest) (discorddomain.UpdateBinding, bool) {
	expectedVersion, ok := discordVersion(body.ExpectedVersion)
	if !ok {
		return discorddomain.UpdateBinding{}, false
	}
	configuration := discorddomain.BindingConfiguration{
		ConnectionID: current.ConnectionID, ServerID: current.ServerID,
		ListenChannelID: current.ListenChannelID, AgentID: current.AgentID,
		Triggers: append([]discorddomain.TriggerType(nil), current.Triggers...), ReplyPolicy: current.ReplyPolicy,
		ReplyChannelID: cloneSnowflake(current.ReplyChannelID),
		AllowedRoleIDs: append([]discorddomain.Snowflake(nil), current.AllowedRoleIDs...),
		AllowedUserIDs: append([]discorddomain.Snowflake(nil), current.AllowedUserIDs...),
		RatePolicy:     current.RatePolicy,
	}
	var err error
	if body.ConnectionID != nil {
		configuration.ConnectionID, err = discorddomain.ParseConnectionID(*body.ConnectionID)
		if err != nil {
			return discorddomain.UpdateBinding{}, false
		}
	}
	if body.ServerID != nil {
		configuration.ServerID, err = discorddomain.ParseSnowflake(*body.ServerID)
		if err != nil {
			return discorddomain.UpdateBinding{}, false
		}
	}
	if body.ListenChannelID != nil {
		configuration.ListenChannelID, err = discorddomain.ParseSnowflake(*body.ListenChannelID)
		if err != nil {
			return discorddomain.UpdateBinding{}, false
		}
	}
	if body.AgentID != nil {
		parsed, parseErr := agents.ParseID(*body.AgentID)
		configuration.AgentID, err = agents.AgentID(parsed), parseErr
		if err != nil {
			return discorddomain.UpdateBinding{}, false
		}
	}
	if body.Triggers != nil {
		configuration.Triggers, err = discordTriggers(*body.Triggers)
		if err != nil {
			return discorddomain.UpdateBinding{}, false
		}
	}
	if body.ReplyPolicy != nil {
		configuration.ReplyPolicy, err = discordReplyPolicy(*body.ReplyPolicy)
		if err != nil {
			return discorddomain.UpdateBinding{}, false
		}
	}
	if body.replyChannelSet {
		configuration.ReplyChannelID = nil
		if body.ReplyChannelID != nil {
			value, parseErr := discorddomain.ParseSnowflake(*body.ReplyChannelID)
			if parseErr != nil {
				return discorddomain.UpdateBinding{}, false
			}
			configuration.ReplyChannelID = &value
		}
	}
	if body.AllowedRoleIDs != nil {
		configuration.AllowedRoleIDs, err = discordSnowflakes(*body.AllowedRoleIDs)
		if err != nil {
			return discorddomain.UpdateBinding{}, false
		}
	}
	if body.AllowedUserIDs != nil {
		configuration.AllowedUserIDs, err = discordSnowflakes(*body.AllowedUserIDs)
		if err != nil {
			return discorddomain.UpdateBinding{}, false
		}
	}
	if body.RateRequests != nil {
		if *body.RateRequests < 1 || *body.RateRequests > 100 {
			return discorddomain.UpdateBinding{}, false
		}
		configuration.RatePolicy.Requests = int32(*body.RateRequests)
	}
	if body.RateWindowSeconds != nil {
		if *body.RateWindowSeconds < 1 || *body.RateWindowSeconds > 86_400 {
			return discorddomain.UpdateBinding{}, false
		}
		configuration.RatePolicy.WindowSeconds = int32(*body.RateWindowSeconds)
	}
	enabled := current.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	command := discorddomain.UpdateBinding{BindingID: id, ExpectedVersion: expectedVersion, Configuration: configuration, Enabled: enabled}
	return command, discorddomain.ValidateUpdateBinding(command) == nil
}

func discordBindingConfiguration(body discordBindingConfigurationRequest) (discorddomain.BindingConfiguration, bool) {
	if body.RateRequests < 1 || body.RateRequests > 100 ||
		body.RateWindowSeconds < 1 || body.RateWindowSeconds > 86_400 {
		return discorddomain.BindingConfiguration{}, false
	}
	connectionID, err := discorddomain.ParseConnectionID(body.ConnectionID)
	if err != nil {
		return discorddomain.BindingConfiguration{}, false
	}
	serverID, err := discorddomain.ParseSnowflake(body.ServerID)
	if err != nil {
		return discorddomain.BindingConfiguration{}, false
	}
	listenChannelID, err := discorddomain.ParseSnowflake(body.ListenChannelID)
	if err != nil {
		return discorddomain.BindingConfiguration{}, false
	}
	rawAgentID, err := agents.ParseID(body.AgentID)
	if err != nil {
		return discorddomain.BindingConfiguration{}, false
	}
	triggers, err := discordTriggers(body.Triggers)
	if err != nil {
		return discorddomain.BindingConfiguration{}, false
	}
	replyPolicy, err := discordReplyPolicy(body.ReplyPolicy)
	if err != nil {
		return discorddomain.BindingConfiguration{}, false
	}
	var replyChannelID *discorddomain.Snowflake
	if body.ReplyChannelID != nil {
		value, parseErr := discorddomain.ParseSnowflake(*body.ReplyChannelID)
		if parseErr != nil {
			return discorddomain.BindingConfiguration{}, false
		}
		replyChannelID = &value
	}
	allowedRoleIDs, err := discordSnowflakes(body.AllowedRoleIDs)
	if err != nil {
		return discorddomain.BindingConfiguration{}, false
	}
	allowedUserIDs, err := discordSnowflakes(body.AllowedUserIDs)
	if err != nil {
		return discorddomain.BindingConfiguration{}, false
	}
	configuration := discorddomain.BindingConfiguration{
		ConnectionID: connectionID, ServerID: serverID, ListenChannelID: listenChannelID,
		AgentID: agents.AgentID(rawAgentID), Triggers: triggers, ReplyPolicy: replyPolicy,
		ReplyChannelID: replyChannelID, AllowedRoleIDs: allowedRoleIDs, AllowedUserIDs: allowedUserIDs,
		RatePolicy: discorddomain.RatePolicy{Requests: int32(body.RateRequests), WindowSeconds: int32(body.RateWindowSeconds)},
	}
	return configuration, discorddomain.ValidateBindingConfiguration(configuration) == nil
}

func discordSnowflakes(values []string) ([]discorddomain.Snowflake, error) {
	if len(values) > 100 {
		return nil, errors.New("Discord snowflake list is too large")
	}
	parsed := make([]discorddomain.Snowflake, len(values))
	for index, value := range values {
		item, err := discorddomain.ParseSnowflake(value)
		if err != nil {
			return nil, err
		}
		parsed[index] = item
	}
	return parsed, nil
}

func discordConnectionLifecycle(value string) (discorddomain.ConnectionLifecycle, error) {
	switch value {
	case "enabled":
		return discorddomain.ConnectionEnabled, nil
	case "disabled":
		return discorddomain.ConnectionDisabled, nil
	default:
		return "", errors.New("Discord connection lifecycle is invalid")
	}
}

func discordTriggers(values []string) ([]discorddomain.TriggerType, error) {
	if len(values) < 1 || len(values) > 2 {
		return nil, errors.New("Discord triggers are invalid")
	}
	result := make([]discorddomain.TriggerType, len(values))
	seen := make(map[discorddomain.TriggerType]struct{}, len(values))
	for index, value := range values {
		switch value {
		case "mention":
			result[index] = discorddomain.TriggerMention
		case "slash_command":
			result[index] = discorddomain.TriggerSlashCommand
		default:
			return nil, errors.New("Discord trigger is invalid")
		}
		if _, exists := seen[result[index]]; exists {
			return nil, errors.New("Discord triggers are invalid")
		}
		seen[result[index]] = struct{}{}
	}
	return result, nil
}

func discordReplyPolicy(value string) (discorddomain.ReplyPolicy, error) {
	switch value {
	case "same_channel":
		return discorddomain.ReplySameChannel, nil
	case "thread":
		return discorddomain.ReplyThread, nil
	case "selected_channel":
		return discorddomain.ReplySelectedChannel, nil
	default:
		return "", errors.New("Discord reply policy is invalid")
	}
}

func discordDirectoryIDs(rawConnectionID, rawServerID string) (discorddomain.ConnectionID, discorddomain.Snowflake, error) {
	connectionID, err := discorddomain.ParseConnectionID(rawConnectionID)
	if err != nil {
		return discorddomain.ConnectionID{}, "", err
	}
	serverID, err := discorddomain.ParseSnowflake(rawServerID)
	return connectionID, serverID, err
}

func discordJSONNull(content []byte, fields ...string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(content, &object) != nil {
		return false
	}
	for _, field := range fields {
		if raw, exists := object[field]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true
		}
	}
	return false
}

func cloneSnowflake(value *discorddomain.Snowflake) *discorddomain.Snowflake {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func newDiscordConnectionResponse(value discorddomain.Connection) discordConnectionResponse {
	return discordConnectionResponse{
		ID: value.ID.String(), DisplayName: value.DisplayName, CredentialID: value.CredentialID.String(),
		ApplicationID: snowflakePointer(value.ApplicationID), BotUserID: snowflakePointer(value.BotUserID),
		BotUsername: value.BotUsername, AvatarHash: value.AvatarHash,
		Lifecycle: strings.ToLower(string(value.Lifecycle)), State: strings.ToLower(string(value.State)),
		GatewayLatencyMS: int32Pointer(value.GatewayLatencyMS), LastHeartbeatAt: value.LastHeartbeatAt,
		LastEventAt: value.LastEventAt, SanitizedError: value.SanitizedError, Version: int(value.Version),
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func newDiscordBindingResponse(value discorddomain.Binding) discordBindingResponse {
	return discordBindingResponse{
		ID: value.ID.String(), ConnectionID: value.ConnectionID.String(), ServerID: string(value.ServerID),
		ListenChannelID: string(value.ListenChannelID), AgentID: value.AgentID.String(),
		Triggers: triggerResponses(value.Triggers), ReplyPolicy: strings.ToLower(string(value.ReplyPolicy)),
		ReplyChannelID: snowflakePointer(value.ReplyChannelID), AllowedRoleIDs: snowflakeStrings(value.AllowedRoleIDs),
		AllowedUserIDs: snowflakeStrings(value.AllowedUserIDs), RateRequests: int(value.RatePolicy.Requests),
		RateWindowSeconds: int(value.RatePolicy.WindowSeconds), Enabled: value.Enabled,
		Health: strings.ToLower(string(value.Health)), SanitizedError: value.SanitizedError,
		Version: int(value.Version), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func triggerResponses(values []discorddomain.TriggerType) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToLower(string(value))
	}
	return result
}

func newDiscordServerResponse(value discorddomain.Server) discordServerResponse {
	return discordServerResponse{ConnectionID: value.ConnectionID.String(), ServerID: string(value.ServerID),
		Name: value.Name, IconHash: value.IconHash, Owner: value.Owner, RefreshedAt: value.RefreshedAt}
}

func newDiscordChannelResponse(value discorddomain.Channel) discordChannelResponse {
	status := "missing"
	required := discorddomain.BasePermissions
	if value.ChannelType == 11 {
		required |= discorddomain.PermissionSendMessagesInThread
	}
	if value.EffectiveBotPermissions&required == required {
		status = "ready"
	}
	return discordChannelResponse{
		ConnectionID: value.ConnectionID.String(), ServerID: string(value.ServerID), ChannelID: string(value.ChannelID),
		ParentID: snowflakePointer(value.ParentID), Name: value.Name, ChannelType: int(value.ChannelType),
		Position: int(value.Position), EffectiveBotPermissions: int(value.EffectiveBotPermissions),
		EveryoneCanView: value.EveryoneCanView, ViewerRoleIDs: snowflakeStrings(value.ViewerRoleIDs),
		ViewerUserIDs: snowflakeStrings(value.ViewerUserIDs), PermissionStatus: status, RefreshedAt: value.RefreshedAt,
	}
}

func newDiscordRoleResponse(value discorddomain.Role) discordRoleResponse {
	return discordRoleResponse{ConnectionID: value.ConnectionID.String(), ServerID: string(value.ServerID),
		RoleID: string(value.RoleID), Name: value.Name, Position: int(value.Position), RefreshedAt: value.RefreshedAt}
}

func int32Pointer(value *int32) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}

func snowflakePointer(value *discorddomain.Snowflake) *string {
	if value == nil {
		return nil
	}
	text := string(*value)
	return &text
}

func snowflakeStrings(values []discorddomain.Snowflake) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func discordProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, discorddomain.ErrNotFound):
		problem.Title, problem.Status, problem.Detail = "Not Found", http.StatusNotFound, "Discord resource not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Idempotency key conflicts with a different request."
	case errors.Is(err, discorddomain.ErrConflict), errors.Is(err, discorddomain.ErrPolicy):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Discord resource state conflicts with the request."
	default:
		problem.Title, problem.Status, problem.Detail = "Internal Server Error", http.StatusInternalServerError, "The request could not be completed."
	}
	return problem
}

func discordConnectionInstance(pattern, id string) string {
	return strings.Replace(pattern, "{connection_id}", id, 1)
}

func discordBindingInstance(pattern, id string) string {
	return strings.Replace(pattern, "{binding_id}", id, 1)
}

func discordDirectoryInstance(pattern, connectionID, serverID string) string {
	return strings.Replace(discordConnectionInstance(pattern, connectionID), "{server_id}", serverID, 1)
}

func documentDiscordRequest(api huma.API, path, method string, requestType reflect.Type, hint string) {
	item := api.OpenAPI().Paths[path]
	if item == nil {
		return
	}
	var slot **huma.Operation
	switch method {
	case http.MethodPost:
		slot = &item.Post
	case http.MethodPatch:
		slot = &item.Patch
	case http.MethodDelete:
		slot = &item.Delete
	default:
		return
	}
	if *slot == nil || (*slot).RequestBody == nil {
		return
	}
	runtimeOperation := *slot
	documentedOperation := **slot
	documentedOperation.Parameters = filterContentTypeParameter(runtimeOperation.Parameters)
	documentedBody := *runtimeOperation.RequestBody
	documentedBody.Required = true
	documentedBody.Content = map[string]*huma.MediaType{
		"application/json": {Schema: api.OpenAPI().Components.Schemas.Schema(requestType, true, hint)},
	}
	documentedOperation.RequestBody = &documentedBody
	*slot = &documentedOperation
	runtimeOperation.RequestBody.Required = false
}

func normalizeDiscordOpenAPI(api huma.API) {
	schemas := api.OpenAPI().Components.Schemas.Map()
	for _, name := range []string{
		"CreateDiscordConnectionRequest", "UpdateDiscordConnectionRequest", "ExpectedDiscordVersionRequest",
		"RotateDiscordTokenRequest", "DiscordInstallationRequest", "CreateDiscordBindingRequest",
		"UpdateDiscordBindingRequest", "DiscordConnectionResponse", "DiscordServerResponse",
		"DiscordChannelResponse", "DiscordRoleResponse", "DiscordBindingResponse", "DiscordInstallationResponse",
	} {
		stripDiscordIntegerFormats(schemas[name])
	}
}

func stripDiscordIntegerFormats(schema *huma.Schema) {
	if schema == nil {
		return
	}
	if schema.Type == "integer" {
		schema.Format = ""
	}
	for _, property := range schema.Properties {
		stripDiscordIntegerFormats(property)
	}
	stripDiscordIntegerFormats(schema.Items)
	for _, candidates := range [][]*huma.Schema{schema.OneOf, schema.AnyOf, schema.AllOf} {
		for _, candidate := range candidates {
			stripDiscordIntegerFormats(candidate)
		}
	}
}
