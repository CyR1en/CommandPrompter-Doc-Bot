package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/danielgtaylor/huma/v2"
)

const (
	credentialsPath     = "/api/v1/credentials"
	credentialBodyLimit = 128 << 10
)

type credentialService interface {
	List(context.Context) ([]credentials.Metadata, error)
	Get(context.Context, credentials.ID) (credentials.Metadata, error)
	Create(context.Context, credentials.CreateCommand, auth.OperatorID, string) (credentials.Metadata, error)
	Rotate(context.Context, credentials.RotateCommand, auth.OperatorID, string) (credentials.Metadata, error)
}

type createCredentialRequest struct {
	Kind   string `json:"kind" enum:"repository_https,website_header,provider_api_key,discord_bot_token,tinyfish_api_key"`
	Label  string `json:"label" minLength:"1" maxLength:"255"`
	Secret string `json:"secret" minLength:"1" maxLength:"16384" format:"password" writeOnly:"true"`
}

type rotateCredentialRequest struct {
	Secret string `json:"secret" minLength:"1" maxLength:"16384" format:"password" writeOnly:"true"`
}

type listCredentialsInput struct {
	SessionCookie string `cookie:"ref0_session"`
}

type createCredentialInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type getCredentialInput struct {
	SessionCookie string `cookie:"ref0_session"`
	CredentialID  string `path:"credential_id" format:"uuid"`
}

type rotateCredentialInput struct {
	SessionCookie  string `cookie:"ref0_session"`
	CSRFToken      string `header:"X-CSRF-Token"`
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	CredentialID   string `path:"credential_id" format:"uuid"`
	RawBody        []byte `contentType:"application/json"`
	ContentType    string `header:"Content-Type"`
}

type credentialResponse struct {
	ID            string     `json:"id" format:"uuid"`
	Kind          string     `json:"kind" enum:"repository_https,website_header,provider_api_key,discord_bot_token,tinyfish_api_key"`
	Label         string     `json:"label"`
	MaskedValue   string     `json:"masked_value"`
	SecretVersion int32      `json:"secret_version"`
	KeyID         string     `json:"key_id"`
	CreatedAt     time.Time  `json:"created_at"`
	RotatedAt     *time.Time `json:"rotated_at"`
}

type credentialsOutput struct {
	Body []credentialResponse `nameHint:"CredentialResponse" nullable:"false"`
}

type credentialOutput struct {
	Body credentialResponse `nameHint:"CredentialResponse"`
}

func registerCredentials(api huma.API, sessions auth.SessionService, service credentialService) {
	registerCredentialList(api, sessions, service)
	registerCredentialCreate(api, sessions, service)
	registerCredentialGet(api, sessions, service)
	registerCredentialRotate(api, sessions, service)
}

func registerCredentialList(api huma.API, sessions auth.SessionService, service credentialService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_credentials_api_v1_credentials_get",
		Method:      http.MethodGet,
		Path:        credentialsPath,
		Summary:     "List Credentials",
		Tags:        []string{"credentials"},
		Errors:      []int{http.StatusUnauthorized},
	}, func(ctx context.Context, input *listCredentialsInput) (*credentialsOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, credentialsPath); err != nil {
			return nil, err
		}
		values, err := service.List(ctx)
		if err != nil {
			return nil, credentialProblem(credentialsPath, err)
		}
		output := &credentialsOutput{Body: make([]credentialResponse, len(values))}
		for index, value := range values {
			output.Body[index] = newCredentialResponse(value)
		}
		return output, nil
	})
}

func registerCredentialCreate(api huma.API, sessions auth.SessionService, service credentialService) {
	huma.Register(api, huma.Operation{
		OperationID:      "create_credential_api_v1_credentials_post",
		Method:           http.MethodPost,
		Path:             credentialsPath,
		Summary:          "Create Credential",
		Tags:             []string{"credentials"},
		DefaultStatus:    http.StatusCreated,
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     credentialBodyLimit,
	}, func(ctx context.Context, input *createCredentialInput) (*credentialOutput, error) {
		_, session, err := RequireAuthenticatedWrite(
			ctx, sessions, input.SessionCookie, input.CSRFToken, credentialsPath,
		)
		if err != nil {
			return nil, err
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, credentialsPath)
		if err != nil {
			return nil, err
		}
		command, ok := createCredentialCommand(input.RawBody, input.ContentType)
		if !ok {
			return nil, validationProblem(credentialsPath)
		}
		value, err := service.Create(ctx, command, session.Operator.ID, requestKey)
		if err != nil {
			return nil, credentialProblem(credentialsPath, err)
		}
		return &credentialOutput{Body: newCredentialResponse(value)}, nil
	})
	documentSecretRequest(api, credentialsPath, reflect.TypeFor[createCredentialRequest](), "CreateCredentialRequest")
}

func registerCredentialGet(api huma.API, sessions auth.SessionService, service credentialService) {
	const path = credentialsPath + "/{credential_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_credential_api_v1_credentials__credential_id__get",
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "Get Credential",
		Tags:        []string{"credentials"},
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *getCredentialInput) (*credentialOutput, error) {
		instance := strings.Replace(path, "{credential_id}", input.CredentialID, 1)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := credentials.ParseID(input.CredentialID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.Get(ctx, id)
		if err != nil {
			return nil, credentialProblem(instance, err)
		}
		return &credentialOutput{Body: newCredentialResponse(value)}, nil
	})
}

func registerCredentialRotate(api huma.API, sessions auth.SessionService, service credentialService) {
	const path = credentialsPath + "/{credential_id}/rotate"
	huma.Register(api, huma.Operation{
		OperationID:      "rotate_credential_api_v1_credentials__credential_id__rotate_post",
		Method:           http.MethodPost,
		Path:             path,
		Summary:          "Rotate Credential",
		Tags:             []string{"credentials"},
		Errors:           []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     credentialBodyLimit,
	}, func(ctx context.Context, input *rotateCredentialInput) (*credentialOutput, error) {
		instance := strings.Replace(path, "{credential_id}", input.CredentialID, 1)
		_, session, err := RequireAuthenticatedWrite(
			ctx, sessions, input.SessionCookie, input.CSRFToken, instance,
		)
		if err != nil {
			return nil, err
		}
		id, err := credentials.ParseID(input.CredentialID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := requiredIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		secret, ok := rotateCredentialSecret(input.RawBody, input.ContentType)
		if !ok {
			return nil, validationProblem(instance)
		}
		value, err := service.Rotate(ctx, credentials.RotateCommand{
			CredentialID: id, Secret: secret,
		}, session.Operator.ID, requestKey)
		if err != nil {
			return nil, credentialProblem(instance, err)
		}
		return &credentialOutput{Body: newCredentialResponse(value)}, nil
	})
	documentSecretRequest(api, path, reflect.TypeFor[rotateCredentialRequest](), "RotateCredentialRequest")
}

func createCredentialCommand(content []byte, contentType string) (credentials.CreateCommand, bool) {
	var body createCredentialRequest
	if !isJSONContentType(contentType) || !decodeSecretRequest(content, &body) ||
		!runeLength(body.Label, 1, 255) || !runeLength(body.Secret, 1, 16_384) {
		return credentials.CreateCommand{}, false
	}
	label := strings.TrimFunc(body.Label, apiPythonWhitespace)
	if label == "" {
		return credentials.CreateCommand{}, false
	}
	kind, ok := credentialKind(body.Kind)
	if !ok {
		return credentials.CreateCommand{}, false
	}
	secret, err := security.NewSecretValue(body.Secret)
	if err != nil {
		return credentials.CreateCommand{}, false
	}
	return credentials.CreateCommand{Kind: kind, Label: label, Secret: secret}, true
}

func rotateCredentialSecret(content []byte, contentType string) (*security.SecretValue, bool) {
	var body rotateCredentialRequest
	if !isJSONContentType(contentType) || !decodeSecretRequest(content, &body) ||
		!runeLength(body.Secret, 1, 16_384) {
		return nil, false
	}
	secret, err := security.NewSecretValue(body.Secret)
	return secret, err == nil
}

func credentialKind(value string) (credentials.Kind, bool) {
	switch value {
	case "repository_https":
		return credentials.RepositoryHTTPS, true
	case "website_header":
		return credentials.WebsiteHeader, true
	case "provider_api_key":
		return credentials.ProviderAPIKey, true
	case "discord_bot_token":
		return credentials.DiscordBotToken, true
	case "tinyfish_api_key":
		return credentials.TinyFishAPIKey, true
	default:
		return "", false
	}
}

func requiredIdempotencyKey(value, instance string) (string, error) {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 255 {
		return "", parameterValidationProblem(instance, "header")
	}
	normalized := strings.TrimFunc(value, apiPythonWhitespace)
	if normalized == "" {
		return "", &apiProblem{
			Type: "about:blank", Title: "Unprocessable Content",
			Status: http.StatusUnprocessableEntity, Detail: "Idempotency-Key is required.",
			Instance: instance,
		}
	}
	return normalized, nil
}

func apiPythonWhitespace(character rune) bool {
	return unicode.IsSpace(character) || character >= '\x1c' && character <= '\x1f'
}

func newCredentialResponse(value credentials.Metadata) credentialResponse {
	return credentialResponse{
		ID: value.ID.String(), Kind: strings.ToLower(string(value.Kind)),
		Label: value.Label, MaskedValue: value.MaskedValue,
		SecretVersion: value.SecretVersion, KeyID: value.KeyID,
		CreatedAt: value.CreatedAt, RotatedAt: value.RotatedAt,
	}
}

func credentialProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, credentials.ErrNotFound):
		problem.Title = "Not Found"
		problem.Status = http.StatusNotFound
		problem.Detail = "Credential not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title = "Conflict"
		problem.Status = http.StatusConflict
		problem.Detail = "Idempotency key conflicts with a different request."
	case errors.Is(err, security.ErrInvalidSecret):
		problem.Title = "Unprocessable Content"
		problem.Status = http.StatusUnprocessableEntity
		problem.Detail = "Credential secret is invalid."
	case errors.Is(err, credentials.ErrInvalidLabel), errors.Is(err, security.ErrInvalidCredentialKind):
		return validationProblem(instance)
	default:
		problem.Title = "Internal Server Error"
		problem.Status = http.StatusInternalServerError
		problem.Detail = "The request could not be completed."
	}
	return problem
}
