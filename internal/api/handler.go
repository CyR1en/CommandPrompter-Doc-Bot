package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

type healthOutput struct {
	Body struct {
		Status string `json:"status" doc:"Process health status"`
	}
}

type readinessOutput struct {
	Status       int
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Status     string `json:"status" doc:"Dependency readiness status"`
		Components struct {
			Database      string `json:"database"`
			Migrations    string `json:"migrations"`
			DataDirectory string `json:"data_directory"`
			MasterKey     string `json:"master_key"`
		} `json:"components"`
	}
}

type routeRegistrar func(huma.API)
type rawRouteRegistrar func(*http.ServeMux)

func newHandler(
	config Config,
	readiness readinessChecker,
	sessionService auth.SessionService,
	eventReader eventReader,
	jobService jobService,
	operationsService operationsService,
	registerer prometheus.Registerer,
	gatherer prometheus.Gatherer,
	logger *slog.Logger,
	registrars ...routeRegistrar,
) (http.Handler, error) {
	return newHandlerWithRaw(
		config, readiness, sessionService, eventReader, jobService, operationsService,
		registerer, gatherer, logger, nil, registrars...,
	)
}

func newHandlerWithRaw(
	config Config,
	readiness readinessChecker,
	sessionService auth.SessionService,
	eventReader eventReader,
	jobService jobService,
	operationsService operationsService,
	registerer prometheus.Registerer,
	gatherer prometheus.Gatherer,
	logger *slog.Logger,
	rawRegistrar rawRouteRegistrar,
	registrars ...routeRegistrar,
) (http.Handler, error) {
	if readiness == nil || sessionService == nil || eventReader == nil || jobService == nil || operationsService == nil || registerer == nil || gatherer == nil {
		return nil, errors.New("API dependencies are incomplete")
	}
	if logger == nil {
		logger = slog.Default()
	}
	eventSettings, err := config.eventSettings()
	if err != nil {
		return nil, err
	}

	metrics, err := registerMetrics(registerer, config.metricsReader, config.applicationMetrics)
	if err != nil {
		return nil, errors.New("register API metrics")
	}

	mux := http.NewServeMux()
	humaConfig := huma.DefaultConfig("ref0 control plane", config.version)
	humaConfig.CreateHooks = nil
	humaConfig.Transformers = nil
	jsonFormat := huma.Format{
		Marshal: func(writer io.Writer, value any) error {
			encoded, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return marshalErr
			}
			_, writeErr := writer.Write(encoded)
			return writeErr
		},
		Unmarshal: json.Unmarshal,
	}
	humaConfig.Formats = map[string]huma.Format{
		"application/json": jsonFormat,
		"json":             jsonFormat,
	}
	humaConfig.DefaultFormat = "application/json"
	humaAPI := humago.New(mux, humaConfig)
	registerHealth(humaAPI, readiness)
	registerAuth(humaAPI, sessionService, config)
	registerEvents(humaAPI, sessionService, eventReader, eventSettings, logger)
	registerJobs(humaAPI, sessionService, jobService)
	registerOperations(humaAPI, sessionService, operationsService)
	for _, register := range registrars {
		if register == nil {
			return nil, errors.New("API route registrar is incomplete")
		}
		register(humaAPI)
	}
	if rawRegistrar != nil {
		rawRegistrar(mux)
	}

	metricsHandler := promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		DisableCompression: true,
		EnableOpenMetrics:  false,
		ErrorHandling:      promhttp.HTTPErrorOnError,
	})
	mux.Handle("GET /metrics", requireMetricsBearer(config.metricsBearerToken, fixedMetricsHeaders(metricsHandler)))
	registerStatic(mux, config.frontendDir, logger)
	return instrumentRequests(problemBoundary(mux), metrics), nil
}

func problemBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		boundary := &problemResponseWriter{
			ResponseWriter:       writer,
			preserveNativeErrors: strings.HasPrefix(request.URL.Path, "/v1/"),
		}
		defer func() {
			if recover() != nil {
				if !boundary.committed {
					boundary.pendingStatus = 0
					if boundary.preserveNativeErrors {
						writeOpenAIError(
							writer,
							http.StatusInternalServerError,
							"server_error",
							"internal_error",
							"The request could not be completed.",
							nil,
						)
					} else {
						writeBoundaryProblem(
							writer,
							request.URL.Path,
							http.StatusInternalServerError,
							"Internal Server Error",
							"The request could not be completed.",
						)
					}
				}
				return
			}
			boundary.finish(request.URL.Path)
		}()
		next.ServeHTTP(boundary, request)
	})
}

type problemResponseWriter struct {
	http.ResponseWriter
	status               int
	pendingStatus        int
	committed            bool
	preserveNativeErrors bool
}

func (writer *problemResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	if !writer.preserveNativeErrors && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) &&
		!strings.HasPrefix(writer.Header().Get("Content-Type"), "application/problem+json") {
		writer.pendingStatus = status
		return
	}
	writer.committed = true
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *problemResponseWriter) Write(content []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if writer.pendingStatus != 0 {
		return len(content), nil
	}
	return writer.ResponseWriter.Write(content)
}

func (writer *problemResponseWriter) Flush() {
	_ = writer.FlushError()
}

func (writer *problemResponseWriter) FlushError() error {
	if writer.pendingStatus != 0 {
		return nil
	}
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(writer.ResponseWriter).Flush()
}

func (writer *problemResponseWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *problemResponseWriter) finish(instance string) {
	status := writer.pendingStatus
	if status == 0 {
		return
	}
	title := "Not Found"
	if status == http.StatusMethodNotAllowed {
		title = "Method Not Allowed"
	}
	writeBoundaryProblem(writer.ResponseWriter, instance, status, title, title+".")
}

func writeBoundaryProblem(
	writer http.ResponseWriter,
	instance string,
	status int,
	title string,
	detail string,
) {
	header := writer.Header()
	var allowedMethods []string
	if status == http.StatusMethodNotAllowed {
		allowedMethods = append(allowedMethods, header.Values("Allow")...)
	}
	for name := range header {
		header.Del(name)
	}
	for _, value := range allowedMethods {
		header.Add("Allow", value)
	}
	header.Set("Content-Type", "application/problem+json")
	body, _ := json.Marshal(apiProblem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: instance,
	})
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}

func registerHealth(api huma.API, readiness readinessChecker) {
	huma.Register(api, huma.Operation{
		OperationID: "live_health_live_get",
		Method:      http.MethodGet,
		Path:        "/health/live",
		Summary:     "Live",
		Tags:        []string{"health"},
	}, func(context.Context, *struct{}) (*healthOutput, error) {
		output := &healthOutput{}
		output.Body.Status = "ok"
		return output, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "ready_health_ready_get",
		Method:      http.MethodGet,
		Path:        "/health/ready",
		Summary:     "Ready",
		Tags:        []string{"health"},
	}, func(ctx context.Context, _ *struct{}) (*readinessOutput, error) {
		result := readiness.Check(ctx)
		output := &readinessOutput{
			Status:       http.StatusOK,
			CacheControl: "no-store",
		}
		output.Body.Status = "ready"
		output.Body.Components.Database = componentStatus(result.database)
		output.Body.Components.Migrations = componentStatus(result.migrations)
		output.Body.Components.DataDirectory = componentStatus(result.dataDirectory)
		output.Body.Components.MasterKey = componentStatus(result.masterKey)
		if !result.database || !result.migrations || !result.dataDirectory || !result.masterKey {
			output.Status = http.StatusServiceUnavailable
			output.Body.Status = "not_ready"
		}
		return output, nil
	})
}

func componentStatus(ready bool) string {
	if ready {
		return "ok"
	}
	return "failed"
}

func registerMetrics(
	registerer prometheus.Registerer,
	reader metricsReader,
	application *applicationMetrics,
) (*applicationMetrics, error) {
	if application == nil {
		application = newApplicationMetrics()
	}
	if reader != nil {
		reader = &cachedMetricsReader{reader: reader, ttl: defaultMetricsCacheTTL}
	}
	collector := &operationalMetricsCollector{reader: reader, application: application}
	if err := registerer.Register(collector); err != nil {
		return nil, err
	}
	return application, nil
}

func instrumentRequests(next http.Handler, metrics *applicationMetrics) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/metrics" {
			next.ServeHTTP(writer, request)
			return
		}
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: writer}
		next.ServeHTTP(recorder, request)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		metrics.Observe(request.URL.Path, status, time.Since(started))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (writer *statusRecorder) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusRecorder) Write(content []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.ResponseWriter.Write(content)
}

func (writer *statusRecorder) Flush() {
	_ = writer.FlushError()
}

func (writer *statusRecorder) FlushError() error {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return http.NewResponseController(writer.ResponseWriter).Flush()
}

func (writer *statusRecorder) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func fixedMetricsHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		wrapped := &metricsResponseWriter{ResponseWriter: writer}
		next.ServeHTTP(wrapped, request)
	})
}

func requireMetricsBearer(secret *security.SecretValue, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !metricsBearerMatches(secret, request.Header.Values("Authorization")) {
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(writer, "Unauthorized.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func metricsBearerMatches(secret *security.SecretValue, headers []string) bool {
	if secret == nil || len(headers) != 1 || len(headers[0]) < len("Bearer ") ||
		!strings.EqualFold(headers[0][:len("Bearer ")], "Bearer ") {
		return false
	}
	actual := sha256.Sum256([]byte(headers[0][len("Bearer "):]))
	expected := sha256.Sum256([]byte(secret.Reveal()))
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

type metricsResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (writer *metricsResponseWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.wroteHeader = true
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", metricsContentType)
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *metricsResponseWriter) Write(content []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(content)
}

func registerStatic(mux *http.ServeMux, directory string, logger *slog.Logger) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return
	}
	index, err := root.Open("index.html")
	if err != nil {
		root.Close()
		return
	}
	info, err := index.Stat()
	index.Close()
	root.Close()
	if err != nil || !info.Mode().IsRegular() {
		return
	}

	serveIndex := func(writer http.ResponseWriter, request *http.Request) {
		root, openErr := os.OpenRoot(directory)
		if openErr != nil {
			http.NotFound(writer, request)
			return
		}
		defer root.Close()
		file, openErr := root.Open("index.html")
		if openErr != nil {
			http.NotFound(writer, request)
			return
		}
		defer file.Close()
		fileInfo, statErr := file.Stat()
		if statErr != nil || !fileInfo.Mode().IsRegular() {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(writer, request, "index.html", fileInfo.ModTime(), file)
	}

	for _, route := range []string{
		"/", "/login", "/bootstrap", "/knowledge-bases", "/sources",
		"/sources/new", "/runs", "/wiki", "/agents", "/agents/new", "/discord", "/providers",
		"/providers/new", "/models", "/jobs", "/settings", "/settings/credentials",
		"/settings/chat-access-tokens",
	} {
		pattern := "GET " + route
		if route == "/" {
			pattern = "GET /{$}"
		}
		mux.HandleFunc(pattern, serveIndex)
	}
	for _, route := range []string{
		"/knowledge-bases/{record_id}", "/jobs/{job_id}", "/sources/{source_id}",
		"/runs/{run_id}", "/providers/{endpoint_id}", "/models/{profile_id}", "/agents/{agent_id}",
	} {
		mux.HandleFunc("GET "+route, serveIndex)
	}

	assetsPath := filepath.Join(directory, "assets")
	mux.HandleFunc("GET /assets/", func(writer http.ResponseWriter, request *http.Request) {
		relative := strings.TrimPrefix(request.URL.Path, "/assets/")
		if !fs.ValidPath(relative) || relative == "." {
			http.NotFound(writer, request)
			return
		}
		root, openErr := os.OpenRoot(assetsPath)
		if openErr != nil {
			http.NotFound(writer, request)
			return
		}
		defer root.Close()
		file, openErr := root.Open(relative)
		if openErr != nil {
			http.NotFound(writer, request)
			return
		}
		defer file.Close()
		fileInfo, statErr := file.Stat()
		if statErr != nil || !fileInfo.Mode().IsRegular() {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(writer, request, filepath.Base(relative), fileInfo.ModTime(), file)
	})
	logger.Info("frontend_enabled")
}
