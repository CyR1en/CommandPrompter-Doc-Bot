package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
)

type fakeSessionService struct {
	authenticated auth.AuthenticatedSession
	session       auth.OperatorSession
	bootstrapErr  error
	loginErr      error
	authErr       error
	logoutErr     error
	bootstrap     []auth.BootstrapCommand
	logins        []auth.LoginCommand
	logoutIDs     []auth.SessionID
}

func (service *fakeSessionService) Bootstrap(_ context.Context, command auth.BootstrapCommand) (auth.AuthenticatedSession, error) {
	service.bootstrap = append(service.bootstrap, command)
	return service.authenticated, service.bootstrapErr
}

func (service *fakeSessionService) Login(_ context.Context, command auth.LoginCommand) (auth.AuthenticatedSession, error) {
	service.logins = append(service.logins, command)
	return service.authenticated, service.loginErr
}

func (service *fakeSessionService) Authenticate(context.Context, auth.SessionToken) (auth.OperatorSession, error) {
	return service.session, service.authErr
}

func (service *fakeSessionService) Logout(_ context.Context, sessionID auth.SessionID) error {
	service.logoutIDs = append(service.logoutIDs, sessionID)
	return service.logoutErr
}

func TestAuthRoutesPreserveSessionCookieAndCSRFContract(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service := &fakeSessionService{
		authenticated: authenticated,
		session:       authenticated.Session,
	}
	config := Config{
		version:             "test",
		sessionCookieMaxAge: 3600,
		sessionCookieSecure: true,
	}
	handler := testHandler(t, config, allReady(), service)

	bootstrap := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/bootstrap",
		`{"username":"  Ｏperator  ","password":"operator-password","bootstrap_token":"bootstrap-secret"}`,
		nil,
	)
	if bootstrap.Code != http.StatusCreated {
		t.Fatalf("bootstrap status = %d body=%s", bootstrap.Code, bootstrap.Body.String())
	}
	assertSessionResponse(t, bootstrap, authenticated, true, 3600)
	if len(service.bootstrap) != 1 || service.bootstrap[0].Username.Display != "Operator" ||
		service.bootstrap[0].Username.Key != "operator" ||
		service.bootstrap[0].Password.Reveal() != "operator-password" ||
		service.bootstrap[0].BootstrapToken.Reveal() != "bootstrap-secret" {
		t.Fatalf("bootstrap command = %#v", service.bootstrap)
	}
	if strings.Contains(bootstrap.Body.String(), "operator-password") ||
		strings.Contains(bootstrap.Body.String(), "bootstrap-secret") ||
		strings.Contains(bootstrap.Body.String(), authenticated.Token.Reveal()) {
		t.Fatal("bootstrap response disclosed a credential")
	}

	login := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/login",
		`{"username":" OPERATOR ","password":"operator-password"}`,
		nil,
	)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	assertSessionResponse(t, login, authenticated, true, 3600)
	if len(service.logins) != 1 || service.logins[0].Username.Key != "operator" {
		t.Fatalf("login command = %#v", service.logins)
	}

	cookie := sessionCookie(authenticated.Token.Reveal())
	current := authRequest(
		t,
		handler,
		http.MethodGet,
		"/api/v1/auth/session",
		"",
		map[string]string{"Cookie": cookie},
	)
	if current.Code != http.StatusOK || current.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("session response = %d %v %s", current.Code, current.Header(), current.Body.String())
	}
	wantCSRF := auth.CSRFTokenFor(authenticated.Token, authenticated.Session.ID)
	if !strings.Contains(current.Body.String(), `"csrf_token":"`+wantCSRF+`"`) {
		t.Fatalf("session CSRF response = %s", current.Body.String())
	}

	missingCSRF := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/logout",
		"",
		map[string]string{"Cookie": cookie},
	)
	wrongCSRF := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/logout",
		"",
		map[string]string{"Cookie": cookie, csrfHeaderName: "wrong"},
	)
	if missingCSRF.Code != http.StatusForbidden || wrongCSRF.Code != http.StatusForbidden ||
		missingCSRF.Body.String() != wrongCSRF.Body.String() {
		t.Fatalf("CSRF responses differ: %d %s / %d %s", missingCSRF.Code, missingCSRF.Body.String(), wrongCSRF.Code, wrongCSRF.Body.String())
	}
	if got := problemDetail(t, missingCSRF); got != "Request verification failed." {
		t.Fatalf("CSRF detail = %q", got)
	}

	logout := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/logout",
		"",
		map[string]string{"Cookie": cookie, csrfHeaderName: wantCSRF},
	)
	if logout.Code != http.StatusNoContent || logout.Body.Len() != 0 ||
		logout.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout response = %d %v %q", logout.Code, logout.Header(), logout.Body.String())
	}
	deleted := logout.Result().Cookies()
	if len(deleted) != 1 || deleted[0].Name != sessionCookieName || deleted[0].MaxAge >= 0 ||
		!deleted[0].HttpOnly || !deleted[0].Secure || deleted[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("deleted cookie = %#v", deleted)
	}
	if len(service.logoutIDs) != 1 || service.logoutIDs[0] != authenticated.Session.ID {
		t.Fatalf("logout IDs = %v", service.logoutIDs)
	}
}

func TestAuthErrorsAndValidationAreGenericAndSecretSafe(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service := &fakeSessionService{
		authenticated: authenticated,
		session:       authenticated.Session,
		bootstrapErr:  auth.ErrBootstrapDenied,
		loginErr:      auth.ErrAuthentication,
	}
	handler := testHandler(t, Config{version: "test"}, allReady(), service)

	denied := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/bootstrap",
		`{"username":"Operator","password":"password-sentinel","bootstrap_token":"bootstrap-sentinel"}`,
		nil,
	)
	if denied.Code != http.StatusForbidden || problemDetail(t, denied) != "Bootstrap is unavailable." {
		t.Fatalf("bootstrap denial = %d %s", denied.Code, denied.Body.String())
	}

	login := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/login",
		`{"username":"Nobody","password":"password-sentinel"}`,
		nil,
	)
	missing := authRequest(t, handler, http.MethodGet, "/api/v1/auth/session", "", nil)
	for _, response := range []*httptest.ResponseRecorder{login, missing} {
		if response.Code != http.StatusUnauthorized || problemDetail(t, response) != "Authentication failed." ||
			response.Header().Get("WWW-Authenticate") != "Session" {
			t.Fatalf("authentication response = %d %v %s", response.Code, response.Header(), response.Body.String())
		}
	}
	service.loginErr = errors.New("unexpected-error-secret-sentinel")
	unexpected := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/login",
		`{"username":"Nobody","password":"password-do-not-echo-sentinel"}`,
		nil,
	)
	if unexpected.Code != http.StatusInternalServerError ||
		problemDetail(t, unexpected) != "The request could not be completed." ||
		strings.Contains(unexpected.Body.String(), "unexpected-error-secret-sentinel") ||
		strings.Contains(unexpected.Body.String(), "password-do-not-echo-sentinel") {
		t.Fatalf("unexpected error response = %d %s", unexpected.Code, unexpected.Body.String())
	}

	invalid := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/bootstrap",
		`{"username":"   ","password":"password-do-not-echo-sentinel","bootstrap_token":"bootstrap-do-not-echo-sentinel"}`,
		nil,
	)
	foldedTooLong := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/bootstrap",
		`{"username":"`+strings.Repeat("a", 254)+`ß","password":"password-do-not-echo-sentinel","bootstrap_token":"bootstrap-do-not-echo-sentinel"}`,
		nil,
	)
	unknownField := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/login",
		`{"username":"Operator","password":"password-do-not-echo-sentinel","extra":"bootstrap-do-not-echo-sentinel"}`,
		nil,
	)
	wrongContentType := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/login",
		`{"username":"Operator","password":"password-do-not-echo-sentinel"}`,
		map[string]string{"Content-Type": "text/plain"},
	)
	malformed := authRequest(
		t,
		handler,
		http.MethodPost,
		"/api/v1/auth/login",
		`{"username":"Operator","password":"password-do-not-echo-sentinel"`,
		nil,
	)
	empty := authRequest(t, handler, http.MethodPost, "/api/v1/auth/login", "", nil)
	for _, response := range []*httptest.ResponseRecorder{
		invalid, foldedTooLong, unknownField, wrongContentType, malformed, empty,
	} {
		if response.Code != http.StatusUnprocessableEntity ||
			!strings.HasPrefix(response.Header().Get("Content-Type"), "application/problem+json") ||
			strings.Contains(response.Body.String(), "password-do-not-echo-sentinel") ||
			strings.Contains(response.Body.String(), "bootstrap-do-not-echo-sentinel") ||
			strings.Contains(response.Body.String(), `"input"`) ||
			strings.Contains(response.Body.String(), `"ctx"`) {
			t.Fatalf("unsafe validation response = %d %v %s", response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestAuthOpenAPIDocumentsOracleRoutesAndSecretSchemas(t *testing.T) {
	handler := testHandler(t, Config{version: "test"}, allReady(), &fakeSessionService{})
	response := request(t, handler, "/openapi.json")
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI response = %d", response.Code)
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	paths := document["paths"].(map[string]any)
	wantOperations := map[string]string{
		"/api/v1/auth/bootstrap": "bootstrap_api_v1_auth_bootstrap_post",
		"/api/v1/auth/login":     "login_api_v1_auth_login_post",
		"/api/v1/auth/session":   "current_session_api_v1_auth_session_get",
		"/api/v1/auth/logout":    "logout_api_v1_auth_logout_post",
	}
	for path, operationID := range wantOperations {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("OpenAPI path %s is absent", path)
		}
		method := "get"
		if path != "/api/v1/auth/session" {
			method = "post"
		}
		operation := item[method].(map[string]any)
		if operation["operationId"] != operationID {
			t.Fatalf("%s operation = %v", path, operation["operationId"])
		}
		if path == "/api/v1/auth/bootstrap" || path == "/api/v1/auth/login" {
			body := operation["requestBody"].(map[string]any)
			if body["required"] != true {
				t.Fatalf("%s request body is optional", path)
			}
		}
	}

	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	bootstrap := schemas["BootstrapRequest"].(map[string]any)
	properties := bootstrap["properties"].(map[string]any)
	password := properties["password"].(map[string]any)
	bootstrapToken := properties["bootstrap_token"].(map[string]any)
	if password["format"] != "password" || password["writeOnly"] != true ||
		password["minLength"] != float64(8) || password["maxLength"] != float64(1024) ||
		bootstrapToken["format"] != "password" || bootstrapToken["writeOnly"] != true {
		t.Fatalf("bootstrap secret schema = %#v / %#v", password, bootstrapToken)
	}
}

func fixedAuthenticatedSession(t *testing.T) auth.AuthenticatedSession {
	t.Helper()
	token, err := auth.NewSessionToken("raw-session-token")
	if err != nil {
		t.Fatal(err)
	}
	var operatorID auth.OperatorID
	var sessionID auth.SessionID
	for index := range operatorID {
		operatorID[index] = byte(index)
		sessionID[index] = byte(index + 16)
	}
	expires := time.Date(2026, time.August, 30, 19, 20, 0, 0, time.UTC)
	session := auth.OperatorSession{
		ID:         sessionID,
		Operator:   auth.Operator{ID: operatorID, Username: "Operator"},
		CreatedAt:  expires.Add(-time.Hour),
		LastSeenAt: expires.Add(-time.Hour),
		ExpiresAt:  expires,
	}
	return auth.AuthenticatedSession{
		Session:   session,
		Token:     token,
		CSRFToken: auth.CSRFTokenFor(token, sessionID),
	}
}

func allReady() fixedReadiness {
	return fixedReadiness(readinessResult{
		database: true, migrations: true, dataDirectory: true, masterKey: true,
	})
}

func authRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertSessionResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	authenticated auth.AuthenticatedSession,
	secure bool,
	maxAge int,
) {
	t.Helper()
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("auth response is cacheable: %v", response.Header())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName ||
		cookies[0].Value != authenticated.Token.Reveal() || cookies[0].Path != "/" ||
		cookies[0].MaxAge != maxAge || cookies[0].Secure != secure ||
		!cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode ||
		!cookies[0].Expires.Equal(authenticated.Session.ExpiresAt) {
		t.Fatalf("session cookie = %#v", cookies)
	}
	if !strings.Contains(response.Body.String(), authenticated.Session.Operator.ID.String()) ||
		!strings.Contains(response.Body.String(), `"username":"Operator"`) ||
		!strings.Contains(response.Body.String(), authenticated.CSRFToken) {
		t.Fatalf("auth response body = %s", response.Body.String())
	}
}

func problemDetail(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("problem Content-Type = %q", response.Header().Get("Content-Type"))
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	return problem.Detail
}

func sessionCookie(token string) string {
	return (&http.Cookie{Name: sessionCookieName, Value: token}).String()
}
