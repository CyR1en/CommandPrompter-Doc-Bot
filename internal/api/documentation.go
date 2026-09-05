package api

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/danielgtaylor/huma/v2"
)

const (
	documentationRunsPath  = "/api/v1/runs"
	documentationBodyLimit = 1 << 20
)

// DocumentationService is the complete service boundary used by the
// documentation HTTP adapters. *docgen.Store satisfies this interface.
type DocumentationService interface {
	GetWikiEvidence(context.Context, docgen.ID, *docgen.WikiVersionID, string, string, string) (docgen.EvidenceExcerpt, error)
	RequestGeneration(context.Context, docgen.ID, int, docgen.ID, string) (jobs.JobID, error)
	ListRuns(context.Context, *docgen.ID, int, int) ([]docgen.RunDetail, error)
	GetRun(context.Context, docgen.RunID) (docgen.RunDetail, error)
	GetWiki(context.Context, docgen.ID, *docgen.WikiVersionID, *string) (docgen.WikiView, error)
	ListWikiVersions(context.Context, docgen.ID) ([]docgen.WikiVersion, error)
	ExportWiki(context.Context, docgen.ID, *docgen.WikiVersionID) ([]byte, error)
}

// DocumentationJobService resolves the job returned by a generation request.
// *jobs.Store satisfies this interface.
type DocumentationJobService interface {
	Get(context.Context, jobs.JobID) (jobs.Snapshot, error)
}

type GenerateDocumentationRequest struct {
	ExpectedVersion int `json:"expected_version" minimum:"1"`
}

type generateDocumentationInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	CSRFToken       string `header:"X-CSRF-Token"`
	IdempotencyKey  string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	KnowledgeBaseID string `path:"knowledge_base_id" format:"uuid"`
	RawBody         []byte `contentType:"application/json"`
	ContentType     string `header:"Content-Type"`
}

type listDocumentationRunsInput struct {
	SessionCookie   string              `cookie:"ref0_session"`
	KnowledgeBaseID optionalStringParam `query:"knowledge_base_id" format:"uuid"`
	Limit           int                 `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Offset          int                 `query:"offset" default:"0" minimum:"0" maximum:"10000"`
}

type getDocumentationRunInput struct {
	SessionCookie string `cookie:"ref0_session"`
	RunID         string `path:"run_id" format:"uuid"`
}

type wikiInput struct {
	SessionCookie   string              `cookie:"ref0_session"`
	KnowledgeBaseID string              `path:"knowledge_base_id" format:"uuid"`
	VersionID       optionalStringParam `query:"version_id" format:"uuid"`
	Slug            optionalStringParam `query:"slug"`
}

type wikiVersionsInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	KnowledgeBaseID string `path:"knowledge_base_id" format:"uuid"`
}

type wikiExportInput struct {
	SessionCookie   string              `cookie:"ref0_session"`
	KnowledgeBaseID string              `path:"knowledge_base_id" format:"uuid"`
	VersionID       optionalStringParam `query:"version_id" format:"uuid"`
}

type DocumentationRunSourceResponse struct {
	SourceID         string `json:"source_id" format:"uuid"`
	SourceRevisionID string `json:"source_revision_id" format:"uuid"`
	Fingerprint      string `json:"fingerprint"`
	Commit           string `json:"commit"`
}

type DocumentationRunModelResponse struct {
	Role                                 string `json:"role" enum:"documentation_planner,documentation_writer"`
	ModelProfileID                       string `json:"model_profile_id" format:"uuid"`
	ModelProfileVersionID                string `json:"model_profile_version_id" format:"uuid"`
	ProfileVersion                       int    `json:"profile_version"`
	ProviderEndpointID                   string `json:"provider_endpoint_id" format:"uuid"`
	CapturedEndpointConfigurationVersion int    `json:"captured_endpoint_configuration_version"`
	CapturedCredentialVersion            *int   `json:"captured_credential_version"`
}

type DocumentationModelUsageResponse struct {
	ModelCalls           int `json:"model_calls"`
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	TotalTokens          int `json:"total_tokens"`
	TruncatedToolResults int `json:"truncated_tool_results"`
}

type DocumentationPageResponse struct {
	ID               string                          `json:"id" format:"uuid"`
	JobID            string                          `json:"job_id" format:"uuid"`
	Position         int                             `json:"position"`
	Slug             string                          `json:"slug"`
	Title            string                          `json:"title"`
	Purpose          string                          `json:"purpose"`
	RelatedPages     []string                        `json:"related_pages" nullable:"false"`
	SourceSeedPaths  []map[string]string             `json:"source_seed_paths" nullable:"false"`
	Status           string                          `json:"status" enum:"pending,running,complete,skipped"`
	SubmissionDigest *string                         `json:"submission_digest"`
	ContentSHA256    *string                         `json:"content_sha256"`
	ClaimsSHA256     *string                         `json:"claims_sha256"`
	SanitizedError   *string                         `json:"sanitized_error"`
	AttemptCount     int                             `json:"attempt_count"`
	Usage            DocumentationModelUsageResponse `json:"usage"`
	CreatedAt        time.Time                       `json:"created_at"`
	UpdatedAt        time.Time                       `json:"updated_at"`
	CompletedAt      *time.Time                      `json:"completed_at"`
}

type DocumentationRunResponse struct {
	ID                     string                           `json:"id" format:"uuid"`
	KnowledgeBaseID        string                           `json:"knowledge_base_id" format:"uuid"`
	Status                 string                           `json:"status" enum:"preparing,planning,generating,finalizing,no_op,published,interrupted,failed"`
	PrepareJobID           string                           `json:"prepare_job_id" format:"uuid"`
	KnowledgeBaseVersion   int                              `json:"knowledge_base_version"`
	Instructions           string                           `json:"instructions"`
	Language               string                           `json:"language"`
	Sources                []DocumentationRunSourceResponse `json:"sources" nullable:"false"`
	Models                 []DocumentationRunModelResponse  `json:"models" nullable:"false"`
	PlannerUsage           DocumentationModelUsageResponse  `json:"planner_usage"`
	Usage                  DocumentationModelUsageResponse  `json:"usage"`
	Pages                  []DocumentationPageResponse      `json:"pages" nullable:"false"`
	PriorWikiVersionID     *string                          `json:"prior_wiki_version_id" format:"uuid"`
	PlanDigest             *string                          `json:"plan_digest"`
	PublishedWikiVersionID *string                          `json:"published_wiki_version_id" format:"uuid"`
	SanitizedError         *string                          `json:"sanitized_error"`
	CreatedAt              time.Time                        `json:"created_at"`
	UpdatedAt              time.Time                        `json:"updated_at"`
	CompletedAt            *time.Time                       `json:"completed_at"`
}

type WikiVersionResponse struct {
	ID                 string    `json:"id" format:"uuid"`
	KnowledgeBaseID    string    `json:"knowledge_base_id" format:"uuid"`
	DocumentationRunID string    `json:"documentation_run_id" format:"uuid"`
	ArtifactKey        string    `json:"artifact_key"`
	ManifestSHA256     string    `json:"manifest_sha256"`
	PageCount          int       `json:"page_count"`
	CreatedAt          time.Time `json:"created_at"`
	PublishedAt        time.Time `json:"published_at"`
}

type WikiPageSummaryResponse struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	PageType    string `json:"page_type"`
}

type WikiEvidenceResponse struct {
	ID                string `json:"id"`
	Resource          string `json:"resource"`
	SourceID          string `json:"source_id" format:"uuid"`
	SourceRevisionID  string `json:"source_revision_id" format:"uuid"`
	SourceFingerprint string `json:"source_fingerprint"`
	Commit            string `json:"commit"`
	Path              string `json:"path"`
	StartLine         *int   `json:"start_line"`
	EndLine           *int   `json:"end_line"`
}

type WikiClaimResponse struct {
	ID        string                 `json:"id"`
	Statement string                 `json:"statement"`
	Evidence  []WikiEvidenceResponse `json:"evidence" nullable:"false"`
}

type WikiPageResponse struct {
	_             struct{}            `json:"-" nullable:"true"`
	Slug          string              `json:"slug"`
	Title         string              `json:"title"`
	Description   string              `json:"description"`
	PageType      string              `json:"page_type"`
	Markdown      string              `json:"markdown"`
	ContentSHA256 string              `json:"content_sha256"`
	ClaimsSHA256  string              `json:"claims_sha256"`
	Claims        []WikiClaimResponse `json:"claims" nullable:"false"`
}

type WikiResponse struct {
	Version WikiVersionResponse       `json:"version"`
	Pages   []WikiPageSummaryResponse `json:"pages" nullable:"false"`
	Page    *WikiPageResponse         `json:"page"`
}

type documentationRunsOutput struct {
	Body []DocumentationRunResponse `nullable:"false"`
}

type documentationRunOutput struct {
	Body DocumentationRunResponse
}

type wikiOutput struct {
	Body WikiResponse
}

type wikiVersionsOutput struct {
	Body []WikiVersionResponse `nullable:"false"`
}

type wikiExportOutput struct {
	CacheControl       string `header:"Cache-Control"`
	ContentDisposition string `header:"Content-Disposition"`
	ContentType        string `header:"Content-Type"`
	Body               []byte
}

// RegisterDocumentationRoutes registers the six Python-parity documentation
// endpoints without coupling them to the central handler constructor.
func RegisterDocumentationRoutes(api huma.API, sessions auth.SessionService, service DocumentationService, jobReader DocumentationJobService) {
	registerGenerateDocumentation(api, sessions, service, jobReader)
	registerDocumentationRunList(api, sessions, service)
	registerDocumentationRunGet(api, sessions, service)
	registerWikiGet(api, sessions, service)
	registerWikiEvidence(api, sessions, service)
	registerWikiVersionList(api, sessions, service)
	registerWikiExport(api, sessions, service)
}

func registerGenerateDocumentation(api huma.API, sessions auth.SessionService, service DocumentationService, jobReader DocumentationJobService) {
	const path = "/api/v1/knowledge-bases/{knowledge_base_id}/generate"
	huma.Register(api, huma.Operation{
		OperationID:      "generate_documentation_api_v1_knowledge_bases__knowledge_base_id__generate_post",
		Method:           http.MethodPost,
		Path:             path,
		Summary:          "Generate Documentation",
		Tags:             []string{"documentation"},
		DefaultStatus:    http.StatusAccepted,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     documentationBodyLimit,
	}, func(ctx context.Context, input *generateDocumentationInput) (*jobOutput, error) {
		instance := documentationInstance(path, input.KnowledgeBaseID)
		_, session, err := RequireAuthenticatedWrite(ctx, sessions, input.SessionCookie, input.CSRFToken, instance)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		knowledgeBaseID, err := docgen.ParseID(input.KnowledgeBaseID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		var body GenerateDocumentationRequest
		if !isJSONContentType(input.ContentType) || !decodeSecretRequest(input.RawBody, &body) || body.ExpectedVersion <= 0 {
			return nil, validationProblem(instance)
		}
		jobID, err := service.RequestGeneration(ctx, knowledgeBaseID, body.ExpectedVersion, docgen.ID(session.Operator.ID), requestKey)
		if err != nil {
			return nil, documentationProblem(instance, err)
		}
		value, err := jobReader.Get(ctx, jobID)
		if err != nil {
			return nil, jobProblem(instance, err)
		}
		return &jobOutput{Body: newJobResponse(value)}, nil
	})
	documentJSONRequest(api, path, http.MethodPost, reflect.TypeFor[GenerateDocumentationRequest](), "GenerateDocumentationRequest")
}

func registerDocumentationRunList(api huma.API, sessions auth.SessionService, service DocumentationService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_documentation_runs_api_v1_runs_get",
		Method:      http.MethodGet,
		Path:        documentationRunsPath,
		Summary:     "List Documentation Runs",
		Tags:        []string{"documentation"},
		Errors:      []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *listDocumentationRunsInput) (*documentationRunsOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, documentationRunsPath); err != nil {
			return nil, err
		}
		var knowledgeBaseID *docgen.ID
		if input.KnowledgeBaseID.IsSet {
			parsed, err := docgen.ParseID(input.KnowledgeBaseID.Value)
			if err != nil {
				return nil, parameterValidationProblem(documentationRunsPath, "query")
			}
			knowledgeBaseID = &parsed
		}
		values, err := service.ListRuns(ctx, knowledgeBaseID, input.Limit, input.Offset)
		if err != nil {
			return nil, documentationProblem(documentationRunsPath, err)
		}
		output := &documentationRunsOutput{Body: make([]DocumentationRunResponse, len(values))}
		for index, value := range values {
			output.Body[index] = documentationRunResponse(value)
		}
		return output, nil
	})
}

func registerDocumentationRunGet(api huma.API, sessions auth.SessionService, service DocumentationService) {
	const path = documentationRunsPath + "/{run_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_documentation_run_api_v1_runs__run_id__get",
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "Get Documentation Run",
		Tags:        []string{"documentation"},
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *getDocumentationRunInput) (*documentationRunOutput, error) {
		instance := documentationInstance(path, input.RunID)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		parsed, err := docgen.ParseID(input.RunID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.GetRun(ctx, docgen.RunID(parsed))
		if err != nil {
			return nil, documentationProblem(instance, err)
		}
		return &documentationRunOutput{Body: documentationRunResponse(value)}, nil
	})
}

func registerWikiGet(api huma.API, sessions auth.SessionService, service DocumentationService) {
	const path = "/api/v1/knowledge-bases/{knowledge_base_id}/wiki"
	huma.Register(api, huma.Operation{
		OperationID: "get_wiki_api_v1_knowledge_bases__knowledge_base_id__wiki_get",
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "Get Wiki",
		Tags:        []string{"documentation"},
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *wikiInput) (*wikiOutput, error) {
		instance := documentationInstance(path, input.KnowledgeBaseID)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		knowledgeBaseID, versionID, err := documentationWikiIDs(input.KnowledgeBaseID, input.VersionID, instance)
		if err != nil {
			return nil, err
		}
		value, err := service.GetWiki(ctx, knowledgeBaseID, versionID, input.Slug.Pointer())
		if err != nil {
			return nil, documentationProblem(instance, err)
		}
		response, err := wikiResponse(value)
		if err != nil {
			return nil, documentationProblem(instance, err)
		}
		return &wikiOutput{Body: response}, nil
	})
}

func registerWikiVersionList(api huma.API, sessions auth.SessionService, service DocumentationService) {
	const path = "/api/v1/knowledge-bases/{knowledge_base_id}/wiki/versions"
	huma.Register(api, huma.Operation{
		OperationID: "list_wiki_versions_api_v1_knowledge_bases__knowledge_base_id__wiki_versions_get",
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "List Wiki Versions",
		Tags:        []string{"documentation"},
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *wikiVersionsInput) (*wikiVersionsOutput, error) {
		instance := documentationInstance(path, input.KnowledgeBaseID)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		knowledgeBaseID, err := docgen.ParseID(input.KnowledgeBaseID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		values, err := service.ListWikiVersions(ctx, knowledgeBaseID)
		if err != nil {
			return nil, documentationProblem(instance, err)
		}
		output := &wikiVersionsOutput{Body: make([]WikiVersionResponse, len(values))}
		for index, value := range values {
			output.Body[index] = wikiVersionResponse(value)
		}
		return output, nil
	})
}

func registerWikiExport(api huma.API, sessions auth.SessionService, service DocumentationService) {
	const path = "/api/v1/knowledge-bases/{knowledge_base_id}/wiki/export"
	huma.Register(api, huma.Operation{
		OperationID: "export_wiki_api_v1_knowledge_bases__knowledge_base_id__wiki_export_get",
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "Export Wiki",
		Tags:        []string{"documentation"},
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
		Responses: map[string]*huma.Response{
			"200": {Description: "Successful Response", Content: map[string]*huma.MediaType{"application/zip": {}}},
		},
	}, func(ctx context.Context, input *wikiExportInput) (*wikiExportOutput, error) {
		instance := documentationInstance(path, input.KnowledgeBaseID)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		knowledgeBaseID, versionID, err := documentationWikiIDs(input.KnowledgeBaseID, input.VersionID, instance)
		if err != nil {
			return nil, err
		}
		resolved, err := service.GetWiki(ctx, knowledgeBaseID, versionID, nil)
		if err != nil {
			return nil, documentationProblem(instance, err)
		}
		selected := resolved.Version.ID
		content, err := service.ExportWiki(ctx, knowledgeBaseID, &selected)
		if err != nil {
			return nil, documentationProblem(instance, err)
		}
		return &wikiExportOutput{
			CacheControl:       "no-store",
			ContentDisposition: `attachment; filename="knowledge-base-` + knowledgeBaseID.String() + `-` + selected.String() + `.zip"`,
			ContentType:        "application/zip",
			Body:               content,
		}, nil
	})
}

func documentationWikiIDs(rawKnowledgeBaseID string, rawVersionID optionalStringParam, instance string) (docgen.ID, *docgen.WikiVersionID, error) {
	knowledgeBaseID, err := docgen.ParseID(rawKnowledgeBaseID)
	if err != nil {
		return docgen.ID{}, nil, parameterValidationProblem(instance, "path")
	}
	if !rawVersionID.IsSet {
		return knowledgeBaseID, nil, nil
	}
	parsed, err := docgen.ParseID(rawVersionID.Value)
	if err != nil {
		return docgen.ID{}, nil, parameterValidationProblem(instance, "query")
	}
	value := docgen.WikiVersionID(parsed)
	return knowledgeBaseID, &value, nil
}

func documentationRunResponse(value docgen.RunDetail) DocumentationRunResponse {
	run := value.Run
	response := DocumentationRunResponse{
		ID: run.ID.String(), KnowledgeBaseID: run.KnowledgeBaseID.String(),
		Status: strings.ToLower(string(run.Status)), PrepareJobID: run.PrepareJobID.String(),
		KnowledgeBaseVersion: run.KnowledgeBaseVersion, Instructions: run.Instructions, Language: run.Language,
		Sources:      make([]DocumentationRunSourceResponse, len(run.Sources)),
		Models:       make([]DocumentationRunModelResponse, len(run.Models)),
		Pages:        make([]DocumentationPageResponse, len(value.Pages)),
		PlannerUsage: documentationUsageResponse(run.PlannerUsage),
		Usage:        documentationUsageResponse(value.Usage()),
		PlanDigest:   optionalHex(run.PlanDigest), SanitizedError: run.SanitizedError,
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, CompletedAt: run.CompletedAt,
	}
	response.PriorWikiVersionID = optionalWikiVersionID(run.PriorWikiVersionID)
	response.PublishedWikiVersionID = optionalWikiVersionID(run.PublishedWikiVersionID)
	for index, source := range run.Sources {
		response.Sources[index] = DocumentationRunSourceResponse{
			SourceID: source.SourceID.String(), SourceRevisionID: source.RevisionID.String(),
			Fingerprint: hex.EncodeToString(source.Fingerprint[:]), Commit: source.Commit,
		}
	}
	for index, model := range run.Models {
		response.Models[index] = DocumentationRunModelResponse{
			Role: strings.ToLower(string(model.Role)), ModelProfileID: model.ProfileID.String(),
			ModelProfileVersionID: model.ProfileVersionID.String(), ProfileVersion: model.ProfileVersion,
			ProviderEndpointID: model.EndpointID.String(), CapturedEndpointConfigurationVersion: model.EndpointConfigurationVersion,
			CapturedCredentialVersion: model.CredentialVersion,
		}
	}
	for index, page := range value.Pages {
		seeds := make([]map[string]string, len(page.Target.SourceSeedPaths))
		for seedIndex, seed := range page.Target.SourceSeedPaths {
			seeds[seedIndex] = map[string]string{"source_id": seed.SourceID.String(), "path": seed.Path}
		}
		response.Pages[index] = DocumentationPageResponse{
			ID: page.ID.String(), JobID: page.JobID.String(), Position: page.Position,
			Slug: page.Target.Slug, Title: page.Target.Title, Purpose: page.Target.Purpose,
			RelatedPages: append([]string{}, page.Target.RelatedPages...), SourceSeedPaths: seeds,
			Status: strings.ToLower(string(page.Status)), SubmissionDigest: optionalHex(page.SubmissionDigest),
			ContentSHA256: optionalHex(page.ContentSHA256), ClaimsSHA256: optionalHex(page.ClaimsSHA256),
			SanitizedError: page.SanitizedError, AttemptCount: page.AttemptCount,
			Usage: documentationUsageResponse(page.Usage), CreatedAt: page.CreatedAt,
			UpdatedAt: page.UpdatedAt, CompletedAt: page.CompletedAt,
		}
	}
	return response
}

func wikiResponse(value docgen.WikiView) (WikiResponse, error) {
	response := WikiResponse{
		Version: wikiVersionResponse(value.Version),
		Pages:   make([]WikiPageSummaryResponse, len(value.Pages)),
	}
	for index, page := range value.Pages {
		response.Pages[index] = wikiPageSummaryResponse(page)
	}
	if value.Page == nil {
		return response, nil
	}
	page := value.Page
	selected := &WikiPageResponse{
		Slug: page.Summary.Slug, Title: page.Summary.Title, Description: page.Summary.Description,
		PageType: page.Summary.PageType, Markdown: page.Markdown,
		ContentSHA256: hex.EncodeToString(page.ContentSHA256[:]), ClaimsSHA256: hex.EncodeToString(page.ClaimsSHA256[:]),
		Claims: make([]WikiClaimResponse, len(page.Claims)),
	}
	for claimIndex, claim := range page.Claims {
		converted := WikiClaimResponse{ID: claim.ID, Statement: claim.Statement, Evidence: make([]WikiEvidenceResponse, len(claim.Evidence))}
		for evidenceIndex, evidence := range claim.Evidence {
			resource, err := evidence.Location.Resource()
			if err != nil {
				return WikiResponse{}, err
			}
			converted.Evidence[evidenceIndex] = WikiEvidenceResponse{
				ID: evidence.ID, Resource: resource, SourceID: evidence.Location.SourceID.String(),
				SourceRevisionID:  evidence.Location.SourceRevisionID.String(),
				SourceFingerprint: hex.EncodeToString(evidence.Location.SourceVersion[:]), Commit: evidence.Location.Commit,
				Path: evidence.Location.Path, StartLine: evidence.Location.StartLine, EndLine: evidence.Location.EndLine,
			}
		}
		selected.Claims[claimIndex] = converted
	}
	response.Page = selected
	return response, nil
}

func wikiVersionResponse(value docgen.WikiVersion) WikiVersionResponse {
	return WikiVersionResponse{
		ID: value.ID.String(), KnowledgeBaseID: value.KnowledgeBaseID.String(),
		DocumentationRunID: value.DocumentationRunID.String(), ArtifactKey: value.ArtifactKey,
		ManifestSHA256: hex.EncodeToString(value.ManifestSHA256[:]), PageCount: value.PageCount,
		CreatedAt: value.CreatedAt, PublishedAt: value.PublishedAt,
	}
}

func wikiPageSummaryResponse(value docgen.WikiPageSummary) WikiPageSummaryResponse {
	return WikiPageSummaryResponse{Slug: value.Slug, Title: value.Title, Description: value.Description, PageType: value.PageType}
}

func documentationUsageResponse(value docgen.ModelUsage) DocumentationModelUsageResponse {
	return DocumentationModelUsageResponse{
		ModelCalls: value.ModelCalls, InputTokens: value.InputTokens,
		OutputTokens: value.OutputTokens, TotalTokens: value.TotalTokens,
		TruncatedToolResults: value.TruncatedToolResults,
	}
}

func optionalHex(value []byte) *string {
	if len(value) == 0 {
		return nil
	}
	encoded := hex.EncodeToString(value)
	return &encoded
}

func optionalWikiVersionID(value *docgen.WikiVersionID) *string {
	if value == nil {
		return nil
	}
	encoded := value.String()
	return &encoded
}

func documentationInstance(pattern, id string) string {
	if strings.Contains(pattern, "{knowledge_base_id}") {
		return strings.Replace(pattern, "{knowledge_base_id}", id, 1)
	}
	return strings.Replace(pattern, "{run_id}", id, 1)
}

func documentationProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, docgen.ErrNotFound):
		problem.Title, problem.Status, problem.Detail = "Not Found", http.StatusNotFound, "Documentation resource was not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Idempotency key conflicts with a different request."
	case errors.Is(err, docgen.ErrConflict):
		problem.Title, problem.Status, problem.Detail = "Conflict", http.StatusConflict, "Documentation state conflicts with the request."
	case errors.Is(err, docgen.ErrValidation):
		problem.Title, problem.Status, problem.Detail = "Unprocessable Content", http.StatusUnprocessableEntity, "Documentation content is invalid."
	default:
		problem.Title, problem.Status, problem.Detail = "Internal Server Error", http.StatusInternalServerError, "The request could not be completed."
	}
	return problem
}

var _ DocumentationService = (*docgen.Store)(nil)
var _ DocumentationJobService = (*jobs.Store)(nil)

type wikiEvidenceInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	KnowledgeBaseID string `path:"knowledge_base_id"`
	VersionID       string `query:"version_id" required:"true"`
	Slug            string `query:"slug" required:"true" maxLength:"255"`
	ClaimID         string `query:"claim_id" required:"true" maxLength:"128"`
	EvidenceID      string `query:"evidence_id" required:"true" maxLength:"255"`
}
type wikiEvidenceOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         docgen.EvidenceExcerpt
}

func registerWikiEvidence(api huma.API, sessions auth.SessionService, service DocumentationService) {
	const path = "/api/v1/knowledge-bases/{knowledge_base_id}/wiki/evidence"
	huma.Register(api, huma.Operation{OperationID: "get_wiki_evidence", Method: http.MethodGet, Path: path, Summary: "Read Published Evidence", Tags: []string{"documentation"}, Errors: []int{401, 404, 422}}, func(ctx context.Context, input *wikiEvidenceInput) (*wikiEvidenceOutput, error) {
		instance := documentationInstance(path, input.KnowledgeBaseID)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		knowledgeBaseID, versionID, err := documentationWikiIDs(input.KnowledgeBaseID, optionalStringParam{Value: input.VersionID, IsSet: true}, instance)
		if err != nil {
			return nil, err
		}
		excerpt, err := service.GetWikiEvidence(ctx, knowledgeBaseID, versionID, input.Slug, input.ClaimID, input.EvidenceID)
		if err != nil {
			return nil, documentationProblem(instance, err)
		}
		return &wikiEvidenceOutput{CacheControl: "no-store", Body: excerpt}, nil
	})
}
