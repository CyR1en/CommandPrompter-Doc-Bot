package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/knowledgebases"
	"github.com/danielgtaylor/huma/v2"
)

const (
	knowledgeBasesPath     = "/api/v1/knowledge-bases"
	knowledgeBaseBodyLimit = 1 << 20
)

type knowledgeBaseService interface {
	List(context.Context) ([]knowledgebases.KnowledgeBase, error)
	Get(context.Context, knowledgebases.ID) (knowledgebases.KnowledgeBase, error)
	Create(context.Context, knowledgebases.CreateCommand, auth.OperatorID, string) (knowledgebases.KnowledgeBase, error)
	Update(context.Context, knowledgebases.UpdateCommand, auth.OperatorID, string) (knowledgebases.KnowledgeBase, error)
	RequestDelete(context.Context, knowledgebases.DeleteCommand, auth.OperatorID, string) (knowledgebases.Deletion, error)
	Restore(context.Context, knowledgebases.RestoreCommand, auth.OperatorID, string) (knowledgebases.KnowledgeBase, error)
}

type createKnowledgeBaseRequest struct {
	Name         string `json:"name"`
	Access       string `json:"access,omitempty" default:"restricted" enum:"public,restricted"`
	Instructions string `json:"instructions,omitempty" default:""`
	Language     string `json:"language,omitempty" default:"en"`
}

type updateKnowledgeBaseRequest struct {
	ExpectedVersion int32   `json:"expected_version" minimum:"1"`
	Name            *string `json:"name,omitempty" nullable:"true"`
	Access          *string `json:"access,omitempty" enum:"public,restricted" nullable:"true"`
	Instructions    *string `json:"instructions,omitempty" nullable:"true"`
	Language        *string `json:"language,omitempty" nullable:"true"`
	Lifecycle       *string `json:"lifecycle,omitempty" enum:"active,archived" nullable:"true"`
}

type deleteKnowledgeBaseRequest struct {
	ExpectedVersion  int32  `json:"expected_version" minimum:"1"`
	ConfirmationName string `json:"confirmation_name" minLength:"1" maxLength:"255"`
}

type restoreKnowledgeBaseRequest struct {
	ExpectedVersion int32 `json:"expected_version" minimum:"1"`
}

type listKnowledgeBasesInput struct {
	SessionCookie string `cookie:"ref0_session"`
}

type createKnowledgeBaseInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type getKnowledgeBaseInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	KnowledgeBaseID string `path:"knowledge_base_id" format:"uuid"`
}

type updateKnowledgeBaseInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	CSRFToken       string `header:"X-CSRF-Token"`
	IdempotencyKey  string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	KnowledgeBaseID string `path:"knowledge_base_id" format:"uuid"`
	RawBody         []byte `contentType:"application/json"`
	ContentType     string `header:"Content-Type"`
}

type deleteKnowledgeBaseInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	CSRFToken       string `header:"X-CSRF-Token"`
	IdempotencyKey  string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	KnowledgeBaseID string `path:"knowledge_base_id" format:"uuid"`
	RawBody         []byte `contentType:"application/json"`
	ContentType     string `header:"Content-Type"`
}

type restoreKnowledgeBaseInput struct {
	SessionCookie   string `cookie:"ref0_session"`
	CSRFToken       string `header:"X-CSRF-Token"`
	IdempotencyKey  string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	KnowledgeBaseID string `path:"knowledge_base_id" format:"uuid"`
	RawBody         []byte `contentType:"application/json"`
	ContentType     string `header:"Content-Type"`
}

type knowledgeBaseResponse struct {
	ID                string     `json:"id" format:"uuid"`
	Name              string     `json:"name"`
	Access            string     `json:"access" enum:"public,restricted"`
	Lifecycle         string     `json:"lifecycle" enum:"active,archived,pending_delete,deleted"`
	Instructions      string     `json:"instructions"`
	Language          string     `json:"language"`
	PublishedWikiID   *string    `json:"published_wiki_id" format:"uuid"`
	ArchivedAt        *time.Time `json:"archived_at"`
	DeleteRequestedAt *time.Time `json:"delete_requested_at"`
	PurgeAfter        *time.Time `json:"purge_after"`
	DeletedAt         *time.Time `json:"deleted_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Version           int32      `json:"version"`
}

type deleteKnowledgeBaseResponse struct {
	knowledgeBaseResponse
	JobID string `json:"job_id" format:"uuid"`
}

type knowledgeBasesOutput struct {
	Body []knowledgeBaseResponse `nameHint:"KnowledgeBaseResponse" nullable:"false"`
}

type knowledgeBaseOutput struct {
	Body knowledgeBaseResponse `nameHint:"KnowledgeBaseResponse"`
}

type deleteKnowledgeBaseOutput struct {
	Body deleteKnowledgeBaseResponse `nameHint:"DeleteKnowledgeBaseResponse"`
}

func registerKnowledgeBases(api huma.API, sessions auth.SessionService, service knowledgeBaseService) {
	registerKnowledgeBaseList(api, sessions, service)
	registerKnowledgeBaseCreate(api, sessions, service)
	registerKnowledgeBaseGet(api, sessions, service)
	registerKnowledgeBaseUpdate(api, sessions, service)
	registerKnowledgeBaseDelete(api, sessions, service)
	registerKnowledgeBaseRestore(api, sessions, service)
	normalizeKnowledgeBaseOpenAPISchemas(api)
}

func normalizeKnowledgeBaseOpenAPISchemas(api huma.API) {
	schemas := api.OpenAPI().Components.Schemas.Map()
	knowledgeBase := schemas["KnowledgeBaseResponse"]
	deletion := schemas["DeleteKnowledgeBaseResponse"]
	if knowledgeBase == nil || deletion == nil {
		return
	}
	for name, property := range knowledgeBase.Properties {
		deletion.Properties[name] = property
	}
	deletion.Required = append(append([]string{}, knowledgeBase.Required...), "job_id")
}

func registerKnowledgeBaseList(api huma.API, sessions auth.SessionService, service knowledgeBaseService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_knowledge_bases_api_v1_knowledge_bases_get",
		Method:      http.MethodGet,
		Path:        knowledgeBasesPath,
		Summary:     "List Knowledge Bases",
		Tags:        []string{"knowledge-bases"},
		Errors:      []int{http.StatusUnauthorized},
	}, func(ctx context.Context, input *listKnowledgeBasesInput) (*knowledgeBasesOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, knowledgeBasesPath); err != nil {
			return nil, err
		}
		values, err := service.List(ctx)
		if err != nil {
			return nil, knowledgeBaseProblem(knowledgeBasesPath, err)
		}
		output := &knowledgeBasesOutput{Body: make([]knowledgeBaseResponse, len(values))}
		for index, value := range values {
			output.Body[index] = newKnowledgeBaseResponse(value)
		}
		return output, nil
	})
}

func registerKnowledgeBaseCreate(api huma.API, sessions auth.SessionService, service knowledgeBaseService) {
	huma.Register(api, huma.Operation{
		OperationID:      "create_knowledge_base_api_v1_knowledge_bases_post",
		Method:           http.MethodPost,
		Path:             knowledgeBasesPath,
		Summary:          "Create Knowledge Base",
		Tags:             []string{"knowledge-bases"},
		DefaultStatus:    http.StatusCreated,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     knowledgeBaseBodyLimit,
	}, func(ctx context.Context, input *createKnowledgeBaseInput) (*knowledgeBaseOutput, error) {
		_, session, err := RequireAuthenticatedWrite(
			ctx, sessions, input.SessionCookie, input.CSRFToken, knowledgeBasesPath,
		)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, knowledgeBasesPath)
		if err != nil {
			return nil, err
		}
		command, err := createKnowledgeBaseCommand(input.RawBody, input.ContentType)
		if err != nil {
			return nil, validationProblem(knowledgeBasesPath)
		}
		value, err := service.Create(ctx, command, session.Operator.ID, requestKey)
		if err != nil {
			return nil, knowledgeBaseProblem(knowledgeBasesPath, err)
		}
		return &knowledgeBaseOutput{Body: newKnowledgeBaseResponse(value)}, nil
	})
	documentJSONRequest(api, knowledgeBasesPath, http.MethodPost, reflect.TypeFor[createKnowledgeBaseRequest](), "CreateKnowledgeBaseRequest")
}

func registerKnowledgeBaseGet(api huma.API, sessions auth.SessionService, service knowledgeBaseService) {
	const path = knowledgeBasesPath + "/{knowledge_base_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__get",
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "Get Knowledge Base",
		Tags:        []string{"knowledge-bases"},
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *getKnowledgeBaseInput) (*knowledgeBaseOutput, error) {
		instance := knowledgeBaseInstance(path, input.KnowledgeBaseID)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := knowledgebases.ParseID(input.KnowledgeBaseID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.Get(ctx, id)
		if err != nil {
			return nil, knowledgeBaseProblem(instance, err)
		}
		return &knowledgeBaseOutput{Body: newKnowledgeBaseResponse(value)}, nil
	})
}

func registerKnowledgeBaseUpdate(api huma.API, sessions auth.SessionService, service knowledgeBaseService) {
	const path = knowledgeBasesPath + "/{knowledge_base_id}"
	huma.Register(api, huma.Operation{
		OperationID:      "update_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__patch",
		Method:           http.MethodPatch,
		Path:             path,
		Summary:          "Update Knowledge Base",
		Tags:             []string{"knowledge-bases"},
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     knowledgeBaseBodyLimit,
	}, func(ctx context.Context, input *updateKnowledgeBaseInput) (*knowledgeBaseOutput, error) {
		instance := knowledgeBaseInstance(path, input.KnowledgeBaseID)
		_, session, err := RequireAuthenticatedWrite(
			ctx, sessions, input.SessionCookie, input.CSRFToken, instance,
		)
		if err != nil {
			return nil, err
		}
		id, err := knowledgebases.ParseID(input.KnowledgeBaseID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		command, err := updateKnowledgeBaseCommand(id, input.RawBody, input.ContentType)
		if err != nil {
			return nil, validationProblem(instance)
		}
		value, err := service.Update(ctx, command, session.Operator.ID, requestKey)
		if err != nil {
			return nil, knowledgeBaseProblem(instance, err)
		}
		return &knowledgeBaseOutput{Body: newKnowledgeBaseResponse(value)}, nil
	})
	documentJSONRequest(api, path, http.MethodPatch, reflect.TypeFor[updateKnowledgeBaseRequest](), "UpdateKnowledgeBaseRequest")
}

func registerKnowledgeBaseDelete(api huma.API, sessions auth.SessionService, service knowledgeBaseService) {
	const path = knowledgeBasesPath + "/{knowledge_base_id}"
	huma.Register(api, huma.Operation{
		OperationID:      "delete_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__delete",
		Method:           http.MethodDelete,
		Path:             path,
		Summary:          "Delete Knowledge Base",
		Tags:             []string{"knowledge-bases"},
		DefaultStatus:    http.StatusAccepted,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     knowledgeBaseBodyLimit,
	}, func(ctx context.Context, input *deleteKnowledgeBaseInput) (*deleteKnowledgeBaseOutput, error) {
		instance := knowledgeBaseInstance(path, input.KnowledgeBaseID)
		_, session, err := RequireAuthenticatedWrite(
			ctx, sessions, input.SessionCookie, input.CSRFToken, instance,
		)
		if err != nil {
			return nil, err
		}
		id, err := knowledgebases.ParseID(input.KnowledgeBaseID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		body, ok := decodeKnowledgeBaseRequest[deleteKnowledgeBaseRequest](input.RawBody, input.ContentType)
		if !ok {
			return nil, validationProblem(instance)
		}
		command := knowledgebases.DeleteCommand{
			KnowledgeBaseID: id, ExpectedVersion: body.ExpectedVersion,
			ConfirmationName: body.ConfirmationName,
		}
		if utf8.RuneCountInString(body.ConfirmationName) < 1 ||
			utf8.RuneCountInString(body.ConfirmationName) > 255 ||
			knowledgebases.ValidateDelete(command) != nil {
			return nil, validationProblem(instance)
		}
		value, err := service.RequestDelete(ctx, command, session.Operator.ID, requestKey)
		if err != nil {
			return nil, knowledgeBaseProblem(instance, err)
		}
		return &deleteKnowledgeBaseOutput{
			Body: deleteKnowledgeBaseResponse{
				knowledgeBaseResponse: newKnowledgeBaseResponse(value.KnowledgeBase),
				JobID:                 value.PurgeJobID.String(),
			},
		}, nil
	})
	documentJSONRequest(api, path, http.MethodDelete, reflect.TypeFor[deleteKnowledgeBaseRequest](), "DeleteKnowledgeBaseRequest")
}

func registerKnowledgeBaseRestore(api huma.API, sessions auth.SessionService, service knowledgeBaseService) {
	const path = knowledgeBasesPath + "/{knowledge_base_id}/restore"
	huma.Register(api, huma.Operation{
		OperationID:      "restore_knowledge_base_api_v1_knowledge_bases__knowledge_base_id__restore_post",
		Method:           http.MethodPost,
		Path:             path,
		Summary:          "Restore Knowledge Base",
		Tags:             []string{"knowledge-bases"},
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     knowledgeBaseBodyLimit,
	}, func(ctx context.Context, input *restoreKnowledgeBaseInput) (*knowledgeBaseOutput, error) {
		instance := knowledgeBaseInstance(path, input.KnowledgeBaseID)
		_, session, err := RequireAuthenticatedWrite(
			ctx, sessions, input.SessionCookie, input.CSRFToken, instance,
		)
		if err != nil {
			return nil, err
		}
		id, err := knowledgebases.ParseID(input.KnowledgeBaseID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		body, ok := decodeKnowledgeBaseRequest[restoreKnowledgeBaseRequest](input.RawBody, input.ContentType)
		if !ok {
			return nil, validationProblem(instance)
		}
		command := knowledgebases.RestoreCommand{
			KnowledgeBaseID: id, ExpectedVersion: body.ExpectedVersion,
		}
		if err = knowledgebases.ValidateRestore(command); err != nil {
			return nil, validationProblem(instance)
		}
		value, err := service.Restore(ctx, command, session.Operator.ID, requestKey)
		if err != nil {
			return nil, knowledgeBaseProblem(instance, err)
		}
		return &knowledgeBaseOutput{Body: newKnowledgeBaseResponse(value)}, nil
	})
	documentJSONRequest(api, path, http.MethodPost, reflect.TypeFor[restoreKnowledgeBaseRequest](), "RestoreKnowledgeBaseRequest")
}

func createKnowledgeBaseCommand(content []byte, contentType string) (knowledgebases.CreateCommand, error) {
	body := createKnowledgeBaseRequest{Access: "restricted", Language: "en"}
	if !isJSONContentType(contentType) || !decodeSecretRequest(content, &body) {
		return knowledgebases.CreateCommand{}, errors.New("knowledge base request is invalid")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil || object == nil || jsonNull(object["name"]) {
		return knowledgebases.CreateCommand{}, errors.New("knowledge base request is invalid")
	}
	for _, field := range []string{"access", "instructions", "language"} {
		if value, exists := object[field]; exists && jsonNull(value) {
			return knowledgebases.CreateCommand{}, errors.New("knowledge base request is invalid")
		}
	}
	name, err := knowledgebases.ParseName(body.Name)
	if err != nil {
		return knowledgebases.CreateCommand{}, err
	}
	access, ok := knowledgeBaseAccess(body.Access)
	if !ok {
		return knowledgebases.CreateCommand{}, errors.New("knowledge base access is invalid")
	}
	language := strings.TrimFunc(body.Language, apiPythonWhitespace)
	command := knowledgebases.CreateCommand{
		Name: name, Access: access, Instructions: body.Instructions, Language: language,
	}
	return command, knowledgebases.ValidateCreate(command)
}

func updateKnowledgeBaseCommand(id knowledgebases.ID, content []byte, contentType string) (knowledgebases.UpdateCommand, error) {
	body, ok := decodeKnowledgeBaseRequest[updateKnowledgeBaseRequest](content, contentType)
	if !ok {
		return knowledgebases.UpdateCommand{}, errors.New("knowledge base request is invalid")
	}
	command := knowledgebases.UpdateCommand{
		KnowledgeBaseID: id, ExpectedVersion: body.ExpectedVersion,
		Instructions: body.Instructions,
	}
	if body.Name != nil {
		value, err := knowledgebases.ParseName(*body.Name)
		if err != nil {
			return knowledgebases.UpdateCommand{}, err
		}
		command.Name = &value
	}
	if body.Access != nil {
		value, ok := knowledgeBaseAccess(*body.Access)
		if !ok {
			return knowledgebases.UpdateCommand{}, errors.New("knowledge base access is invalid")
		}
		command.Access = &value
	}
	if body.Language != nil {
		value := strings.TrimFunc(*body.Language, apiPythonWhitespace)
		command.Language = &value
	}
	if body.Lifecycle != nil {
		value, ok := knowledgeBaseLifecycle(*body.Lifecycle)
		if !ok {
			return knowledgebases.UpdateCommand{}, errors.New("knowledge base lifecycle is invalid")
		}
		command.Lifecycle = &value
	}
	return command, knowledgebases.ValidateUpdate(command)
}

func decodeKnowledgeBaseRequest[T any](content []byte, contentType string) (T, bool) {
	var body T
	if !isJSONContentType(contentType) || !decodeSecretRequest(content, &body) {
		return body, false
	}
	return body, true
}

func jsonNull(value json.RawMessage) bool {
	return len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func knowledgeBaseAccess(value string) (knowledgebases.Access, bool) {
	switch value {
	case "public":
		return knowledgebases.Public, true
	case "restricted":
		return knowledgebases.Restricted, true
	default:
		return "", false
	}
}

func knowledgeBaseLifecycle(value string) (knowledgebases.Lifecycle, bool) {
	switch value {
	case "active":
		return knowledgebases.Active, true
	case "archived":
		return knowledgebases.Archived, true
	default:
		return "", false
	}
}

func knowledgeBaseInstance(pattern, id string) string {
	return strings.Replace(pattern, "{knowledge_base_id}", id, 1)
}

func newKnowledgeBaseResponse(value knowledgebases.KnowledgeBase) knowledgeBaseResponse {
	return knowledgeBaseResponse{
		ID: value.ID.String(), Name: value.Name,
		Access:       strings.ToLower(string(value.Access)),
		Lifecycle:    strings.ToLower(string(value.Lifecycle)),
		Instructions: value.Instructions, Language: value.Language,
		PublishedWikiID: knowledgeBaseUUID(value.PublishedWikiID),
		ArchivedAt:      value.ArchivedAt, DeleteRequestedAt: value.DeleteRequestedAt,
		PurgeAfter: value.PurgeAfter, DeletedAt: value.DeletedAt,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Version: value.Version,
	}
}

func knowledgeBaseUUID(value *[16]byte) *string {
	if value == nil {
		return nil
	}
	text := knowledgebases.ID(*value).String()
	return &text
}

func knowledgeBaseProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, knowledgebases.ErrNotFound):
		problem.Title = "Not Found"
		problem.Status = http.StatusNotFound
		problem.Detail = "Knowledge base not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title = "Conflict"
		problem.Status = http.StatusConflict
		problem.Detail = "Idempotency key conflicts with a different request."
	case errors.Is(err, knowledgebases.ErrConflict):
		problem.Title = "Conflict"
		problem.Status = http.StatusConflict
		problem.Detail = "Knowledge base state conflicts with the request."
	default:
		problem.Title = "Internal Server Error"
		problem.Status = http.StatusInternalServerError
		problem.Detail = "The request could not be completed."
	}
	return problem
}

func documentJSONRequest(api huma.API, path, method string, requestType reflect.Type, hint string) {
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
		"application/json": {
			Schema: api.OpenAPI().Components.Schemas.Schema(requestType, true, hint),
		},
	}
	documentedOperation.RequestBody = &documentedBody
	*slot = &documentedOperation
	runtimeOperation.RequestBody.Required = false
}
