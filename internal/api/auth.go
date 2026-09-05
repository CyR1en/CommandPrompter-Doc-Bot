package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/danielgtaylor/huma/v2"
)

const (
	sessionCookieName = "ref0_session"
	csrfHeaderName    = "X-CSRF-Token"
	authBodyLimit     = 64 << 10
)

type bootstrapRequest struct {
	Username       string `json:"username" minLength:"1" maxLength:"255"`
	Password       string `json:"password" minLength:"8" maxLength:"1024" format:"password" writeOnly:"true"`
	BootstrapToken string `json:"bootstrap_token" minLength:"1" maxLength:"2048" format:"password" writeOnly:"true"`
}

type loginRequest struct {
	Username string `json:"username" minLength:"1" maxLength:"255"`
	Password string `json:"password" minLength:"1" maxLength:"1024" format:"password" writeOnly:"true"`
}

type bootstrapInput struct {
	RawBody     []byte `contentType:"application/json"`
	ContentType string `header:"Content-Type"`
}

type loginInput struct {
	RawBody     []byte `contentType:"application/json"`
	ContentType string `header:"Content-Type"`
}

type sessionInput struct {
	SessionCookie string `cookie:"ref0_session"`
}

type logoutInput struct {
	SessionCookie string `cookie:"ref0_session"`
	CSRFToken     string `header:"X-CSRF-Token"`
}

type operatorResponse struct {
	ID       string `json:"id" format:"uuid"`
	Username string `json:"username"`
}

type authSessionResponse struct {
	Operator  operatorResponse `json:"operator"`
	ExpiresAt time.Time        `json:"expires_at"`
	CSRFToken string           `json:"csrf_token"`
}

type authCookieOutput struct {
	Status       int
	CacheControl string              `header:"Cache-Control"`
	SetCookie    http.Cookie         `header:"Set-Cookie"`
	Body         authSessionResponse `nameHint:"AuthSessionResponse"`
}

type authSessionOutput struct {
	CacheControl string              `header:"Cache-Control"`
	Body         authSessionResponse `nameHint:"AuthSessionResponse"`
}

type logoutOutput struct {
	Status       int
	CacheControl string      `header:"Cache-Control"`
	SetCookie    http.Cookie `header:"Set-Cookie"`
}

func registerAuth(api huma.API, service auth.SessionService, config Config) {
	registerBootstrap(api, service, config)
	registerLogin(api, service, config)
	registerCurrentSession(api, service)
	registerLogout(api, service, config)
}

func registerBootstrap(api huma.API, service auth.SessionService, config Config) {
	const path = "/api/v1/auth/bootstrap"
	huma.Register(api, huma.Operation{
		OperationID:      "bootstrap_api_v1_auth_bootstrap_post",
		Method:           http.MethodPost,
		Path:             path,
		Summary:          "Bootstrap",
		Tags:             []string{"authentication"},
		DefaultStatus:    http.StatusCreated,
		Errors:           []int{http.StatusForbidden, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     authBodyLimit,
	}, func(ctx context.Context, input *bootstrapInput) (*authCookieOutput, error) {
		command, ok := bootstrapCommand(input.RawBody, input.ContentType)
		if !ok {
			return nil, validationProblem(path)
		}
		authenticated, err := service.Bootstrap(ctx, command)
		if err != nil {
			return nil, authProblem(path, err)
		}
		return cookieOutput(authenticated, config, http.StatusCreated), nil
	})
	documentSecretRequest(api, path, reflect.TypeFor[bootstrapRequest](), "BootstrapRequest")
}

func registerLogin(api huma.API, service auth.SessionService, config Config) {
	const path = "/api/v1/auth/login"
	huma.Register(api, huma.Operation{
		OperationID:      "login_api_v1_auth_login_post",
		Method:           http.MethodPost,
		Path:             path,
		Summary:          "Login",
		Tags:             []string{"authentication"},
		Errors:           []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
		SkipValidateBody: true,
		MaxBodyBytes:     authBodyLimit,
	}, func(ctx context.Context, input *loginInput) (*authCookieOutput, error) {
		command, ok := loginCommand(input.RawBody, input.ContentType)
		if !ok {
			return nil, validationProblem(path)
		}
		authenticated, err := service.Login(ctx, command)
		if err != nil {
			return nil, authProblem(path, err)
		}
		return cookieOutput(authenticated, config, http.StatusOK), nil
	})
	documentSecretRequest(api, path, reflect.TypeFor[loginRequest](), "LoginRequest")
}

func registerCurrentSession(api huma.API, service auth.SessionService) {
	const path = "/api/v1/auth/session"
	huma.Register(api, huma.Operation{
		OperationID: "current_session_api_v1_auth_session_get",
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "Current Session",
		Tags:        []string{"authentication"},
		Errors:      []int{http.StatusUnauthorized},
	}, func(ctx context.Context, input *sessionInput) (*authSessionOutput, error) {
		token, session, err := AuthenticateSession(ctx, service, input.SessionCookie, path)
		if err != nil {
			return nil, err
		}
		return &authSessionOutput{
			CacheControl: "no-store",
			Body:         responseBody(session, auth.CSRFTokenFor(token, session.ID)),
		}, nil
	})
}

func registerLogout(api huma.API, service auth.SessionService, config Config) {
	const path = "/api/v1/auth/logout"
	huma.Register(api, huma.Operation{
		OperationID:   "logout_api_v1_auth_logout_post",
		Method:        http.MethodPost,
		Path:          path,
		Summary:       "Logout",
		Tags:          []string{"authentication"},
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden},
	}, func(ctx context.Context, input *logoutInput) (*logoutOutput, error) {
		_, session, err := RequireAuthenticatedWrite(
			ctx,
			service,
			input.SessionCookie,
			input.CSRFToken,
			path,
		)
		if err != nil {
			return nil, err
		}
		if err = service.Logout(ctx, session.ID); err != nil {
			return nil, authProblem(path, err)
		}
		return &logoutOutput{
			Status:       http.StatusNoContent,
			CacheControl: "no-store",
			SetCookie: http.Cookie{
				Name:     sessionCookieName,
				Path:     "/",
				Expires:  time.Unix(1, 0).UTC(),
				MaxAge:   -1,
				Secure:   config.sessionCookieSecure,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			},
		}, nil
	})
}

// AuthenticateSession is the shared read-route dependency for operator-only
// API handlers. It deliberately maps every token/store failure to the generic
// public authentication problem.
func AuthenticateSession(
	ctx context.Context,
	service auth.SessionService,
	rawCookie string,
	instance string,
) (auth.SessionToken, auth.OperatorSession, error) {
	token, err := auth.NewSessionToken(rawCookie)
	if err != nil {
		return auth.SessionToken{}, auth.OperatorSession{}, authProblem(instance, auth.ErrAuthentication)
	}
	session, err := service.Authenticate(ctx, token)
	if err != nil {
		return auth.SessionToken{}, auth.OperatorSession{}, authProblem(instance, err)
	}
	return token, session, nil
}

// RequireAuthenticatedWrite is the shared unsafe-route dependency. Callers
// pass the ref0_session cookie and X-CSRF-Token header captured by their Huma
// input struct before performing any mutation.
func RequireAuthenticatedWrite(
	ctx context.Context,
	service auth.SessionService,
	rawCookie string,
	csrfToken string,
	instance string,
) (auth.SessionToken, auth.OperatorSession, error) {
	token, session, err := AuthenticateSession(ctx, service, rawCookie, instance)
	if err != nil {
		return auth.SessionToken{}, auth.OperatorSession{}, err
	}
	if csrfToken == "" || !auth.CSRFTokenMatches(token, session.ID, csrfToken) {
		return auth.SessionToken{}, auth.OperatorSession{}, authProblem(instance, auth.ErrCSRF)
	}
	return token, session, nil
}

func bootstrapCommand(content []byte, contentType string) (auth.BootstrapCommand, bool) {
	var body bootstrapRequest
	if !isJSONContentType(contentType) || !decodeSecretRequest(content, &body) ||
		!runeLength(body.Username, 1, 255) ||
		!runeLength(body.Password, 8, 1024) ||
		!runeLength(body.BootstrapToken, 1, 2048) {
		return auth.BootstrapCommand{}, false
	}
	username, err := auth.ParseUsername(body.Username)
	if err != nil {
		return auth.BootstrapCommand{}, false
	}
	password, err := security.NewSecretValue(body.Password)
	if err != nil {
		return auth.BootstrapCommand{}, false
	}
	bootstrapToken, err := security.NewSecretValue(body.BootstrapToken)
	if err != nil {
		return auth.BootstrapCommand{}, false
	}
	return auth.BootstrapCommand{
		Username:       username,
		Password:       password,
		BootstrapToken: bootstrapToken,
	}, true
}

func loginCommand(content []byte, contentType string) (auth.LoginCommand, bool) {
	var body loginRequest
	if !isJSONContentType(contentType) || !decodeSecretRequest(content, &body) ||
		!runeLength(body.Username, 1, 255) ||
		!runeLength(body.Password, 1, 1024) {
		return auth.LoginCommand{}, false
	}
	username, err := auth.ParseUsername(body.Username)
	if err != nil {
		return auth.LoginCommand{}, false
	}
	password, err := security.NewSecretValue(body.Password)
	if err != nil {
		return auth.LoginCommand{}, false
	}
	return auth.LoginCommand{Username: username, Password: password}, true
}

func decodeSecretRequest(content []byte, destination any) bool {
	if !utf8.Valid(content) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false
	}
	var extra any
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func runeLength(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}

func isJSONContentType(value string) bool {
	if value == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"))
}

func cookieOutput(
	authenticated auth.AuthenticatedSession,
	config Config,
	status int,
) *authCookieOutput {
	return &authCookieOutput{
		Status:       status,
		CacheControl: "no-store",
		SetCookie: http.Cookie{
			Name:     sessionCookieName,
			Value:    authenticated.Token.Reveal(),
			Path:     "/",
			Expires:  authenticated.Session.ExpiresAt,
			MaxAge:   config.sessionCookieMaxAge,
			Secure:   config.sessionCookieSecure,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		},
		Body: responseBody(authenticated.Session, authenticated.CSRFToken),
	}
}

func responseBody(session auth.OperatorSession, csrfToken string) authSessionResponse {
	return authSessionResponse{
		Operator: operatorResponse{
			ID:       session.Operator.ID.String(),
			Username: session.Operator.Username,
		},
		ExpiresAt: session.ExpiresAt,
		CSRFToken: csrfToken,
	}
}

type invalidParameter struct {
	Location []string `json:"loc"`
	Message  string   `json:"msg"`
	Type     string   `json:"type"`
}

type apiProblem struct {
	Type          string             `json:"type"`
	Title         string             `json:"title"`
	Status        int                `json:"status"`
	Detail        string             `json:"detail"`
	Instance      string             `json:"instance"`
	InvalidParams []invalidParameter `json:"invalid_params,omitempty"`
}

func (problem *apiProblem) Error() string {
	return problem.Detail
}

func (problem *apiProblem) GetStatus() int {
	return problem.Status
}

func (*apiProblem) ContentType(contentType string) string {
	if contentType == "application/json" {
		return "application/problem+json"
	}
	return contentType
}

func validationProblem(instance string) error {
	return &apiProblem{
		Type:     "about:blank",
		Title:    "Unprocessable Content",
		Status:   http.StatusUnprocessableEntity,
		Detail:   "Request validation failed.",
		Instance: instance,
		InvalidParams: []invalidParameter{{
			Location: []string{"body"},
			Message:  "Invalid value.",
			Type:     "value_error",
		}},
	}
}

func authProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, auth.ErrAuthentication):
		problem.Title = "Unauthorized"
		problem.Status = http.StatusUnauthorized
		problem.Detail = "Authentication failed."
		return huma.ErrorWithHeaders(problem, http.Header{
			"WWW-Authenticate": {"Session"},
		})
	case errors.Is(err, auth.ErrBootstrapDenied):
		problem.Title = "Forbidden"
		problem.Status = http.StatusForbidden
		problem.Detail = "Bootstrap is unavailable."
	case errors.Is(err, auth.ErrCSRF):
		problem.Title = "Forbidden"
		problem.Status = http.StatusForbidden
		problem.Detail = "Request verification failed."
	default:
		problem.Title = "Internal Server Error"
		problem.Status = http.StatusInternalServerError
		problem.Detail = "The request could not be completed."
	}
	return problem
}

func documentSecretRequest(api huma.API, path string, requestType reflect.Type, hint string) {
	item := api.OpenAPI().Paths[path]
	if item == nil || item.Post == nil || item.Post.RequestBody == nil {
		return
	}
	runtimeOperation := item.Post
	documentedOperation := *runtimeOperation
	documentedOperation.Parameters = filterContentTypeParameter(runtimeOperation.Parameters)
	documentedBody := *runtimeOperation.RequestBody
	documentedBody.Required = true
	documentedBody.Content = map[string]*huma.MediaType{
		"application/json": {
			Schema: api.OpenAPI().Components.Schemas.Schema(requestType, true, hint),
		},
	}
	documentedOperation.RequestBody = &documentedBody
	item.Post = &documentedOperation
	// Raw bodies avoid echoing secret values from framework validation. Runtime
	// validation remains entirely in the handler, including an absent body.
	runtimeOperation.RequestBody.Required = false
}

func filterContentTypeParameter(parameters []*huma.Param) []*huma.Param {
	filtered := make([]*huma.Param, 0, len(parameters))
	for _, parameter := range parameters {
		if parameter.In == "header" && strings.EqualFold(parameter.Name, "Content-Type") {
			continue
		}
		filtered = append(filtered, parameter)
	}
	return filtered
}
