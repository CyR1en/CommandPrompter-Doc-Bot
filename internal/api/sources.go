package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	sourcedomain "github.com/cyr1en/ref0/internal/sources"
	"github.com/danielgtaylor/huma/v2"
)

const sourcesPath = "/api/v1/sources"

const sourceBodyLimit = 1 << 20

type SourceService interface {
	List(context.Context, *sourcedomain.ID) ([]sourcedomain.Source, error)
	Get(context.Context, sourcedomain.ID) (sourcedomain.Source, error)
	CreateRepository(context.Context, sourcedomain.CreateRepository, sourcedomain.ID, string) (sourcedomain.Created, error)
	CreateWebsite(context.Context, sourcedomain.CreateWebsite, sourcedomain.ID, string) (sourcedomain.Created, error)
	UpdateRepository(context.Context, sourcedomain.UpdateRepository, sourcedomain.ID, string) (sourcedomain.Source, error)
	UpdateWebsite(context.Context, sourcedomain.UpdateWebsite, sourcedomain.ID, string) (sourcedomain.Source, error)
	ChangeLifecycle(context.Context, sourcedomain.ChangeLifecycle, sourcedomain.ID, string) (sourcedomain.Source, error)
	RequestValidation(context.Context, sourcedomain.RequestOperation, sourcedomain.ID, string) (sourcedomain.Sync, error)
	RequestSync(context.Context, sourcedomain.RequestOperation, sourcedomain.ID, string) (sourcedomain.Sync, error)
	ListRevisions(context.Context, sourcedomain.ID) ([]sourcedomain.Revision, error)
	ListSyncs(context.Context, sourcedomain.ID) ([]sourcedomain.Sync, error)
}

var _ SourceService = (*sourcedomain.Store)(nil)

type SourceRepositoryConfigurationRequest struct {
	Name                string   `json:"name" minLength:"1" maxLength:"255"`
	Privacy             string   `json:"privacy" enum:"public,private"`
	RemoteURL           string   `json:"remote_url" minLength:"1" maxLength:"2048"`
	RefKind             string   `json:"ref_kind" enum:"branch,commit"`
	RefValue            string   `json:"ref_value" minLength:"1" maxLength:"512"`
	CredentialUsername  *string  `json:"credential_username,omitempty" minLength:"1" maxLength:"255" nullable:"true"`
	CredentialID        *string  `json:"credential_id,omitempty" format:"uuid" nullable:"true"`
	IncludePatterns     []string `json:"include_patterns,omitempty" default:"[]" maxItems:"100"`
	ExcludePatterns     []string `json:"exclude_patterns,omitempty" default:"[]" maxItems:"100"`
	PollIntervalSeconds *int     `json:"poll_interval_seconds,omitempty" minimum:"60" maximum:"604800" nullable:"true"`
}

type createRepositorySourceRequest struct {
	Name                string   `json:"name" minLength:"1" maxLength:"255"`
	Privacy             string   `json:"privacy" enum:"public,private"`
	RemoteURL           string   `json:"remote_url" minLength:"1" maxLength:"2048"`
	RefKind             string   `json:"ref_kind" enum:"branch,commit"`
	RefValue            string   `json:"ref_value" minLength:"1" maxLength:"512"`
	CredentialUsername  *string  `json:"credential_username,omitempty" minLength:"1" maxLength:"255" nullable:"true"`
	CredentialID        *string  `json:"credential_id,omitempty" format:"uuid" nullable:"true"`
	IncludePatterns     []string `json:"include_patterns,omitempty" default:"[]" maxItems:"100"`
	ExcludePatterns     []string `json:"exclude_patterns,omitempty" default:"[]" maxItems:"100"`
	PollIntervalSeconds *int     `json:"poll_interval_seconds,omitempty" minimum:"60" maximum:"604800" nullable:"true"`
	KnowledgeBaseID     string   `json:"knowledge_base_id" format:"uuid"`
}

type updateRepositorySourceRequest struct {
	Name                string   `json:"name" minLength:"1" maxLength:"255"`
	Privacy             string   `json:"privacy" enum:"public,private"`
	RemoteURL           string   `json:"remote_url" minLength:"1" maxLength:"2048"`
	RefKind             string   `json:"ref_kind" enum:"branch,commit"`
	RefValue            string   `json:"ref_value" minLength:"1" maxLength:"512"`
	CredentialUsername  *string  `json:"credential_username,omitempty" minLength:"1" maxLength:"255" nullable:"true"`
	CredentialID        *string  `json:"credential_id,omitempty" format:"uuid" nullable:"true"`
	IncludePatterns     []string `json:"include_patterns,omitempty" default:"[]" maxItems:"100"`
	ExcludePatterns     []string `json:"exclude_patterns,omitempty" default:"[]" maxItems:"100"`
	PollIntervalSeconds *int     `json:"poll_interval_seconds,omitempty" minimum:"60" maximum:"604800" nullable:"true"`
	ExpectedVersion     int      `json:"expected_version" minimum:"1"`
}

func (request createRepositorySourceRequest) configuration() SourceRepositoryConfigurationRequest {
	return SourceRepositoryConfigurationRequest{
		Name: request.Name, Privacy: request.Privacy, RemoteURL: request.RemoteURL,
		RefKind: request.RefKind, RefValue: request.RefValue,
		CredentialUsername: request.CredentialUsername, CredentialID: request.CredentialID,
		IncludePatterns: request.IncludePatterns, ExcludePatterns: request.ExcludePatterns,
		PollIntervalSeconds: request.PollIntervalSeconds,
	}
}

func (request updateRepositorySourceRequest) configuration() SourceRepositoryConfigurationRequest {
	return SourceRepositoryConfigurationRequest{
		Name: request.Name, Privacy: request.Privacy, RemoteURL: request.RemoteURL,
		RefKind: request.RefKind, RefValue: request.RefValue,
		CredentialUsername: request.CredentialUsername, CredentialID: request.CredentialID,
		IncludePatterns: request.IncludePatterns, ExcludePatterns: request.ExcludePatterns,
		PollIntervalSeconds: request.PollIntervalSeconds,
	}
}

type SourceWebsiteConfigurationRequest struct {
	Name                 string  `json:"name" minLength:"1" maxLength:"255"`
	Privacy              string  `json:"privacy" enum:"public,private"`
	RootURL              string  `json:"root_url" minLength:"1" maxLength:"2048"`
	CredentialHeader     *string `json:"credential_header,omitempty" minLength:"1" maxLength:"127" nullable:"true"`
	CredentialPrefix     *string `json:"credential_prefix,omitempty" maxLength:"128" nullable:"true"`
	CredentialID         *string `json:"credential_id,omitempty" format:"uuid" nullable:"true"`
	MaxConcurrency       int     `json:"max_concurrency,omitempty" default:"4" minimum:"1" maximum:"16"`
	RequestsPerSecond    int     `json:"requests_per_second,omitempty" default:"4" minimum:"1" maximum:"100"`
	MaxPages             int     `json:"max_pages,omitempty" default:"500" minimum:"1" maximum:"10000"`
	MaxPageBytes         int     `json:"max_page_bytes,omitempty" default:"2097152" minimum:"1024" maximum:"10485760"`
	MaxTotalBytes        int64   `json:"max_total_bytes,omitempty" default:"104857600" minimum:"1024" maximum:"1073741824"`
	MaxDepth             int     `json:"max_depth,omitempty" default:"3" minimum:"0" maximum:"10"`
	PollIntervalSeconds  *int    `json:"poll_interval_seconds,omitempty" minimum:"60" maximum:"604800" nullable:"true"`
	AcquisitionMode      string  `json:"acquisition_mode,omitempty" default:"builtin_crawl" enum:"builtin_crawl,tinyfish_crawl,direct_json_api"`
	TinyFishCredentialID *string `json:"tinyfish_credential_id,omitempty" format:"uuid" nullable:"true"`
}

type createWebsiteSourceRequest struct {
	Name                 string  `json:"name" minLength:"1" maxLength:"255"`
	Privacy              string  `json:"privacy" enum:"public,private"`
	RootURL              string  `json:"root_url" minLength:"1" maxLength:"2048"`
	CredentialHeader     *string `json:"credential_header,omitempty" minLength:"1" maxLength:"127" nullable:"true"`
	CredentialPrefix     *string `json:"credential_prefix,omitempty" maxLength:"128" nullable:"true"`
	CredentialID         *string `json:"credential_id,omitempty" format:"uuid" nullable:"true"`
	MaxConcurrency       int     `json:"max_concurrency,omitempty" default:"4" minimum:"1" maximum:"16"`
	RequestsPerSecond    int     `json:"requests_per_second,omitempty" default:"4" minimum:"1" maximum:"100"`
	MaxPages             int     `json:"max_pages,omitempty" default:"500" minimum:"1" maximum:"10000"`
	MaxPageBytes         int     `json:"max_page_bytes,omitempty" default:"2097152" minimum:"1024" maximum:"10485760"`
	MaxTotalBytes        int64   `json:"max_total_bytes,omitempty" default:"104857600" minimum:"1024" maximum:"1073741824"`
	MaxDepth             int     `json:"max_depth,omitempty" default:"3" minimum:"0" maximum:"10"`
	PollIntervalSeconds  *int    `json:"poll_interval_seconds,omitempty" minimum:"60" maximum:"604800" nullable:"true"`
	AcquisitionMode      string  `json:"acquisition_mode,omitempty" default:"builtin_crawl" enum:"builtin_crawl,tinyfish_crawl,direct_json_api"`
	TinyFishCredentialID *string `json:"tinyfish_credential_id,omitempty" format:"uuid" nullable:"true"`
	KnowledgeBaseID      string  `json:"knowledge_base_id" format:"uuid"`
}

type updateWebsiteSourceRequest struct {
	Name                 string  `json:"name" minLength:"1" maxLength:"255"`
	Privacy              string  `json:"privacy" enum:"public,private"`
	RootURL              string  `json:"root_url" minLength:"1" maxLength:"2048"`
	CredentialHeader     *string `json:"credential_header,omitempty" minLength:"1" maxLength:"127" nullable:"true"`
	CredentialPrefix     *string `json:"credential_prefix,omitempty" maxLength:"128" nullable:"true"`
	CredentialID         *string `json:"credential_id,omitempty" format:"uuid" nullable:"true"`
	MaxConcurrency       int     `json:"max_concurrency,omitempty" default:"4" minimum:"1" maximum:"16"`
	RequestsPerSecond    int     `json:"requests_per_second,omitempty" default:"4" minimum:"1" maximum:"100"`
	MaxPages             int     `json:"max_pages,omitempty" default:"500" minimum:"1" maximum:"10000"`
	MaxPageBytes         int     `json:"max_page_bytes,omitempty" default:"2097152" minimum:"1024" maximum:"10485760"`
	MaxTotalBytes        int64   `json:"max_total_bytes,omitempty" default:"104857600" minimum:"1024" maximum:"1073741824"`
	MaxDepth             int     `json:"max_depth,omitempty" default:"3" minimum:"0" maximum:"10"`
	PollIntervalSeconds  *int    `json:"poll_interval_seconds,omitempty" minimum:"60" maximum:"604800" nullable:"true"`
	AcquisitionMode      string  `json:"acquisition_mode,omitempty" default:"builtin_crawl" enum:"builtin_crawl,tinyfish_crawl,direct_json_api"`
	TinyFishCredentialID *string `json:"tinyfish_credential_id,omitempty" format:"uuid" nullable:"true"`
	ExpectedVersion      int     `json:"expected_version" minimum:"1"`
}

func (request createWebsiteSourceRequest) configuration() SourceWebsiteConfigurationRequest {
	return SourceWebsiteConfigurationRequest{
		Name: request.Name, Privacy: request.Privacy, RootURL: request.RootURL,
		CredentialHeader: request.CredentialHeader, CredentialPrefix: request.CredentialPrefix,
		CredentialID: request.CredentialID, MaxConcurrency: request.MaxConcurrency,
		RequestsPerSecond: request.RequestsPerSecond, MaxPages: request.MaxPages,
		MaxPageBytes: request.MaxPageBytes, MaxTotalBytes: request.MaxTotalBytes, MaxDepth: request.MaxDepth,
		PollIntervalSeconds: request.PollIntervalSeconds, AcquisitionMode: request.AcquisitionMode,
		TinyFishCredentialID: request.TinyFishCredentialID,
	}
}

func (request updateWebsiteSourceRequest) configuration() SourceWebsiteConfigurationRequest {
	return SourceWebsiteConfigurationRequest{
		Name: request.Name, Privacy: request.Privacy, RootURL: request.RootURL,
		CredentialHeader: request.CredentialHeader, CredentialPrefix: request.CredentialPrefix,
		CredentialID: request.CredentialID, MaxConcurrency: request.MaxConcurrency,
		RequestsPerSecond: request.RequestsPerSecond, MaxPages: request.MaxPages,
		MaxPageBytes: request.MaxPageBytes, MaxTotalBytes: request.MaxTotalBytes, MaxDepth: request.MaxDepth,
		PollIntervalSeconds: request.PollIntervalSeconds, AcquisitionMode: request.AcquisitionMode,
		TinyFishCredentialID: request.TinyFishCredentialID,
	}
}

type sourceUpdateRequest struct {
	repository *updateRepositorySourceRequest
	website    *updateWebsiteSourceRequest
}

func (sourceUpdateRequest) Schema(registry huma.Registry) *huma.Schema {
	return &huma.Schema{AnyOf: []*huma.Schema{
		registry.Schema(reflect.TypeFor[updateRepositorySourceRequest](), true, "UpdateRepositorySourceRequest"),
		registry.Schema(reflect.TypeFor[updateWebsiteSourceRequest](), true, "UpdateWebsiteSourceRequest"),
	}}
}

func (request *sourceUpdateRequest) UnmarshalJSON(content []byte) error {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(content, &shape); err != nil {
		return err
	}
	_, repository := shape["remote_url"]
	_, website := shape["root_url"]
	if repository == website {
		return errors.New("source update shape is ambiguous")
	}
	if repository {
		var value updateRepositorySourceRequest
		if err := json.Unmarshal(content, &value); err != nil {
			return err
		}
		request.repository = &value
		return nil
	}
	defaults := sourcedomain.DefaultCrawlLimits()
	value := updateWebsiteSourceRequest{
		MaxConcurrency: defaults.Concurrency, RequestsPerSecond: defaults.RequestsPerSecond,
		MaxPages: defaults.MaxPages, MaxPageBytes: defaults.MaxPageBytes,
		MaxTotalBytes: defaults.MaxTotalBytes, MaxDepth: defaults.MaxDepth,
		AcquisitionMode: "builtin_crawl",
	}
	if err := json.Unmarshal(content, &value); err != nil {
		return err
	}
	request.website = &value
	return nil
}

type changeSourceLifecycleRequest struct {
	ExpectedVersion int    `json:"expected_version" minimum:"1"`
	Lifecycle       string `json:"lifecycle" enum:"active,disabled,removed"`
}

type requestSourceOperationRequest struct {
	ExpectedVersion int `json:"expected_version" minimum:"1"`
}

type sourceListInput struct {
	SessionCookie   string              `cookie:"ref0_session"`
	KnowledgeBaseID optionalStringParam `query:"knowledge_base_id" format:"uuid"`
}

type sourceReadInput struct {
	SessionCookie string `cookie:"ref0_session"`
	SourceID      string `path:"source_id" format:"uuid"`
}

type sourceCreateInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type sourceWriteInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	SourceID       string `path:"source_id" format:"uuid"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type SourceCommonResponse struct {
	ID                            string     `json:"id" format:"uuid"`
	KnowledgeBaseID               string     `json:"knowledge_base_id" format:"uuid"`
	Name                          string     `json:"name"`
	Privacy                       string     `json:"privacy" enum:"public,private"`
	PollIntervalSeconds           *int       `json:"poll_interval_seconds"`
	Lifecycle                     string     `json:"lifecycle" enum:"draft,active,disabled,removed"`
	Health                        string     `json:"health" enum:"unknown,healthy,unhealthy"`
	SanitizedError                *string    `json:"sanitized_error"`
	CheckedAt                     *time.Time `json:"checked_at"`
	CurrentRevisionID             *string    `json:"current_revision_id" format:"uuid"`
	Version                       int        `json:"version"`
	ConfigurationVersion          int        `json:"configuration_version"`
	ValidatedConfigurationVersion *int       `json:"validated_configuration_version"`
	CreatedAt                     time.Time  `json:"created_at"`
	UpdatedAt                     time.Time  `json:"updated_at"`
	DisabledAt                    *time.Time `json:"disabled_at"`
	RemovedAt                     *time.Time `json:"removed_at"`
}

type repositorySourceResponse struct {
	SourceCommonResponse
	Kind               string   `json:"kind" enum:"repository"`
	RemoteURL          string   `json:"remote_url"`
	RemoteHost         string   `json:"remote_host"`
	RepositoryPath     string   `json:"repository_path"`
	RefKind            string   `json:"ref_kind" enum:"branch,commit"`
	RefValue           string   `json:"ref_value"`
	CredentialUsername *string  `json:"credential_username"`
	CredentialID       *string  `json:"credential_id" format:"uuid"`
	IncludePatterns    []string `json:"include_patterns"`
	ExcludePatterns    []string `json:"exclude_patterns"`
}

type websiteSourceResponse struct {
	SourceCommonResponse
	Kind                 string  `json:"kind" enum:"website"`
	RootURL              string  `json:"root_url"`
	RootHost             string  `json:"root_host"`
	CredentialHeader     *string `json:"credential_header"`
	CredentialPrefix     *string `json:"credential_prefix"`
	CredentialID         *string `json:"credential_id" format:"uuid"`
	MaxConcurrency       int     `json:"max_concurrency"`
	RequestsPerSecond    int     `json:"requests_per_second"`
	MaxPages             int     `json:"max_pages"`
	MaxPageBytes         int     `json:"max_page_bytes"`
	MaxTotalBytes        int64   `json:"max_total_bytes"`
	MaxDepth             int     `json:"max_depth"`
	AcquisitionMode      string  `json:"acquisition_mode" enum:"builtin_crawl,tinyfish_crawl,direct_json_api"`
	TinyFishCredentialID *string `json:"tinyfish_credential_id" format:"uuid"`
}

type sourceAPIResponse struct {
	Repository *repositorySourceResponse
	Website    *websiteSourceResponse
}

func (sourceAPIResponse) Schema(registry huma.Registry) *huma.Schema {
	return &huma.Schema{AnyOf: []*huma.Schema{
		registry.Schema(reflect.TypeFor[repositorySourceResponse](), true, "RepositorySourceResponse"),
		registry.Schema(reflect.TypeFor[websiteSourceResponse](), true, "WebsiteSourceResponse"),
	}}
}

func (response sourceAPIResponse) MarshalJSON() ([]byte, error) {
	if response.Repository != nil && response.Website == nil {
		return json.Marshal(response.Repository)
	}
	if response.Website != nil && response.Repository == nil {
		return json.Marshal(response.Website)
	}
	return nil, errors.New("source response kind is invalid")
}

type SourceSyncCommonResponse struct {
	ID                           string     `json:"id" format:"uuid"`
	SourceID                     string     `json:"source_id" format:"uuid"`
	JobID                        string     `json:"job_id" format:"uuid"`
	Kind                         string     `json:"kind" enum:"validation,sync"`
	RequestedByOperatorID        *string    `json:"requested_by_operator_id" format:"uuid"`
	CapturedSourceVersion        int        `json:"captured_source_version"`
	CapturedConfigurationVersion int        `json:"captured_configuration_version"`
	CapturedPrivacy              string     `json:"captured_privacy" enum:"public,private"`
	CapturedCredentialID         *string    `json:"captured_credential_id" format:"uuid"`
	CapturedCredentialVersion    *int       `json:"captured_credential_version"`
	CandidateRevisionID          *string    `json:"candidate_revision_id" format:"uuid"`
	Status                       string     `json:"status" enum:"pending,running,succeeded,failed,superseded"`
	ResultRevisionID             *string    `json:"result_revision_id" format:"uuid"`
	ResolvedNativeVersion        *string    `json:"resolved_native_version"`
	SanitizedError               *string    `json:"sanitized_error"`
	CreatedAt                    time.Time  `json:"created_at"`
	StartedAt                    *time.Time `json:"started_at"`
	CompletedAt                  *time.Time `json:"completed_at"`
}

type SourceSyncResponse struct {
	SourceSyncCommonResponse
	CapturedRemoteURL          string   `json:"captured_remote_url"`
	CapturedRefKind            string   `json:"captured_ref_kind" enum:"branch,commit"`
	CapturedRefValue           string   `json:"captured_ref_value"`
	CapturedCredentialUsername *string  `json:"captured_credential_username"`
	CapturedIncludePatterns    []string `json:"captured_include_patterns"`
	CapturedExcludePatterns    []string `json:"captured_exclude_patterns"`
}

type websiteSourceSyncResponse struct {
	SourceSyncCommonResponse
	CapturedRootURL                   string  `json:"captured_root_url"`
	CapturedCredentialHeader          *string `json:"captured_credential_header"`
	CapturedCredentialPrefix          *string `json:"captured_credential_prefix"`
	CapturedMaxConcurrency            int     `json:"captured_max_concurrency"`
	CapturedRequestsPerSecond         int     `json:"captured_requests_per_second"`
	CapturedMaxPages                  int     `json:"captured_max_pages"`
	CapturedMaxPageBytes              int     `json:"captured_max_page_bytes"`
	CapturedMaxTotalBytes             int64   `json:"captured_max_total_bytes"`
	CapturedMaxDepth                  int     `json:"captured_max_depth"`
	CapturedAcquisitionMode           string  `json:"captured_acquisition_mode" enum:"builtin_crawl,tinyfish_crawl,direct_json_api"`
	CapturedTinyFishCredentialID      *string `json:"captured_tinyfish_credential_id" format:"uuid"`
	CapturedTinyFishCredentialVersion *int    `json:"captured_tinyfish_credential_version"`
	CapturedPreviousRevisionID        *string `json:"captured_previous_revision_id" format:"uuid"`
}

type sourceSyncAPIResponse struct {
	Repository *SourceSyncResponse
	Website    *websiteSourceSyncResponse
}

func (sourceSyncAPIResponse) Schema(registry huma.Registry) *huma.Schema {
	return &huma.Schema{AnyOf: []*huma.Schema{
		registry.Schema(reflect.TypeFor[SourceSyncResponse](), true, "SourceSyncResponse"),
		registry.Schema(reflect.TypeFor[websiteSourceSyncResponse](), true, "WebsiteSourceSyncResponse"),
	}}
}

func (response sourceSyncAPIResponse) MarshalJSON() ([]byte, error) {
	if response.Repository != nil && response.Website == nil {
		return json.Marshal(response.Repository)
	}
	if response.Website != nil && response.Repository == nil {
		return json.Marshal(response.Website)
	}
	return nil, errors.New("source sync response kind is invalid")
}

type websiteRevisionPageResponse struct {
	CanonicalURL         string  `json:"canonical_url"`
	ContentPath          string  `json:"content_path"`
	ContentSHA256        string  `json:"content_sha256"`
	EvidenceURI          string  `json:"evidence_uri"`
	Freshness            string  `json:"freshness" enum:"fresh,reused"`
	ETag                 *string `json:"etag"`
	LastModified         *string `json:"last_modified"`
	ReusedFromRevisionID *string `json:"reused_from_revision_id" format:"uuid"`
}

type sourceRevisionResponse struct {
	ID              string                        `json:"id" format:"uuid"`
	SourceID        string                        `json:"source_id" format:"uuid"`
	ObservedRefKind string                        `json:"observed_ref_kind" enum:"branch,commit,root"`
	ObservedRef     string                        `json:"observed_ref"`
	NativeVersion   string                        `json:"native_version"`
	Fingerprint     string                        `json:"fingerprint"`
	ArtifactKey     string                        `json:"artifact_key"`
	FileCount       int                           `json:"file_count"`
	ByteCount       int64                         `json:"byte_count"`
	IgnoredPaths    []string                      `json:"ignored_paths"`
	CreatedAt       time.Time                     `json:"created_at"`
	WebsitePages    []websiteRevisionPageResponse `json:"website_pages"`
}

type sourceCreatedResponse struct {
	Source     sourceAPIResponse     `json:"source"`
	Validation sourceSyncAPIResponse `json:"validation"`
}

type sourceListOutput struct {
	Body []sourceAPIResponse `nullable:"false"`
}
type sourceOutput struct{ Body sourceAPIResponse }
type sourceCreatedOutput struct {
	Status int
	Body   sourceCreatedResponse
}
type sourceSyncOutput struct {
	Status int
	Body   sourceSyncAPIResponse
}
type sourceSyncsOutput struct {
	Body []sourceSyncAPIResponse `nullable:"false"`
}
type sourceRevisionsOutput struct {
	Body []sourceRevisionResponse `nullable:"false"`
}

func RegisterSourceRoutes(api huma.API, sessions auth.SessionService, service SourceService) {
	registerSourceList(api, sessions, service)
	registerSourceCreateRepository(api, sessions, service)
	registerSourceCreateWebsite(api, sessions, service)
	registerSourceGetAndUpdate(api, sessions, service)
	registerSourceLifecycle(api, sessions, service)
	registerSourceOperation(api, sessions, service, true)
	registerSourceOperation(api, sessions, service, false)
	registerSourceRevisions(api, sessions, service)
	registerSourceSyncs(api, sessions, service)
	normalizeSourceOpenAPISchemas(api)
}

func registerSourceList(api huma.API, sessions auth.SessionService, service SourceService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_sources_api_v1_sources_get", Method: http.MethodGet,
		Path: sourcesPath, Summary: "List Sources", Tags: []string{"sources"},
		Errors: []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *sourceListInput) (*sourceListOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, sourcesPath); err != nil {
			return nil, err
		}
		var knowledgeBaseID *sourcedomain.ID
		if raw := input.KnowledgeBaseID.Pointer(); raw != nil {
			value, err := sourcedomain.ParseID(*raw)
			if err != nil {
				return nil, parameterValidationProblem(sourcesPath, "query")
			}
			knowledgeBaseID = &value
		}
		values, err := service.List(ctx, knowledgeBaseID)
		if err != nil {
			return nil, sourceProblem(sourcesPath, err)
		}
		result := &sourceListOutput{Body: make([]sourceAPIResponse, len(values))}
		for index, value := range values {
			result.Body[index] = sourceResponse(value)
		}
		return result, nil
	})
}

func registerSourceCreateRepository(api huma.API, sessions auth.SessionService, service SourceService) {
	const path = sourcesPath + "/repositories"
	huma.Register(api, huma.Operation{
		OperationID: "create_repository_source_api_v1_sources_repositories_post", Method: http.MethodPost,
		Path: path, Summary: "Create Repository Source", Tags: []string{"sources"}, DefaultStatus: http.StatusCreated,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true, MaxBodyBytes: sourceBodyLimit,
	}, func(ctx context.Context, input *sourceCreateInput) (*sourceCreatedOutput, error) {
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, path)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, path)
		if err != nil {
			return nil, err
		}
		body := createRepositorySourceRequest{IncludePatterns: []string{}, ExcludePatterns: []string{}}
		if !decodeSourceRequest(input.RawBody, input.ContentType, &body) || sourceFieldsContainNull(input.RawBody, "include_patterns", "exclude_patterns") {
			return nil, validationProblem(path)
		}
		knowledgeBaseID, err := sourcedomain.ParseID(body.KnowledgeBaseID)
		if err != nil {
			return nil, validationProblem(path)
		}
		configuration, err := repositorySourceConfiguration(body.configuration())
		if err != nil {
			return nil, sourceConfigurationProblem(path)
		}
		created, err := service.CreateRepository(ctx, sourcedomain.CreateRepository{KnowledgeBaseID: knowledgeBaseID, Configuration: configuration}, sourcedomain.ID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, sourceProblem(path, err)
		}
		return &sourceCreatedOutput{Status: http.StatusCreated, Body: createdSourceResponse(created)}, nil
	})
	documentJSONRequest(api, path, http.MethodPost, reflect.TypeFor[createRepositorySourceRequest](), "CreateRepositorySourceRequest")
}

func registerSourceCreateWebsite(api huma.API, sessions auth.SessionService, service SourceService) {
	const path = sourcesPath + "/websites"
	huma.Register(api, huma.Operation{
		OperationID: "create_website_source_api_v1_sources_websites_post", Method: http.MethodPost,
		Path: path, Summary: "Create Website Source", Tags: []string{"sources"}, DefaultStatus: http.StatusCreated,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true, MaxBodyBytes: sourceBodyLimit,
	}, func(ctx context.Context, input *sourceCreateInput) (*sourceCreatedOutput, error) {
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, path)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, path)
		if err != nil {
			return nil, err
		}
		body := defaultCreateWebsiteSourceRequest()
		if !decodeSourceRequest(input.RawBody, input.ContentType, &body) || sourceFieldsContainNull(input.RawBody,
			"max_concurrency", "requests_per_second", "max_pages", "max_page_bytes", "max_total_bytes", "max_depth", "acquisition_mode") {
			return nil, validationProblem(path)
		}
		knowledgeBaseID, err := sourcedomain.ParseID(body.KnowledgeBaseID)
		if err != nil {
			return nil, validationProblem(path)
		}
		configuration, err := websiteSourceConfiguration(body.configuration())
		if err != nil {
			return nil, sourceConfigurationProblem(path)
		}
		created, err := service.CreateWebsite(ctx, sourcedomain.CreateWebsite{KnowledgeBaseID: knowledgeBaseID, Configuration: configuration}, sourcedomain.ID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, sourceProblem(path, err)
		}
		return &sourceCreatedOutput{Status: http.StatusCreated, Body: createdSourceResponse(created)}, nil
	})
	documentJSONRequest(api, path, http.MethodPost, reflect.TypeFor[createWebsiteSourceRequest](), "CreateWebsiteSourceRequest")
}

func registerSourceGetAndUpdate(api huma.API, sessions auth.SessionService, service SourceService) {
	const path = sourcesPath + "/{source_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_source_api_v1_sources__source_id__get", Method: http.MethodGet,
		Path: path, Summary: "Get Source", Tags: []string{"sources"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *sourceReadInput) (*sourceOutput, error) {
		instance := strings.Replace(path, "{source_id}", input.SourceID, 1)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := sourceAPIID(input.SourceID, instance)
		if err != nil {
			return nil, err
		}
		value, err := service.Get(ctx, id)
		if err != nil {
			return nil, sourceProblem(instance, err)
		}
		return &sourceOutput{Body: sourceResponse(value)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update_source_api_v1_sources__source_id__patch", Method: http.MethodPatch,
		Path: path, Summary: "Update Source", Tags: []string{"sources"},
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true, MaxBodyBytes: sourceBodyLimit,
	}, func(ctx context.Context, input *sourceWriteInput) (*sourceOutput, error) {
		instance := strings.Replace(path, "{source_id}", input.SourceID, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, err := sourceAPIID(input.SourceID, instance)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		repository, website, ok := decodeSourceUpdate(input.RawBody, input.ContentType)
		if !ok {
			return nil, validationProblem(instance)
		}
		var value sourcedomain.Source
		if repository != nil {
			if repository.ExpectedVersion < 1 {
				return nil, validationProblem(instance)
			}
			configuration, configErr := repositorySourceConfiguration(repository.configuration())
			if configErr != nil {
				return nil, sourceConfigurationProblem(instance)
			}
			value, err = service.UpdateRepository(ctx, sourcedomain.UpdateRepository{SourceID: id, ExpectedVersion: repository.ExpectedVersion, Configuration: configuration}, sourcedomain.ID(session.Operator.ID), requestKey)
		} else if website != nil {
			if website.ExpectedVersion < 1 {
				return nil, validationProblem(instance)
			}
			configuration, configErr := websiteSourceConfiguration(website.configuration())
			if configErr != nil {
				return nil, sourceConfigurationProblem(instance)
			}
			value, err = service.UpdateWebsite(ctx, sourcedomain.UpdateWebsite{SourceID: id, ExpectedVersion: website.ExpectedVersion, Configuration: configuration}, sourcedomain.ID(session.Operator.ID), requestKey)
		} else {
			return nil, validationProblem(instance)
		}
		if err != nil {
			return nil, sourceProblem(instance, err)
		}
		return &sourceOutput{Body: sourceResponse(value)}, nil
	})
	documentJSONRequest(api, path, http.MethodPatch, reflect.TypeFor[sourceUpdateRequest](), "UpdateSourceRequest")
}

func registerSourceLifecycle(api huma.API, sessions auth.SessionService, service SourceService) {
	const path = sourcesPath + "/{source_id}/lifecycle"
	huma.Register(api, huma.Operation{
		OperationID: "change_source_lifecycle_api_v1_sources__source_id__lifecycle_post", Method: http.MethodPost,
		Path: path, Summary: "Change Source Lifecycle", Tags: []string{"sources"},
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true, MaxBodyBytes: sourceBodyLimit,
	}, func(ctx context.Context, input *sourceWriteInput) (*sourceOutput, error) {
		instance := strings.Replace(path, "{source_id}", input.SourceID, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, err := sourceAPIID(input.SourceID, instance)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		var body changeSourceLifecycleRequest
		if !decodeSourceRequest(input.RawBody, input.ContentType, &body) || body.ExpectedVersion < 1 {
			return nil, validationProblem(instance)
		}
		lifecycle, ok := sourceLifecycle(body.Lifecycle)
		if !ok {
			return nil, validationProblem(instance)
		}
		value, err := service.ChangeLifecycle(ctx, sourcedomain.ChangeLifecycle{SourceID: id, ExpectedVersion: body.ExpectedVersion, Lifecycle: lifecycle}, sourcedomain.ID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, sourceProblem(instance, err)
		}
		return &sourceOutput{Body: sourceResponse(value)}, nil
	})
	documentJSONRequest(api, path, http.MethodPost, reflect.TypeFor[changeSourceLifecycleRequest](), "ChangeSourceLifecycleRequest")
}

func registerSourceOperation(api huma.API, sessions auth.SessionService, service SourceService, validation bool) {
	suffix, operationID, summary := "/sync", "request_source_sync_api_v1_sources__source_id__sync_post", "Request Source Sync"
	if validation {
		suffix, operationID, summary = "/validate", "request_source_validation_api_v1_sources__source_id__validate_post", "Request Source Validation"
	}
	path := sourcesPath + "/{source_id}" + suffix
	huma.Register(api, huma.Operation{
		OperationID: operationID, Method: http.MethodPost, Path: path, Summary: summary, Tags: []string{"sources"}, DefaultStatus: http.StatusAccepted,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true, MaxBodyBytes: sourceBodyLimit,
	}, func(ctx context.Context, input *sourceWriteInput) (*sourceSyncOutput, error) {
		instance := strings.Replace(path, "{source_id}", input.SourceID, 1)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		id, err := sourceAPIID(input.SourceID, instance)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		var body requestSourceOperationRequest
		if !decodeSourceRequest(input.RawBody, input.ContentType, &body) || body.ExpectedVersion < 1 {
			return nil, validationProblem(instance)
		}
		command := sourcedomain.RequestOperation{SourceID: id, ExpectedVersion: body.ExpectedVersion}
		var value sourcedomain.Sync
		if validation {
			value, err = service.RequestValidation(ctx, command, sourcedomain.ID(session.Operator.ID), requestKey)
		} else {
			value, err = service.RequestSync(ctx, command, sourcedomain.ID(session.Operator.ID), requestKey)
		}
		if err != nil {
			return nil, sourceProblem(instance, err)
		}
		return &sourceSyncOutput{Status: http.StatusAccepted, Body: newSourceSyncResponse(value)}, nil
	})
	documentJSONRequest(api, path, http.MethodPost, reflect.TypeFor[requestSourceOperationRequest](), "RequestSourceOperationRequest")
}

func registerSourceRevisions(api huma.API, sessions auth.SessionService, service SourceService) {
	const path = sourcesPath + "/{source_id}/revisions"
	huma.Register(api, huma.Operation{
		OperationID: "list_source_revisions_api_v1_sources__source_id__revisions_get", Method: http.MethodGet,
		Path: path, Summary: "List Source Revisions", Tags: []string{"sources"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *sourceReadInput) (*sourceRevisionsOutput, error) {
		instance := strings.Replace(path, "{source_id}", input.SourceID, 1)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := sourceAPIID(input.SourceID, instance)
		if err != nil {
			return nil, err
		}
		values, err := service.ListRevisions(ctx, id)
		if err != nil {
			return nil, sourceProblem(instance, err)
		}
		output := &sourceRevisionsOutput{Body: make([]sourceRevisionResponse, len(values))}
		for index, value := range values {
			output.Body[index] = sourceRevision(value)
		}
		return output, nil
	})
}

func registerSourceSyncs(api huma.API, sessions auth.SessionService, service SourceService) {
	const path = sourcesPath + "/{source_id}/syncs"
	huma.Register(api, huma.Operation{
		OperationID: "list_source_syncs_api_v1_sources__source_id__syncs_get", Method: http.MethodGet,
		Path: path, Summary: "List Source Syncs", Tags: []string{"sources"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *sourceReadInput) (*sourceSyncsOutput, error) {
		instance := strings.Replace(path, "{source_id}", input.SourceID, 1)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := sourceAPIID(input.SourceID, instance)
		if err != nil {
			return nil, err
		}
		values, err := service.ListSyncs(ctx, id)
		if err != nil {
			return nil, sourceProblem(instance, err)
		}
		output := &sourceSyncsOutput{Body: make([]sourceSyncAPIResponse, len(values))}
		for index, value := range values {
			output.Body[index] = newSourceSyncResponse(value)
		}
		return output, nil
	})
}

func decodeSourceRequest(content []byte, contentType string, destination any) bool {
	return isJSONContentType(contentType) && decodeSecretRequest(content, destination)
}

func sourceFieldsContainNull(content []byte, fields ...string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil {
		return true
	}
	for _, field := range fields {
		if value, exists := object[field]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return true
		}
	}
	return false
}

func defaultCreateWebsiteSourceRequest() createWebsiteSourceRequest {
	limits := sourcedomain.DefaultCrawlLimits()
	return createWebsiteSourceRequest{
		MaxConcurrency: limits.Concurrency, RequestsPerSecond: limits.RequestsPerSecond,
		MaxPages: limits.MaxPages, MaxPageBytes: limits.MaxPageBytes,
		MaxTotalBytes: limits.MaxTotalBytes, MaxDepth: limits.MaxDepth,
		AcquisitionMode: "builtin_crawl",
	}
}

func defaultUpdateWebsiteSourceRequest() updateWebsiteSourceRequest {
	limits := sourcedomain.DefaultCrawlLimits()
	return updateWebsiteSourceRequest{
		MaxConcurrency: limits.Concurrency, RequestsPerSecond: limits.RequestsPerSecond,
		MaxPages: limits.MaxPages, MaxPageBytes: limits.MaxPageBytes,
		MaxTotalBytes: limits.MaxTotalBytes, MaxDepth: limits.MaxDepth,
		AcquisitionMode: "builtin_crawl",
	}
}

func decodeSourceUpdate(content []byte, contentType string) (*updateRepositorySourceRequest, *updateWebsiteSourceRequest, bool) {
	if !isJSONContentType(contentType) {
		return nil, nil, false
	}
	var shape map[string]json.RawMessage
	if !decodeSecretRequest(content, &shape) || shape == nil {
		return nil, nil, false
	}
	_, repository := shape["remote_url"]
	_, website := shape["root_url"]
	if repository == website {
		return nil, nil, false
	}
	if repository {
		body := updateRepositorySourceRequest{IncludePatterns: []string{}, ExcludePatterns: []string{}}
		if !decodeSecretRequest(content, &body) || sourceFieldsContainNull(content, "include_patterns", "exclude_patterns") {
			return nil, nil, false
		}
		return &body, nil, true
	}
	body := defaultUpdateWebsiteSourceRequest()
	if !decodeSecretRequest(content, &body) || sourceFieldsContainNull(content,
		"max_concurrency", "requests_per_second", "max_pages", "max_page_bytes", "max_total_bytes", "max_depth", "acquisition_mode") {
		return nil, nil, false
	}
	return nil, &body, true
}

func normalizeSourceOpenAPISchemas(api huma.API) {
	schemas := api.OpenAPI().Components.Schemas.Map()
	for name, fields := range map[string][]string{
		"CreateRepositorySourceRequest": {"include_patterns", "exclude_patterns"},
		"UpdateRepositorySourceRequest": {"include_patterns", "exclude_patterns"},
		"RepositorySourceResponse":      {"include_patterns", "exclude_patterns"},
		"SourceSyncResponse":            {"captured_include_patterns", "captured_exclude_patterns"},
		"SourceRevisionResponse":        {"ignored_paths", "website_pages"},
	} {
		schema := schemas[name]
		if schema == nil {
			continue
		}
		for _, field := range fields {
			if property := schema.Properties[field]; property != nil {
				property.Nullable = false
			}
		}
	}
}

func repositorySourceConfiguration(request SourceRepositoryConfigurationRequest) (sourcedomain.RepositoryConfiguration, error) {
	name, err := sourcedomain.ParseName(request.Name)
	if err != nil {
		return sourcedomain.RepositoryConfiguration{}, err
	}
	remote, err := sourcedomain.ParseRepositoryRemote(request.RemoteURL)
	if err != nil {
		return sourcedomain.RepositoryConfiguration{}, err
	}
	privacy, ok := sourcePrivacy(request.Privacy)
	if !ok {
		return sourcedomain.RepositoryConfiguration{}, errors.New("source privacy is invalid")
	}
	referenceKind, ok := sourceRefKind(request.RefKind)
	if !ok {
		return sourcedomain.RepositoryConfiguration{}, errors.New("source reference is invalid")
	}
	reference, err := sourcedomain.ParseReference(referenceKind, request.RefValue)
	if err != nil {
		return sourcedomain.RepositoryConfiguration{}, err
	}
	credentialID, err := optionalSourceID(request.CredentialID)
	if err != nil {
		return sourcedomain.RepositoryConfiguration{}, err
	}
	configuration := sourcedomain.RepositoryConfiguration{
		Name: name, Privacy: privacy, Remote: remote, Reference: reference,
		CredentialUsername: request.CredentialUsername, CredentialID: credentialID,
		IncludePatterns: emptyStrings(request.IncludePatterns), ExcludePatterns: emptyStrings(request.ExcludePatterns),
		PollIntervalSeconds: request.PollIntervalSeconds,
	}
	return sourcedomain.NormalizeRepositoryConfiguration(configuration)
}

func websiteSourceConfiguration(request SourceWebsiteConfigurationRequest) (sourcedomain.WebsiteConfiguration, error) {
	name, err := sourcedomain.ParseName(request.Name)
	if err != nil {
		return sourcedomain.WebsiteConfiguration{}, err
	}
	remote, err := sourcedomain.ParseWebsiteRemote(request.RootURL)
	if err != nil {
		return sourcedomain.WebsiteConfiguration{}, err
	}
	privacy, ok := sourcePrivacy(request.Privacy)
	if !ok {
		return sourcedomain.WebsiteConfiguration{}, errors.New("source privacy is invalid")
	}
	credentialID, err := optionalSourceID(request.CredentialID)
	if err != nil {
		return sourcedomain.WebsiteConfiguration{}, err
	}
	tinyFishID, err := optionalSourceID(request.TinyFishCredentialID)
	if err != nil {
		return sourcedomain.WebsiteConfiguration{}, err
	}
	mode, ok := sourceAcquisitionMode(request.AcquisitionMode)
	if !ok {
		return sourcedomain.WebsiteConfiguration{}, errors.New("website acquisition mode is invalid")
	}
	configuration := sourcedomain.WebsiteConfiguration{
		Name: name, Privacy: privacy, Remote: remote,
		CredentialHeader: request.CredentialHeader, CredentialPrefix: request.CredentialPrefix, CredentialID: credentialID,
		Limits:              sourcedomain.CrawlLimits{Concurrency: request.MaxConcurrency, RequestsPerSecond: request.RequestsPerSecond, MaxPages: request.MaxPages, MaxPageBytes: request.MaxPageBytes, MaxTotalBytes: request.MaxTotalBytes, MaxDepth: request.MaxDepth},
		PollIntervalSeconds: request.PollIntervalSeconds, AcquisitionMode: mode, TinyFishCredentialID: tinyFishID,
	}
	return sourcedomain.NormalizeWebsiteConfiguration(configuration)
}

func sourcePrivacy(value string) (sourcedomain.Privacy, bool) {
	switch value {
	case "public":
		return sourcedomain.Public, true
	case "private":
		return sourcedomain.Private, true
	default:
		return "", false
	}
}

func sourceRefKind(value string) (sourcedomain.RefKind, bool) {
	switch value {
	case "branch":
		return sourcedomain.Branch, true
	case "commit":
		return sourcedomain.Commit, true
	default:
		return "", false
	}
}

func sourceAcquisitionMode(value string) (sourcedomain.AcquisitionMode, bool) {
	switch value {
	case "builtin_crawl":
		return sourcedomain.BuiltinCrawl, true
	case "tinyfish_crawl":
		return sourcedomain.TinyFishCrawl, true
	case "direct_json_api":
		return sourcedomain.DirectJSONAPI, true
	default:
		return "", false
	}
}

func sourceLifecycle(value string) (sourcedomain.Lifecycle, bool) {
	switch value {
	case "active":
		return sourcedomain.Active, true
	case "disabled":
		return sourcedomain.Disabled, true
	case "removed":
		return sourcedomain.Removed, true
	default:
		return "", false
	}
}

func optionalSourceID(value *string) (*sourcedomain.ID, error) {
	if value == nil {
		return nil, nil
	}
	id, err := sourcedomain.ParseID(*value)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func sourceAPIID(raw, instance string) (sourcedomain.ID, error) {
	id, err := sourcedomain.ParseID(raw)
	if err != nil {
		return sourcedomain.ID{}, parameterValidationProblem(instance, "path")
	}
	return id, nil
}

func emptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return slices.Clone(values)
}

func sourceResponse(value sourcedomain.Source) sourceAPIResponse {
	common := SourceCommonResponse{
		ID: value.ID.String(), KnowledgeBaseID: value.KnowledgeBaseID.String(), Name: value.Name,
		Privacy: strings.ToLower(string(value.Privacy)), Lifecycle: strings.ToLower(string(value.Lifecycle)),
		Health: strings.ToLower(string(value.Health)), SanitizedError: value.SanitizedError,
		CheckedAt: value.CheckedAt, CurrentRevisionID: sourceIDString(value.CurrentRevisionID),
		Version: value.Version, ConfigurationVersion: value.ConfigurationVersion,
		ValidatedConfigurationVersion: value.ValidatedConfigurationVersion,
		CreatedAt:                     value.CreatedAt, UpdatedAt: value.UpdatedAt, DisabledAt: value.DisabledAt, RemovedAt: value.RemovedAt,
	}
	if value.Repository != nil {
		common.PollIntervalSeconds = value.Repository.PollIntervalSeconds
		return sourceAPIResponse{Repository: &repositorySourceResponse{
			SourceCommonResponse: common, Kind: "repository", RemoteURL: value.Repository.Remote.URL,
			RemoteHost: value.Repository.Remote.Host, RepositoryPath: sourceRepositoryPath(value.Repository.Remote.URL),
			RefKind: strings.ToLower(string(value.Repository.Reference.Kind)), RefValue: value.Repository.Reference.Value,
			CredentialUsername: value.Repository.CredentialUsername, CredentialID: sourceIDString(value.Repository.CredentialID),
			IncludePatterns: emptyStrings(value.Repository.IncludePatterns), ExcludePatterns: emptyStrings(value.Repository.ExcludePatterns),
		}}
	}
	if value.Website != nil {
		common.PollIntervalSeconds = value.Website.PollIntervalSeconds
		return sourceAPIResponse{Website: &websiteSourceResponse{
			SourceCommonResponse: common, Kind: "website", RootURL: value.Website.Remote.URL, RootHost: value.Website.Remote.Host,
			CredentialHeader: value.Website.CredentialHeader, CredentialPrefix: value.Website.CredentialPrefix,
			CredentialID: sourceIDString(value.Website.CredentialID), MaxConcurrency: value.Website.Limits.Concurrency,
			RequestsPerSecond: value.Website.Limits.RequestsPerSecond, MaxPages: value.Website.Limits.MaxPages,
			MaxPageBytes: value.Website.Limits.MaxPageBytes, MaxTotalBytes: value.Website.Limits.MaxTotalBytes,
			MaxDepth: value.Website.Limits.MaxDepth, AcquisitionMode: strings.ToLower(string(value.Website.AcquisitionMode)),
			TinyFishCredentialID: sourceIDString(value.Website.TinyFishCredentialID),
		}}
	}
	return sourceAPIResponse{}
}

func newSourceSyncResponse(value sourcedomain.Sync) sourceSyncAPIResponse {
	common := SourceSyncCommonResponse{
		ID: value.ID.String(), SourceID: value.SourceID.String(), JobID: value.JobID.String(), Kind: strings.ToLower(string(value.Kind)),
		RequestedByOperatorID: sourceIDString(value.RequestedBy), CapturedSourceVersion: value.CapturedSourceVersion,
		CapturedConfigurationVersion: value.CapturedConfigurationVersion, CandidateRevisionID: sourceIDString(value.CandidateRevisionID),
		Status: strings.ToLower(string(value.Status)), ResultRevisionID: sourceIDString(value.ResultRevisionID),
		ResolvedNativeVersion: value.ResolvedNativeVersion, SanitizedError: value.SanitizedError,
		CreatedAt: value.CreatedAt, StartedAt: value.StartedAt, CompletedAt: value.CompletedAt,
	}
	if value.Repository != nil {
		common.CapturedPrivacy = strings.ToLower(string(value.Repository.Privacy))
		common.CapturedCredentialID = sourceIDString(value.Repository.CredentialID)
		common.CapturedCredentialVersion = value.Repository.CredentialVersion
		return sourceSyncAPIResponse{Repository: &SourceSyncResponse{
			SourceSyncCommonResponse: common, CapturedRemoteURL: value.Repository.Remote.URL,
			CapturedRefKind: strings.ToLower(string(value.Repository.Reference.Kind)), CapturedRefValue: value.Repository.Reference.Value,
			CapturedCredentialUsername: value.Repository.CredentialUsername,
			CapturedIncludePatterns:    emptyStrings(value.Repository.IncludePatterns), CapturedExcludePatterns: emptyStrings(value.Repository.ExcludePatterns),
		}}
	}
	if value.Website != nil {
		common.CapturedPrivacy = strings.ToLower(string(value.Website.Privacy))
		common.CapturedCredentialID = sourceIDString(value.Website.CredentialID)
		common.CapturedCredentialVersion = value.Website.CredentialVersion
		return sourceSyncAPIResponse{Website: &websiteSourceSyncResponse{
			SourceSyncCommonResponse: common, CapturedRootURL: value.Website.Remote.URL,
			CapturedCredentialHeader: value.Website.CredentialHeader, CapturedCredentialPrefix: value.Website.CredentialPrefix,
			CapturedMaxConcurrency: value.Website.Limits.Concurrency, CapturedRequestsPerSecond: value.Website.Limits.RequestsPerSecond,
			CapturedMaxPages: value.Website.Limits.MaxPages, CapturedMaxPageBytes: value.Website.Limits.MaxPageBytes,
			CapturedMaxTotalBytes: value.Website.Limits.MaxTotalBytes, CapturedMaxDepth: value.Website.Limits.MaxDepth,
			CapturedAcquisitionMode:           strings.ToLower(string(value.Website.AcquisitionMode)),
			CapturedTinyFishCredentialID:      sourceIDString(value.Website.TinyFishCredentialID),
			CapturedTinyFishCredentialVersion: value.Website.TinyFishCredentialVersion,
			CapturedPreviousRevisionID:        sourceIDString(value.Website.PreviousRevisionID),
		}}
	}
	return sourceSyncAPIResponse{}
}

func sourceRevision(value sourcedomain.Revision) sourceRevisionResponse {
	pages := make([]websiteRevisionPageResponse, len(value.WebsitePages))
	for index, page := range value.WebsitePages {
		pages[index] = websiteRevisionPageResponse{
			CanonicalURL: page.CanonicalURL, ContentPath: page.ContentPath,
			ContentSHA256: hex.EncodeToString(page.ContentSHA256[:]), EvidenceURI: page.EvidenceURI,
			Freshness: page.Freshness, ETag: page.ETag, LastModified: page.LastModified,
			ReusedFromRevisionID: sourceIDString(page.ReusedFromRevisionID),
		}
	}
	return sourceRevisionResponse{
		ID: value.ID.String(), SourceID: value.SourceID.String(), ObservedRefKind: strings.ToLower(string(value.ObservedRef.Kind)),
		ObservedRef: value.ObservedRef.Value, NativeVersion: value.NativeVersion,
		Fingerprint: hex.EncodeToString(value.Fingerprint[:]), ArtifactKey: value.ArtifactKey,
		FileCount: value.FileCount, ByteCount: value.ByteCount, IgnoredPaths: emptyStrings(value.IgnoredPaths),
		CreatedAt: value.CreatedAt, WebsitePages: pages,
	}
}

func createdSourceResponse(value sourcedomain.Created) sourceCreatedResponse {
	return sourceCreatedResponse{Source: sourceResponse(value.Source), Validation: newSourceSyncResponse(value.Validation)}
}

func sourceIDString(value *sourcedomain.ID) *string {
	if value == nil {
		return nil
	}
	result := value.String()
	return &result
}

func sourceRepositoryPath(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(parsed.Path, "/")
}

func sourceConfigurationProblem(instance string) error {
	return &apiProblem{Type: "about:blank", Title: "Unprocessable Content", Status: http.StatusUnprocessableEntity, Detail: "Source configuration is invalid.", Instance: instance}
}

func sourceProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, sourcedomain.ErrNotFound):
		problem.Title, problem.Status, problem.Detail = "Not Found", http.StatusNotFound, "Source resource not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Idempotency key conflicts with a different request."
	case errors.Is(err, sourcedomain.ErrConflict), errors.Is(err, sourcedomain.ErrTransition):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Source resource state conflicts with the request."
	default:
		problem.Title, problem.Status, problem.Detail = "Internal Server Error", http.StatusInternalServerError, "The request could not be completed."
	}
	return problem
}
