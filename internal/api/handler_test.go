package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyr1en/ref0/internal/auth"
	commitlog "github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/operations"
	"github.com/prometheus/client_golang/prometheus"
)

type fixedReadiness readinessResult

func (result fixedReadiness) Check(context.Context) readinessResult {
	return readinessResult(result)
}

type inertSessionService struct{}

type inertEventReader struct{}

type inertJobService struct{}

type inertOperationsService struct{}

func (inertEventReader) ReadAfter(context.Context, int64, int) ([]commitlog.Event, error) {
	return nil, nil
}

func (inertEventReader) Window(context.Context) (commitlog.CursorWindow, error) {
	return commitlog.CursorWindow{}, nil
}

func (inertJobService) List(context.Context, jobs.ListOptions) ([]jobs.Snapshot, error) {
	return nil, nil
}

func (inertJobService) Get(context.Context, jobs.JobID) (jobs.Snapshot, error) {
	return jobs.Snapshot{}, jobs.ErrJobNotFound
}

func (inertJobService) Cancel(context.Context, jobs.JobID, jobs.ActorID, string) (jobs.Snapshot, error) {
	return jobs.Snapshot{}, jobs.ErrJobNotFound
}

func (inertOperationsService) Overview(context.Context) (operations.OperationalOverview, error) {
	return operations.OperationalOverview{}, nil
}

func (inertOperationsService) ExportConfiguration(context.Context) (operations.ConfigurationExport, error) {
	return operations.ConfigurationExport{}, nil
}

func (inertSessionService) Bootstrap(context.Context, auth.BootstrapCommand) (auth.AuthenticatedSession, error) {
	return auth.AuthenticatedSession{}, auth.ErrServiceUnavailable
}

func (inertSessionService) Login(context.Context, auth.LoginCommand) (auth.AuthenticatedSession, error) {
	return auth.AuthenticatedSession{}, auth.ErrServiceUnavailable
}

func (inertSessionService) Authenticate(context.Context, auth.SessionToken) (auth.OperatorSession, error) {
	return auth.OperatorSession{}, auth.ErrAuthentication
}

func (inertSessionService) Logout(context.Context, auth.SessionID) error {
	return auth.ErrServiceUnavailable
}

func TestHealthResponsesMatchThePublicContract(t *testing.T) {
	ready := readinessResult{
		database:      true,
		migrations:    true,
		dataDirectory: true,
		masterKey:     true,
	}
	handler := testHandler(t, Config{version: "test"}, fixedReadiness(ready))

	live := request(t, handler, "/health/live")
	if live.Code != http.StatusOK || live.Body.String() != `{"status":"ok"}` {
		t.Fatalf("liveness = %d %q", live.Code, live.Body.String())
	}

	response := request(t, handler, "/health/ready")
	want := `{"status":"ready","components":{"database":"ok","migrations":"ok","data_directory":"ok","master_key":"ok"}}`
	if response.Code != http.StatusOK || response.Body.String() != want {
		t.Fatalf("readiness = %d %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestReadinessFailsWithoutLeakingComponentDetails(t *testing.T) {
	handler := testHandler(t, Config{version: "test"}, fixedReadiness(readinessResult{
		database:      true,
		migrations:    false,
		dataDirectory: false,
		masterKey:     true,
	}))
	response := request(t, handler, "/health/ready")
	want := `{"status":"not_ready","components":{"database":"ok","migrations":"failed","data_directory":"failed","master_key":"ok"}}`
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != want {
		t.Fatalf("readiness = %d %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestMetricsAndHumaDocumentationAreExposed(t *testing.T) {
	ready := fixedReadiness(readinessResult{
		database: true, migrations: true, dataDirectory: true, masterKey: true,
	})
	handler := testHandler(t, Config{version: "9.8.7", metricsBearerToken: testMetricsSecret(t)}, ready)
	_ = request(t, handler, "/health/live")

	unauthorized := request(t, handler, "/metrics")
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") == "" ||
		unauthorized.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthenticated metrics = %d headers=%v", unauthorized.Code, unauthorized.Header())
	}
	metrics := authRequest(t, handler, http.MethodGet, "/metrics", "", map[string]string{
		"Authorization": "Bearer " + testMetricsBearerToken,
	})
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", metrics.Code)
	}
	if got := metrics.Header().Get("Content-Type"); got != metricsContentType {
		t.Fatalf("metrics Content-Type = %q", got)
	}
	if got := metrics.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("metrics Cache-Control = %q", got)
	}
	for _, family := range []string{
		"ref0_job_queue_depth",
		"ref0_model_tokens_total",
		"ref0_application_request_duration_seconds_count",
		"ref0_discord_bindings",
	} {
		if !strings.Contains(metrics.Body.String(), family) {
			t.Fatalf("metric family %q is absent", family)
		}
	}
	if strings.Contains(metrics.Body.String(), "resource_id") {
		t.Fatal("metrics contain an unbounded resource label")
	}

	docs := request(t, handler, "/docs")
	if docs.Code != http.StatusOK || !strings.HasPrefix(docs.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("docs = %d %q", docs.Code, docs.Header().Get("Content-Type"))
	}
	openAPI := request(t, handler, "/openapi.json")
	if openAPI.Code != http.StatusOK {
		t.Fatalf("OpenAPI status = %d", openAPI.Code)
	}
	var document struct {
		Info struct {
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"info"`
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPI.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}
	if document.Info.Title != "ref0 control plane" || document.Info.Version != "9.8.7" {
		t.Fatalf("OpenAPI info = %#v", document.Info)
	}
	if document.Paths["/health/live"] == nil || document.Paths["/health/ready"] == nil {
		t.Fatalf("OpenAPI paths = %v", document.Paths)
	}
}

func TestProblemBoundaryReturnsRFCProblemsAndPreservesHumaErrors(t *testing.T) {
	handler := testHandler(t, Config{version: "test"}, fixedReadiness(readinessResult{
		database: true, migrations: true, dataDirectory: true, masterKey: true,
	}))

	notFound := request(t, handler, "/api/v1/does-not-exist")
	assertBoundaryProblem(t, notFound, http.StatusNotFound, "Not Found", "Not Found.")

	methodNotAllowed := httptest.NewRecorder()
	handler.ServeHTTP(
		methodNotAllowed,
		httptest.NewRequest(http.MethodPost, "/health/live", nil),
	)
	assertBoundaryProblem(
		t,
		methodNotAllowed,
		http.StatusMethodNotAllowed,
		"Method Not Allowed",
		"Method Not Allowed.",
	)
	if methodNotAllowed.Header().Get("Allow") == "" {
		t.Fatal("method problem discarded the Allow header")
	}

	const original = `{"type":"about:blank","title":"Not Found","status":404,"detail":"Domain resource not found.","instance":"/api/v1/resource"}`
	preserved := httptest.NewRecorder()
	problemBoundary(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(original))
	})).ServeHTTP(preserved, httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil))
	if preserved.Code != http.StatusNotFound || preserved.Body.String() != original {
		t.Fatalf("existing Huma problem changed: %d %s", preserved.Code, preserved.Body.String())
	}
}

func TestProblemBoundaryRecoversWithoutDisclosingPanic(t *testing.T) {
	const secret = "panic-secret-sentinel"
	handler := problemBoundary(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Unsafe-Detail", secret)
		writer.Header().Set("Allow", secret)
		panic(secret)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/failure", nil))

	assertBoundaryProblem(
		t,
		response,
		http.StatusInternalServerError,
		"Internal Server Error",
		"The request could not be completed.",
	)
	if strings.Contains(response.Body.String(), secret) || response.Header().Get("X-Unsafe-Detail") != "" ||
		response.Header().Get("Allow") != "" {
		t.Fatalf("panic secret escaped: headers=%v body=%s", response.Header(), response.Body.String())
	}
}

func TestProblemBoundaryPreservesStreamingFlushAndStatus(t *testing.T) {
	handler := problemBoundary(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("event: ready\n\n"))
		if err := http.NewResponseController(writer).Flush(); err != nil {
			t.Errorf("flush event stream: %v", err)
		}
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, eventStreamPath, nil))

	if response.Code != http.StatusOK || !response.Flushed ||
		response.Body.String() != "event: ready\n\n" ||
		!strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream changed: status=%d flushed=%v headers=%v body=%q", response.Code, response.Flushed, response.Header(), response.Body.String())
	}
}

func assertBoundaryProblem(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	title string,
	detail string,
) {
	t.Helper()
	if response.Code != status ||
		!strings.HasPrefix(response.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("problem response = %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	var problem apiProblem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Type != "about:blank" || problem.Title != title || problem.Status != status ||
		problem.Detail != detail || problem.Instance == "" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestStaticFrontendUsesSPAAndBoundedAssetServing(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	assets := filepath.Join(dist, "assets")
	if err := os.MkdirAll(assets, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html><title>ref0</title>"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "app.js"), []byte("export default 'ref0';"), 0o640); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assets, "escape.txt")); err != nil {
		t.Fatal(err)
	}

	ready := fixedReadiness(readinessResult{
		database: true, migrations: true, dataDirectory: true, masterKey: true,
	})
	handler := testHandler(t, Config{version: "test", frontendDir: dist}, ready)
	for _, route := range []string{
		"/", "/login", "/knowledge-bases/record-id", "/jobs/job-id",
		"/agents", "/agents/new", "/agents/agent-id", "/settings/chat-access-tokens",
	} {
		response := request(t, handler, route)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<title>ref0</title>") {
			t.Fatalf("SPA route %s = %d %q", route, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("SPA route %s is cacheable", route)
		}
	}
	if legacy := request(t, handler, "/chat"); legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy SPA route = %d %q", legacy.Code, legacy.Body.String())
	}

	asset := request(t, handler, "/assets/app.js")
	if asset.Code != http.StatusOK || asset.Body.String() != "export default 'ref0';" {
		t.Fatalf("asset = %d %q", asset.Code, asset.Body.String())
	}
	if asset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		asset.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("asset headers = %v", asset.Header())
	}
	if escaped := request(t, handler, "/assets/escape.txt"); escaped.Code == http.StatusOK {
		t.Fatalf("asset symlink escaped its root: %q", escaped.Body.String())
	}
	if unknown := request(t, handler, "/api/v1/not-implemented"); unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown API route = %d", unknown.Code)
	}
}

func TestWritableDirectoryCleansItsProbe(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	if !writableDirectory(directory) {
		t.Fatal("directory was not writable")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("readiness probe left files: %v", entries)
	}
}

func testHandler(
	t *testing.T,
	config Config,
	readiness readinessChecker,
	services ...auth.SessionService,
) http.Handler {
	t.Helper()
	sessionService := auth.SessionService(inertSessionService{})
	if len(services) > 0 {
		sessionService = services[0]
	}
	registry := prometheus.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newHandler(config, readiness, sessionService, inertEventReader{}, inertJobService{}, inertOperationsService{}, registry, registry, logger)
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}
	return handler
}

func request(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}
