package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type fixedMetricsReader struct {
	values metricValues
	err    error
}

type countingMetricsReader struct{ calls atomic.Int64 }

func (reader *countingMetricsReader) Read(context.Context) (metricValues, error) {
	reader.calls.Add(1)
	return metricValues{}, nil
}

func testMetricsSecret(t *testing.T) *security.SecretValue {
	t.Helper()
	secret, err := metricsSecret(testMetricsBearerToken)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func (reader fixedMetricsReader) Read(context.Context) (metricValues, error) {
	return reader.values, reader.err
}

func TestOperationalMetricsExposeOnlyBoundedDurableFamilies(t *testing.T) {
	succeeded := "SUCCEEDED"
	failed := "FAILED"
	providerFailure := "provider: unavailable"
	agentTimeout := "agent_execution:provider_timeout"
	application := newApplicationMetrics()
	application.Observe("/api/v1/jobs", http.StatusAccepted, 2*time.Second)
	values := metricValues{
		queue:           map[string]uint64{"PENDING": 2},
		leaseRecoveries: 3,
		sourceSyncs: []sourceMetricRow{
			{status: "SUCCEEDED", kind: "REPOSITORY", count: 4, duration: 5},
			{status: "FAILED", kind: "WEBSITE", count: 1, duration: 2},
		},
		documentation: []attemptMetricRow{
			{phase: "PLAN_RUN", outcome: &succeeded, count: 2, duration: 3},
			{phase: "GENERATE_PAGE", outcome: &failed, count: 1, duration: 4, sanitizedError: &providerFailure},
		},
		pages:              map[string]uint64{"COMPLETE": 6, "SKIPPED": 1},
		pageRetries:        2,
		documentationUsage: [8]int64{2, 10, 11, 21, 3, 12, 13, 25},
		agentRuns: []agentMetricRow{
			{outcome: "ANSWERED", count: 1, usage: [4]int64{1, 7, 8, 15}, latencySeconds: 0.25},
			{outcome: "FAILED", errorClass: boundedMetricError(&agentTimeout), count: 1, latencySeconds: 0.75},
		},
		revisionLags:       []float64{5, 10},
		wikiAges:           []float64{20, 30},
		discordStates:      map[string]uint64{"READY": 1},
		discordLatencies:   []float64{0.1, 0.3},
		bindingHealths:     map[string]uint64{"HEALTHY": 2},
		discordReconnects:  4,
		permissionFailures: 1,
	}
	ready := fixedReadiness(readinessResult{
		database: true, migrations: true, dataDirectory: true, masterKey: true,
	})
	handler := testHandler(t, Config{
		version: "test", metricsBearerToken: testMetricsSecret(t),
		metricsReader: fixedMetricsReader{values: values}, applicationMetrics: application,
	}, ready)
	response := authRequest(t, handler, http.MethodGet, "/metrics", "", map[string]string{
		"Authorization": "Bearer " + testMetricsBearerToken,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("metrics=%d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	families := []string{
		"ref0_job_queue_depth", "ref0_job_lease_recoveries_total",
		"ref0_source_sync_duration_seconds", "ref0_source_sync_failures_total",
		"ref0_documentation_phase_duration_seconds", "ref0_documentation_pages_total",
		"ref0_documentation_page_retries_total", "ref0_model_calls_total",
		"ref0_model_tokens_total", "ref0_model_latency_seconds", "ref0_model_errors_total",
		"ref0_agent_retained_results", "ref0_agent_retained_model_calls", "ref0_agent_retained_model_tokens",
		"ref0_agent_retained_errors", "ref0_agent_retained_run_latency_count", "ref0_agent_retained_run_latency_seconds_sum",
		"ref0_application_request_duration_seconds",
		"ref0_published_wiki_age_seconds", "ref0_source_revision_lag_seconds",
		"ref0_discord_connections", "ref0_discord_gateway_latency_seconds",
		"ref0_discord_reconnects_total", "ref0_discord_permission_failures", "ref0_discord_bindings",
	}
	for _, family := range families {
		if !strings.Contains(body, "# HELP "+family+" ") {
			t.Errorf("metric family %q is absent", family)
		}
	}
	for _, sample := range []string{
		`ref0_job_queue_depth{status="pending"} 2`,
		`ref0_application_request_duration_seconds_count{route_class="job_control",status_class="2xx"} 1`,
		`ref0_application_request_duration_seconds_sum{route_class="job_control",status_class="2xx"} 2`,
		`# TYPE ref0_agent_retained_results gauge`,
		`ref0_agent_retained_results{outcome="answered"} 1`,
		`ref0_agent_retained_model_calls 1`,
		`ref0_agent_retained_model_tokens{token_type="total"} 15`,
		`ref0_agent_retained_errors{error_class="timeout"} 1`,
		`# TYPE ref0_agent_retained_run_latency_count gauge`,
		`ref0_agent_retained_run_latency_count 2`,
		`# TYPE ref0_agent_retained_run_latency_seconds_sum gauge`,
		`ref0_agent_retained_run_latency_seconds_sum 1`,
		`ref0_source_revision_lag_seconds{statistic="average"} 7.5`,
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("sample %q is absent", sample)
		}
	}
	for _, forbidden := range []string{
		"resource_id", "knowledge_base_id", "go_gc_", "process_cpu_", providerFailure, agentTimeout,
		"ref0_agent_results_total", "ref0_agent_run_latency_seconds", `role="agent"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics disclose forbidden value %q", forbidden)
		}
	}
}

func TestOperationalMetricFailureIsSanitized(t *testing.T) {
	const secret = "database-secret-sentinel"
	ready := fixedReadiness(readinessResult{
		database: true, migrations: true, dataDirectory: true, masterKey: true,
	})
	handler := testHandler(t, Config{
		version: "test", metricsBearerToken: testMetricsSecret(t),
		metricsReader: fixedMetricsReader{err: errors.New(secret)},
	}, ready)
	response := authRequest(t, handler, http.MethodGet, "/metrics", "", map[string]string{
		"Authorization": "Bearer " + testMetricsBearerToken,
	})
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("metrics failure=%d %q", response.Code, response.Body.String())
	}
}

func TestMetricsRequestStormAuthenticatesBeforeCachedAggregation(t *testing.T) {
	reader := &countingMetricsReader{}
	handler := testHandler(t, Config{
		version: "test", metricsBearerToken: testMetricsSecret(t), metricsReader: reader,
	}, allReady())
	const requests = 64
	runStorm := func(authorization string) []int {
		statuses := make(chan int, requests)
		var wait sync.WaitGroup
		wait.Add(requests)
		for range requests {
			go func() {
				defer wait.Done()
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
				if authorization != "" {
					request.Header.Set("Authorization", authorization)
				}
				handler.ServeHTTP(recorder, request)
				statuses <- recorder.Code
			}()
		}
		wait.Wait()
		close(statuses)
		result := make([]int, 0, requests)
		for status := range statuses {
			result = append(result, status)
		}
		return result
	}

	for _, status := range runStorm("") {
		if status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated metrics status = %d", status)
		}
	}
	if calls := reader.calls.Load(); calls != 0 {
		t.Fatalf("unauthenticated storm performed %d database aggregations", calls)
	}

	for _, status := range runStorm("Bearer " + testMetricsBearerToken) {
		if status != http.StatusOK {
			t.Fatalf("authenticated metrics status = %d", status)
		}
	}
	if calls := reader.calls.Load(); calls != 1 {
		t.Fatalf("authenticated storm performed %d database aggregations", calls)
	}
}

func TestDatabaseMetricsReaderReadsAnEmptyMigratedSchema(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDocumentationAPIDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `TRUNCATE operators,knowledge_bases,sources,jobs,event_log,audit_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	reader := &databaseMetricsReader{pool: pool}
	values, err := reader.Read(ctx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	registry := prometheus.NewRegistry()
	if err = registry.Register(&operationalMetricsCollector{reader: reader, application: newApplicationMetrics()}); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Gather(); err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if len(values.queue) != 0 || len(values.agentRuns) != 0 || values.leaseRecoveries != 0 {
		t.Fatalf("unexpected durable values: %+v", values)
	}
}

func TestDatabaseMetricsReaderAggregatesPopulatedAgentLedgerWithinCollectorDeadline(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDocumentationAPIDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `TRUNCATE operators,knowledge_bases,sources,jobs,event_log,audit_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES('20000000-0000-4000-8000-000000000001','Metrics Operator','metrics operator','unused');
		INSERT INTO provider_endpoints(id,display_name,display_key,base_url,lifecycle,health,health_checked_at)
		VALUES('20000000-0000-4000-8000-000000000002','Metrics Endpoint','metrics endpoint','https://models.example.test','ACTIVE','HEALTHY',clock_timestamp());
		INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id)
		VALUES('20000000-0000-4000-8000-000000000003','20000000-0000-4000-8000-000000000002','metrics-model','AVAILABLE','20000000-0000-4000-8000-000000000004');
		INSERT INTO model_profile_versions(
			id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,
			supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,
			timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id
		) VALUES('20000000-0000-4000-8000-000000000004','20000000-0000-4000-8000-000000000003',1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,'NONE',30,0,1,'{}','{}','OPERATOR','20000000-0000-4000-8000-000000000001');
		INSERT INTO agents(id,agent_key,lifecycle,current_version_id,activated_at)
		VALUES('20000000-0000-4000-8000-000000000005','metrics-agent','ACTIVE','20000000-0000-4000-8000-000000000006',clock_timestamp());
		INSERT INTO agent_versions(
			id,agent_id,version_number,display_name,response_language,identity_instructions,model_profile_id,
			reasoning_effort,answer_mode,evidence_access,refusal_markdown,max_tool_calls,max_answer_tokens,created_by_operator_id
		) VALUES('20000000-0000-4000-8000-000000000006','20000000-0000-4000-8000-000000000005',1,'Metrics Agent','en','Use metrics evidence.','20000000-0000-4000-8000-000000000003','NONE','SINGLE_PASS','WIKI_ONLY','Cannot answer.',0,1024,'20000000-0000-4000-8000-000000000001');
		INSERT INTO agent_runs(
			agent_id,agent_version_id,agent_resource_version,agent_version_number,
			model_profile_id,model_profile_version_id,model_profile_version_number,
			provider_endpoint_id,captured_endpoint_configuration_version,origin,subject,
			request_digest,effective_access_policy,outcome,model_usage,latency_ms,sanitized_error,
			created_at,completed_at
		)
		SELECT '20000000-0000-4000-8000-000000000005','20000000-0000-4000-8000-000000000006',1,1,
		       '20000000-0000-4000-8000-000000000003','20000000-0000-4000-8000-000000000004',1,
		       '20000000-0000-4000-8000-000000000002',1,'HTTP','metrics-fixture',
		       decode(repeat('ab',32),'hex'),'PUBLIC',
		       (ARRAY['ANSWERED','REFUSED','INSUFFICIENT_EVIDENCE','FAILED'])[(value%4)+1],
		       jsonb_build_object('model_calls',1,'input_tokens',2,'output_tokens',3,'total_tokens',5),
		       25,
		       CASE WHEN value%4=3 THEN 'agent_execution:provider_timeout' ELSE NULL END,
		       clock_timestamp(),clock_timestamp()
		FROM generate_series(1,100000) AS value
	`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	readContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	values, err := (&databaseMetricsReader{pool: pool}).Read(readContext)
	if err != nil {
		t.Fatal(err)
	}
	var count uint64
	var calls int64
	var latency float64
	for _, row := range values.agentRuns {
		count += row.count
		calls += row.usage[0]
		latency += row.latencySeconds
	}
	if count != 100000 || calls != 100000 || latency != 2500 || len(values.agentRuns) != 4 {
		t.Fatalf("Agent aggregates rows=%d count=%d calls=%d latency=%v", len(values.agentRuns), count, calls, latency)
	}
	registry := prometheus.NewRegistry()
	if err = registry.Register(&operationalMetricsCollector{reader: &databaseMetricsReader{pool: pool}}); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Gather(); err != nil {
		t.Fatalf("collector deadline fixture: %v", err)
	}
}

func TestMetricHelpersMatchBoundedOracleSemantics(t *testing.T) {
	if got := metricRouteClass("/v1/chat/completions"); got != "query" {
		t.Fatalf("compatibility completion route class = %q", got)
	}
	if got := metricRouteClass("/api/v1/chat"); got != "other" {
		t.Fatalf("removed dashboard chat route class = %q", got)
	}
	if got := metricRouteClass("/api/v1/knowledge-bases/id/generate"); got != "job_control" {
		t.Fatalf("generate route class = %q", got)
	}
	if got := boundedMetricError(stringPointer("model timed out")); got != "timeout" {
		t.Fatalf("error class = %q", got)
	}
}

func TestRetainedAgentAggregationSaturatesAcrossGroups(t *testing.T) {
	maximumInt64 := int64(^uint64(0) >> 1)
	maximumUint64 := ^uint64(0)
	rows := []agentMetricRow{
		{outcome: "FAILED", errorClass: "timeout", count: uint64(maximumInt64), usage: [4]int64{maximumInt64, maximumInt64, maximumInt64, maximumInt64}},
		{outcome: "FAILED", errorClass: "provider", count: uint64(maximumInt64), usage: [4]int64{maximumInt64, maximumInt64, maximumInt64, maximumInt64}},
		{outcome: "FAILED", errorClass: "timeout", count: uint64(maximumInt64), usage: [4]int64{maximumInt64, maximumInt64, maximumInt64, maximumInt64}},
		{outcome: "FAILED", errorClass: "timeout", count: 2, usage: [4]int64{2, 2, 2, 2}},
	}
	values := aggregateRetainedAgentMetrics(rows)
	if values.outcomes["FAILED"] != maximumUint64 || values.calls != maximumUint64 ||
		values.tokens != [3]uint64{maximumUint64, maximumUint64, maximumUint64} ||
		values.latencyCount != maximumUint64 || values.errors["timeout"] != maximumUint64 {
		t.Fatalf("saturated Agent aggregation = %#v", values)
	}
}

func stringPointer(value string) *string { return &value }
