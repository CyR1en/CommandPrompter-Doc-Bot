package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/operations"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeOperationsService struct {
	overview    operations.OperationalOverview
	export      operations.ConfigurationExport
	overviewErr error
	exportErr   error
}

func (service *fakeOperationsService) Overview(context.Context) (operations.OperationalOverview, error) {
	return service.overview, service.overviewErr
}

func (service *fakeOperationsService) ExportConfiguration(context.Context) (operations.ConfigurationExport, error) {
	return service.export, service.exportErr
}

func TestOperationsRoutesRequireAuthenticationAndSetDownloadSafetyHeaders(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	sessions := &fakeSessionService{session: authenticated.Session}
	generatedAt := time.Date(2026, 8, 30, 12, 34, 56, 0, time.UTC)
	service := &fakeOperationsService{
		overview: operations.OperationalOverview{
			GeneratedAt: generatedAt, UnhealthySources: []operations.UnhealthySource{},
			FailedJobs: []operations.FailedJob{}, KnowledgeBaseIssues: []operations.KnowledgeBaseIssue{},
			ProviderErrors: []operations.ProviderError{}, AgentFailures: []operations.AgentFailure{},
		},
		export: operations.ConfigurationExport{
			FormatVersion: 1, GeneratedAt: generatedAt, RedactedFields: []string{"credentials.ciphertext"},
			Credentials: []operations.CredentialConfiguration{}, KnowledgeBases: []operations.KnowledgeBaseConfiguration{},
			Sources: []any{}, Providers: []operations.ProviderConfiguration{}, Models: []operations.ModelConfiguration{},
			ModelAssignments:   []operations.ModelAssignmentConfiguration{},
			DiscordConnections: []operations.DiscordConnectionConfiguration{},
			DiscordBindings:    []operations.DiscordBindingConfiguration{},
		},
	}
	handler := operationsTestHandler(t, sessions, service)
	if response := authRequest(t, handler, http.MethodGet, overviewPath, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized overview=%d %s", response.Code, response.Body.String())
	}
	cookie := sessionCookie(authenticated.Token.Reveal())
	overview := authRequest(t, handler, http.MethodGet, overviewPath, "", map[string]string{"Cookie": cookie})
	if overview.Code != http.StatusOK || overview.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(overview.Body.String(), generatedAt.Format(time.RFC3339)) {
		t.Fatalf("overview=%d headers=%v body=%s", overview.Code, overview.Header(), overview.Body.String())
	}
	exported := authRequest(t, handler, http.MethodGet, exportPath, "", map[string]string{"Cookie": cookie})
	if exported.Code != http.StatusOK || exported.Header().Get("Cache-Control") != "no-store" ||
		exported.Header().Get("Content-Disposition") != `attachment; filename="ref0-configuration.json"` ||
		exported.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(exported.Body.String(), `"format_version":1`) {
		t.Fatalf("export=%d headers=%v body=%s", exported.Code, exported.Header(), exported.Body.String())
	}
}

func TestOperationsRoutesDoNotDiscloseServiceErrors(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	service := &fakeOperationsService{overviewErr: errors.New("database-password-sentinel"), exportErr: errors.New("ciphertext-sentinel")}
	handler := operationsTestHandler(t, &fakeSessionService{session: authenticated.Session}, service)
	cookie := sessionCookie(authenticated.Token.Reveal())
	for _, path := range []string{overviewPath, exportPath} {
		response := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": cookie})
		if response.Code != http.StatusInternalServerError ||
			problemDetail(t, response) != "The request could not be completed." ||
			strings.Contains(response.Body.String(), "sentinel") {
			t.Fatalf("%s=%d %s", path, response.Code, response.Body.String())
		}
	}
}

func operationsTestHandler(t *testing.T, sessions auth.SessionService, service operationsService) http.Handler {
	t.Helper()
	registry := prometheus.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newHandler(
		Config{version: "test"}, allReady(), sessions, inertEventReader{}, inertJobService{},
		service, registry, registry, logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

var _ operationsService = (*fakeOperationsService)(nil)
