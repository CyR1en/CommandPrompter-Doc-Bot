package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/knowledgebases"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

func TestKnowledgeBaseRoutesExposeExactSixAuthenticatedOperations(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	sessions := &fakeSessionService{session: authenticated.Session}
	value := knowledgeBaseRouteValue(t)
	jobID, err := jobs.ParseUUID("20000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeKnowledgeBaseRouteService{
		value: value, values: []knowledgebases.KnowledgeBase{value},
		deletion: knowledgebases.Deletion{KnowledgeBase: value, PurgeJobID: jobs.JobID(jobID)},
	}
	handler := knowledgeBaseRoutesTestHandler(t, sessions, service)
	cookie := sessionCookie(authenticated.Token.Reveal())
	csrf := authenticated.CSRFToken

	if response := authRequest(t, handler, http.MethodGet, knowledgeBasesPath, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list=%d %s", response.Code, response.Body.String())
	}
	listed := authRequest(t, handler, http.MethodGet, knowledgeBasesPath, "", map[string]string{"Cookie": cookie})
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), value.ID.String()) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}

	created := authRequest(t, handler, http.MethodPost, knowledgeBasesPath,
		`{"name":"  Ｄocs Straße  ","language":" en "}`,
		map[string]string{
			"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": " create-kb ",
		})
	if created.Code != http.StatusCreated {
		t.Fatalf("created=%d %s", created.Code, created.Body.String())
	}
	if service.create.Name.Display != "Docs Straße" || service.create.Name.Key != "docs strasse" ||
		service.create.Access != knowledgebases.Restricted || service.create.Instructions != "" ||
		service.create.Language != "en" || service.createKey != "create-kb" ||
		service.createActor != authenticated.Session.Operator.ID {
		t.Fatalf("create command=%+v key=%q actor=%s", service.create, service.createKey, service.createActor)
	}

	path := knowledgeBasesPath + "/" + value.ID.String()
	fetched := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": cookie})
	if fetched.Code != http.StatusOK || !strings.Contains(fetched.Body.String(), `"published_wiki_id":null`) {
		t.Fatalf("fetched=%d %s", fetched.Code, fetched.Body.String())
	}
	updated := authRequest(t, handler, http.MethodPatch, path,
		`{"expected_version":1,"name":" Renamed ","access":"public","instructions":"","language":" fr ","lifecycle":"archived"}`,
		map[string]string{
			"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": " update-kb ",
		})
	if updated.Code != http.StatusOK {
		t.Fatalf("updated=%d %s", updated.Code, updated.Body.String())
	}
	if service.update.KnowledgeBaseID != value.ID || service.update.ExpectedVersion != 1 ||
		service.update.Name == nil || service.update.Name.Display != "Renamed" ||
		service.update.Access == nil || *service.update.Access != knowledgebases.Public ||
		service.update.Instructions == nil || *service.update.Instructions != "" ||
		service.update.Language == nil || *service.update.Language != "fr" ||
		service.update.Lifecycle == nil || *service.update.Lifecycle != knowledgebases.Archived ||
		service.updateKey != "update-kb" {
		t.Fatalf("update command=%+v key=%q", service.update, service.updateKey)
	}

	deleted := authRequest(t, handler, http.MethodDelete, path,
		`{"expected_version":2,"confirmation_name":"Docs"}`,
		map[string]string{
			"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": " delete-kb ",
		})
	if deleted.Code != http.StatusAccepted || !strings.Contains(deleted.Body.String(), jobID.String()) ||
		service.delete.KnowledgeBaseID != value.ID || service.delete.ExpectedVersion != 2 ||
		service.delete.ConfirmationName != "Docs" || service.deleteKey != "delete-kb" {
		t.Fatalf("deleted=%d %s command=%+v key=%q", deleted.Code, deleted.Body.String(), service.delete, service.deleteKey)
	}
	restored := authRequest(t, handler, http.MethodPost, path+"/restore",
		`{"expected_version":3}`,
		map[string]string{
			"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": " restore-kb ",
		})
	if restored.Code != http.StatusOK || service.restore.KnowledgeBaseID != value.ID ||
		service.restore.ExpectedVersion != 3 || service.restoreKey != "restore-kb" {
		t.Fatalf("restored=%d %s command=%+v key=%q", restored.Code, restored.Body.String(), service.restore, service.restoreKey)
	}
}

func TestKnowledgeBaseRoutesValidateAndSanitizeErrors(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	value := knowledgeBaseRouteValue(t)
	service := &fakeKnowledgeBaseRouteService{value: value}
	handler := knowledgeBaseRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, service)
	headers := map[string]string{
		"Cookie":       sessionCookie(authenticated.Token.Reveal()),
		csrfHeaderName: authenticated.CSRFToken, "Idempotency-Key": "request-one",
	}

	missingCSRF := authRequest(t, handler, http.MethodPost, knowledgeBasesPath,
		`{"name":"Docs"}`, map[string]string{"Cookie": headers["Cookie"], "Idempotency-Key": "one"})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF=%d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	missingKey := authRequest(t, handler, http.MethodPost, knowledgeBasesPath,
		`{"name":"Docs"}`, map[string]string{"Cookie": headers["Cookie"], csrfHeaderName: headers[csrfHeaderName]})
	if missingKey.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing key=%d %s", missingKey.Code, missingKey.Body.String())
	}
	sentinel := "instruction-secret-sentinel"
	invalid := authRequest(t, handler, http.MethodPost, knowledgeBasesPath,
		`{"name":" ","instructions":"`+sentinel+`"}`, headers)
	if invalid.Code != http.StatusUnprocessableEntity || strings.Contains(invalid.Body.String(), sentinel) {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
	unknown := authRequest(t, handler, http.MethodPost, knowledgeBasesPath,
		`{"name":"Docs","unexpected":"`+sentinel+`"}`, headers)
	if unknown.Code != http.StatusUnprocessableEntity || strings.Contains(unknown.Body.String(), sentinel) {
		t.Fatalf("unknown=%d %s", unknown.Code, unknown.Body.String())
	}

	path := knowledgeBasesPath + "/" + value.ID.String()
	emptyUpdate := authRequest(t, handler, http.MethodPatch, path, `{"expected_version":1}`, headers)
	if emptyUpdate.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty update=%d %s", emptyUpdate.Code, emptyUpdate.Body.String())
	}
	service.getErr = knowledgebases.ErrNotFound
	missing := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": headers["Cookie"]})
	if missing.Code != http.StatusNotFound || problemDetail(t, missing) != "Knowledge base not found." {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
	service.getErr = nil
	service.updateErr = knowledgebases.ErrConflict
	conflict := authRequest(t, handler, http.MethodPatch, path,
		`{"expected_version":1,"instructions":"state-secret-sentinel"}`, headers)
	if conflict.Code != http.StatusConflict ||
		problemDetail(t, conflict) != "Knowledge base state conflicts with the request." ||
		strings.Contains(conflict.Body.String(), "state-secret-sentinel") {
		t.Fatalf("state conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	service.updateErr = idempotency.ErrConflict
	idempotencyConflict := authRequest(t, handler, http.MethodPatch, path,
		`{"expected_version":1,"instructions":"idempotency-secret-sentinel"}`, headers)
	if idempotencyConflict.Code != http.StatusConflict ||
		problemDetail(t, idempotencyConflict) != "Idempotency key conflicts with a different request." ||
		strings.Contains(idempotencyConflict.Body.String(), "idempotency-secret-sentinel") {
		t.Fatalf("idempotency conflict=%d %s", idempotencyConflict.Code, idempotencyConflict.Body.String())
	}
	service.updateErr = errors.New("database-password-sentinel")
	failed := authRequest(t, handler, http.MethodPatch, path,
		`{"expected_version":1,"instructions":"failure-secret-sentinel"}`, headers)
	if failed.Code != http.StatusInternalServerError || problemDetail(t, failed) != "The request could not be completed." ||
		strings.Contains(failed.Body.String(), "sentinel") {
		t.Fatalf("failure=%d %s", failed.Code, failed.Body.String())
	}
}

func TestKnowledgeBaseOpenAPIMatchesOracleOperations(t *testing.T) {
	handler := knowledgeBaseRoutesTestHandler(t, &fakeSessionService{}, &fakeKnowledgeBaseRouteService{})
	document := openAPIDocument(t, handler)
	paths := document["paths"].(map[string]any)
	want := map[string]map[string]string{
		knowledgeBasesPath: {
			"get":  "list_knowledge_bases_api_v1_knowledge_bases_get",
			"post": "create_knowledge_base_api_v1_knowledge_bases_post",
		},
		knowledgeBasesPath + "/{knowledge_base_id}": {
			"get":    "get_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__get",
			"patch":  "update_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__patch",
			"delete": "delete_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__delete",
		},
		knowledgeBasesPath + "/{knowledge_base_id}/restore": {
			"post": "restore_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__restore_post",
		},
	}
	for path, operations := range want {
		item := paths[path].(map[string]any)
		for method, operationID := range operations {
			operation := item[method].(map[string]any)
			if operation["operationId"] != operationID {
				t.Fatalf("%s %s operation=%v", method, path, operation["operationId"])
			}
		}
	}
}

type fakeKnowledgeBaseRouteService struct {
	value        knowledgebases.KnowledgeBase
	values       []knowledgebases.KnowledgeBase
	deletion     knowledgebases.Deletion
	create       knowledgebases.CreateCommand
	update       knowledgebases.UpdateCommand
	delete       knowledgebases.DeleteCommand
	restore      knowledgebases.RestoreCommand
	createActor  auth.OperatorID
	updateActor  auth.OperatorID
	deleteActor  auth.OperatorID
	restoreActor auth.OperatorID
	createKey    string
	updateKey    string
	deleteKey    string
	restoreKey   string
	listErr      error
	getErr       error
	createErr    error
	updateErr    error
	deleteErr    error
	restoreErr   error
}

func (service *fakeKnowledgeBaseRouteService) List(context.Context) ([]knowledgebases.KnowledgeBase, error) {
	return service.values, service.listErr
}

func (service *fakeKnowledgeBaseRouteService) Get(context.Context, knowledgebases.ID) (knowledgebases.KnowledgeBase, error) {
	return service.value, service.getErr
}

func (service *fakeKnowledgeBaseRouteService) Create(_ context.Context, command knowledgebases.CreateCommand, actor auth.OperatorID, key string) (knowledgebases.KnowledgeBase, error) {
	service.create, service.createActor, service.createKey = command, actor, key
	return service.value, service.createErr
}

func (service *fakeKnowledgeBaseRouteService) Update(_ context.Context, command knowledgebases.UpdateCommand, actor auth.OperatorID, key string) (knowledgebases.KnowledgeBase, error) {
	service.update, service.updateActor, service.updateKey = command, actor, key
	return service.value, service.updateErr
}

func (service *fakeKnowledgeBaseRouteService) RequestDelete(_ context.Context, command knowledgebases.DeleteCommand, actor auth.OperatorID, key string) (knowledgebases.Deletion, error) {
	service.delete, service.deleteActor, service.deleteKey = command, actor, key
	return service.deletion, service.deleteErr
}

func (service *fakeKnowledgeBaseRouteService) Restore(_ context.Context, command knowledgebases.RestoreCommand, actor auth.OperatorID, key string) (knowledgebases.KnowledgeBase, error) {
	service.restore, service.restoreActor, service.restoreKey = command, actor, key
	return service.value, service.restoreErr
}

func knowledgeBaseRouteValue(t *testing.T) knowledgebases.KnowledgeBase {
	t.Helper()
	id, err := knowledgebases.ParseID("10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return knowledgebases.KnowledgeBase{
		ID: id, Name: "Docs", Access: knowledgebases.Restricted,
		Lifecycle: knowledgebases.Active, Instructions: "Answer from sources.", Language: "en",
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}
}

func knowledgeBaseRoutesTestHandler(t *testing.T, sessions auth.SessionService, service knowledgeBaseService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ref0 test", "test")
	config.CreateHooks = nil
	config.Transformers = nil
	api := humago.New(mux, config)
	registerKnowledgeBases(api, sessions, service)
	return problemBoundary(mux)
}

var _ knowledgeBaseService = (*fakeKnowledgeBaseRouteService)(nil)
