package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	providerdomain "github.com/cyr1en/ref0/internal/providers"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type fakeDocumentationService struct {
	detail   docgen.RunDetail
	wiki     docgen.WikiView
	versions []docgen.WikiVersion
	export   []byte
	err      error

	requestedKnowledgeBaseID docgen.ID
	requestedExpectedVersion int
	requestedActor           docgen.ID
	requestedKey             string
	listedKnowledgeBaseID    *docgen.ID
	listedLimit              int
	listedOffset             int
	wikiKnowledgeBaseID      docgen.ID
	wikiVersionID            *docgen.WikiVersionID
	wikiSlug                 *string
	exportedVersionID        *docgen.WikiVersionID
}

func (service *fakeDocumentationService) RequestGeneration(_ context.Context, knowledgeBaseID docgen.ID, expectedVersion int, actor docgen.ID, key string) (jobs.JobID, error) {
	service.requestedKnowledgeBaseID, service.requestedExpectedVersion = knowledgeBaseID, expectedVersion
	service.requestedActor, service.requestedKey = actor, key
	return service.detail.Run.PrepareJobID, service.err
}

func (service *fakeDocumentationService) ListRuns(_ context.Context, knowledgeBaseID *docgen.ID, limit, offset int) ([]docgen.RunDetail, error) {
	service.listedKnowledgeBaseID, service.listedLimit, service.listedOffset = knowledgeBaseID, limit, offset
	return []docgen.RunDetail{service.detail}, service.err
}

func (service *fakeDocumentationService) GetRun(context.Context, docgen.RunID) (docgen.RunDetail, error) {
	return service.detail, service.err
}

func (service *fakeDocumentationService) GetWiki(_ context.Context, knowledgeBaseID docgen.ID, versionID *docgen.WikiVersionID, slug *string) (docgen.WikiView, error) {
	service.wikiKnowledgeBaseID, service.wikiVersionID, service.wikiSlug = knowledgeBaseID, versionID, slug
	return service.wiki, service.err
}

func (service *fakeDocumentationService) ListWikiVersions(context.Context, docgen.ID) ([]docgen.WikiVersion, error) {
	return service.versions, service.err
}

func (service *fakeDocumentationService) ExportWiki(_ context.Context, _ docgen.ID, versionID *docgen.WikiVersionID) ([]byte, error) {
	service.exportedVersionID = versionID
	return service.export, service.err
}

type fakeDocumentationJobService struct {
	value jobs.Snapshot
	err   error
}

func (service *fakeDocumentationJobService) Get(context.Context, jobs.JobID) (jobs.Snapshot, error) {
	return service.value, service.err
}

func TestDocumentationRoutesExposeExactAuthenticatedBehavior(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service, jobReader := documentationRouteFixture(t)
	handler := documentationRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service, jobReader)
	cookie := sessionCookie(authenticated.Token.Reveal())
	kbID := service.detail.Run.KnowledgeBaseID.String()

	unauthorized := authRequest(t, handler, http.MethodGet, documentationRunsPath, "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}

	generatePath := "/api/v1/knowledge-bases/" + kbID + "/generate"
	missingCSRF := authRequest(t, handler, http.MethodPost, generatePath, `{"expected_version":2}`, map[string]string{
		"Cookie": cookie, "Idempotency-Key": "generate-one",
	})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF=%d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	blankKey := authRequest(t, handler, http.MethodPost, generatePath, `{"expected_version":2}`, map[string]string{
		"Cookie": cookie, csrfHeaderName: authenticated.CSRFToken, "Idempotency-Key": " \t ",
	})
	if blankKey.Code != http.StatusUnprocessableEntity || problemDetail(t, blankKey) != "Idempotency-Key is required." {
		t.Fatalf("blank idempotency key=%d %s", blankKey.Code, blankKey.Body.String())
	}
	invalid := authRequest(t, handler, http.MethodPost, generatePath, `{"expected_version":2,"private":"body-secret"}`, map[string]string{
		"Cookie": cookie, csrfHeaderName: authenticated.CSRFToken, "Idempotency-Key": "generate-one",
	})
	if invalid.Code != http.StatusUnprocessableEntity || strings.Contains(invalid.Body.String(), "body-secret") {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
	generated := authRequest(t, handler, http.MethodPost, generatePath, `{"expected_version":2}`, map[string]string{
		"Cookie": cookie, csrfHeaderName: authenticated.CSRFToken, "Idempotency-Key": "  generate-one  ",
	})
	if generated.Code != http.StatusAccepted || !strings.Contains(generated.Body.String(), service.detail.Run.PrepareJobID.String()) ||
		service.requestedKnowledgeBaseID != service.detail.Run.KnowledgeBaseID || service.requestedExpectedVersion != 2 ||
		service.requestedActor != docgen.ID(authenticated.Session.Operator.ID) || service.requestedKey != "generate-one" {
		t.Fatalf("generated=%d %s request=%s/%d/%s/%q", generated.Code, generated.Body.String(),
			service.requestedKnowledgeBaseID.String(), service.requestedExpectedVersion, service.requestedActor.String(), service.requestedKey)
	}

	listed := authRequest(t, handler, http.MethodGet, documentationRunsPath+"?knowledge_base_id="+kbID, "", map[string]string{"Cookie": cookie})
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"status":"published"`) ||
		!strings.Contains(listed.Body.String(), `"role":"documentation_planner"`) ||
		!strings.Contains(listed.Body.String(), `"total_tokens":25`) ||
		!strings.Contains(listed.Body.String(), `"truncated_tool_results":3`) || service.listedKnowledgeBaseID == nil ||
		*service.listedKnowledgeBaseID != service.detail.Run.KnowledgeBaseID || service.listedLimit != 50 || service.listedOffset != 0 {
		t.Fatalf("listed=%d %s selection=%v/%d/%d", listed.Code, listed.Body.String(), service.listedKnowledgeBaseID, service.listedLimit, service.listedOffset)
	}
	fetched := authRequest(t, handler, http.MethodGet, documentationRunsPath+"/"+service.detail.Run.ID.String(), "", map[string]string{"Cookie": cookie})
	if fetched.Code != http.StatusOK || !strings.Contains(fetched.Body.String(), `"content_sha256":"`+strings.Repeat("33", 32)+`"`) {
		t.Fatalf("fetched=%d %s", fetched.Code, fetched.Body.String())
	}

	wikiPath := "/api/v1/knowledge-bases/" + kbID + "/wiki"
	wiki := authRequest(t, handler, http.MethodGet, wikiPath+"?version_id="+service.wiki.Version.ID.String()+"&slug=overview", "", map[string]string{"Cookie": cookie})
	if wiki.Code != http.StatusOK || !strings.Contains(wiki.Body.String(), `"resource":"repo://`) ||
		!strings.Contains(wiki.Body.String(), `"source_fingerprint":"`+strings.Repeat("11", 32)+`"`) ||
		service.wikiVersionID == nil || *service.wikiVersionID != service.wiki.Version.ID || service.wikiSlug == nil || *service.wikiSlug != "overview" {
		t.Fatalf("wiki=%d %s version=%v slug=%v", wiki.Code, wiki.Body.String(), service.wikiVersionID, service.wikiSlug)
	}
	versions := authRequest(t, handler, http.MethodGet, wikiPath+"/versions", "", map[string]string{"Cookie": cookie})
	if versions.Code != http.StatusOK || !strings.Contains(versions.Body.String(), `"manifest_sha256":"`+strings.Repeat("55", 32)+`"`) {
		t.Fatalf("versions=%d %s", versions.Code, versions.Body.String())
	}
	exported := authRequest(t, handler, http.MethodGet, wikiPath+"/export", "", map[string]string{"Cookie": cookie})
	wantDisposition := `attachment; filename="knowledge-base-` + kbID + `-` + service.wiki.Version.ID.String() + `.zip"`
	if exported.Code != http.StatusOK || exported.Header().Get("Content-Type") != "application/zip" ||
		exported.Header().Get("Cache-Control") != "no-store" || exported.Header().Get("Content-Disposition") != wantDisposition ||
		exported.Body.String() != string(service.export) || service.exportedVersionID == nil || *service.exportedVersionID != service.wiki.Version.ID {
		t.Fatalf("exported=%d headers=%v body=%q selected=%v", exported.Code, exported.Header(), exported.Body.String(), service.exportedVersionID)
	}

	evidencePath := wikiPath + "/evidence?version_id=" + service.wiki.Version.ID.String() + "&slug=overview&claim_id=entry&evidence_id=entry-source"
	for _, authorized := range []bool{false, true} {
		headers := map[string]string{}
		if authorized {
			headers["Cookie"] = cookie
		}
		response := authRequest(t, handler, http.MethodGet, evidencePath, "", headers)
		if !authorized && response.Code != http.StatusUnauthorized || authorized && (response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "exact evidence")) {
			t.Fatalf("evidence authorization=%t %d %s", authorized, response.Code, response.Body.String())
		}
	}
	invalidVersion := authRequest(t, handler, http.MethodGet, wikiPath+"?version_id=not-a-uuid", "", map[string]string{"Cookie": cookie})
	invalidLimit := authRequest(t, handler, http.MethodGet, documentationRunsPath+"?limit=101", "", map[string]string{"Cookie": cookie})
	if invalidVersion.Code != http.StatusUnprocessableEntity || invalidLimit.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid query=%d/%d", invalidVersion.Code, invalidLimit.Code)
	}
}

func TestDocumentationRoutesMapErrorsWithoutDisclosingDetails(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service, jobReader := documentationRouteFixture(t)
	handler := documentationRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service, jobReader)
	headers := map[string]string{"Cookie": sessionCookie(authenticated.Token.Reveal())}
	runPath := documentationRunsPath + "/" + service.detail.Run.ID.String()
	wikiPath := "/api/v1/knowledge-bases/" + service.detail.Run.KnowledgeBaseID.String() + "/wiki"

	for _, value := range []struct {
		err    error
		path   string
		status int
		detail string
	}{
		{docgen.ErrNotFound, runPath, http.StatusNotFound, "Documentation resource was not found."},
		{docgen.ErrConflict, runPath, http.StatusConflict, "Documentation state conflicts with the request."},
		{docgen.ErrValidation, wikiPath + "?slug=INVALID", http.StatusUnprocessableEntity, "Documentation content is invalid."},
		{errors.New("database-password-sentinel"), runPath, http.StatusInternalServerError, "The request could not be completed."},
	} {
		service.err = value.err
		response := authRequest(t, handler, http.MethodGet, value.path, "", headers)
		if response.Code != value.status || problemDetail(t, response) != value.detail || strings.Contains(response.Body.String(), "sentinel") {
			t.Fatalf("%s=%d %s", value.path, response.Code, response.Body.String())
		}
	}
	service.err = idempotency.ErrConflict
	generatePath := "/api/v1/knowledge-bases/" + service.detail.Run.KnowledgeBaseID.String() + "/generate"
	conflict := authRequest(t, handler, http.MethodPost, generatePath, `{"expected_version":2}`, map[string]string{
		"Cookie": headers["Cookie"], csrfHeaderName: authenticated.CSRFToken, "Idempotency-Key": "conflict",
	})
	if conflict.Code != http.StatusConflict || problemDetail(t, conflict) != "Idempotency key conflicts with a different request." {
		t.Fatalf("idempotency conflict=%d %s", conflict.Code, conflict.Body.String())
	}
}

func TestDocumentationOpenAPIHasSevenExactOperationsAndSchemas(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service, jobReader := documentationRouteFixture(t)
	handler := documentationRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service, jobReader)
	document := openAPIDocument(t, handler)
	paths := document["paths"].(map[string]any)
	want := map[string]map[string]string{
		"/api/v1/knowledge-bases/{knowledge_base_id}/wiki/evidence": {"get": "get_wiki_evidence"},
		"/api/v1/knowledge-bases/{knowledge_base_id}/generate":      {"post": "generate_documentation_api_v1_knowledge_bases__knowledge_base_id__generate_post"},
		"/api/v1/runs":          {"get": "list_documentation_runs_api_v1_runs_get"},
		"/api/v1/runs/{run_id}": {"get": "get_documentation_run_api_v1_runs__run_id__get"},
		"/api/v1/knowledge-bases/{knowledge_base_id}/wiki":          {"get": "get_wiki_api_v1_knowledge_bases__knowledge_base_id__wiki_get"},
		"/api/v1/knowledge-bases/{knowledge_base_id}/wiki/versions": {"get": "list_wiki_versions_api_v1_knowledge_bases__knowledge_base_id__wiki_versions_get"},
		"/api/v1/knowledge-bases/{knowledge_base_id}/wiki/export":   {"get": "export_wiki_api_v1_knowledge_bases__knowledge_base_id__wiki_export_get"},
	}
	for path, methods := range want {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing OpenAPI path %s", path)
		}
		for method, operationID := range methods {
			operation := item[method].(map[string]any)
			if operation["operationId"] != operationID {
				t.Fatalf("%s %s operation=%v", method, path, operation["operationId"])
			}
		}
	}
	generate := paths["/api/v1/knowledge-bases/{knowledge_base_id}/generate"].(map[string]any)["post"].(map[string]any)
	parameters := generate["parameters"].([]any)
	seenCSRF, seenKey := false, false
	for _, raw := range parameters {
		parameter := raw.(map[string]any)
		switch parameter["name"] {
		case "X-CSRF-Token":
			seenCSRF = true
		case "Idempotency-Key":
			seenKey = parameter["required"] == true
		case "Content-Type":
			t.Fatalf("raw Content-Type escaped into OpenAPI: %v", parameter)
		}
	}
	if !seenCSRF || !seenKey || generate["requestBody"].(map[string]any)["required"] != true {
		t.Fatalf("generate contract parameters=%v body=%v", parameters, generate["requestBody"])
	}
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{
		"GenerateDocumentationRequest", "DocumentationRunResponse", "DocumentationPageResponse",
		"DocumentationModelUsageResponse", "WikiResponse", "WikiVersionResponse", "WikiPageResponse",
	} {
		if components[name] == nil {
			t.Fatalf("missing component %s; available=%v", name, components)
		}
	}
	export := paths["/api/v1/knowledge-bases/{knowledge_base_id}/wiki/export"].(map[string]any)["get"].(map[string]any)
	response := export["responses"].(map[string]any)["200"].(map[string]any)
	if response["content"].(map[string]any)["application/zip"] == nil {
		t.Fatalf("export response=%v", response)
	}
}

func documentationRoutesTestHandler(t *testing.T, sessions auth.SessionService, service DocumentationService, jobReader DocumentationJobService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ref0 test", "test")
	config.CreateHooks, config.Transformers = nil, nil
	api := humago.New(mux, config)
	RegisterDocumentationRoutes(api, sessions, service, jobReader)
	return problemBoundary(mux)
}

func documentationRouteFixture(t *testing.T) (*fakeDocumentationService, *fakeDocumentationJobService) {
	t.Helper()
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	runID := mustDocumentationID(t, "10000000-0000-4000-8000-000000000001")
	kbID := mustDocumentationID(t, "20000000-0000-4000-8000-000000000002")
	sourceID := mustDocumentationID(t, "30000000-0000-4000-8000-000000000003")
	revisionID := mustDocumentationID(t, "40000000-0000-4000-8000-000000000004")
	endpointID := mustDocumentationID(t, "50000000-0000-4000-8000-000000000005")
	profileID := mustDocumentationID(t, "60000000-0000-4000-8000-000000000006")
	profileVersionID := mustDocumentationID(t, "70000000-0000-4000-8000-000000000007")
	pageID := mustDocumentationID(t, "80000000-0000-4000-8000-000000000008")
	pageJobID := jobs.JobID(mustDocumentationID(t, "90000000-0000-4000-8000-000000000009"))
	wikiVersionID := docgen.WikiVersionID(mustDocumentationID(t, "a0000000-0000-4000-8000-00000000000a"))
	prepareJobID := jobs.JobID(mustDocumentationID(t, "b0000000-0000-4000-8000-00000000000b"))
	credentialVersion := 1
	completed := now.Add(time.Minute)
	fingerprint := [32]byte{}
	for index := range fingerprint {
		fingerprint[index] = 0x11
	}
	planDigest, submissionDigest := make([]byte, 32), make([]byte, 32)
	contentDigest, claimsDigest := make([]byte, 32), make([]byte, 32)
	for index := range planDigest {
		planDigest[index], submissionDigest[index] = 0x22, 0x44
		contentDigest[index], claimsDigest[index] = 0x33, 0x66
	}
	detail := docgen.RunDetail{Run: docgen.Run{
		ID: docgen.RunID(runID), KnowledgeBaseID: kbID, Status: docgen.RunPublished,
		PrepareJobID: prepareJobID, KnowledgeBaseVersion: 2, Instructions: "Use exact evidence.", Language: "en",
		Sources: []docgen.CapturedSource{{SourceID: sourceID, RevisionID: revisionID, Fingerprint: fingerprint, Commit: strings.Repeat("a", 40), Kind: "REPOSITORY"}},
		Models: []docgen.CapturedModel{{
			Role: providerdomain.DocumentationPlanner, ProfileID: providerdomain.ProfileID(profileID),
			ProfileVersionID: providerdomain.ProfileVersionID(profileVersionID), ProfileVersion: 1,
			EndpointID: providerdomain.EndpointID(endpointID), EndpointConfigurationVersion: 1,
			CredentialVersion: &credentialVersion, ReasoningEffort: providerdomain.EffortNone, MaxConcurrentTasks: 1,
		}},
		PlanDigest: planDigest, PublishedWikiVersionID: &wikiVersionID,
		CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed,
		PlannerUsage: docgen.ModelUsage{ModelCalls: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15, TruncatedToolResults: 2},
	}, Pages: []docgen.Page{{
		ID: docgen.PageID(pageID), RunID: docgen.RunID(runID), JobID: pageJobID, Position: 0,
		Target: docgen.PlannedPage{Slug: "overview", Title: "Overview", Purpose: "Explain the system.", RelatedPages: []string{}, SourceSeedPaths: []docgen.SourceSeedPath{{SourceID: sourceID, Path: "README.md"}}},
		Status: docgen.PageComplete, SubmissionDigest: submissionDigest, ContentSHA256: contentDigest, ClaimsSHA256: claimsDigest,
		AttemptCount: 1, CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed,
		Usage: docgen.ModelUsage{ModelCalls: 1, InputTokens: 8, OutputTokens: 2, TotalTokens: 10, TruncatedToolResults: 1},
	}}}
	manifest := [32]byte{}
	for index := range manifest {
		manifest[index] = 0x55
	}
	version := docgen.WikiVersion{
		ID: wikiVersionID, KnowledgeBaseID: kbID, DocumentationRunID: docgen.RunID(runID),
		ArtifactKey:    "knowledge-bases/" + kbID.String() + "/wiki/" + wikiVersionID.String(),
		ManifestSHA256: manifest, PageCount: 1, CreatedAt: completed, PublishedAt: completed,
	}
	one := 1
	wiki := docgen.WikiView{
		Version: version,
		Pages:   []docgen.WikiPageSummary{{Slug: "overview", Title: "Overview", Description: "System overview.", PageType: "Concept"}},
		Page: &docgen.PublishedWikiPage{
			Summary:  docgen.WikiPageSummary{Slug: "overview", Title: "Overview", Description: "System overview.", PageType: "Concept"},
			Markdown: "# Overview\n", ContentSHA256: [32]byte{1}, ClaimsSHA256: [32]byte{2},
			Claims: []docgen.PublishedClaim{{ID: "overview", Statement: "The system is documented.", Evidence: []docgen.PublishedEvidence{{
				ID: "evidence-1", Location: docgen.EvidenceLocation{
					SourceID: sourceID, SourceRevisionID: revisionID, SourceVersion: fingerprint,
					Commit: strings.Repeat("a", 40), Path: "README.md", StartLine: &one, EndLine: &one,
				},
			}}}},
		},
	}
	job := jobs.Snapshot{
		ID: prepareJobID, Type: jobs.PrepareRun, TargetType: "knowledge_base", TargetID: jobs.UUID(kbID),
		Status: jobs.Pending, MaxAttempts: 3, Result: nil, CreatedAt: now, UpdatedAt: now,
	}
	return &fakeDocumentationService{detail: detail, wiki: wiki, versions: []docgen.WikiVersion{version}, export: []byte("PK\x03\x04wiki")},
		&fakeDocumentationJobService{value: job}
}

func mustDocumentationID(t *testing.T, raw string) docgen.ID {
	t.Helper()
	id, err := docgen.ParseID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var _ DocumentationService = (*fakeDocumentationService)(nil)
var _ DocumentationJobService = (*fakeDocumentationJobService)(nil)

func (service *fakeDocumentationService) GetWikiEvidence(context.Context, docgen.ID, *docgen.WikiVersionID, string, string, string) (docgen.EvidenceExcerpt, error) {
	return docgen.EvidenceExcerpt{Text: "exact evidence", StartLine: 1, EndLine: 1}, nil
}
