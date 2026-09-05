package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/chattokens"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/danielgtaylor/huma/v2"
)

const (
	agentsPath           = "/api/v1/agents"
	chatTokensPath       = "/api/v1/chat-access-tokens"
	chatTokenPreviewPath = chatTokensPath + "/preview"
	agentBodyLimit       = 1 << 20
	chatTokenBodyLimit   = 256 << 10
)

type agentService interface {
	agentScopeService
	ListPage(context.Context, *agents.PageCursor, int) (agents.Page, error)
	Get(context.Context, agents.AgentID) (agents.Agent, error)
	Create(context.Context, agents.CreateCommand, auth.OperatorID, string) (agents.Agent, error)
	ReplaceConfiguration(context.Context, agents.ReplaceConfigurationCommand, auth.OperatorID, string) (agents.Agent, error)
	SetLifecycle(context.Context, agents.SetLifecycleCommand, auth.OperatorID, string) (agents.Agent, error)
	ListVersions(context.Context, agents.AgentID, *agents.VersionPageCursor, int) (agents.VersionPage, error)
	GetVersion(context.Context, agents.AgentID, agents.VersionID) (agents.Version, error)
	EvaluateReadiness(context.Context, agents.AgentID) (agents.Readiness, error)
	ListRuns(context.Context, agents.AgentID, *agents.RunPageCursor, int) (agents.RunPage, error)
	GetRun(context.Context, agents.AgentID, agents.RunID) (agents.RunDetail, error)
}

type agentScopeService interface {
	DescribeScopes(context.Context, []agents.AgentID) ([]agents.ScopeDescription, error)
}

type chatTokenService interface {
	List(context.Context, *chattokens.PageCursor, int) (chattokens.Page, error)
	Create(context.Context, chattokens.CreateCommand, auth.OperatorID, string) (chattokens.Issued, error)
	Revoke(context.Context, chattokens.ID, auth.OperatorID, string) (chattokens.Summary, error)
}

type agentConfigurationRequest struct {
	DisplayName            string   `json:"display_name" minLength:"1" maxLength:"255"`
	Description            string   `json:"description,omitempty" maxLength:"2000"`
	ResponseLanguage       string   `json:"response_language" minLength:"1" maxLength:"35"`
	IdentityInstructions   string   `json:"identity_instructions" minLength:"1" maxLength:"16000"`
	ModelProfileID         string   `json:"model_profile_id" format:"uuid"`
	ReasoningEffort        string   `json:"reasoning_effort" enum:"none,minimal,low,medium,high,max"`
	AnswerMode             string   `json:"answer_mode" enum:"tool_calling,single_pass"`
	BehavioralInstructions string   `json:"behavioral_instructions,omitempty" maxLength:"16000"`
	EvidenceAccess         string   `json:"evidence_access" enum:"wiki_only,wiki_and_source"`
	RefusalMarkdown        string   `json:"refusal_markdown" minLength:"1" maxLength:"4000"`
	MaxToolCalls           int32    `json:"max_tool_calls" minimum:"0" maximum:"64"`
	MaxAnswerTokens        int32    `json:"max_answer_tokens" minimum:"1" maximum:"262144"`
	KnowledgeBaseIDs       []string `json:"knowledge_base_ids" minItems:"1" maxItems:"32" nullable:"false"`
}

type createAgentRequest struct {
	Key           string                    `json:"key" minLength:"1" maxLength:"64"`
	Configuration agentConfigurationRequest `json:"configuration"`
}

type replaceAgentConfigurationRequest struct {
	ExpectedVersion int32                     `json:"expected_version" minimum:"1"`
	Configuration   agentConfigurationRequest `json:"configuration"`
}

type setAgentLifecycleRequest struct {
	ExpectedVersion int32  `json:"expected_version" minimum:"1"`
	Lifecycle       string `json:"lifecycle" enum:"active,archived"`
}

type agentConfigurationResponse struct {
	DisplayName            string   `json:"display_name"`
	Description            string   `json:"description"`
	ResponseLanguage       string   `json:"response_language"`
	IdentityInstructions   string   `json:"identity_instructions"`
	ModelProfileID         string   `json:"model_profile_id" format:"uuid"`
	ReasoningEffort        string   `json:"reasoning_effort" enum:"none,minimal,low,medium,high,max"`
	AnswerMode             string   `json:"answer_mode" enum:"tool_calling,single_pass"`
	BehavioralInstructions string   `json:"behavioral_instructions"`
	EvidenceAccess         string   `json:"evidence_access" enum:"wiki_only,wiki_and_source"`
	RefusalMarkdown        string   `json:"refusal_markdown"`
	MaxToolCalls           int32    `json:"max_tool_calls"`
	MaxAnswerTokens        int32    `json:"max_answer_tokens"`
	KnowledgeBaseIDs       []string `json:"knowledge_base_ids" nullable:"false"`
}

type agentVersionResponse struct {
	ID                string                     `json:"id" format:"uuid"`
	AgentID           string                     `json:"agent_id" format:"uuid"`
	VersionNumber     int32                      `json:"version_number"`
	Configuration     agentConfigurationResponse `json:"configuration"`
	CreatedByOperator string                     `json:"created_by_operator_id" format:"uuid"`
	CreatedAt         time.Time                  `json:"created_at"`
}

type agentResponse struct {
	ID               string               `json:"id" format:"uuid"`
	Key              string               `json:"key"`
	Selector         string               `json:"selector"`
	Lifecycle        string               `json:"lifecycle" enum:"draft,active,archived"`
	CurrentVersionID string               `json:"current_version_id" format:"uuid"`
	CurrentVersion   agentVersionResponse `json:"current_version"`
	Version          int32                `json:"version"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
	ActivatedAt      *time.Time           `json:"activated_at"`
	ArchivedAt       *time.Time           `json:"archived_at"`
}

type readinessIssueResponse struct {
	Code            string  `json:"code" enum:"model_unavailable,endpoint_unavailable,credential_unavailable,model_configuration_stale,model_limits_unknown,model_capability_missing,reasoning_unsupported,knowledge_base_missing,knowledge_base_inactive,knowledge_base_unpublished"`
	KnowledgeBaseID *string `json:"knowledge_base_id,omitempty" format:"uuid"`
}

type agentReadinessResponse struct {
	Ready                        bool                     `json:"ready"`
	EffectiveAccess              string                   `json:"effective_access" enum:"public,restricted"`
	ModelProfileVersionID        *string                  `json:"model_profile_version_id,omitempty" format:"uuid"`
	ModelProfileVersionNumber    *int32                   `json:"model_profile_version_number,omitempty"`
	ProviderEndpointID           *string                  `json:"provider_endpoint_id,omitempty" format:"uuid"`
	EndpointConfigurationVersion *int32                   `json:"endpoint_configuration_version,omitempty"`
	Issues                       []readinessIssueResponse `json:"issues" nullable:"false"`
}

type listAgentsResponse struct {
	Items      []agentResponse `json:"items" nullable:"false"`
	NextCursor *string         `json:"next_cursor,omitempty"`
}

type listAgentVersionsResponse struct {
	Items      []agentVersionResponse `json:"items" nullable:"false"`
	NextCursor *string                `json:"next_cursor,omitempty"`
}

type createAgentInput struct {
	SessionCookie  string             `cookie:"ref0_session"`
	CSRFToken      string             `header:"X-CSRF-Token"`
	IdempotencyKey string             `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	Body           createAgentRequest `json:"body"`
}

type listAgentsInput struct {
	SessionCookie string              `cookie:"ref0_session"`
	Cursor        optionalStringParam `query:"cursor"`
	Limit         int                 `query:"limit" default:"50" minimum:"1" maximum:"100"`
}

type getAgentInput struct {
	SessionCookie string `cookie:"ref0_session"`
	AgentID       string `path:"agent_id" format:"uuid"`
}

type replaceAgentConfigurationInput struct {
	SessionCookie  string                           `cookie:"ref0_session"`
	CSRFToken      string                           `header:"X-CSRF-Token"`
	IdempotencyKey string                           `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	AgentID        string                           `path:"agent_id" format:"uuid"`
	Body           replaceAgentConfigurationRequest `json:"body"`
}

type setAgentLifecycleInput struct {
	SessionCookie  string                   `cookie:"ref0_session"`
	CSRFToken      string                   `header:"X-CSRF-Token"`
	IdempotencyKey string                   `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	AgentID        string                   `path:"agent_id" format:"uuid"`
	Body           setAgentLifecycleRequest `json:"body"`
}

type getAgentVersionInput struct {
	SessionCookie string `cookie:"ref0_session"`
	AgentID       string `path:"agent_id" format:"uuid"`
	VersionID     string `path:"version_id" format:"uuid"`
}

type listAgentVersionsInput struct {
	SessionCookie string              `cookie:"ref0_session"`
	AgentID       string              `path:"agent_id" format:"uuid"`
	Cursor        optionalStringParam `query:"cursor"`
	Limit         int                 `query:"limit" default:"50" minimum:"1" maximum:"100"`
}

type agentOutput struct{ Body agentResponse }
type agentsOutput struct{ Body listAgentsResponse }
type agentVersionOutput struct{ Body agentVersionResponse }
type agentVersionsOutput struct {
	Body listAgentVersionsResponse
}
type agentReadinessOutput struct{ Body agentReadinessResponse }

type chatTokenResponse struct {
	ID          string                        `json:"id" format:"uuid"`
	Prefix      string                        `json:"prefix"`
	Label       string                        `json:"label"`
	AgentIDs    []string                      `json:"agent_ids" format:"uuid" nullable:"false"`
	AgentScopes []chatTokenAgentScopeResponse `json:"agent_scopes" nullable:"false"`
	CreatedAt   time.Time                     `json:"created_at"`
	ExpiresAt   time.Time                     `json:"expires_at"`
	RevokedAt   *time.Time                    `json:"revoked_at"`
	LastUsedAt  *time.Time                    `json:"last_used_at"`
}

type chatTokenSummaryResponse struct {
	ID         string     `json:"id" format:"uuid"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label"`
	AgentCount int        `json:"agent_count" minimum:"1" maximum:"2048"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type issuedChatTokenResponse struct {
	ID          string                        `json:"id" format:"uuid"`
	Prefix      string                        `json:"prefix"`
	Label       string                        `json:"label"`
	AgentIDs    []string                      `json:"agent_ids" format:"uuid" nullable:"false"`
	AgentScopes []chatTokenAgentScopeResponse `json:"agent_scopes" nullable:"false"`
	CreatedAt   time.Time                     `json:"created_at"`
	ExpiresAt   time.Time                     `json:"expires_at"`
	RevokedAt   *time.Time                    `json:"revoked_at"`
	LastUsedAt  *time.Time                    `json:"last_used_at"`
	Secret      string                        `json:"secret" readOnly:"true"`
}

type listChatTokensResponse struct {
	Items      []chatTokenSummaryResponse `json:"items" nullable:"false"`
	NextCursor *string                    `json:"next_cursor,omitempty"`
}

type createChatTokenRequest struct {
	Label     string    `json:"label" minLength:"1" maxLength:"255"`
	AgentIDs  []string  `json:"agent_ids" format:"uuid" minItems:"1" maxItems:"2048" uniqueItems:"true" nullable:"false"`
	ExpiresAt time.Time `json:"expires_at"`
}

type previewChatTokenScopesRequest struct {
	AgentIDs []string `json:"agent_ids" format:"uuid" minItems:"1" maxItems:"2048" uniqueItems:"true" nullable:"false"`
}

type previewChatTokenScopesResponse struct {
	AgentIDs         []string                      `json:"agent_ids" format:"uuid" nullable:"false"`
	AgentScopes      []chatTokenAgentScopeResponse `json:"agent_scopes" nullable:"false"`
	KnowledgeBaseIDs []string                      `json:"knowledge_base_ids" format:"uuid" nullable:"false"`
	EffectiveAccess  string                        `json:"effective_access" enum:"public,restricted"`
	Ready            bool                          `json:"ready"`
}

type listChatTokensInput struct {
	SessionCookie string              `cookie:"ref0_session"`
	Cursor        optionalStringParam `query:"cursor"`
	Limit         int                 `query:"limit" default:"50" minimum:"1" maximum:"100"`
}

type createChatTokenInput struct {
	SessionCookie  string                 `cookie:"ref0_session"`
	CSRFToken      string                 `header:"X-CSRF-Token"`
	IdempotencyKey string                 `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	Body           createChatTokenRequest `json:"body"`
}

type previewChatTokenScopesInput struct {
	SessionCookie string                        `cookie:"ref0_session"`
	Body          previewChatTokenScopesRequest `json:"body"`
}

type revokeChatTokenInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	TokenID        string `path:"token_id" format:"uuid"`
}

type chatTokensOutput struct{ Body listChatTokensResponse }
type chatTokenSummaryOutput struct{ Body chatTokenSummaryResponse }
type issuedChatTokenOutput struct {
	Status       int
	CacheControl string                  `header:"Cache-Control"`
	Body         issuedChatTokenResponse `nameHint:"IssuedChatTokenResponse"`
}

type previewChatTokenScopesOutput struct {
	CacheControl string                         `header:"Cache-Control"`
	Body         previewChatTokenScopesResponse `nameHint:"PreviewChatTokenScopesResponse"`
}

type runSummaryResponse struct {
	ID                   string         `json:"id" format:"uuid"`
	AgentID              string         `json:"agent_id" format:"uuid"`
	AgentVersionID       string         `json:"agent_version_id" format:"uuid"`
	AgentResourceVersion int32          `json:"agent_resource_version"`
	AgentVersionNumber   int32          `json:"agent_version_number"`
	Origin               string         `json:"origin" enum:"http,discord"`
	Subject              string         `json:"subject"`
	Outcome              string         `json:"outcome" enum:"answered,refused,insufficient_evidence,failed"`
	Usage                map[string]int `json:"usage"`
	LatencyMS            int            `json:"latency_ms"`
	CreatedAt            time.Time      `json:"created_at"`
	CompletedAt          time.Time      `json:"completed_at"`
}

type listRunsResponse struct {
	Items      []runSummaryResponse `json:"items" nullable:"false"`
	NextCursor *string              `json:"next_cursor,omitempty"`
}

type runKnowledgeBaseResponse struct {
	Position             int32    `json:"position"`
	KnowledgeBaseID      string   `json:"knowledge_base_id" format:"uuid"`
	KnowledgeBaseVersion int32    `json:"knowledge_base_version"`
	AccessPolicy         string   `json:"access_policy" enum:"public,restricted"`
	WikiVersionID        string   `json:"wiki_version_id" format:"uuid"`
	DocumentationRunID   string   `json:"documentation_run_id" format:"uuid"`
	SourceRevisionIDs    []string `json:"source_revision_ids" nullable:"false"`
	SourceScopeDigest    string   `json:"source_scope_digest"`
}

type runDetailResponse struct {
	ID                                   string                     `json:"id" format:"uuid"`
	AgentID                              string                     `json:"agent_id" format:"uuid"`
	AgentVersionID                       string                     `json:"agent_version_id" format:"uuid"`
	AgentResourceVersion                 int32                      `json:"agent_resource_version"`
	AgentVersionNumber                   int32                      `json:"agent_version_number"`
	Origin                               string                     `json:"origin" enum:"http,discord"`
	Subject                              string                     `json:"subject"`
	Outcome                              string                     `json:"outcome" enum:"answered,refused,insufficient_evidence,failed"`
	Usage                                map[string]int             `json:"usage"`
	LatencyMS                            int                        `json:"latency_ms"`
	CreatedAt                            time.Time                  `json:"created_at"`
	CompletedAt                          time.Time                  `json:"completed_at"`
	ModelProfileID                       string                     `json:"model_profile_id" format:"uuid"`
	ModelProfileVersionID                string                     `json:"model_profile_version_id" format:"uuid"`
	ModelProfileVersionNumber            int32                      `json:"model_profile_version_number"`
	ProviderEndpointID                   string                     `json:"provider_endpoint_id" format:"uuid"`
	CapturedEndpointConfigurationVersion int32                      `json:"captured_endpoint_configuration_version"`
	CapturedCredentialID                 *string                    `json:"captured_credential_id" format:"uuid"`
	CapturedCredentialVersion            *int32                     `json:"captured_credential_version"`
	EffectiveAccess                      string                     `json:"effective_access" enum:"public,restricted"`
	ToolCalls                            []string                   `json:"tool_calls" nullable:"false"`
	Citations                            []agents.Citation          `json:"citations" nullable:"false"`
	SanitizedError                       *string                    `json:"sanitized_error"`
	KnowledgeBases                       []runKnowledgeBaseResponse `json:"knowledge_bases" nullable:"false"`
}

type listRunsInput struct {
	SessionCookie string              `cookie:"ref0_session"`
	AgentID       string              `path:"agent_id" format:"uuid"`
	Cursor        optionalStringParam `query:"cursor"`
	Limit         int                 `query:"limit" default:"50" minimum:"1" maximum:"100"`
}

type getRunInput struct {
	SessionCookie string `cookie:"ref0_session"`
	AgentID       string `path:"agent_id" format:"uuid"`
	RunID         string `path:"run_id" format:"uuid"`
}

type runsOutput struct{ Body listRunsResponse }
type runOutput struct{ Body runDetailResponse }

type chatTokenAgentScopeResponse struct {
	AgentID          string   `json:"agent_id" format:"uuid"`
	AgentKey         string   `json:"agent_key"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids" format:"uuid" nullable:"false"`
	EffectiveAccess  string   `json:"effective_access" enum:"public,restricted"`
	Ready            bool     `json:"ready"`
}

func registerAgents(api huma.API, sessions auth.SessionService, service agentService) {
	registerAgentList(api, sessions, service)
	registerAgentCreate(api, sessions, service)
	registerAgentGet(api, sessions, service)
	registerAgentReplace(api, sessions, service)
	registerAgentLifecycle(api, sessions, service)
	registerAgentVersions(api, sessions, service)
	registerAgentVersionGet(api, sessions, service)
	registerAgentReadiness(api, sessions, service)
	registerAgentRuns(api, sessions, service)
	registerAgentRunGet(api, sessions, service)
	normalizeAgentMutationOpenAPI(api)
}

func registerChatTokens(api huma.API, sessions auth.SessionService, tokens chatTokenService, agentCatalog agentScopeService) {
	registerChatTokenList(api, sessions, tokens)
	registerChatTokenScopePreview(api, sessions, agentCatalog)
	registerChatTokenCreate(api, sessions, tokens, agentCatalog)
	registerChatTokenRevoke(api, sessions, tokens)
	normalizeChatTokenCreateOpenAPI(api)
}

func registerAgentList(api huma.API, sessions auth.SessionService, service agentService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_agents_api_v1_agents_get", Method: http.MethodGet, Path: agentsPath,
		Summary: "List Agents", Tags: []string{"agents"},
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *listAgentsInput) (*agentsOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, agentsPath); err != nil {
			return nil, err
		}
		cursor, err := decodeAgentCursor(input.Cursor.Pointer())
		if err != nil {
			return nil, parameterValidationProblem(agentsPath, "query")
		}
		page, err := service.ListPage(ctx, cursor, input.Limit)
		if err != nil {
			return nil, agentProblem(agentsPath, err)
		}
		body := listAgentsResponse{Items: make([]agentResponse, len(page.Agents))}
		for index, value := range page.Agents {
			body.Items[index] = newAgentResponse(value)
		}
		if page.NextCursor != nil {
			encoded := encodeAgentCursor(*page.NextCursor)
			body.NextCursor = &encoded
		}
		return &agentsOutput{Body: body}, nil
	})
}

func registerAgentCreate(api huma.API, sessions auth.SessionService, service agentService) {
	huma.Register(api, huma.Operation{
		OperationID: "create_agent_api_v1_agents_post", Method: http.MethodPost, Path: agentsPath,
		Summary: "Create Agent", Tags: []string{"agents"}, DefaultStatus: http.StatusCreated,
		Errors:       []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		MaxBodyBytes: agentBodyLimit,
	}, func(ctx context.Context, input *createAgentInput) (*agentOutput, error) {
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, agentsPath)
		if err != nil {
			return nil, err
		}
		key, err := requiredIdempotencyKey(input.IdempotencyKey, agentsPath)
		if err != nil {
			return nil, err
		}
		configuration, ok := agentConfiguration(input.Body.Configuration)
		if !ok {
			return nil, validationProblem(agentsPath)
		}
		value, err := service.Create(ctx, agents.CreateCommand{Key: input.Body.Key, Configuration: configuration}, session.Operator.ID, key)
		if err != nil {
			return nil, agentProblem(agentsPath, err)
		}
		return &agentOutput{Body: newAgentResponse(value)}, nil
	})
}

func registerAgentGet(api huma.API, sessions auth.SessionService, service agentService) {
	const path = agentsPath + "/{agent_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_agent_api_v1_agents__agent_id__get", Method: http.MethodGet, Path: path,
		Summary: "Get Agent", Tags: []string{"agents"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *getAgentInput) (*agentOutput, error) {
		instance := replaceAgentPath(path, input.AgentID, "")
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := parseAgentID(input.AgentID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.Get(ctx, id)
		if err != nil {
			return nil, agentProblem(instance, err)
		}
		return &agentOutput{Body: newAgentResponse(value)}, nil
	})
}

func registerAgentReplace(api huma.API, sessions auth.SessionService, service agentService) {
	const path = agentsPath + "/{agent_id}/configuration"
	huma.Register(api, huma.Operation{
		OperationID: "replace_agent_configuration_api_v1_agents__agent_id__configuration_put",
		Method:      http.MethodPut, Path: path, Summary: "Replace Agent Configuration", Tags: []string{"agents"},
		Errors:       []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		MaxBodyBytes: agentBodyLimit,
	}, func(ctx context.Context, input *replaceAgentConfigurationInput) (*agentOutput, error) {
		instance := replaceAgentPath(path, input.AgentID, "")
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, err := parseAgentID(input.AgentID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		key, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		configuration, ok := agentConfiguration(input.Body.Configuration)
		if !ok {
			return nil, validationProblem(instance)
		}
		value, err := service.ReplaceConfiguration(ctx, agents.ReplaceConfigurationCommand{
			AgentID: id, ExpectedVersion: input.Body.ExpectedVersion, Configuration: configuration,
		}, session.Operator.ID, key)
		if err != nil {
			if readiness, ok := agents.NotReadyDetails(err); ok {
				return nil, newAgentCandidateNotReadyProblem(instance, readiness)
			}
			return nil, agentProblem(instance, err)
		}
		return &agentOutput{Body: newAgentResponse(value)}, nil
	})
}

func registerAgentLifecycle(api huma.API, sessions auth.SessionService, service agentService) {
	const path = agentsPath + "/{agent_id}/lifecycle"
	huma.Register(api, huma.Operation{
		OperationID: "set_agent_lifecycle_api_v1_agents__agent_id__lifecycle_patch",
		Method:      http.MethodPatch, Path: path, Summary: "Set Agent Lifecycle", Tags: []string{"agents"},
		Errors:       []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		MaxBodyBytes: agentBodyLimit,
	}, func(ctx context.Context, input *setAgentLifecycleInput) (*agentOutput, error) {
		instance := replaceAgentPath(path, input.AgentID, "")
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, err := parseAgentID(input.AgentID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		key, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		lifecycle, ok := agentLifecycle(input.Body.Lifecycle)
		if !ok {
			return nil, validationProblem(instance)
		}
		value, err := service.SetLifecycle(ctx, agents.SetLifecycleCommand{
			AgentID: id, ExpectedVersion: input.Body.ExpectedVersion, Lifecycle: lifecycle,
		}, session.Operator.ID, key)
		if err != nil {
			if readiness, ok := agents.NotReadyDetails(err); ok {
				return nil, newAgentCandidateNotReadyProblem(instance, readiness)
			}
			return nil, agentProblem(instance, err)
		}
		return &agentOutput{Body: newAgentResponse(value)}, nil
	})
}

func registerAgentVersions(api huma.API, sessions auth.SessionService, service agentService) {
	const path = agentsPath + "/{agent_id}/versions"
	huma.Register(api, huma.Operation{
		OperationID: "list_agent_versions_api_v1_agents__agent_id__versions_get",
		Method:      http.MethodGet, Path: path, Summary: "List Agent Versions", Tags: []string{"agents"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *listAgentVersionsInput) (*agentVersionsOutput, error) {
		instance := replaceAgentPath(path, input.AgentID, "")
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := parseAgentID(input.AgentID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		cursor, err := decodeAgentVersionCursor(input.Cursor.Pointer())
		if err != nil {
			return nil, parameterValidationProblem(instance, "query")
		}
		page, err := service.ListVersions(ctx, id, cursor, input.Limit)
		if err != nil {
			return nil, agentProblem(instance, err)
		}
		body := listAgentVersionsResponse{Items: make([]agentVersionResponse, len(page.Versions))}
		for index, value := range page.Versions {
			body.Items[index] = newAgentVersionResponse(value)
		}
		if page.NextCursor != nil {
			encoded := encodeAgentVersionCursor(*page.NextCursor)
			body.NextCursor = &encoded
		}
		return &agentVersionsOutput{Body: body}, nil
	})
}

func registerAgentVersionGet(api huma.API, sessions auth.SessionService, service agentService) {
	const path = agentsPath + "/{agent_id}/versions/{version_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_agent_version_api_v1_agents__agent_id__versions__version_id__get",
		Method:      http.MethodGet, Path: path, Summary: "Get Agent Version", Tags: []string{"agents"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *getAgentVersionInput) (*agentVersionOutput, error) {
		instance := replaceAgentPath(path, input.AgentID, input.VersionID)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		agentID, err := parseAgentID(input.AgentID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		versionID, err := parseVersionID(input.VersionID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.GetVersion(ctx, agentID, versionID)
		if err != nil {
			return nil, agentProblem(instance, err)
		}
		return &agentVersionOutput{Body: newAgentVersionResponse(value)}, nil
	})
}

func registerAgentReadiness(api huma.API, sessions auth.SessionService, service agentService) {
	const path = agentsPath + "/{agent_id}/readiness"
	huma.Register(api, huma.Operation{
		OperationID: "get_agent_readiness_api_v1_agents__agent_id__readiness_get",
		Method:      http.MethodGet, Path: path, Summary: "Get Agent Readiness", Tags: []string{"agents"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *getAgentInput) (*agentReadinessOutput, error) {
		instance := replaceAgentPath(path, input.AgentID, "")
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := parseAgentID(input.AgentID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.EvaluateReadiness(ctx, id)
		if err != nil {
			return nil, agentProblem(instance, err)
		}
		return &agentReadinessOutput{Body: newAgentReadinessResponse(value)}, nil
	})
}

func registerAgentRuns(api huma.API, sessions auth.SessionService, service agentService) {
	const path = agentsPath + "/{agent_id}/runs"
	huma.Register(api, huma.Operation{
		OperationID: "list_agent_runs_api_v1_agents__agent_id__runs_get",
		Method:      http.MethodGet, Path: path, Summary: "List Agent Runs", Tags: []string{"agents"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *listRunsInput) (*runsOutput, error) {
		instance := replaceAgentPath(path, input.AgentID, "")
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		agentID, err := parseAgentID(input.AgentID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		cursor, err := decodeRunCursor(input.Cursor.Pointer())
		if err != nil {
			return nil, parameterValidationProblem(instance, "query")
		}
		page, err := service.ListRuns(ctx, agentID, cursor, input.Limit)
		if err != nil {
			return nil, agentProblem(instance, err)
		}
		body := listRunsResponse{Items: make([]runSummaryResponse, len(page.Runs))}
		for index, value := range page.Runs {
			body.Items[index] = newRunSummaryResponse(value)
		}
		if page.NextCursor != nil {
			encoded := encodeRunCursor(*page.NextCursor)
			body.NextCursor = &encoded
		}
		return &runsOutput{Body: body}, nil
	})
}

func registerAgentRunGet(api huma.API, sessions auth.SessionService, service agentService) {
	const path = agentsPath + "/{agent_id}/runs/{run_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_agent_run_api_v1_agents__agent_id__runs__run_id__get",
		Method:      http.MethodGet, Path: path, Summary: "Get Agent Run", Tags: []string{"agents"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *getRunInput) (*runOutput, error) {
		instance := strings.Replace(strings.Replace(path, "{agent_id}", input.AgentID, 1), "{run_id}", input.RunID, 1)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		agentID, err := parseAgentID(input.AgentID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		runID, err := parseRunID(input.RunID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.GetRun(ctx, agentID, runID)
		if err != nil {
			return nil, agentProblem(instance, err)
		}
		return &runOutput{Body: newRunDetailResponse(value)}, nil
	})
}

func registerChatTokenList(api huma.API, sessions auth.SessionService, service chatTokenService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_chat_access_tokens_api_v1_chat_access_tokens_get",
		Method:      http.MethodGet, Path: chatTokensPath, Summary: "List Chat Access Tokens", Tags: []string{"chat-access-tokens"},
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *listChatTokensInput) (*chatTokensOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, chatTokensPath); err != nil {
			return nil, err
		}
		cursor, err := decodeChatTokenCursor(input.Cursor.Pointer())
		if err != nil {
			return nil, parameterValidationProblem(chatTokensPath, "query")
		}
		page, err := service.List(ctx, cursor, input.Limit)
		if err != nil {
			return nil, chatTokenProblem(chatTokensPath, err)
		}
		body := listChatTokensResponse{Items: make([]chatTokenSummaryResponse, len(page.Summaries))}
		for index, value := range page.Summaries {
			body.Items[index] = newChatTokenSummaryResponse(value)
		}
		if page.NextCursor != nil {
			encoded := encodeChatTokenCursor(*page.NextCursor)
			body.NextCursor = &encoded
		}
		return &chatTokensOutput{Body: body}, nil
	})
}

func registerChatTokenScopePreview(api huma.API, sessions auth.SessionService, agentCatalog agentScopeService) {
	huma.Register(api, huma.Operation{
		OperationID:  "preview_chat_access_token_scopes_api_v1_chat_access_tokens_preview_post",
		Method:       http.MethodPost,
		Path:         chatTokenPreviewPath,
		Summary:      "Preview Current Effective Chat Access Token Scope",
		Tags:         []string{"chat-access-tokens"},
		Errors:       []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
		MaxBodyBytes: chatTokenBodyLimit,
	}, func(ctx context.Context, input *previewChatTokenScopesInput) (*previewChatTokenScopesOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, chatTokenPreviewPath); err != nil {
			return nil, err
		}
		agentIDs, ok := parseAgentIDs(input.Body.AgentIDs)
		if !ok {
			return nil, validationProblem(chatTokenPreviewPath)
		}
		sortAgentIDs(agentIDs)
		descriptionsByID, err := resolveTokenScopes(ctx, agentCatalog, agentIDs)
		if err != nil {
			return nil, agentProblem(chatTokenPreviewPath, err)
		}
		return &previewChatTokenScopesOutput{
			CacheControl: "no-store",
			Body:         newPreviewChatTokenScopesResponse(agentIDs, descriptionsByID),
		}, nil
	})
}

func registerChatTokenCreate(api huma.API, sessions auth.SessionService, service chatTokenService, agentCatalog agentScopeService) {
	huma.Register(api, huma.Operation{
		OperationID: "create_chat_access_token_api_v1_chat_access_tokens_post",
		Method:      http.MethodPost, Path: chatTokensPath, Summary: "Create Chat Access Token", Tags: []string{"chat-access-tokens"},
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		MaxBodyBytes:  chatTokenBodyLimit,
	}, func(ctx context.Context, input *createChatTokenInput) (*issuedChatTokenOutput, error) {
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, chatTokensPath)
		if err != nil {
			return nil, err
		}
		key, err := requiredIdempotencyKey(input.IdempotencyKey, chatTokensPath)
		if err != nil {
			return nil, err
		}
		agentIDs, ok := parseAgentIDs(input.Body.AgentIDs)
		if !ok {
			return nil, validationProblem(chatTokensPath)
		}
		descriptionsByID, err := resolveTokenScopes(ctx, agentCatalog, agentIDs)
		if err != nil {
			return nil, chatTokenProblem(chatTokensPath, err)
		}
		issued, err := service.Create(ctx, chattokens.CreateCommand{
			Label: input.Body.Label, AgentIDs: agentIDs, ExpiresAt: input.Body.ExpiresAt,
		}, session.Operator.ID, key)
		metadata := newChatTokenResponse(issued.Token, descriptionsByID)
		if errors.Is(err, chattokens.ErrSecretAlreadyIssued) {
			return nil, &ChatTokenReplayProblem{
				Type: "about:blank", Title: "Conflict", Status: http.StatusConflict,
				Detail: "Chat access token secret was already issued.", Instance: chatTokensPath,
				Code: "secret_already_issued", Token: metadata,
			}
		}
		if err != nil {
			return nil, chatTokenProblem(chatTokensPath, err)
		}
		if issued.Secret == nil {
			return nil, chatTokenProblem(chatTokensPath, errors.New("chat access token secret is unavailable"))
		}
		return &issuedChatTokenOutput{
			Status: http.StatusCreated, CacheControl: "no-store",
			Body: newIssuedChatTokenResponse(metadata, issued.Secret.Reveal()),
		}, nil
	})
}

func registerChatTokenRevoke(api huma.API, sessions auth.SessionService, service chatTokenService) {
	const path = chatTokensPath + "/{token_id}"
	huma.Register(api, huma.Operation{
		OperationID: "revoke_chat_access_token_api_v1_chat_access_tokens__token_id__delete",
		Method:      http.MethodDelete, Path: path, Summary: "Revoke Chat Access Token", Tags: []string{"chat-access-tokens"},
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *revokeChatTokenInput) (*chatTokenSummaryOutput, error) {
		instance := strings.Replace(path, "{token_id}", input.TokenID, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, err := chattokens.ParseID(input.TokenID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		key, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		value, err := service.Revoke(ctx, id, session.Operator.ID, key)
		if err != nil {
			return nil, chatTokenProblem(instance, err)
		}
		return &chatTokenSummaryOutput{Body: newChatTokenSummaryResponse(value)}, nil
	})
}

// AgentCandidateNotReadyProblem carries the exact readiness snapshot evaluated
// within the rejected Agent mutation transaction.
type AgentCandidateNotReadyProblem struct {
	Type      string                 `json:"type"`
	Title     string                 `json:"title"`
	Status    int                    `json:"status"`
	Detail    string                 `json:"detail"`
	Instance  string                 `json:"instance"`
	Code      string                 `json:"code" enum:"candidate_not_ready"`
	Readiness agentReadinessResponse `json:"readiness"`
}

func (problem *AgentCandidateNotReadyProblem) Error() string  { return problem.Detail }
func (problem *AgentCandidateNotReadyProblem) GetStatus() int { return problem.Status }
func (*AgentCandidateNotReadyProblem) ContentType(contentType string) string {
	if contentType == "application/json" {
		return "application/problem+json"
	}
	return contentType
}

func newAgentCandidateNotReadyProblem(instance string, readiness agents.Readiness) *AgentCandidateNotReadyProblem {
	return &AgentCandidateNotReadyProblem{
		Type: "about:blank", Title: "Conflict", Status: http.StatusConflict,
		Detail: "The candidate Agent configuration is not ready.", Instance: instance,
		Code: "candidate_not_ready", Readiness: newAgentReadinessResponse(readiness),
	}
}

// ChatTokenReplayProblem tells an operator that an idempotent creation replay
// can recover token metadata but can never recover the issue-once secret.
type ChatTokenReplayProblem struct {
	Type     string            `json:"type"`
	Title    string            `json:"title"`
	Status   int               `json:"status"`
	Detail   string            `json:"detail"`
	Instance string            `json:"instance"`
	Code     string            `json:"code" enum:"secret_already_issued"`
	Token    chatTokenResponse `json:"token"`
}

func (problem *ChatTokenReplayProblem) Error() string  { return problem.Detail }
func (problem *ChatTokenReplayProblem) GetStatus() int { return problem.Status }
func (*ChatTokenReplayProblem) ContentType(contentType string) string {
	if contentType == "application/json" {
		return "application/problem+json"
	}
	return contentType
}

func normalizeChatTokenCreateOpenAPI(api huma.API) {
	item := api.OpenAPI().Paths[chatTokensPath]
	if item == nil || item.Post == nil {
		return
	}
	item.Post.Responses["409"] = &huma.Response{
		Description: "The idempotency key was replayed after secret issuance or conflicts with a different request.",
		Content: map[string]*huma.MediaType{
			"application/problem+json": {
				Schema: &huma.Schema{OneOf: []*huma.Schema{
					api.OpenAPI().Components.Schemas.Schema(
						reflect.TypeFor[ChatTokenReplayProblem](), true, "ChatTokenReplayProblem",
					),
					{Ref: "#/components/schemas/ErrorModel"},
				}},
			},
		},
	}
}

func normalizeAgentMutationOpenAPI(api huma.API) {
	for _, operation := range []*huma.Operation{
		api.OpenAPI().Paths[agentsPath+"/{agent_id}/configuration"].Put,
		api.OpenAPI().Paths[agentsPath+"/{agent_id}/lifecycle"].Patch,
	} {
		operation.Responses["409"] = &huma.Response{
			Description: "The candidate is not ready, the resource is stale, or the idempotency key conflicts.",
			Content: map[string]*huma.MediaType{
				"application/problem+json": {
					Schema: &huma.Schema{OneOf: []*huma.Schema{
						api.OpenAPI().Components.Schemas.Schema(
							reflect.TypeFor[AgentCandidateNotReadyProblem](), true, "AgentCandidateNotReadyProblem",
						),
						{Ref: "#/components/schemas/ErrorModel"},
					}},
				},
			},
		}
	}
}

func agentConfiguration(value agentConfigurationRequest) (agents.Configuration, bool) {
	profileID, err := agents.ParseID(value.ModelProfileID)
	if err != nil {
		return agents.Configuration{}, false
	}
	knowledgeBaseIDs := make([]agents.KnowledgeBaseID, len(value.KnowledgeBaseIDs))
	for index, raw := range value.KnowledgeBaseIDs {
		id, parseErr := agents.ParseID(raw)
		if parseErr != nil {
			return agents.Configuration{}, false
		}
		knowledgeBaseIDs[index] = agents.KnowledgeBaseID(id)
	}
	reasoning, ok := agentReasoning(value.ReasoningEffort)
	if !ok {
		return agents.Configuration{}, false
	}
	mode, ok := agentAnswerMode(value.AnswerMode)
	if !ok {
		return agents.Configuration{}, false
	}
	evidence, ok := agentEvidenceAccess(value.EvidenceAccess)
	if !ok {
		return agents.Configuration{}, false
	}
	return agents.Configuration{
		DisplayName: value.DisplayName, Description: value.Description, ResponseLanguage: value.ResponseLanguage,
		IdentityInstructions: value.IdentityInstructions, ModelProfileID: agents.ModelProfileID(profileID),
		ReasoningEffort: reasoning, AnswerMode: mode, BehavioralInstructions: value.BehavioralInstructions,
		EvidenceAccess: evidence, RefusalMarkdown: value.RefusalMarkdown, MaxToolCalls: value.MaxToolCalls,
		MaxAnswerTokens: value.MaxAnswerTokens, KnowledgeBaseIDs: knowledgeBaseIDs,
	}, true
}

func agentReasoning(value string) (agents.ReasoningEffort, bool) {
	switch value {
	case "none":
		return agents.ReasoningNone, true
	case "minimal":
		return agents.ReasoningMinimal, true
	case "low":
		return agents.ReasoningLow, true
	case "medium":
		return agents.ReasoningMedium, true
	case "high":
		return agents.ReasoningHigh, true
	case "max":
		return agents.ReasoningMax, true
	default:
		return "", false
	}
}

func agentAnswerMode(value string) (agents.AnswerMode, bool) {
	switch value {
	case "tool_calling":
		return agents.ToolCalling, true
	case "single_pass":
		return agents.SinglePass, true
	default:
		return "", false
	}
}

func agentEvidenceAccess(value string) (agents.EvidenceAccess, bool) {
	switch value {
	case "wiki_only":
		return agents.WikiOnly, true
	case "wiki_and_source":
		return agents.WikiAndSource, true
	default:
		return "", false
	}
}

func agentLifecycle(value string) (agents.Lifecycle, bool) {
	switch value {
	case "active":
		return agents.Active, true
	case "archived":
		return agents.Archived, true
	default:
		return "", false
	}
}

func newAgentResponse(value agents.Agent) agentResponse {
	return agentResponse{
		ID: value.ID.String(), Key: value.Key, Selector: value.Selector(),
		Lifecycle: strings.ToLower(string(value.Lifecycle)), CurrentVersionID: value.CurrentVersionID.String(),
		CurrentVersion: newAgentVersionResponse(value.CurrentVersion), Version: value.Version,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, ActivatedAt: value.ActivatedAt, ArchivedAt: value.ArchivedAt,
	}
}

func newAgentVersionResponse(value agents.Version) agentVersionResponse {
	return agentVersionResponse{
		ID: value.ID.String(), AgentID: value.AgentID.String(), VersionNumber: value.VersionNumber,
		Configuration:     newAgentConfigurationResponse(value.Configuration),
		CreatedByOperator: agents.ID(value.CreatedByOperator).String(), CreatedAt: value.CreatedAt,
	}
}

func newAgentConfigurationResponse(value agents.Configuration) agentConfigurationResponse {
	ids := make([]string, len(value.KnowledgeBaseIDs))
	for index, id := range value.KnowledgeBaseIDs {
		ids[index] = id.String()
	}
	return agentConfigurationResponse{
		DisplayName: value.DisplayName, Description: value.Description, ResponseLanguage: value.ResponseLanguage,
		IdentityInstructions: value.IdentityInstructions, ModelProfileID: value.ModelProfileID.String(),
		ReasoningEffort: strings.ToLower(string(value.ReasoningEffort)), AnswerMode: strings.ToLower(string(value.AnswerMode)),
		BehavioralInstructions: value.BehavioralInstructions, EvidenceAccess: strings.ToLower(string(value.EvidenceAccess)),
		RefusalMarkdown: value.RefusalMarkdown, MaxToolCalls: value.MaxToolCalls, MaxAnswerTokens: value.MaxAnswerTokens,
		KnowledgeBaseIDs: ids,
	}
}

func newAgentReadinessResponse(value agents.Readiness) agentReadinessResponse {
	result := agentReadinessResponse{
		Ready: value.Ready, EffectiveAccess: strings.ToLower(string(value.EffectiveAccess)),
		ModelProfileVersionNumber:    value.ModelProfileVersionNumber,
		EndpointConfigurationVersion: value.EndpointConfigurationVersion,
		Issues:                       make([]readinessIssueResponse, len(value.Issues)),
	}
	if value.ModelProfileVersionID != nil {
		text := value.ModelProfileVersionID.String()
		result.ModelProfileVersionID = &text
	}
	if value.ProviderEndpointID != nil {
		text := value.ProviderEndpointID.String()
		result.ProviderEndpointID = &text
	}
	for index, issue := range value.Issues {
		result.Issues[index].Code = strings.ToLower(string(issue.Code))
		if issue.KnowledgeBaseID != nil {
			text := issue.KnowledgeBaseID.String()
			result.Issues[index].KnowledgeBaseID = &text
		}
	}
	return result
}

func resolveTokenScopes(
	ctx context.Context,
	agentCatalog agentScopeService,
	ids []agents.AgentID,
) (map[agents.AgentID]agents.ScopeDescription, error) {
	values, err := agentCatalog.DescribeScopes(ctx, ids)
	if err != nil {
		return nil, err
	}
	wanted := make(map[agents.AgentID]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	result := make(map[agents.AgentID]agents.ScopeDescription, len(values))
	for _, value := range values {
		_, expected := wanted[value.AgentID]
		_, duplicate := result[value.AgentID]
		if !expected || duplicate || value.AgentKey == "" || len(value.KnowledgeBaseIDs) == 0 ||
			(value.EffectiveAccess != agents.Public && value.EffectiveAccess != agents.Restricted) {
			return nil, errors.New("Agent scope metadata is invalid")
		}
		result[value.AgentID] = value
	}
	if len(result) != len(wanted) {
		return nil, errors.New("Agent scope metadata is incomplete")
	}
	return result, nil
}

func newChatTokenResponse(
	value chattokens.Token,
	descriptions map[agents.AgentID]agents.ScopeDescription,
) chatTokenResponse {
	agentIDs := canonicalAgentIDs(value.AgentIDs)
	responseAgentIDs, responseScopes := newChatTokenScopeResponses(agentIDs, descriptions)
	result := chatTokenResponse{
		ID: value.ID.String(), Prefix: value.Prefix, Label: value.Label,
		AgentIDs: responseAgentIDs, AgentScopes: responseScopes,
		CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt, RevokedAt: value.RevokedAt, LastUsedAt: value.LastUsedAt,
	}
	return result
}

func newChatTokenSummaryResponse(value chattokens.Summary) chatTokenSummaryResponse {
	return chatTokenSummaryResponse{
		ID: value.ID.String(), Prefix: value.Prefix, Label: value.Label, AgentCount: value.AgentCount,
		CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt,
		RevokedAt: value.RevokedAt, LastUsedAt: value.LastUsedAt,
	}
}

func newPreviewChatTokenScopesResponse(
	agentIDs []agents.AgentID,
	descriptions map[agents.AgentID]agents.ScopeDescription,
) previewChatTokenScopesResponse {
	responseAgentIDs, scopes := newChatTokenScopeResponses(agentIDs, descriptions)
	knowledgeBaseSet := make(map[agents.KnowledgeBaseID]struct{})
	effectiveAccess := agents.Public
	ready := true
	for _, id := range agentIDs {
		description := descriptions[id]
		if description.EffectiveAccess == agents.Restricted {
			effectiveAccess = agents.Restricted
		}
		if !description.Ready {
			ready = false
		}
		for _, knowledgeBaseID := range description.KnowledgeBaseIDs {
			knowledgeBaseSet[knowledgeBaseID] = struct{}{}
		}
	}
	knowledgeBaseIDs := make([]agents.KnowledgeBaseID, 0, len(knowledgeBaseSet))
	for knowledgeBaseID := range knowledgeBaseSet {
		knowledgeBaseIDs = append(knowledgeBaseIDs, knowledgeBaseID)
	}
	sort.Slice(knowledgeBaseIDs, func(left, right int) bool {
		return knowledgeBaseIDs[left].String() < knowledgeBaseIDs[right].String()
	})
	responseKnowledgeBaseIDs := make([]string, len(knowledgeBaseIDs))
	for index, knowledgeBaseID := range knowledgeBaseIDs {
		responseKnowledgeBaseIDs[index] = knowledgeBaseID.String()
	}
	return previewChatTokenScopesResponse{
		AgentIDs: responseAgentIDs, AgentScopes: scopes, KnowledgeBaseIDs: responseKnowledgeBaseIDs,
		EffectiveAccess: strings.ToLower(string(effectiveAccess)), Ready: ready,
	}
}

func newChatTokenScopeResponses(
	agentIDs []agents.AgentID,
	descriptions map[agents.AgentID]agents.ScopeDescription,
) ([]string, []chatTokenAgentScopeResponse) {
	responseAgentIDs := make([]string, len(agentIDs))
	scopes := make([]chatTokenAgentScopeResponse, len(agentIDs))
	for index, id := range agentIDs {
		responseAgentIDs[index] = id.String()
		scopes[index] = newChatTokenAgentScopeResponse(descriptions[id])
	}
	return responseAgentIDs, scopes
}

func newChatTokenAgentScopeResponse(description agents.ScopeDescription) chatTokenAgentScopeResponse {
	knowledgeBaseIDs := make([]string, len(description.KnowledgeBaseIDs))
	for index, knowledgeBaseID := range description.KnowledgeBaseIDs {
		knowledgeBaseIDs[index] = knowledgeBaseID.String()
	}
	return chatTokenAgentScopeResponse{
		AgentID: description.AgentID.String(), AgentKey: description.AgentKey,
		KnowledgeBaseIDs: knowledgeBaseIDs,
		EffectiveAccess:  strings.ToLower(string(description.EffectiveAccess)), Ready: description.Ready,
	}
}

func canonicalAgentIDs(values []agents.AgentID) []agents.AgentID {
	result := append([]agents.AgentID(nil), values...)
	sortAgentIDs(result)
	return result
}

func sortAgentIDs(values []agents.AgentID) {
	sort.Slice(values, func(left, right int) bool { return values[left].String() < values[right].String() })
}

func newIssuedChatTokenResponse(value chatTokenResponse, secret string) issuedChatTokenResponse {
	return issuedChatTokenResponse{
		ID: value.ID, Prefix: value.Prefix, Label: value.Label, AgentIDs: value.AgentIDs, AgentScopes: value.AgentScopes,
		CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt, RevokedAt: value.RevokedAt, LastUsedAt: value.LastUsedAt,
		Secret: secret,
	}
}

func newRunSummaryResponse(value agents.RunSummary) runSummaryResponse {
	return runSummaryResponse{
		ID: value.ID.String(), AgentID: value.AgentID.String(), AgentVersionID: value.AgentVersionID.String(),
		AgentResourceVersion: value.AgentResourceVersion, AgentVersionNumber: value.AgentVersionNumber,
		Origin: strings.ToLower(string(value.Origin)), Subject: value.Subject, Outcome: strings.ToLower(string(value.Outcome)),
		Usage: value.Usage, LatencyMS: value.LatencyMS, CreatedAt: value.CreatedAt, CompletedAt: value.CompletedAt,
	}
}

func newRunDetailResponse(value agents.RunDetail) runDetailResponse {
	summary := newRunSummaryResponse(value.RunSummary)
	result := runDetailResponse{
		ID: summary.ID, AgentID: summary.AgentID, AgentVersionID: summary.AgentVersionID,
		AgentResourceVersion: summary.AgentResourceVersion, AgentVersionNumber: summary.AgentVersionNumber,
		Origin: summary.Origin, Subject: summary.Subject, Outcome: summary.Outcome, Usage: summary.Usage,
		LatencyMS: summary.LatencyMS, CreatedAt: summary.CreatedAt, CompletedAt: summary.CompletedAt,
		ModelProfileID: value.ModelProfileID.String(), ModelProfileVersionID: value.ModelProfileVersionID.String(),
		ModelProfileVersionNumber: value.ModelProfileVersionNumber, ProviderEndpointID: value.ProviderEndpointID.String(),
		CapturedEndpointConfigurationVersion: value.CapturedEndpointConfigurationVersion,
		CapturedCredentialID:                 agentOptionalCredentialID(value.CapturedCredentialID),
		CapturedCredentialVersion:            value.CapturedCredentialVersion, EffectiveAccess: strings.ToLower(string(value.EffectiveAccess)),
		ToolCalls: value.ToolCalls, Citations: value.Citations, SanitizedError: value.SanitizedError,
		KnowledgeBases: make([]runKnowledgeBaseResponse, len(value.KnowledgeBases)),
	}
	for index, knowledgeBase := range value.KnowledgeBases {
		revisions := make([]string, len(knowledgeBase.SourceRevisionIDs))
		for revisionIndex, revision := range knowledgeBase.SourceRevisionIDs {
			revisions[revisionIndex] = revision.String()
		}
		result.KnowledgeBases[index] = runKnowledgeBaseResponse{
			Position: knowledgeBase.Position, KnowledgeBaseID: knowledgeBase.KnowledgeBaseID.String(),
			KnowledgeBaseVersion: knowledgeBase.KnowledgeBaseVersion,
			AccessPolicy:         strings.ToLower(string(knowledgeBase.AccessPolicy)), WikiVersionID: knowledgeBase.WikiVersionID.String(),
			DocumentationRunID: knowledgeBase.DocumentationRunID.String(), SourceRevisionIDs: revisions,
			SourceScopeDigest: hex.EncodeToString(knowledgeBase.SourceScopeDigest[:]),
		}
	}
	return result
}

func agentOptionalCredentialID(value *agents.CredentialID) *string {
	if value == nil {
		return nil
	}
	encoded := value.String()
	return &encoded
}

func parseAgentID(raw string) (agents.AgentID, error) {
	id, err := agents.ParseID(raw)
	return agents.AgentID(id), err
}

func parseVersionID(raw string) (agents.VersionID, error) {
	id, err := agents.ParseID(raw)
	return agents.VersionID(id), err
}

func parseRunID(raw string) (agents.RunID, error) {
	id, err := agents.ParseID(raw)
	return agents.RunID(id), err
}

func parseAgentIDs(raw []string) ([]agents.AgentID, bool) {
	if len(raw) == 0 || len(raw) > chattokens.MaxAgentScopesPerToken {
		return nil, false
	}
	result := make([]agents.AgentID, len(raw))
	seen := make(map[agents.AgentID]struct{}, len(raw))
	for index, value := range raw {
		id, err := parseAgentID(value)
		if err != nil || id == (agents.AgentID{}) {
			return nil, false
		}
		if _, exists := seen[id]; exists {
			return nil, false
		}
		seen[id] = struct{}{}
		result[index] = id
	}
	return result, true
}

func replaceAgentPath(pattern, agentID, versionID string) string {
	result := strings.Replace(pattern, "{agent_id}", agentID, 1)
	if versionID != "" {
		result = strings.Replace(result, "{version_id}", versionID, 1)
	}
	return result
}

type cursorEnvelope struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type agentVersionCursorEnvelope struct {
	VersionNumber int32  `json:"version_number"`
	ID            string `json:"id"`
}

func encodeAgentCursor(value agents.PageCursor) string {
	return encodeCursor(value.CreatedAt, value.AgentID.String())
}

func decodeAgentCursor(raw *string) (*agents.PageCursor, error) {
	value, err := decodeCursor(raw)
	if value == nil || err != nil {
		return nil, err
	}
	id, err := parseAgentID(value.ID)
	if err != nil {
		return nil, err
	}
	return &agents.PageCursor{CreatedAt: value.CreatedAt, AgentID: id}, nil
}

func encodeAgentVersionCursor(value agents.VersionPageCursor) string {
	encoded, _ := json.Marshal(agentVersionCursorEnvelope{VersionNumber: value.VersionNumber, ID: value.VersionID.String()})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeAgentVersionCursor(raw *string) (*agents.VersionPageCursor, error) {
	if raw == nil {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*raw)
	if err != nil || len(decoded) > 1024 {
		return nil, errors.New("cursor is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var value agentVersionCursorEnvelope
	if err = decoder.Decode(&value); err != nil || value.VersionNumber <= 0 || value.ID == "" {
		return nil, errors.New("cursor is invalid")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("cursor is invalid")
	}
	id, err := parseVersionID(value.ID)
	if err != nil {
		return nil, err
	}
	return &agents.VersionPageCursor{VersionNumber: value.VersionNumber, VersionID: id}, nil
}

func encodeRunCursor(value agents.RunPageCursor) string {
	return encodeCursor(value.CreatedAt, value.RunID.String())
}

func decodeRunCursor(raw *string) (*agents.RunPageCursor, error) {
	value, err := decodeCursor(raw)
	if value == nil || err != nil {
		return nil, err
	}
	id, err := parseRunID(value.ID)
	if err != nil {
		return nil, err
	}
	return &agents.RunPageCursor{CreatedAt: value.CreatedAt, RunID: id}, nil
}

func encodeChatTokenCursor(value chattokens.PageCursor) string {
	return encodeCursor(value.CreatedAt, value.TokenID.String())
}

func decodeChatTokenCursor(raw *string) (*chattokens.PageCursor, error) {
	value, err := decodeCursor(raw)
	if value == nil || err != nil {
		return nil, err
	}
	id, err := chattokens.ParseID(value.ID)
	if err != nil {
		return nil, err
	}
	return &chattokens.PageCursor{CreatedAt: value.CreatedAt, TokenID: id}, nil
}

func encodeCursor(createdAt time.Time, id string) string {
	encoded, _ := json.Marshal(cursorEnvelope{CreatedAt: createdAt, ID: id})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(raw *string) (*cursorEnvelope, error) {
	if raw == nil {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*raw)
	if err != nil || len(decoded) > 1024 {
		return nil, errors.New("cursor is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var value cursorEnvelope
	if err = decoder.Decode(&value); err != nil || value.CreatedAt.IsZero() || value.ID == "" {
		return nil, errors.New("cursor is invalid")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("cursor is invalid")
	}
	return &value, nil
}

func agentProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, agents.ErrInvalid):
		return validationProblem(instance)
	case errors.Is(err, agents.ErrNotFound):
		problem.Title, problem.Status, problem.Detail = "Not Found", http.StatusNotFound, "Agent resource not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Idempotency key conflicts with a different request."
	case errors.Is(err, agents.ErrConflict), errors.Is(err, agents.ErrNotReady):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Agent state conflicts with the request."
	default:
		problem.Title, problem.Status, problem.Detail = "Internal Server Error", http.StatusInternalServerError, "The request could not be completed."
	}
	return problem
}

func chatTokenProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, chattokens.ErrInvalid):
		return validationProblem(instance)
	case errors.Is(err, chattokens.ErrNotFound), errors.Is(err, agents.ErrNotFound):
		problem.Title, problem.Status, problem.Detail = "Not Found", http.StatusNotFound, "Chat access token resource not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Idempotency key conflicts with a different request."
	case errors.Is(err, chattokens.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Chat access token state conflicts with the request."
	default:
		problem.Title, problem.Status, problem.Detail = "Internal Server Error", http.StatusInternalServerError, "The request could not be completed."
	}
	return problem
}
