package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	sourcedomain "github.com/cyr1en/ref0/internal/sources"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type fakeSourceRouteService struct {
	repository sourcedomain.Source
	website    sourcedomain.Source
	repoSync   sourcedomain.Sync
	webSync    sourcedomain.Sync
	revision   sourcedomain.Revision
	err        error

	listKnowledgeBaseID *sourcedomain.ID
	createRepository    sourcedomain.CreateRepository
	createWebsite       sourcedomain.CreateWebsite
	updateRepository    sourcedomain.UpdateRepository
	updateWebsite       sourcedomain.UpdateWebsite
	lifecycle           sourcedomain.ChangeLifecycle
	validation          sourcedomain.RequestOperation
	sync                sourcedomain.RequestOperation
	actor               sourcedomain.ID
	keys                []string
}

func (service *fakeSourceRouteService) List(_ context.Context, knowledgeBaseID *sourcedomain.ID) ([]sourcedomain.Source, error) {
	service.listKnowledgeBaseID = knowledgeBaseID
	return []sourcedomain.Source{service.repository, service.website}, service.err
}

func (service *fakeSourceRouteService) Get(context.Context, sourcedomain.ID) (sourcedomain.Source, error) {
	return service.repository, service.err
}

func (service *fakeSourceRouteService) CreateRepository(_ context.Context, command sourcedomain.CreateRepository, actor sourcedomain.ID, key string) (sourcedomain.Created, error) {
	service.createRepository, service.actor = command, actor
	service.keys = append(service.keys, key)
	return sourcedomain.Created{Source: service.repository, Validation: service.repoSync}, service.err
}

func (service *fakeSourceRouteService) CreateWebsite(_ context.Context, command sourcedomain.CreateWebsite, actor sourcedomain.ID, key string) (sourcedomain.Created, error) {
	service.createWebsite, service.actor = command, actor
	service.keys = append(service.keys, key)
	return sourcedomain.Created{Source: service.website, Validation: service.webSync}, service.err
}

func (service *fakeSourceRouteService) UpdateRepository(_ context.Context, command sourcedomain.UpdateRepository, actor sourcedomain.ID, key string) (sourcedomain.Source, error) {
	service.updateRepository, service.actor = command, actor
	service.keys = append(service.keys, key)
	return service.repository, service.err
}

func (service *fakeSourceRouteService) UpdateWebsite(_ context.Context, command sourcedomain.UpdateWebsite, actor sourcedomain.ID, key string) (sourcedomain.Source, error) {
	service.updateWebsite, service.actor = command, actor
	service.keys = append(service.keys, key)
	return service.website, service.err
}

func (service *fakeSourceRouteService) ChangeLifecycle(_ context.Context, command sourcedomain.ChangeLifecycle, actor sourcedomain.ID, key string) (sourcedomain.Source, error) {
	service.lifecycle, service.actor = command, actor
	service.keys = append(service.keys, key)
	return service.repository, service.err
}

func (service *fakeSourceRouteService) RequestValidation(_ context.Context, command sourcedomain.RequestOperation, actor sourcedomain.ID, key string) (sourcedomain.Sync, error) {
	service.validation, service.actor = command, actor
	service.keys = append(service.keys, key)
	return service.repoSync, service.err
}

func (service *fakeSourceRouteService) RequestSync(_ context.Context, command sourcedomain.RequestOperation, actor sourcedomain.ID, key string) (sourcedomain.Sync, error) {
	service.sync, service.actor = command, actor
	service.keys = append(service.keys, key)
	return service.webSync, service.err
}

func (service *fakeSourceRouteService) ListRevisions(context.Context, sourcedomain.ID) ([]sourcedomain.Revision, error) {
	return []sourcedomain.Revision{service.revision}, service.err
}

func (service *fakeSourceRouteService) ListSyncs(context.Context, sourcedomain.ID) ([]sourcedomain.Sync, error) {
	return []sourcedomain.Sync{service.repoSync, service.webSync}, service.err
}

func TestSourceRoutesExposeAllTenAuthenticatedOperations(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service := sourceRouteService(t)
	handler := sourceRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service)
	cookie := sessionCookie(authenticated.Token.Reveal())
	headers := map[string]string{"Cookie": cookie, csrfHeaderName: authenticated.CSRFToken}

	unauthorized := authRequest(t, handler, http.MethodGet, sourcesPath, "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	listPath := sourcesPath + "?knowledge_base_id=" + service.repository.KnowledgeBaseID.String()
	listed := authRequest(t, handler, http.MethodGet, listPath, "", map[string]string{"Cookie": cookie})
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"kind":"repository"`) ||
		!strings.Contains(listed.Body.String(), `"kind":"website"`) || service.listKnowledgeBaseID == nil ||
		*service.listKnowledgeBaseID != service.repository.KnowledgeBaseID {
		t.Fatalf("listed=%d %s selected=%v", listed.Code, listed.Body.String(), service.listKnowledgeBaseID)
	}

	repositoryBody := `{"knowledge_base_id":"` + service.repository.KnowledgeBaseID.String() +
		`","name":"  Ｄocs Straße  ","privacy":"public","remote_url":"https://GitHub.com/Org/Repo.git/",` +
		`"ref_kind":"branch","ref_value":"main","include_patterns":["/docs/"],"exclude_patterns":["tmp/**"]}`
	repositoryHeaders := cloneHeaders(headers, " repo-create ")
	createdRepository := authRequest(t, handler, http.MethodPost, sourcesPath+"/repositories", repositoryBody, repositoryHeaders)
	if createdRepository.Code != http.StatusCreated ||
		!strings.Contains(createdRepository.Body.String(), `"remote_url":"https://github.com/Org/Repo.git"`) ||
		!strings.Contains(createdRepository.Body.String(), `"validation":`) {
		t.Fatalf("created repository=%d %s", createdRepository.Code, createdRepository.Body.String())
	}
	repository := service.createRepository.Configuration
	if repository.Name.Display != "Docs Straße" || repository.Name.Key != "docs strasse" ||
		repository.Remote.URL != "https://github.com/Org/Repo.git" ||
		len(repository.IncludePatterns) != 1 || repository.IncludePatterns[0] != "docs/" ||
		service.keys[len(service.keys)-1] != "repo-create" ||
		service.actor != sourcedomain.ID(authenticated.Session.Operator.ID) {
		t.Fatalf("repository command=%+v key=%q actor=%s", service.createRepository, service.keys[len(service.keys)-1], service.actor)
	}

	websiteBody := `{"knowledge_base_id":"` + service.website.KnowledgeBaseID.String() +
		`","name":"Docs site","privacy":"public","root_url":"https://DOCS.example/start/",` +
		`"acquisition_mode":"tinyfish_crawl","tinyfish_credential_id":"` + service.website.Website.TinyFishCredentialID.String() + `"}`
	createdWebsite := authRequest(t, handler, http.MethodPost, sourcesPath+"/websites", websiteBody, cloneHeaders(headers, "web-create"))
	if createdWebsite.Code != http.StatusCreated || !strings.Contains(createdWebsite.Body.String(), `"acquisition_mode":"tinyfish_crawl"`) {
		t.Fatalf("created website=%d %s", createdWebsite.Code, createdWebsite.Body.String())
	}
	website := service.createWebsite.Configuration
	if website.Remote.URL != "https://docs.example/start/" || website.Limits != sourcedomain.DefaultCrawlLimits() ||
		website.AcquisitionMode != sourcedomain.TinyFishCrawl || website.TinyFishCredentialID == nil {
		t.Fatalf("website command=%+v", service.createWebsite)
	}

	resourcePath := sourcesPath + "/" + service.repository.ID.String()
	fetched := authRequest(t, handler, http.MethodGet, resourcePath, "", map[string]string{"Cookie": cookie})
	if fetched.Code != http.StatusOK || !strings.Contains(fetched.Body.String(), `"repository_path":"Org/Repo.git"`) ||
		!strings.Contains(fetched.Body.String(), `"credential_id":null`) {
		t.Fatalf("fetched=%d %s", fetched.Code, fetched.Body.String())
	}

	updatedRepository := authRequest(t, handler, http.MethodPatch, resourcePath,
		`{"expected_version":2,"name":"Repo renamed","privacy":"public","remote_url":"https://git.example/org/repo",`+
			`"ref_kind":"commit","ref_value":"0123456789ABCDEF0123456789ABCDEF01234567"}`,
		cloneHeaders(headers, "repo-update"))
	if updatedRepository.Code != http.StatusOK || service.updateRepository.ExpectedVersion != 2 ||
		service.updateRepository.Configuration.Reference.Value != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("updated repository=%d %s command=%+v", updatedRepository.Code, updatedRepository.Body.String(), service.updateRepository)
	}

	updatedWebsite := authRequest(t, handler, http.MethodPatch, resourcePath,
		`{"expected_version":3,"name":"JSON","privacy":"public","root_url":"https://api.example/data",`+
			`"max_pages":1,"max_depth":0,"acquisition_mode":"direct_json_api"}`,
		cloneHeaders(headers, "web-update"))
	if updatedWebsite.Code != http.StatusOK || service.updateWebsite.ExpectedVersion != 3 ||
		service.updateWebsite.Configuration.AcquisitionMode != sourcedomain.DirectJSONAPI ||
		service.updateWebsite.Configuration.Limits.MaxPages != 1 {
		t.Fatalf("updated website=%d %s command=%+v", updatedWebsite.Code, updatedWebsite.Body.String(), service.updateWebsite)
	}

	lifecycle := authRequest(t, handler, http.MethodPost, resourcePath+"/lifecycle",
		`{"expected_version":4,"lifecycle":"disabled"}`, cloneHeaders(headers, "lifecycle"))
	if lifecycle.Code != http.StatusOK || service.lifecycle.ExpectedVersion != 4 || service.lifecycle.Lifecycle != sourcedomain.Disabled {
		t.Fatalf("lifecycle=%d %s command=%+v", lifecycle.Code, lifecycle.Body.String(), service.lifecycle)
	}
	validated := authRequest(t, handler, http.MethodPost, resourcePath+"/validate",
		`{"expected_version":5}`, cloneHeaders(headers, "validate"))
	if validated.Code != http.StatusAccepted || service.validation.ExpectedVersion != 5 ||
		!strings.Contains(validated.Body.String(), `"captured_remote_url":`) {
		t.Fatalf("validation=%d %s command=%+v", validated.Code, validated.Body.String(), service.validation)
	}
	synced := authRequest(t, handler, http.MethodPost, resourcePath+"/sync",
		`{"expected_version":6}`, cloneHeaders(headers, "sync"))
	if synced.Code != http.StatusAccepted || service.sync.ExpectedVersion != 6 ||
		!strings.Contains(synced.Body.String(), `"captured_root_url":`) {
		t.Fatalf("sync=%d %s command=%+v", synced.Code, synced.Body.String(), service.sync)
	}

	revisions := authRequest(t, handler, http.MethodGet, resourcePath+"/revisions", "", map[string]string{"Cookie": cookie})
	if revisions.Code != http.StatusOK || !strings.Contains(revisions.Body.String(), strings.Repeat("ab", 32)) ||
		!strings.Contains(revisions.Body.String(), strings.Repeat("cd", 32)) {
		t.Fatalf("revisions=%d %s", revisions.Code, revisions.Body.String())
	}
	syncs := authRequest(t, handler, http.MethodGet, resourcePath+"/syncs", "", map[string]string{"Cookie": cookie})
	if syncs.Code != http.StatusOK || !strings.Contains(syncs.Body.String(), `"captured_remote_url":`) ||
		!strings.Contains(syncs.Body.String(), `"captured_root_url":`) {
		t.Fatalf("syncs=%d %s", syncs.Code, syncs.Body.String())
	}
}

func TestSourceUpdateUnionAppliesWebsiteDefaultsBeforeDomainValidation(t *testing.T) {
	var request sourceUpdateRequest
	if err := json.Unmarshal([]byte(`{"expected_version":3,"name":"JSON","privacy":"public","root_url":"https://api.example/data","max_pages":1,"max_depth":0,"acquisition_mode":"direct_json_api"}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.website == nil {
		t.Fatal("website update was not selected")
	}
	configuration, err := websiteSourceConfiguration(request.website.configuration())
	if err != nil {
		t.Fatalf("website configuration: %v; request=%+v", err, request.website)
	}
	if configuration.Limits.MaxPages != 1 || configuration.Limits.MaxDepth != 0 || configuration.AcquisitionMode != sourcedomain.DirectJSONAPI {
		t.Fatalf("configuration=%+v", configuration)
	}
}

func TestSourceRoutesValidateAndSanitizeFailures(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service := sourceRouteService(t)
	handler := sourceRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service)
	headers := map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()), csrfHeaderName: authenticated.CSRFToken,
		"Idempotency-Key": "request-one",
	}
	body := `{"knowledge_base_id":"` + service.repository.KnowledgeBaseID.String() +
		`","name":"Repo","privacy":"public","remote_url":"https://git.example/org/repo",` +
		`"ref_kind":"branch","ref_value":"main"}`
	missingCSRF := authRequest(t, handler, http.MethodPost, sourcesPath+"/repositories", body,
		map[string]string{"Cookie": headers["Cookie"], "Idempotency-Key": "one"})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF=%d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	invalid := authRequest(t, handler, http.MethodPost, sourcesPath+"/repositories",
		strings.Replace(body, `"privacy":"public"`, `"privacy":"public","credential_username":"user","credential_id":"`+service.repository.ID.String()+`"`, 1), headers)
	if invalid.Code != http.StatusUnprocessableEntity || problemDetail(t, invalid) != "Source configuration is invalid." {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
	secret := "inline-source-secret-sentinel"
	unknown := authRequest(t, handler, http.MethodPost, sourcesPath+"/repositories",
		strings.TrimSuffix(body, "}")+`,"secret":"`+secret+`"}`, headers)
	if unknown.Code != http.StatusUnprocessableEntity || strings.Contains(unknown.Body.String(), secret) {
		t.Fatalf("unknown=%d %s", unknown.Code, unknown.Body.String())
	}
	nullPatterns := authRequest(t, handler, http.MethodPost, sourcesPath+"/repositories",
		strings.TrimSuffix(body, "}")+`,"include_patterns":null}`, headers)
	if nullPatterns.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null patterns=%d %s", nullPatterns.Code, nullPatterns.Body.String())
	}
	websiteBody := `{"knowledge_base_id":"` + service.website.KnowledgeBaseID.String() +
		`","name":"Site","privacy":"public","root_url":"https://docs.example/","max_pages":null}`
	nullWebsiteDefault := authRequest(t, handler, http.MethodPost, sourcesPath+"/websites", websiteBody, headers)
	if nullWebsiteDefault.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null website default=%d %s", nullWebsiteDefault.Code, nullWebsiteDefault.Body.String())
	}

	path := sourcesPath + "/" + service.repository.ID.String()
	service.err = sourcedomain.ErrNotFound
	missing := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": headers["Cookie"]})
	if missing.Code != http.StatusNotFound || problemDetail(t, missing) != "Source resource not found." {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
	service.err = sourcedomain.ErrConflict
	conflict := authRequest(t, handler, http.MethodPost, path+"/sync", `{"expected_version":1}`, headers)
	if conflict.Code != http.StatusConflict || problemDetail(t, conflict) != "Source resource state conflicts with the request." {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	service.err = idempotency.ErrConflict
	idempotencyConflict := authRequest(t, handler, http.MethodPost, path+"/sync", `{"expected_version":1}`, headers)
	if idempotencyConflict.Code != http.StatusConflict || problemDetail(t, idempotencyConflict) != "Idempotency key conflicts with a different request." {
		t.Fatalf("idempotency conflict=%d %s", idempotencyConflict.Code, idempotencyConflict.Body.String())
	}
	service.err = errors.New("database-source-password-sentinel")
	failed := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": headers["Cookie"]})
	if failed.Code != http.StatusInternalServerError || problemDetail(t, failed) != "The request could not be completed." ||
		strings.Contains(failed.Body.String(), "sentinel") {
		t.Fatalf("failed=%d %s", failed.Code, failed.Body.String())
	}
}

func TestSourceOpenAPIMatchesOracleOperationsAndUnions(t *testing.T) {
	handler := sourceRoutesTestHandler(t, &fakeSessionService{}, sourceRouteService(t))
	document := openAPIDocument(t, handler)
	paths := document["paths"].(map[string]any)
	want := map[string]map[string]string{
		sourcesPath:                   {"get": "list_sources_api_v1_sources_get"},
		sourcesPath + "/repositories": {"post": "create_repository_source_api_v1_sources_repositories_post"},
		sourcesPath + "/websites":     {"post": "create_website_source_api_v1_sources_websites_post"},
		sourcesPath + "/{source_id}": {
			"get": "get_source_api_v1_sources__source_id__get", "patch": "update_source_api_v1_sources__source_id__patch",
		},
		sourcesPath + "/{source_id}/lifecycle": {"post": "change_source_lifecycle_api_v1_sources__source_id__lifecycle_post"},
		sourcesPath + "/{source_id}/validate":  {"post": "request_source_validation_api_v1_sources__source_id__validate_post"},
		sourcesPath + "/{source_id}/sync":      {"post": "request_source_sync_api_v1_sources__source_id__sync_post"},
		sourcesPath + "/{source_id}/revisions": {"get": "list_source_revisions_api_v1_sources__source_id__revisions_get"},
		sourcesPath + "/{source_id}/syncs":     {"get": "list_source_syncs_api_v1_sources__source_id__syncs_get"},
	}
	count := 0
	for path, methods := range want {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing path %s", path)
		}
		for method, operationID := range methods {
			operation, ok := item[method].(map[string]any)
			if !ok || operation["operationId"] != operationID {
				t.Fatalf("%s %s operation=%v", method, path, operation["operationId"])
			}
			count++
		}
	}
	if count != 10 {
		t.Fatalf("operation count=%d", count)
	}
	patch := paths[sourcesPath+"/{source_id}"].(map[string]any)["patch"].(map[string]any)
	requestBody := patch["requestBody"].(map[string]any)
	schema := requestBody["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if alternatives, ok := schema["anyOf"].([]any); !ok || len(alternatives) != 2 {
		t.Fatalf("patch schema=%v", schema)
	}
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{
		"CreateRepositorySourceRequest", "CreateWebsiteSourceRequest",
		"UpdateRepositorySourceRequest", "UpdateWebsiteSourceRequest",
		"RepositorySourceResponse", "WebsiteSourceResponse", "SourceSyncResponse",
		"WebsiteSourceSyncResponse", "SourceCreatedResponse", "SourceRevisionResponse",
	} {
		if components[name] == nil {
			t.Fatalf("missing OpenAPI schema %s; components=%v", name, components)
		}
	}
	repositorySchema := components["CreateRepositorySourceRequest"].(map[string]any)
	if repositorySchema["additionalProperties"] != false {
		t.Fatalf("repository additionalProperties=%v", repositorySchema["additionalProperties"])
	}
	if includeType := repositorySchema["properties"].(map[string]any)["include_patterns"].(map[string]any)["type"]; includeType != "array" {
		t.Fatalf("include_patterns type=%v", includeType)
	}
	required := repositorySchema["required"].([]any)
	for _, field := range []string{"knowledge_base_id", "name", "privacy", "remote_url", "ref_kind", "ref_value"} {
		found := false
		for _, value := range required {
			found = found || value == field
		}
		if !found {
			t.Fatalf("repository required fields=%v; missing=%s", required, field)
		}
	}
	websiteProperties := components["CreateWebsiteSourceRequest"].(map[string]any)["properties"].(map[string]any)
	for field, want := range map[string]any{
		"max_concurrency": float64(4), "max_pages": float64(500), "max_depth": float64(3),
		"acquisition_mode": "builtin_crawl",
	} {
		property := websiteProperties[field].(map[string]any)
		if property["default"] != want {
			t.Fatalf("%s default=%v", field, property["default"])
		}
	}
	for name, fields := range map[string][]string{
		"RepositorySourceResponse":  {"id", "knowledge_base_id", "kind", "name", "privacy", "remote_url", "lifecycle", "version"},
		"WebsiteSourceResponse":     {"id", "knowledge_base_id", "kind", "name", "privacy", "root_url", "lifecycle", "version"},
		"SourceSyncResponse":        {"id", "source_id", "job_id", "kind", "captured_privacy", "captured_remote_url", "status"},
		"WebsiteSourceSyncResponse": {"id", "source_id", "job_id", "kind", "captured_privacy", "captured_root_url", "status"},
	} {
		properties := components[name].(map[string]any)["properties"].(map[string]any)
		for _, field := range fields {
			if properties[field] == nil {
				t.Fatalf("%s missing %s: %v", name, field, components[name])
			}
		}
	}
}

func sourceRoutesTestHandler(t *testing.T, sessions auth.SessionService, service SourceService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ref0 test", "test")
	config.CreateHooks, config.Transformers = nil, nil
	api := humago.New(mux, config)
	RegisterSourceRoutes(api, sessions, service)
	return problemBoundary(mux)
}

func sourceRouteService(t *testing.T) *fakeSourceRouteService {
	t.Helper()
	now := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	repositoryID := mustSourceID(t, "10000000-0000-4000-8000-000000000101")
	websiteID := mustSourceID(t, "10000000-0000-4000-8000-000000000102")
	knowledgeBaseID := mustSourceID(t, "20000000-0000-4000-8000-000000000101")
	tinyFishID := mustSourceID(t, "21000000-0000-4000-8000-000000000101")
	revisionID := mustSourceID(t, "40000000-0000-4000-8000-000000000101")
	requestedBy := mustSourceID(t, "50000000-0000-4000-8000-000000000101")
	repository := sourcedomain.Source{
		ID: repositoryID, KnowledgeBaseID: knowledgeBaseID, Kind: sourcedomain.Repository, Name: "Docs Repo",
		Privacy: sourcedomain.Public, Lifecycle: sourcedomain.Active, Health: sourcedomain.Healthy,
		Version: 2, ConfigurationVersion: 1, CreatedAt: now, UpdatedAt: now,
		Repository: &sourcedomain.RepositoryConfiguration{
			Name: sourcedomain.Name{Display: "Docs Repo", Key: "docs repo"}, Privacy: sourcedomain.Public,
			Remote:          sourcedomain.Remote{URL: "https://github.com/Org/Repo.git", Host: "github.com"},
			Reference:       sourcedomain.Reference{Kind: sourcedomain.Branch, Value: "main"},
			IncludePatterns: []string{"docs/**"}, ExcludePatterns: []string{},
		},
	}
	website := sourcedomain.Source{
		ID: websiteID, KnowledgeBaseID: knowledgeBaseID, Kind: sourcedomain.Website, Name: "Docs site",
		Privacy: sourcedomain.Public, Lifecycle: sourcedomain.Active, Health: sourcedomain.Unknown,
		Version: 1, ConfigurationVersion: 1, CreatedAt: now, UpdatedAt: now,
		Website: &sourcedomain.WebsiteConfiguration{
			Name: sourcedomain.Name{Display: "Docs site", Key: "docs site"}, Privacy: sourcedomain.Public,
			Remote: sourcedomain.Remote{URL: "https://docs.example/start/", Host: "docs.example"},
			Limits: sourcedomain.DefaultCrawlLimits(), AcquisitionMode: sourcedomain.TinyFishCrawl,
			TinyFishCredentialID: &tinyFishID,
		},
	}
	repoSyncID := mustSourceID(t, "30000000-0000-4000-8000-000000000101")
	webSyncID := mustSourceID(t, "30000000-0000-4000-8000-000000000102")
	repoSync := sourcedomain.Sync{
		ID: repoSyncID, SourceID: repositoryID, JobID: jobs.JobID(mustSourceID(t, "31000000-0000-4000-8000-000000000101")),
		Kind: sourcedomain.Validation, RequestedBy: &requestedBy, CapturedSourceVersion: 1, CapturedConfigurationVersion: 1,
		Repository: &sourcedomain.CapturedRepository{Privacy: sourcedomain.Public,
			Remote:    sourcedomain.Remote{URL: "https://github.com/Org/Repo.git", Host: "github.com"},
			Reference: sourcedomain.Reference{Kind: sourcedomain.Branch, Value: "main"}, IncludePatterns: []string{}, ExcludePatterns: []string{}},
		Status: sourcedomain.SyncPending, CreatedAt: now,
	}
	webSync := sourcedomain.Sync{
		ID: webSyncID, SourceID: websiteID, JobID: jobs.JobID(mustSourceID(t, "31000000-0000-4000-8000-000000000102")),
		Kind: sourcedomain.Synchronization, RequestedBy: &requestedBy, CapturedSourceVersion: 1, CapturedConfigurationVersion: 1,
		Website: &sourcedomain.CapturedWebsite{Privacy: sourcedomain.Public,
			Remote: sourcedomain.Remote{URL: "https://docs.example/start/", Host: "docs.example"},
			Limits: sourcedomain.DefaultCrawlLimits(), AcquisitionMode: sourcedomain.TinyFishCrawl, TinyFishCredentialID: &tinyFishID},
		CandidateRevisionID: &revisionID, Status: sourcedomain.SyncPending, CreatedAt: now,
	}
	var fingerprint, content [32]byte
	for index := range fingerprint {
		fingerprint[index], content[index] = 0xab, 0xcd
	}
	revision := sourcedomain.Revision{
		ID: revisionID, SourceID: websiteID, ObservedRef: sourcedomain.Reference{Kind: sourcedomain.Root, Value: "https://docs.example/start/"},
		NativeVersion: strings.Repeat("e", 64), Fingerprint: fingerprint, ArtifactKey: "source/website/snapshot", FileCount: 1,
		ByteCount: 42, IgnoredPaths: []string{}, CreatedAt: now, WebsitePages: []sourcedomain.PageCapture{{
			CanonicalURL: "https://docs.example/start/", ContentPath: "pages/page.md", ContentSHA256: content,
			EvidenceURI: "website:https://docs.example/start/", Freshness: "fresh",
		}},
	}
	return &fakeSourceRouteService{repository: repository, website: website, repoSync: repoSync, webSync: webSync, revision: revision}
}

func cloneHeaders(base map[string]string, idempotencyKey string) map[string]string {
	result := make(map[string]string, len(base)+1)
	for key, value := range base {
		result[key] = value
	}
	result["Idempotency-Key"] = idempotencyKey
	return result
}

func mustSourceID(t *testing.T, raw string) sourcedomain.ID {
	t.Helper()
	id, err := sourcedomain.ParseID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var _ SourceService = (*fakeSourceRouteService)(nil)
