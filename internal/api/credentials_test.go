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
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

func TestCredentialRoutesAuthenticateNormalizeAndNeverReturnSecrets(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	sessions := &fakeSessionService{session: authenticated.Session}
	metadata := credentialRouteMetadata(t)
	service := &fakeCredentialRouteService{value: metadata, values: []credentials.Metadata{metadata}}
	handler := credentialRoutesTestHandler(t, sessions, service)
	cookie := sessionCookie(authenticated.Token.Reveal())
	csrf := authenticated.CSRFToken

	if response := authRequest(t, handler, http.MethodGet, credentialsPath, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list=%d %s", response.Code, response.Body.String())
	}
	listed := authRequest(t, handler, http.MethodGet, credentialsPath, "", map[string]string{"Cookie": cookie})
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "secret-sentinel") ||
		!strings.Contains(listed.Body.String(), `"masked_value":"••••"`) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}

	missingCSRF := authRequest(t, handler, http.MethodPost, credentialsPath,
		`{"kind":"provider_api_key","label":"Provider","secret":"secret-sentinel-value"}`,
		map[string]string{"Cookie": cookie, "Idempotency-Key": "create-one"})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF=%d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	missingKey := authRequest(t, handler, http.MethodPost, credentialsPath,
		`{"kind":"provider_api_key","label":"Provider","secret":"secret-sentinel-value"}`,
		map[string]string{"Cookie": cookie, csrfHeaderName: csrf})
	if missingKey.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing key=%d %s", missingKey.Code, missingKey.Body.String())
	}

	created := authRequest(t, handler, http.MethodPost, credentialsPath,
		`{"kind":"provider_api_key","label":"\u2003Provider key\u001f","secret":"secret-sentinel-value"}`,
		map[string]string{
			"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "  create-one  ",
		})
	if created.Code != http.StatusCreated || strings.Contains(created.Body.String(), "secret-sentinel-value") {
		t.Fatalf("created=%d %s", created.Code, created.Body.String())
	}
	if service.create.Kind != credentials.ProviderAPIKey || service.create.Label != "Provider key" ||
		service.create.Secret.Reveal() != "secret-sentinel-value" || service.createKey != "create-one" ||
		service.createActor != authenticated.Session.Operator.ID {
		t.Fatalf("create command=%+v key=%q actor=%s", service.create, service.createKey, service.createActor)
	}

	path := credentialsPath + "/" + metadata.ID.String()
	fetched := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": cookie})
	if fetched.Code != http.StatusOK || !strings.Contains(fetched.Body.String(), metadata.ID.String()) {
		t.Fatalf("fetched=%d %s", fetched.Code, fetched.Body.String())
	}
	rotated := authRequest(t, handler, http.MethodPost, path+"/rotate",
		`{"secret":"rotated-secret-sentinel"}`,
		map[string]string{
			"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": " rotate-one ",
		})
	if rotated.Code != http.StatusOK || strings.Contains(rotated.Body.String(), "rotated-secret-sentinel") ||
		service.rotate.CredentialID != metadata.ID || service.rotate.Secret.Reveal() != "rotated-secret-sentinel" ||
		service.rotateKey != "rotate-one" {
		t.Fatalf("rotated=%d %s command=%+v key=%q", rotated.Code, rotated.Body.String(), service.rotate, service.rotateKey)
	}
}

func TestCredentialRoutesValidateAndMapOnlyGenericPublicErrors(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	sessions := &fakeSessionService{session: authenticated.Session}
	metadata := credentialRouteMetadata(t)
	service := &fakeCredentialRouteService{value: metadata}
	handler := credentialRoutesTestHandler(t, sessions, service)
	headers := map[string]string{
		"Cookie":       sessionCookie(authenticated.Token.Reveal()),
		csrfHeaderName: authenticated.CSRFToken, "Idempotency-Key": "request-one",
	}

	for _, body := range []string{
		`{"kind":"PROVIDER_API_KEY","label":"Provider","secret":"long-enough-secret"}`,
		`{"kind":"provider_api_key","label":"Provider","secret":"long-enough-secret","extra":true}`,
		`{"kind":"provider_api_key","label":" ","secret":"long-enough-secret"}`,
		`{"kind":"provider_api_key","label":"Provider","secret":null}`,
	} {
		response := authRequest(t, handler, http.MethodPost, credentialsPath, body, headers)
		if response.Code != http.StatusUnprocessableEntity || strings.Contains(response.Body.String(), "long-enough-secret") {
			t.Fatalf("invalid body=%s response=%d %s", body, response.Code, response.Body.String())
		}
	}

	short := authRequest(t, handler, http.MethodPost, credentialsPath,
		`{"kind":"provider_api_key","label":"Provider","secret":"short"}`, headers)
	if short.Code != http.StatusUnprocessableEntity || problemDetail(t, short) != "Credential secret is invalid." ||
		strings.Contains(short.Body.String(), "short") {
		t.Fatalf("short secret=%d %s", short.Code, short.Body.String())
	}

	path := credentialsPath + "/" + metadata.ID.String()
	service.getErr = credentials.ErrNotFound
	missing := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": headers["Cookie"]})
	if missing.Code != http.StatusNotFound || problemDetail(t, missing) != "Credential not found." {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
	service.getErr = nil
	service.rotateErr = idempotency.ErrConflict
	conflict := authRequest(t, handler, http.MethodPost, path+"/rotate",
		`{"secret":"different-secret-sentinel"}`, headers)
	if conflict.Code != http.StatusConflict ||
		problemDetail(t, conflict) != "Idempotency key conflicts with a different request." ||
		strings.Contains(conflict.Body.String(), "different-secret-sentinel") {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	service.rotateErr = errors.New("database-password-sentinel")
	failed := authRequest(t, handler, http.MethodPost, path+"/rotate",
		`{"secret":"failure-secret-sentinel"}`, headers)
	if failed.Code != http.StatusInternalServerError ||
		problemDetail(t, failed) != "The request could not be completed." || strings.Contains(failed.Body.String(), "sentinel") {
		t.Fatalf("failure=%d %s", failed.Code, failed.Body.String())
	}
}

func TestCredentialOpenAPIMatchesOracleAndMarksSecretsWriteOnly(t *testing.T) {
	handler := credentialRoutesTestHandler(t, &fakeSessionService{}, &fakeCredentialRouteService{})
	document := openAPIDocument(t, handler)
	paths := document["paths"].(map[string]any)
	want := map[string]map[string]string{
		credentialsPath: {
			"get":  "list_credentials_api_v1_credentials_get",
			"post": "create_credential_api_v1_credentials_post",
		},
		credentialsPath + "/{credential_id}": {
			"get": "get_credential_api_v1_credentials__credential_id__get",
		},
		credentialsPath + "/{credential_id}/rotate": {
			"post": "rotate_credential_api_v1_credentials__credential_id__rotate_post",
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
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"CreateCredentialRequest", "RotateCredentialRequest"} {
		schema := schemas[name].(map[string]any)
		secret := schema["properties"].(map[string]any)["secret"].(map[string]any)
		if secret["format"] != "password" || secret["writeOnly"] != true ||
			secret["minLength"] != float64(1) || secret["maxLength"] != float64(16_384) {
			t.Fatalf("%s secret schema=%#v", name, secret)
		}
	}
}

type fakeCredentialRouteService struct {
	value       credentials.Metadata
	values      []credentials.Metadata
	create      credentials.CreateCommand
	rotate      credentials.RotateCommand
	createActor auth.OperatorID
	rotateActor auth.OperatorID
	createKey   string
	rotateKey   string
	listErr     error
	getErr      error
	createErr   error
	rotateErr   error
}

func (service *fakeCredentialRouteService) List(context.Context) ([]credentials.Metadata, error) {
	return service.values, service.listErr
}

func (service *fakeCredentialRouteService) Get(context.Context, credentials.ID) (credentials.Metadata, error) {
	return service.value, service.getErr
}

func (service *fakeCredentialRouteService) Create(_ context.Context, command credentials.CreateCommand, actor auth.OperatorID, key string) (credentials.Metadata, error) {
	service.create, service.createActor, service.createKey = command, actor, key
	if service.createErr != nil {
		return credentials.Metadata{}, service.createErr
	}
	if err := credentials.ValidateCreate(command); err != nil {
		return credentials.Metadata{}, err
	}
	return service.value, nil
}

func (service *fakeCredentialRouteService) Rotate(_ context.Context, command credentials.RotateCommand, actor auth.OperatorID, key string) (credentials.Metadata, error) {
	service.rotate, service.rotateActor, service.rotateKey = command, actor, key
	return service.value, service.rotateErr
}

func credentialRouteMetadata(t *testing.T) credentials.Metadata {
	t.Helper()
	id, err := credentials.ParseID("10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return credentials.Metadata{
		ID: id, Kind: credentials.ProviderAPIKey, Label: "Provider key",
		MaskedValue: credentials.MaskedValue, SecretVersion: 1, KeyID: "v1",
		CreatedAt: created,
	}
}

func credentialRoutesTestHandler(t *testing.T, sessions auth.SessionService, service credentialService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ref0 test", "test")
	config.CreateHooks = nil
	config.Transformers = nil
	api := humago.New(mux, config)
	registerCredentials(api, sessions, service)
	return problemBoundary(mux)
}

func openAPIDocument(t *testing.T, handler http.Handler) map[string]any {
	t.Helper()
	response := authRequest(t, handler, http.MethodGet, "/openapi.json", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI=%d %s", response.Code, response.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

var _ credentialService = (*fakeCredentialRouteService)(nil)
