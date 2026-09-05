package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/idempotency"
	providerdomain "github.com/cyr1en/ref0/internal/providers"
	"github.com/danielgtaylor/huma/v2"
	"golang.org/x/text/cases"
)

const (
	providerEndpointsPath = "/api/v1/provider-endpoints"
	modelProfilesPath     = "/api/v1/model-profiles"
	providerBodyLimit     = 1 << 20
)

// ProviderService is the complete dependency needed by the 12 provider API
// operations. It intentionally contains no HTTP or worker/runtime concerns.
type ProviderService interface {
	ListEndpoints(context.Context) ([]providerdomain.Endpoint, error)
	GetEndpoint(context.Context, providerdomain.EndpointID) (providerdomain.Endpoint, error)
	CreateEndpoint(context.Context, providerdomain.CreateEndpoint, providerdomain.ActorID, string) (providerdomain.Endpoint, error)
	UpdateEndpoint(context.Context, providerdomain.UpdateEndpoint, providerdomain.ActorID, string) (providerdomain.Endpoint, error)
	ScheduleDiscovery(context.Context, providerdomain.ScheduleDiscovery, providerdomain.ActorID, string) (providerdomain.DiscoveryRun, error)
	ListProfiles(context.Context, *providerdomain.EndpointID) ([]providerdomain.Profile, error)
	GetProfile(context.Context, providerdomain.ProfileID) (providerdomain.Profile, error)
	CreateProfile(context.Context, providerdomain.CreateProfile, providerdomain.ActorID, string) (providerdomain.Profile, error)
	EditProfile(context.Context, providerdomain.EditProfile, providerdomain.ActorID, string) (providerdomain.Profile, error)
	ScheduleProbe(context.Context, providerdomain.ScheduleProbe, providerdomain.ActorID, string) (providerdomain.ProbeRun, error)
	ListAssignments(context.Context, providerdomain.KnowledgeBaseID) ([]providerdomain.Assignment, error)
	Assign(context.Context, providerdomain.AssignModel, providerdomain.ActorID, string) (providerdomain.Assignment, error)
}

type providerConfigurationRequest struct {
	DisplayName         string            `json:"display_name" minLength:"1" maxLength:"255"`
	BaseURL             string            `json:"base_url" minLength:"1" maxLength:"2048"`
	CredentialID        *string           `json:"credential_id,omitempty" format:"uuid" nullable:"true"`
	Headers             map[string]string `json:"headers,omitempty"`
	ChatCompletionsPath string            `json:"chat_completions_path,omitempty" default:"chat/completions" minLength:"1" maxLength:"255"`
	ResponsesPath       *string           `json:"responses_path,omitempty" default:"responses" minLength:"1" maxLength:"255" nullable:"true"`
	ModelsPath          string            `json:"models_path,omitempty" default:"models" minLength:"1" maxLength:"255"`
	AllowHTTP           bool              `json:"allow_http,omitempty" default:"false"`
	AllowPrivateNetwork bool              `json:"allow_private_network,omitempty" default:"false"`
}

type createProviderEndpointRequest providerConfigurationRequest

type updateProviderEndpointRequest struct {
	providerConfigurationRequest
	ExpectedVersion int32  `json:"expected_version" minimum:"1"`
	Lifecycle       string `json:"lifecycle,omitempty" default:"active" enum:"active,archived"`
}

type scheduleDiscoveryRequest struct {
	ExpectedVersion int32 `json:"expected_version" minimum:"1"`
}

type modelSettingsRequest struct {
	Transport                string         `json:"transport,omitempty" default:"chat_completions" enum:"chat_completions,responses"`
	ContextWindowTokens      *int32         `json:"context_window_tokens,omitempty" minimum:"1" nullable:"true"`
	MaxOutputTokens          *int32         `json:"max_output_tokens,omitempty" minimum:"1" nullable:"true"`
	SupportsStreaming        *bool          `json:"supports_streaming,omitempty" nullable:"true"`
	SupportsTools            *bool          `json:"supports_tools,omitempty" nullable:"true"`
	SupportsStructuredOutput *bool          `json:"supports_structured_output,omitempty" nullable:"true"`
	SupportsTemperature      *bool          `json:"supports_temperature,omitempty" nullable:"true"`
	ReasoningTransport       string         `json:"reasoning_transport,omitempty" default:"none" enum:"none,reasoning_effort,custom"`
	ReasoningMapping         map[string]any `json:"reasoning_mapping,omitempty" nullable:"true"`
	TimeoutSeconds           int32          `json:"timeout_seconds,omitempty" default:"60" minimum:"1" maximum:"60"`
	MaxRetries               int32          `json:"max_retries,omitempty" default:"2" minimum:"0" maximum:"10"`
	MaxConcurrentTasks       int32          `json:"max_concurrent_tasks,omitempty" default:"1" minimum:"1" maximum:"32"`
	ExtraBody                map[string]any `json:"extra_body,omitempty"`
}

type createModelProfileRequest struct {
	EndpointID string               `json:"endpoint_id" format:"uuid"`
	ModelID    string               `json:"model_id" minLength:"1" maxLength:"512"`
	Settings   modelSettingsRequest `json:"settings"`
}

type editModelProfileRequest struct {
	ExpectedVersion int32                `json:"expected_version" minimum:"1"`
	Settings        modelSettingsRequest `json:"settings"`
}

type scheduleProbeRequest struct {
	ProfileID       string   `json:"profile_id" format:"uuid"`
	ExpectedVersion int32    `json:"expected_version" minimum:"1"`
	SelectedChecks  []string `json:"selected_checks" minItems:"1" maxItems:"4" uniqueItems:"true" enum:"chat,streaming,tools,structured_output" nullable:"false"`
	AcknowledgeCost bool     `json:"acknowledge_cost" const:"true"`
}

type putModelAssignmentRequest struct {
	ProfileID       string `json:"profile_id" format:"uuid"`
	ReasoningEffort string `json:"reasoning_effort,omitempty" default:"none" enum:"none,minimal,low,medium,high,max"`
	AnswerMode      string `json:"answer_mode,omitempty" default:"tool_calling" enum:"tool_calling,single_pass"`
	ExpectedVersion *int32 `json:"expected_version,omitempty" minimum:"1" nullable:"true"`
}

type providerReadInput struct {
	SessionCookie string `cookie:"ref0_session"`
}

type providerWriteInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type endpointReadInput struct {
	SessionCookie string `cookie:"ref0_session"`
	EndpointID    string `path:"endpoint_id" format:"uuid"`
}

type endpointWriteInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	EndpointID     string `path:"endpoint_id" format:"uuid"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type listProfilesInput struct {
	SessionCookie string              `cookie:"ref0_session"`
	EndpointID    optionalStringParam `query:"endpoint_id"`
}

type profileReadInput struct {
	SessionCookie string `cookie:"ref0_session"`
	ProfileID     string `path:"profile_id" format:"uuid"`
}

type profileWriteInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	ProfileID      string `path:"profile_id" format:"uuid"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type assignmentReadInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	KnowledgeBaseID string `path:"knowledge_base_id" format:"uuid"`
}

type assignmentWriteInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	CSRFToken       string `header:"X-CSRF-Token"`
	IdempotencyKey  string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	KnowledgeBaseID string `path:"knowledge_base_id" format:"uuid"`
	Role            string `path:"role" enum:"documentation_planner,documentation_writer"`
	RawBody         []byte `contentType:"application/json"`
	ContentType     string `header:"Content-Type"`
}

type providerEndpointResponse struct {
	ID                   string            `json:"id" format:"uuid"`
	DisplayName          string            `json:"display_name"`
	BaseURL              string            `json:"base_url"`
	CredentialID         *string           `json:"credential_id" format:"uuid"`
	Headers              map[string]string `json:"headers"`
	ChatCompletionsPath  string            `json:"chat_completions_path"`
	ResponsesPath        *string           `json:"responses_path"`
	ModelsPath           string            `json:"models_path"`
	AllowHTTP            bool              `json:"allow_http"`
	AllowPrivateNetwork  bool              `json:"allow_private_network"`
	Lifecycle            string            `json:"lifecycle" enum:"active,archived"`
	Health               string            `json:"health" enum:"unknown,healthy,unhealthy"`
	HealthCheckedAt      *time.Time        `json:"health_checked_at"`
	Version              int32             `json:"version"`
	ConfigurationVersion int32             `json:"configuration_version"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	ArchivedAt           *time.Time        `json:"archived_at"`
}

type modelSettingsResponse struct {
	Transport                string            `json:"transport" enum:"chat_completions,responses"`
	ContextWindowTokens      *int32            `json:"context_window_tokens"`
	MaxOutputTokens          *int32            `json:"max_output_tokens"`
	SupportsStreaming        *bool             `json:"supports_streaming"`
	SupportsTools            *bool             `json:"supports_tools"`
	SupportsStructuredOutput *bool             `json:"supports_structured_output"`
	SupportsTemperature      *bool             `json:"supports_temperature"`
	ReasoningTransport       string            `json:"reasoning_transport" enum:"none,reasoning_effort,custom"`
	ReasoningMapping         map[string]any    `json:"reasoning_mapping" nullable:"true"`
	TimeoutSeconds           int32             `json:"timeout_seconds"`
	MaxRetries               int32             `json:"max_retries"`
	MaxConcurrentTasks       int32             `json:"max_concurrent_tasks"`
	ExtraBody                map[string]any    `json:"extra_body"`
	MetadataOrigin           map[string]string `json:"metadata_origin"`
}

type modelVersionResponse struct {
	ID                   string                `json:"id" format:"uuid"`
	VersionNumber        int32                 `json:"version_number"`
	ConfigurationVersion int32                 `json:"configuration_version"`
	Source               string                `json:"source" enum:"discovery,operator,probe"`
	CreatedByOperatorID  *string               `json:"created_by_operator_id" format:"uuid"`
	CreatedAt            time.Time             `json:"created_at"`
	Settings             modelSettingsResponse `json:"settings"`
}

type modelProfileResponse struct {
	ID             string               `json:"id" format:"uuid"`
	EndpointID     string               `json:"endpoint_id" format:"uuid"`
	ModelID        string               `json:"model_id"`
	Availability   string               `json:"availability" enum:"available,unavailable,manual"`
	CurrentVersion modelVersionResponse `json:"current_version"`
	Version        int32                `json:"version"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

type discoveryRunResponse struct {
	ID                           string         `json:"id" format:"uuid"`
	EndpointID                   string         `json:"endpoint_id" format:"uuid"`
	JobID                        string         `json:"job_id" format:"uuid"`
	CapturedConfigurationVersion int32          `json:"captured_configuration_version"`
	CapturedCredentialVersion    *int32         `json:"captured_credential_version"`
	TLSRequired                  bool           `json:"tls_required"`
	RequestedByOperatorID        string         `json:"requested_by_operator_id" format:"uuid"`
	Status                       string         `json:"status" enum:"pending,running,succeeded,failed,superseded"`
	ModelIDs                     []string       `json:"model_ids" nullable:"false"`
	RawResponse                  map[string]any `json:"raw_response" nullable:"true"`
	TLSVerified                  *bool          `json:"tls_verified"`
	AuthenticationSucceeded      *bool          `json:"authentication_succeeded"`
	HTTPStatus                   *int32         `json:"http_status"`
	ResponseSHA256               *string        `json:"response_sha256"`
	ModelCount                   *int32         `json:"model_count"`
	SanitizedError               *string        `json:"sanitized_error"`
	CreatedAt                    time.Time      `json:"created_at"`
	StartedAt                    *time.Time     `json:"started_at"`
	CompletedAt                  *time.Time     `json:"completed_at"`
}

type probeRunResponse struct {
	ID                           string                        `json:"id" format:"uuid"`
	ModelProfileID               string                        `json:"model_profile_id" format:"uuid"`
	JobID                        string                        `json:"job_id" format:"uuid"`
	CapturedConfigurationVersion int32                         `json:"captured_configuration_version"`
	CapturedCredentialVersion    *int32                        `json:"captured_credential_version"`
	CapturedProfileVersionID     string                        `json:"captured_profile_version_id" format:"uuid"`
	RequestedByOperatorID        string                        `json:"requested_by_operator_id" format:"uuid"`
	SelectedChecks               []string                      `json:"selected_checks" enum:"chat,streaming,tools,structured_output" nullable:"false"`
	AcknowledgeCost              bool                          `json:"acknowledge_cost"`
	Status                       string                        `json:"status" enum:"pending,running,succeeded,failed,superseded"`
	Findings                     *providerdomain.ProbeFindings `json:"findings"`
	RawResponse                  map[string]any                `json:"raw_response" nullable:"true"`
	SanitizedError               *string                       `json:"sanitized_error"`
	ResultingVersionID           *string                       `json:"resulting_version_id" format:"uuid"`
	CreatedAt                    time.Time                     `json:"created_at"`
	StartedAt                    *time.Time                    `json:"started_at"`
	CompletedAt                  *time.Time                    `json:"completed_at"`
}

type modelAssignmentResponse struct {
	ID              string    `json:"id" format:"uuid"`
	KnowledgeBaseID string    `json:"knowledge_base_id" format:"uuid"`
	Role            string    `json:"role" enum:"documentation_planner,documentation_writer"`
	ModelProfileID  string    `json:"model_profile_id" format:"uuid"`
	ReasoningEffort string    `json:"reasoning_effort" enum:"none,minimal,low,medium,high,max"`
	AnswerMode      string    `json:"answer_mode" enum:"tool_calling,single_pass"`
	Version         int32     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type providerEndpointsOutput struct {
	Body []providerEndpointResponse `nullable:"false"`
}
type providerEndpointOutput struct {
	Body providerEndpointResponse
}
type modelProfilesOutput struct {
	Body []modelProfileResponse `nullable:"false"`
}
type modelProfileOutput struct {
	Body modelProfileResponse
}
type discoveryRunOutput struct {
	Body discoveryRunResponse
}
type probeRunOutput struct {
	Body probeRunResponse
}
type assignmentsOutput struct {
	Body []modelAssignmentResponse `nullable:"false"`
}
type assignmentOutput struct{ Body modelAssignmentResponse }

// RegisterProviderRoutes exposes the provider adapters without coupling the
// central handler constructor to this slice. The central composition root calls
// it once a ProviderService is available.
func RegisterProviderRoutes(api huma.API, sessions auth.SessionService, service ProviderService) {
	registerProviderEndpointRoutes(api, sessions, service)
	registerModelProfileRoutes(api, sessions, service)
	registerModelAssignmentRoutes(api, sessions, service)
	normalizeProviderOpenAPISchemas(api)
}

func registerProviderEndpointRoutes(api huma.API, sessions auth.SessionService, service ProviderService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_provider_endpoints_api_v1_provider_endpoints_get", Method: http.MethodGet,
		Path: providerEndpointsPath, Summary: "List Provider Endpoints", Tags: []string{"providers"},
		Errors: []int{http.StatusUnauthorized},
	}, func(ctx context.Context, input *providerReadInput) (*providerEndpointsOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, providerEndpointsPath); err != nil {
			return nil, err
		}
		values, err := service.ListEndpoints(ctx)
		if err != nil {
			return nil, providerProblem(providerEndpointsPath, err)
		}
		output := &providerEndpointsOutput{Body: make([]providerEndpointResponse, len(values))}
		for index, value := range values {
			output.Body[index] = endpointResponse(value)
		}
		return output, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "create_provider_endpoint_api_v1_provider_endpoints_post", Method: http.MethodPost,
		Path: providerEndpointsPath, Summary: "Create Provider Endpoint", Tags: []string{"providers"},
		DefaultStatus:    http.StatusCreated,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     providerBodyLimit,
	}, func(ctx context.Context, input *providerWriteInput) (*providerEndpointOutput, error) {
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, providerEndpointsPath)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, providerEndpointsPath)
		if err != nil {
			return nil, err
		}
		body := createProviderEndpointRequest(defaultProviderConfigurationRequest())
		if !decodeProviderRequest(input.RawBody, input.ContentType, &body) {
			return nil, validationProblem(providerEndpointsPath)
		}
		configuration, ok := providerConfiguration(providerConfigurationRequest(body))
		if !ok {
			return nil, validationProblem(providerEndpointsPath)
		}
		value, err := service.CreateEndpoint(ctx, providerdomain.CreateEndpoint{Configuration: configuration},
			providerdomain.ActorID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, providerProblem(providerEndpointsPath, err)
		}
		return &providerEndpointOutput{Body: endpointResponse(value)}, nil
	})
	documentProviderJSONRequest(api, providerEndpointsPath, http.MethodPost, reflect.TypeFor[createProviderEndpointRequest](), "CreateProviderEndpointRequest")

	const endpointPath = providerEndpointsPath + "/{endpoint_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_provider_endpoint_api_v1_provider_endpoints__endpoint_id__get", Method: http.MethodGet,
		Path: endpointPath, Summary: "Get Provider Endpoint", Tags: []string{"providers"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *endpointReadInput) (*providerEndpointOutput, error) {
		instance := strings.Replace(endpointPath, "{endpoint_id}", input.EndpointID, 1)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, ok := endpointID(input.EndpointID)
		if !ok {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.GetEndpoint(ctx, id)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		return &providerEndpointOutput{Body: endpointResponse(value)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update_provider_endpoint_api_v1_provider_endpoints__endpoint_id__patch", Method: http.MethodPatch,
		Path: endpointPath, Summary: "Update Provider Endpoint", Tags: []string{"providers"},
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     providerBodyLimit,
	}, func(ctx context.Context, input *endpointWriteInput) (*providerEndpointOutput, error) {
		instance := strings.Replace(endpointPath, "{endpoint_id}", input.EndpointID, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, ok := endpointID(input.EndpointID)
		if !ok {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		body := updateProviderEndpointRequest{
			providerConfigurationRequest: defaultProviderConfigurationRequest(),
			Lifecycle:                    "active",
		}
		if !decodeProviderRequest(input.RawBody, input.ContentType, &body) {
			return nil, validationProblem(instance)
		}
		configuration, ok := providerConfiguration(body.providerConfigurationRequest)
		lifecycle, validLifecycle := providerLifecycle(body.Lifecycle)
		if !ok || !validLifecycle || body.ExpectedVersion <= 0 {
			return nil, validationProblem(instance)
		}
		value, err := service.UpdateEndpoint(ctx, providerdomain.UpdateEndpoint{
			EndpointID: id, ExpectedVersion: body.ExpectedVersion,
			Configuration: configuration, Lifecycle: lifecycle,
		}, providerdomain.ActorID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		return &providerEndpointOutput{Body: endpointResponse(value)}, nil
	})
	documentProviderJSONRequest(api, endpointPath, http.MethodPatch, updateProviderEndpointDocumentType(), "UpdateProviderEndpointRequest")

	const discoveryPath = providerEndpointsPath + "/{endpoint_id}/discover"
	huma.Register(api, huma.Operation{
		OperationID: "schedule_provider_discovery_api_v1_provider_endpoints__endpoint_id__discover_post", Method: http.MethodPost,
		Path: discoveryPath, Summary: "Schedule Provider Discovery", Tags: []string{"providers"},
		DefaultStatus:    http.StatusAccepted,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     providerBodyLimit,
	}, func(ctx context.Context, input *endpointWriteInput) (*discoveryRunOutput, error) {
		instance := strings.Replace(discoveryPath, "{endpoint_id}", input.EndpointID, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, ok := endpointID(input.EndpointID)
		if !ok {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		var body scheduleDiscoveryRequest
		if !decodeProviderRequest(input.RawBody, input.ContentType, &body) || body.ExpectedVersion <= 0 {
			return nil, validationProblem(instance)
		}
		value, err := service.ScheduleDiscovery(ctx, providerdomain.ScheduleDiscovery{
			EndpointID: id, ExpectedVersion: body.ExpectedVersion,
		}, providerdomain.ActorID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		return &discoveryRunOutput{Body: discoveryResponse(value)}, nil
	})
	documentProviderJSONRequest(api, discoveryPath, http.MethodPost, reflect.TypeFor[scheduleDiscoveryRequest](), "ScheduleDiscoveryRequest")

	const probePath = providerEndpointsPath + "/{endpoint_id}/probe"
	huma.Register(api, huma.Operation{
		OperationID: "schedule_provider_probe_api_v1_provider_endpoints__endpoint_id__probe_post", Method: http.MethodPost,
		Path: probePath, Summary: "Schedule Provider Probe", Tags: []string{"providers"},
		DefaultStatus:    http.StatusAccepted,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     providerBodyLimit,
	}, func(ctx context.Context, input *endpointWriteInput) (*probeRunOutput, error) {
		instance := strings.Replace(probePath, "{endpoint_id}", input.EndpointID, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		selectedEndpoint, ok := endpointID(input.EndpointID)
		if !ok {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		var body scheduleProbeRequest
		if !decodeProviderRequest(input.RawBody, input.ContentType, &body) {
			return nil, validationProblem(instance)
		}
		profileID, profileOK := profileID(body.ProfileID)
		checks, checksOK := probeChecks(body.SelectedChecks)
		if !profileOK || !checksOK || body.ExpectedVersion <= 0 || !body.AcknowledgeCost {
			return nil, validationProblem(instance)
		}
		profile, err := service.GetProfile(ctx, profileID)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		if profile.EndpointID != selectedEndpoint {
			return nil, unprocessableProviderProblem(instance, "Model profile does not belong to this endpoint.")
		}
		value, err := service.ScheduleProbe(ctx, providerdomain.ScheduleProbe{
			ProfileID: profileID, ExpectedVersion: body.ExpectedVersion,
			SelectedChecks: checks, AcknowledgeCost: true,
		}, providerdomain.ActorID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		return &probeRunOutput{Body: probeResponse(value)}, nil
	})
	documentProviderJSONRequest(api, probePath, http.MethodPost, reflect.TypeFor[scheduleProbeRequest](), "ScheduleProbeRequest")
}

func registerModelProfileRoutes(api huma.API, sessions auth.SessionService, service ProviderService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_model_profiles_api_v1_model_profiles_get", Method: http.MethodGet,
		Path: modelProfilesPath, Summary: "List Model Profiles", Tags: []string{"providers"},
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *listProfilesInput) (*modelProfilesOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, modelProfilesPath); err != nil {
			return nil, err
		}
		var endpoint *providerdomain.EndpointID
		if input.EndpointID.IsSet {
			id, ok := endpointID(input.EndpointID.Value)
			if !ok {
				return nil, parameterValidationProblem(modelProfilesPath, "query")
			}
			endpoint = &id
		}
		values, err := service.ListProfiles(ctx, endpoint)
		if err != nil {
			return nil, providerProblem(modelProfilesPath, err)
		}
		output := &modelProfilesOutput{Body: make([]modelProfileResponse, len(values))}
		for index, value := range values {
			output.Body[index] = profileResponse(value)
		}
		return output, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "create_model_profile_api_v1_model_profiles_post", Method: http.MethodPost,
		Path: modelProfilesPath, Summary: "Create Model Profile", Tags: []string{"providers"},
		DefaultStatus:    http.StatusCreated,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     providerBodyLimit,
	}, func(ctx context.Context, input *providerWriteInput) (*modelProfileOutput, error) {
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, modelProfilesPath)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, modelProfilesPath)
		if err != nil {
			return nil, err
		}
		body := createModelProfileRequest{Settings: defaultModelSettingsRequest()}
		if !decodeProviderRequest(input.RawBody, input.ContentType, &body) || !requiredProviderObject(input.RawBody, "settings") {
			return nil, validationProblem(modelProfilesPath)
		}
		endpoint, endpointOK := endpointID(body.EndpointID)
		settings, settingsOK := providerSettings(body.Settings)
		modelID := strings.TrimFunc(body.ModelID, apiPythonWhitespace)
		if !endpointOK || !settingsOK || modelID == "" {
			return nil, validationProblem(modelProfilesPath)
		}
		value, err := service.CreateProfile(ctx, providerdomain.CreateProfile{
			EndpointID: endpoint, ModelID: modelID, Settings: settings,
		}, providerdomain.ActorID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, providerProblem(modelProfilesPath, err)
		}
		return &modelProfileOutput{Body: profileResponse(value)}, nil
	})
	documentProviderJSONRequest(api, modelProfilesPath, http.MethodPost, reflect.TypeFor[createModelProfileRequest](), "CreateModelProfileRequest")

	const profilePath = modelProfilesPath + "/{profile_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_model_profile_api_v1_model_profiles__profile_id__get", Method: http.MethodGet,
		Path: profilePath, Summary: "Get Model Profile", Tags: []string{"providers"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *profileReadInput) (*modelProfileOutput, error) {
		instance := strings.Replace(profilePath, "{profile_id}", input.ProfileID, 1)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, ok := profileID(input.ProfileID)
		if !ok {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.GetProfile(ctx, id)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		return &modelProfileOutput{Body: profileResponse(value)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "edit_model_profile_api_v1_model_profiles__profile_id__patch", Method: http.MethodPatch,
		Path: profilePath, Summary: "Edit Model Profile", Tags: []string{"providers"},
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     providerBodyLimit,
	}, func(ctx context.Context, input *profileWriteInput) (*modelProfileOutput, error) {
		instance := strings.Replace(profilePath, "{profile_id}", input.ProfileID, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, ok := profileID(input.ProfileID)
		if !ok {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		body := editModelProfileRequest{Settings: defaultModelSettingsRequest()}
		if !decodeProviderRequest(input.RawBody, input.ContentType, &body) || !requiredProviderObject(input.RawBody, "settings") {
			return nil, validationProblem(instance)
		}
		settings, settingsOK := providerSettings(body.Settings)
		if !settingsOK || body.ExpectedVersion <= 0 {
			return nil, validationProblem(instance)
		}
		value, err := service.EditProfile(ctx, providerdomain.EditProfile{
			ProfileID: id, ExpectedVersion: body.ExpectedVersion, Settings: settings,
		}, providerdomain.ActorID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		return &modelProfileOutput{Body: profileResponse(value)}, nil
	})
	documentProviderJSONRequest(api, profilePath, http.MethodPatch, reflect.TypeFor[editModelProfileRequest](), "EditModelProfileRequest")
}

func registerModelAssignmentRoutes(api huma.API, sessions auth.SessionService, service ProviderService) {
	const assignmentsPath = "/api/v1/knowledge-bases/{knowledge_base_id}/model-assignments"
	huma.Register(api, huma.Operation{
		OperationID: "list_model_assignments_api_v1_knowledge_bases__knowledge_base_id__model_assignments_get", Method: http.MethodGet,
		Path: assignmentsPath, Summary: "List Model Assignments", Tags: []string{"providers"},
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *assignmentReadInput) (*assignmentsOutput, error) {
		instance := strings.Replace(assignmentsPath, "{knowledge_base_id}", input.KnowledgeBaseID, 1)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, ok := knowledgeBaseID(input.KnowledgeBaseID)
		if !ok {
			return nil, parameterValidationProblem(instance, "path")
		}
		values, err := service.ListAssignments(ctx, id)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		output := &assignmentsOutput{Body: make([]modelAssignmentResponse, len(values))}
		for index, value := range values {
			output.Body[index] = assignmentResponse(value)
		}
		return output, nil
	})

	const assignmentPath = assignmentsPath + "/{role}"
	huma.Register(api, huma.Operation{
		OperationID: "put_model_assignment_api_v1_knowledge_bases__knowledge_base_id__model_assignments__role__put", Method: http.MethodPut,
		Path: assignmentPath, Summary: "Put Model Assignment", Tags: []string{"providers"},
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     providerBodyLimit,
	}, func(ctx context.Context, input *assignmentWriteInput) (*assignmentOutput, error) {
		instance := strings.Replace(assignmentPath, "{knowledge_base_id}", input.KnowledgeBaseID, 1)
		instance = strings.Replace(instance, "{role}", input.Role, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		knowledgeBase, kbOK := knowledgeBaseID(input.KnowledgeBaseID)
		role, roleOK := modelRole(input.Role)
		if !kbOK || !roleOK {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		body := putModelAssignmentRequest{ReasoningEffort: "none", AnswerMode: "tool_calling"}
		if !decodeProviderRequest(input.RawBody, input.ContentType, &body) {
			return nil, validationProblem(instance)
		}
		profile, profileOK := profileID(body.ProfileID)
		effort, effortOK := reasoningEffort(body.ReasoningEffort)
		mode, modeOK := answerMode(body.AnswerMode)
		if !profileOK || !effortOK || !modeOK || body.ExpectedVersion != nil && *body.ExpectedVersion <= 0 {
			return nil, validationProblem(instance)
		}
		value, err := service.Assign(ctx, providerdomain.AssignModel{
			KnowledgeBaseID: knowledgeBase, Role: role, ProfileID: profile,
			ReasoningEffort: effort, AnswerMode: mode, ExpectedVersion: body.ExpectedVersion,
		}, providerdomain.ActorID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, providerProblem(instance, err)
		}
		return &assignmentOutput{Body: assignmentResponse(value)}, nil
	})
	documentProviderJSONRequest(api, assignmentPath, http.MethodPut, reflect.TypeFor[putModelAssignmentRequest](), "PutModelAssignmentRequest")
}

func defaultProviderConfigurationRequest() providerConfigurationRequest {
	responsesPath := "responses"
	return providerConfigurationRequest{
		Headers:             map[string]string{},
		ChatCompletionsPath: "chat/completions",
		ResponsesPath:       &responsesPath,
		ModelsPath:          "models",
	}
}

func defaultModelSettingsRequest() modelSettingsRequest {
	return modelSettingsRequest{
		Transport:          "chat_completions",
		ReasoningTransport: "none",
		TimeoutSeconds:     60,
		MaxRetries:         2,
		MaxConcurrentTasks: 1,
		ExtraBody:          map[string]any{},
	}
}

func decodeProviderRequest(content []byte, contentType string, destination any) bool {
	return isJSONContentType(contentType) && decodeSecretRequest(content, destination)
}

func requiredProviderObject(content []byte, name string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil {
		return false
	}
	value, exists := object[name]
	value = bytes.TrimSpace(value)
	return exists && len(value) > 0 && value[0] == '{'
}

func documentProviderJSONRequest(api huma.API, path, method string, requestType reflect.Type, hint string) {
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
	case http.MethodPut:
		slot = &item.Put
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
		"application/json": {
			Schema: api.OpenAPI().Components.Schemas.Schema(requestType, true, hint),
		},
	}
	documentedOperation.RequestBody = &documentedBody
	*slot = &documentedOperation
	// Runtime decoding stays raw so framework validation never embeds provider
	// header or request-body values in an error response.
	runtimeOperation.RequestBody.Required = false
}

// updateProviderEndpointDocumentType mirrors the flattened JSON
// representation of updateProviderEndpointRequest. encoding/json flattens the
// embedded configuration, while reflection-based schema generators do not.
func updateProviderEndpointDocumentType() reflect.Type {
	return reflect.TypeOf(struct {
		DisplayName         string            `json:"display_name" minLength:"1" maxLength:"255"`
		BaseURL             string            `json:"base_url" minLength:"1" maxLength:"2048"`
		CredentialID        *string           `json:"credential_id,omitempty" format:"uuid" nullable:"true"`
		Headers             map[string]string `json:"headers,omitempty"`
		ChatCompletionsPath string            `json:"chat_completions_path,omitempty" default:"chat/completions" minLength:"1" maxLength:"255"`
		ResponsesPath       *string           `json:"responses_path,omitempty" default:"responses" minLength:"1" maxLength:"255" nullable:"true"`
		ModelsPath          string            `json:"models_path,omitempty" default:"models" minLength:"1" maxLength:"255"`
		AllowHTTP           bool              `json:"allow_http,omitempty" default:"false"`
		AllowPrivateNetwork bool              `json:"allow_private_network,omitempty" default:"false"`
		ExpectedVersion     int32             `json:"expected_version" minimum:"1"`
		Lifecycle           string            `json:"lifecycle,omitempty" default:"active" enum:"active,archived"`
	}{})
}

func normalizeProviderOpenAPISchemas(api huma.API) {
	schemas := api.OpenAPI().Components.Schemas.Map()
	probe := schemas["ProbeRunResponse"]
	if probe != nil {
		if findings := probe.Properties["findings"]; findings != nil && findings.Ref != "" {
			findings.AnyOf = []*huma.Schema{{Ref: findings.Ref}, {Type: "null"}}
			findings.Ref = ""
		}
	}
	settings := schemas["ModelSettingsResponse"]
	if settings != nil {
		if origins := settings.Properties["metadata_origin"]; origins != nil {
			if values, ok := origins.AdditionalProperties.(*huma.Schema); ok {
				values.Enum = []any{"unknown", "discovered", "probed", "operator"}
			}
		}
	}
}

func providerConfiguration(body providerConfigurationRequest) (providerdomain.Configuration, bool) {
	displayName := strings.TrimFunc(body.DisplayName, apiPythonWhitespace)
	baseURL := strings.TrimFunc(body.BaseURL, apiPythonWhitespace)
	if displayName == "" || baseURL == "" {
		return providerdomain.Configuration{}, false
	}
	var credentialID *credentials.ID
	if body.CredentialID != nil {
		value, err := credentials.ParseID(*body.CredentialID)
		if err != nil {
			return providerdomain.Configuration{}, false
		}
		credentialID = &value
	}
	configuration, err := (providerdomain.Configuration{
		DisplayName: displayName, DisplayKey: cases.Fold().String(displayName), BaseURL: baseURL,
		CredentialID: credentialID, Headers: body.Headers,
		ChatCompletionsPath: body.ChatCompletionsPath, ResponsesPath: body.ResponsesPath,
		ModelsPath: body.ModelsPath, AllowHTTP: body.AllowHTTP,
		AllowPrivateNetwork: body.AllowPrivateNetwork,
	}).Normalize()
	return configuration, err == nil
}

func providerSettings(body modelSettingsRequest) (providerdomain.Settings, bool) {
	transport, ok := modelTransport(body.Transport)
	if !ok {
		return providerdomain.Settings{}, false
	}
	reasoningTransport, ok := reasoningTransport(body.ReasoningTransport)
	if !ok {
		return providerdomain.Settings{}, false
	}
	mapping, ok := reasoningMapping(body.ReasoningMapping)
	if !ok {
		return providerdomain.Settings{}, false
	}
	origins := map[string]providerdomain.MetadataOrigin{
		"model_id": providerdomain.OriginOperator, "transport": providerdomain.OriginOperator,
		"context_window_tokens": providerdomain.OriginOperator, "max_output_tokens": providerdomain.OriginOperator,
		"supports_streaming": providerdomain.OriginOperator, "supports_tools": providerdomain.OriginOperator,
		"supports_structured_output": providerdomain.OriginOperator,
		"supports_temperature":       providerdomain.OriginOperator,
		"reasoning_transport":        providerdomain.OriginOperator, "reasoning_mapping": providerdomain.OriginOperator,
		"timeout_seconds": providerdomain.OriginOperator, "max_retries": providerdomain.OriginOperator,
		"max_concurrent_tasks": providerdomain.OriginOperator,
		"extra_body":           providerdomain.OriginOperator,
	}
	if body.ContextWindowTokens == nil {
		origins["context_window_tokens"] = providerdomain.OriginUnknown
	}
	if body.MaxOutputTokens == nil {
		origins["max_output_tokens"] = providerdomain.OriginUnknown
	}
	if body.SupportsStreaming == nil {
		origins["supports_streaming"] = providerdomain.OriginUnknown
	}
	if body.SupportsTools == nil {
		origins["supports_tools"] = providerdomain.OriginUnknown
	}
	if body.SupportsStructuredOutput == nil {
		origins["supports_structured_output"] = providerdomain.OriginUnknown
	}
	if body.SupportsTemperature == nil {
		origins["supports_temperature"] = providerdomain.OriginUnknown
	}
	if mapping == nil {
		origins["reasoning_mapping"] = providerdomain.OriginUnknown
	}
	settings, err := (providerdomain.Settings{
		Transport: transport, ContextWindowTokens: body.ContextWindowTokens,
		MaxOutputTokens: body.MaxOutputTokens, SupportsStreaming: body.SupportsStreaming,
		SupportsTools: body.SupportsTools, SupportsStructuredOutput: body.SupportsStructuredOutput,
		SupportsTemperature: body.SupportsTemperature, ReasoningTransport: reasoningTransport,
		ReasoningMapping: mapping, TimeoutSeconds: body.TimeoutSeconds, MaxRetries: body.MaxRetries,
		MaxConcurrentTasks: body.MaxConcurrentTasks,
		ExtraBody:          body.ExtraBody, MetadataOrigin: origins,
	}).Normalize()
	return settings, err == nil
}

func reasoningMapping(value map[string]any) (*providerdomain.CustomReasoningMapping, bool) {
	if value == nil {
		return nil, true
	}
	if len(value) != 2 {
		return nil, false
	}
	field, fieldOK := value["field"].(string)
	values, valuesOK := value["values"].(map[string]any)
	if !fieldOK || !valuesOK {
		return nil, false
	}
	return &providerdomain.CustomReasoningMapping{Field: field, Values: values}, true
}

func endpointID(raw string) (providerdomain.EndpointID, bool) {
	id, err := providerdomain.ParseID(raw)
	return providerdomain.EndpointID(id), err == nil
}

func profileID(raw string) (providerdomain.ProfileID, bool) {
	id, err := providerdomain.ParseID(raw)
	return providerdomain.ProfileID(id), err == nil
}

func knowledgeBaseID(raw string) (providerdomain.KnowledgeBaseID, bool) {
	id, err := providerdomain.ParseID(raw)
	return providerdomain.KnowledgeBaseID(id), err == nil
}

func providerLifecycle(value string) (providerdomain.Lifecycle, bool) {
	switch value {
	case "active":
		return providerdomain.Active, true
	case "archived":
		return providerdomain.Archived, true
	default:
		return "", false
	}
}

func modelTransport(value string) (providerdomain.Transport, bool) {
	switch value {
	case "chat_completions":
		return providerdomain.ChatCompletions, true
	case "responses":
		return providerdomain.Responses, true
	default:
		return "", false
	}
}

func reasoningTransport(value string) (providerdomain.ReasoningTransport, bool) {
	switch value {
	case "none":
		return providerdomain.NoReasoning, true
	case "reasoning_effort":
		return providerdomain.ReasoningEffort, true
	case "custom":
		return providerdomain.CustomReasoning, true
	default:
		return "", false
	}
}

func probeChecks(values []string) ([]providerdomain.ProbeCheck, bool) {
	if len(values) < 1 || len(values) > 4 {
		return nil, false
	}
	result := make([]providerdomain.ProbeCheck, len(values))
	seen := map[providerdomain.ProbeCheck]struct{}{}
	for index, value := range values {
		switch value {
		case "chat":
			result[index] = providerdomain.ProbeChat
		case "streaming":
			result[index] = providerdomain.ProbeStreaming
		case "tools":
			result[index] = providerdomain.ProbeTools
		case "structured_output":
			result[index] = providerdomain.ProbeStructuredOutput
		default:
			return nil, false
		}
		if _, exists := seen[result[index]]; exists {
			return nil, false
		}
		seen[result[index]] = struct{}{}
	}
	return result, true
}

func modelRole(value string) (providerdomain.ModelRole, bool) {
	switch value {
	case "documentation_planner":
		return providerdomain.DocumentationPlanner, true
	case "documentation_writer":
		return providerdomain.DocumentationWriter, true
	default:
		return "", false
	}
}

func reasoningEffort(value string) (providerdomain.Effort, bool) {
	switch value {
	case "none":
		return providerdomain.EffortNone, true
	case "minimal":
		return providerdomain.EffortMinimal, true
	case "low":
		return providerdomain.EffortLow, true
	case "medium":
		return providerdomain.EffortMedium, true
	case "high":
		return providerdomain.EffortHigh, true
	case "max":
		return providerdomain.EffortMax, true
	default:
		return "", false
	}
}

func answerMode(value string) (providerdomain.AnswerMode, bool) {
	switch value {
	case "tool_calling":
		return providerdomain.ToolCalling, true
	case "single_pass":
		return providerdomain.SinglePass, true
	default:
		return "", false
	}
}

func endpointResponse(value providerdomain.Endpoint) providerEndpointResponse {
	var credentialID *string
	if value.Configuration.CredentialID != nil {
		text := value.Configuration.CredentialID.String()
		credentialID = &text
	}
	return providerEndpointResponse{
		ID: value.ID.String(), DisplayName: value.Configuration.DisplayName,
		BaseURL: value.Configuration.BaseURL, CredentialID: credentialID,
		Headers: value.Configuration.Headers, ChatCompletionsPath: value.Configuration.ChatCompletionsPath,
		ResponsesPath: value.Configuration.ResponsesPath, ModelsPath: value.Configuration.ModelsPath,
		AllowHTTP: value.Configuration.AllowHTTP, AllowPrivateNetwork: value.Configuration.AllowPrivateNetwork,
		Lifecycle: strings.ToLower(string(value.Lifecycle)), Health: strings.ToLower(string(value.Health)),
		HealthCheckedAt: value.HealthCheckedAt, Version: value.Version,
		ConfigurationVersion: value.ConfigurationVersion, CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt, ArchivedAt: value.ArchivedAt,
	}
}

func profileResponse(value providerdomain.Profile) modelProfileResponse {
	version := value.CurrentVersion
	var actorID *string
	if version.CreatedByActorID != nil {
		text := version.CreatedByActorID.String()
		actorID = &text
	}
	return modelProfileResponse{
		ID: value.ID.String(), EndpointID: value.EndpointID.String(), ModelID: value.ModelID,
		Availability: strings.ToLower(string(value.Availability)), Version: value.Version,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		CurrentVersion: modelVersionResponse{
			ID: version.ID.String(), VersionNumber: version.VersionNumber,
			ConfigurationVersion: version.ConfigurationVersion, Source: strings.ToLower(string(version.Source)),
			CreatedByOperatorID: actorID, CreatedAt: version.CreatedAt, Settings: settingsResponse(version.Settings),
		},
	}
}

func settingsResponse(value providerdomain.Settings) modelSettingsResponse {
	origins := make(map[string]string, len(value.MetadataOrigin))
	for name, origin := range value.MetadataOrigin {
		origins[name] = strings.ToLower(string(origin))
	}
	var mapping map[string]any
	if value.ReasoningMapping != nil {
		mapping = map[string]any{"field": value.ReasoningMapping.Field, "values": value.ReasoningMapping.Values}
	}
	return modelSettingsResponse{
		Transport: strings.ToLower(string(value.Transport)), ContextWindowTokens: value.ContextWindowTokens,
		MaxOutputTokens: value.MaxOutputTokens, SupportsStreaming: value.SupportsStreaming,
		SupportsTools: value.SupportsTools, SupportsStructuredOutput: value.SupportsStructuredOutput,
		SupportsTemperature: value.SupportsTemperature,
		ReasoningTransport:  strings.ToLower(string(value.ReasoningTransport)), ReasoningMapping: mapping,
		TimeoutSeconds: value.TimeoutSeconds, MaxRetries: value.MaxRetries,
		MaxConcurrentTasks: value.MaxConcurrentTasks,
		ExtraBody:          value.ExtraBody, MetadataOrigin: origins,
	}
}

func discoveryResponse(value providerdomain.DiscoveryRun) discoveryRunResponse {
	var digest *string
	if len(value.ResponseSHA256) != 0 {
		text := hex.EncodeToString(value.ResponseSHA256)
		digest = &text
	}
	return discoveryRunResponse{
		ID: value.ID.String(), EndpointID: value.EndpointID.String(), JobID: value.JobID.String(),
		CapturedConfigurationVersion: value.CapturedConfigurationVersion,
		CapturedCredentialVersion:    value.CapturedCredentialVersion, TLSRequired: value.TLSRequired,
		RequestedByOperatorID: value.RequestedByActorID.String(), Status: strings.ToLower(string(value.Status)),
		ModelIDs: append([]string{}, value.ModelIDs...), RawResponse: value.RawResponse, TLSVerified: value.TLSVerified,
		AuthenticationSucceeded: value.AuthenticationSucceeded, HTTPStatus: value.HTTPStatus,
		ResponseSHA256: digest, ModelCount: value.ModelCount, SanitizedError: value.SanitizedError,
		CreatedAt: value.CreatedAt, StartedAt: value.StartedAt, CompletedAt: value.CompletedAt,
	}
}

func probeResponse(value providerdomain.ProbeRun) probeRunResponse {
	checks := make([]string, len(value.SelectedChecks))
	for index, check := range value.SelectedChecks {
		checks[index] = strings.ToLower(string(check))
	}
	var resultingVersion *string
	if value.ResultingVersionID != nil {
		text := value.ResultingVersionID.String()
		resultingVersion = &text
	}
	return probeRunResponse{
		ID: value.ID.String(), ModelProfileID: value.ProfileID.String(), JobID: value.JobID.String(),
		CapturedConfigurationVersion: value.CapturedConfigurationVersion,
		CapturedCredentialVersion:    value.CapturedCredentialVersion,
		CapturedProfileVersionID:     value.CapturedProfileVersionID.String(),
		RequestedByOperatorID:        value.RequestedByActorID.String(), SelectedChecks: checks,
		AcknowledgeCost: value.AcknowledgeCost, Status: strings.ToLower(string(value.Status)),
		Findings: value.Findings, RawResponse: value.RawResponse, SanitizedError: value.SanitizedError,
		ResultingVersionID: resultingVersion, CreatedAt: value.CreatedAt,
		StartedAt: value.StartedAt, CompletedAt: value.CompletedAt,
	}
}

func assignmentResponse(value providerdomain.Assignment) modelAssignmentResponse {
	return modelAssignmentResponse{
		ID: value.ID.String(), KnowledgeBaseID: value.KnowledgeBaseID.String(),
		Role: strings.ToLower(string(value.Role)), ModelProfileID: value.ProfileID.String(),
		ReasoningEffort: strings.ToLower(string(value.ReasoningEffort)),
		AnswerMode:      strings.ToLower(string(value.AnswerMode)), Version: value.Version,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func providerProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, providerdomain.ErrNotFound):
		problem.Title, problem.Status, problem.Detail = "Not Found", http.StatusNotFound, "Provider resource not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Idempotency key conflicts with a different request."
	case errors.Is(err, providerdomain.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Provider resource state conflicts with the request."
	default:
		problem.Title, problem.Status, problem.Detail = "Internal Server Error", http.StatusInternalServerError, "The request could not be completed."
	}
	return problem
}

func unprocessableProviderProblem(instance, detail string) error {
	return &apiProblem{Type: "about:blank", Title: "Unprocessable Content",
		Status: http.StatusUnprocessableEntity, Detail: detail, Instance: instance}
}
