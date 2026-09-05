package api

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	queueStatuses       = []string{"PENDING", "LEASED", "RETRY_WAIT", "CANCEL_REQUESTED"}
	sourceStatuses      = []string{"SUCCEEDED", "FAILED", "SUPERSEDED"}
	sourceKinds         = []string{"REPOSITORY", "WEBSITE"}
	documentationPhases = []string{"PREPARE_RUN", "PLAN_RUN", "GENERATE_PAGE", "FINALIZE_RUN"}
	modelRoles          = []string{"DOCUMENTATION_PLANNER", "DOCUMENTATION_WRITER"}
	agentOutcomes       = []string{"ANSWERED", "REFUSED", "INSUFFICIENT_EVIDENCE", "FAILED"}
	discordStates       = []string{"DISABLED", "CONNECTING", "READY", "DEGRADED"}
	bindingHealths      = []string{"DRAFT", "HEALTHY", "UNHEALTHY"}
	metricErrorClasses  = []string{"rate_limit", "timeout", "validation", "provider", "other"}
)

const defaultMetricsCacheTTL = 15 * time.Second

type applicationMetricKey struct {
	routeClass  string
	statusClass string
}

type applicationMetricValue struct {
	count    uint64
	duration float64
}

type applicationMetrics struct {
	mu     sync.Mutex
	values map[applicationMetricKey]applicationMetricValue
}

func newApplicationMetrics() *applicationMetrics {
	return &applicationMetrics{values: map[applicationMetricKey]applicationMetricValue{}}
}

func (metrics *applicationMetrics) Observe(path string, status int, duration time.Duration) {
	statusClass := status / 100
	if statusClass < 1 {
		statusClass = 1
	}
	if statusClass > 5 {
		statusClass = 5
	}
	key := applicationMetricKey{routeClass: metricRouteClass(path), statusClass: string(rune('0'+statusClass)) + "xx"}
	seconds := duration.Seconds()
	if seconds < 0 {
		seconds = 0
	}
	metrics.mu.Lock()
	value := metrics.values[key]
	value.count++
	value.duration += seconds
	metrics.values[key] = value
	metrics.mu.Unlock()
}

func (metrics *applicationMetrics) Snapshot() map[applicationMetricKey]applicationMetricValue {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	result := make(map[applicationMetricKey]applicationMetricValue, len(metrics.values))
	for key, value := range metrics.values {
		result[key] = value
	}
	return result
}

func metricRouteClass(path string) string {
	if path == compatibilityCompletionsPath {
		return "query"
	}
	if strings.HasPrefix(path, "/api/v1/jobs") {
		return "job_control"
	}
	for _, token := range []string{"/sync", "/generate", "/discover", "/probe", "/refresh"} {
		if strings.Contains(path, token) {
			return "job_control"
		}
	}
	return "other"
}

type sourceMetricRow struct {
	status   string
	kind     string
	count    uint64
	duration float64
}

type attemptMetricRow struct {
	phase          string
	outcome        *string
	count          uint64
	duration       float64
	sanitizedError *string
}

type agentMetricRow struct {
	outcome        string
	errorClass     string
	count          uint64
	usage          [4]int64
	latencySeconds float64
}

type retainedAgentMetrics struct {
	outcomes     map[string]uint64
	errors       map[string]uint64
	calls        uint64
	tokens       [3]uint64
	latencyCount uint64
	latencySum   float64
}

type metricValues struct {
	queue              map[string]uint64
	leaseRecoveries    uint64
	sourceSyncs        []sourceMetricRow
	documentation      []attemptMetricRow
	pages              map[string]uint64
	pageRetries        uint64
	documentationUsage [8]int64
	agentRuns          []agentMetricRow
	revisionLags       []float64
	wikiAges           []float64
	discordStates      map[string]uint64
	discordLatencies   []float64
	bindingHealths     map[string]uint64
	discordReconnects  uint64
	permissionFailures uint64
}

type metricsReader interface {
	Read(context.Context) (metricValues, error)
}

type cachedMetricsReader struct {
	reader metricsReader
	ttl    time.Duration

	mu      sync.Mutex
	values  metricValues
	err     error
	expires time.Time
	loaded  bool
	loading chan struct{}
}

func (reader *cachedMetricsReader) Read(ctx context.Context) (metricValues, error) {
	for {
		reader.mu.Lock()
		if reader.loaded && time.Now().Before(reader.expires) {
			values, err := reader.values, reader.err
			reader.mu.Unlock()
			return values, err
		}
		if reader.loading != nil {
			loading := reader.loading
			reader.mu.Unlock()
			select {
			case <-loading:
				continue
			case <-ctx.Done():
				return metricValues{}, ctx.Err()
			}
		}
		reader.loading = make(chan struct{})
		loading := reader.loading
		reader.mu.Unlock()

		values, err := reader.reader.Read(ctx)
		reader.mu.Lock()
		reader.values = values
		reader.err = err
		reader.expires = time.Now().Add(reader.ttl)
		reader.loaded = true
		reader.loading = nil
		close(loading)
		reader.mu.Unlock()
		return values, err
	}
}

type databaseMetricsReader struct{ pool *pgxpool.Pool }

func (reader *databaseMetricsReader) Read(ctx context.Context) (metricValues, error) {
	if reader == nil || reader.pool == nil {
		return metricValues{}, errors.New("metrics database is unavailable")
	}
	tx, err := reader.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return metricValues{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	values := metricValues{}
	if values.queue, err = metricCounts(ctx, tx, `
		SELECT status,count(*) FROM jobs
		WHERE status=ANY($1::varchar[]) GROUP BY status
	`, queueStatuses); err != nil {
		return metricValues{}, err
	}
	if values.leaseRecoveries, err = metricCount(ctx, tx, `SELECT count(*) FROM job_attempts WHERE outcome='LEASE_EXPIRED'`); err != nil {
		return metricValues{}, err
	}
	if values.sourceSyncs, err = readSourceMetrics(ctx, tx); err != nil {
		return metricValues{}, err
	}
	if values.documentation, err = readDocumentationMetrics(ctx, tx); err != nil {
		return metricValues{}, err
	}
	if values.pages, err = metricCounts(ctx, tx, `
		SELECT status,count(*) FROM documentation_pages
		WHERE status=ANY($1::varchar[]) GROUP BY status
	`, []string{"COMPLETE", "SKIPPED"}); err != nil {
		return metricValues{}, err
	}
	if values.pageRetries, err = metricCount(ctx, tx, `
		SELECT coalesce(sum(greatest(attempt_count-1,0)),0) FROM documentation_pages
	`); err != nil {
		return metricValues{}, err
	}
	if err = tx.QueryRow(ctx, `
		SELECT
			coalesce((SELECT sum(planner_model_calls) FROM documentation_runs),0),
			coalesce((SELECT sum(planner_input_tokens) FROM documentation_runs),0),
			coalesce((SELECT sum(planner_output_tokens) FROM documentation_runs),0),
			coalesce((SELECT sum(planner_total_tokens) FROM documentation_runs),0),
			coalesce((SELECT sum(model_calls) FROM documentation_pages),0),
			coalesce((SELECT sum(input_tokens) FROM documentation_pages),0),
			coalesce((SELECT sum(output_tokens) FROM documentation_pages),0),
			coalesce((SELECT sum(total_tokens) FROM documentation_pages),0)
	`).Scan(
		&values.documentationUsage[0], &values.documentationUsage[1],
		&values.documentationUsage[2], &values.documentationUsage[3],
		&values.documentationUsage[4], &values.documentationUsage[5],
		&values.documentationUsage[6], &values.documentationUsage[7],
	); err != nil {
		return metricValues{}, err
	}
	if values.agentRuns, err = readAgentMetrics(ctx, tx); err != nil {
		return metricValues{}, err
	}
	if values.revisionLags, err = metricFloats(ctx, tx, `
		SELECT extract(epoch FROM current_revision.created_at-published_revision.created_at)::double precision
		FROM sources
		JOIN source_revisions AS current_revision ON current_revision.id=sources.current_revision_id
		JOIN knowledge_bases ON knowledge_bases.id=sources.knowledge_base_id
		JOIN wiki_versions ON wiki_versions.id=knowledge_bases.published_wiki_id
		JOIN documentation_run_sources ON documentation_run_sources.run_id=wiki_versions.documentation_run_id
			AND documentation_run_sources.source_id=sources.id
		JOIN source_revisions AS published_revision ON published_revision.id=documentation_run_sources.source_revision_id
	`); err != nil {
		return metricValues{}, err
	}
	if values.wikiAges, err = metricFloats(ctx, tx, `
		SELECT extract(epoch FROM clock_timestamp()-wiki_versions.published_at)::double precision
		FROM knowledge_bases JOIN wiki_versions ON wiki_versions.id=knowledge_bases.published_wiki_id
	`); err != nil {
		return metricValues{}, err
	}
	if values.discordStates, err = metricCounts(ctx, tx, `SELECT state,count(*) FROM discord_connections GROUP BY state`, nil); err != nil {
		return metricValues{}, err
	}
	if values.discordLatencies, err = metricFloats(ctx, tx, `
		SELECT gateway_latency_ms::double precision/1000 FROM discord_connections WHERE gateway_latency_ms IS NOT NULL
	`); err != nil {
		return metricValues{}, err
	}
	if values.bindingHealths, err = metricCounts(ctx, tx, `
		SELECT health,count(*) FROM channel_bindings WHERE deleted_at IS NULL GROUP BY health
	`, nil); err != nil {
		return metricValues{}, err
	}
	if values.discordReconnects, err = metricCount(ctx, tx, `
		SELECT count(*) FROM audit_events
		WHERE action='discord.connection.state' AND details->>'state'='connecting'
	`); err != nil {
		return metricValues{}, err
	}
	if values.permissionFailures, err = metricCount(ctx, tx, `
		SELECT count(*) FROM channel_bindings
		WHERE deleted_at IS NULL AND health='UNHEALTHY'
		  AND lower(coalesce(sanitized_error,'')) LIKE '%permission%'
	`); err != nil {
		return metricValues{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return metricValues{}, err
	}
	return values, nil
}

func metricCounts(ctx context.Context, tx pgx.Tx, query string, values []string) (map[string]uint64, error) {
	var rows pgx.Rows
	var err error
	if values == nil {
		rows, err = tx.Query(ctx, query)
	} else {
		rows, err = tx.Query(ctx, query, values)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]uint64{}
	for rows.Next() {
		var key string
		var count int64
		if err = rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		result[key] = nonnegativeCount(count)
	}
	return result, rows.Err()
}

func metricCount(ctx context.Context, tx pgx.Tx, query string) (uint64, error) {
	var count int64
	if err := tx.QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	return nonnegativeCount(count), nil
}

func metricFloats(ctx context.Context, tx pgx.Tx, query string) ([]float64, error) {
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []float64{}
	for rows.Next() {
		var value float64
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func readSourceMetrics(ctx context.Context, tx pgx.Tx) ([]sourceMetricRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT status,captured_source_kind,count(*),
		       coalesce(sum(extract(epoch FROM completed_at-started_at)),0)::double precision
		FROM source_syncs WHERE status=ANY($1::varchar[])
		GROUP BY status,captured_source_kind
	`, sourceStatuses)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []sourceMetricRow{}
	for rows.Next() {
		var value sourceMetricRow
		var count int64
		if err = rows.Scan(&value.status, &value.kind, &count, &value.duration); err != nil {
			return nil, err
		}
		value.count = nonnegativeCount(count)
		result = append(result, value)
	}
	return result, rows.Err()
}

func readDocumentationMetrics(ctx context.Context, tx pgx.Tx) ([]attemptMetricRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT jobs.job_type,job_attempts.outcome,count(*),
		       coalesce(sum(extract(epoch FROM job_attempts.finished_at-job_attempts.started_at)),0)::double precision,
		       job_attempts.sanitized_error
		FROM job_attempts JOIN jobs ON jobs.id=job_attempts.job_id
		WHERE jobs.job_type=ANY($1::varchar[]) AND job_attempts.finished_at IS NOT NULL
		GROUP BY jobs.job_type,job_attempts.outcome,job_attempts.sanitized_error
	`, documentationPhases)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []attemptMetricRow{}
	for rows.Next() {
		var value attemptMetricRow
		var count int64
		var outcome pgtype.Text
		var sanitized pgtype.Text
		if err = rows.Scan(&value.phase, &outcome, &count, &value.duration, &sanitized); err != nil {
			return nil, err
		}
		if outcome.Valid {
			value.outcome = &outcome.String
		}
		value.count = nonnegativeCount(count)
		if sanitized.Valid {
			value.sanitizedError = &sanitized.String
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func readAgentMetrics(ctx context.Context, tx pgx.Tx) ([]agentMetricRow, error) {
	rows, err := tx.Query(ctx, `
		WITH normalized AS (
			SELECT outcome,
			       CASE
			         WHEN outcome <> 'FAILED' THEN ''
			         WHEN lower(coalesce(sanitized_error,'')) LIKE '%rate%'
			          AND lower(coalesce(sanitized_error,'')) LIKE '%limit%' THEN 'rate_limit'
			         WHEN lower(coalesce(sanitized_error,'')) LIKE '%timeout%'
			           OR lower(coalesce(sanitized_error,'')) LIKE '%timed out%' THEN 'timeout'
			         WHEN lower(coalesce(sanitized_error,'')) LIKE '%valid%'
			           OR lower(coalesce(sanitized_error,'')) LIKE '%schema%' THEN 'validation'
			         WHEN lower(coalesce(sanitized_error,'')) LIKE '%provider%'
			           OR lower(coalesce(sanitized_error,'')) LIKE '%model%' THEN 'provider'
			         ELSE 'other'
			       END AS error_class,
			       CASE WHEN jsonb_typeof(model_usage->'model_calls')='number'
			                  AND model_usage->>'model_calls' ~ '^[0-9]+$'
			                  AND (model_usage->>'model_calls')::numeric <= 9223372036854775807
			            THEN (model_usage->>'model_calls')::numeric ELSE 0 END AS model_calls,
			       CASE WHEN jsonb_typeof(model_usage->'input_tokens')='number'
			                  AND model_usage->>'input_tokens' ~ '^[0-9]+$'
			                  AND (model_usage->>'input_tokens')::numeric <= 9223372036854775807
			            THEN (model_usage->>'input_tokens')::numeric ELSE 0 END AS input_tokens,
			       CASE WHEN jsonb_typeof(model_usage->'output_tokens')='number'
			                  AND model_usage->>'output_tokens' ~ '^[0-9]+$'
			                  AND (model_usage->>'output_tokens')::numeric <= 9223372036854775807
			            THEN (model_usage->>'output_tokens')::numeric ELSE 0 END AS output_tokens,
			       CASE WHEN jsonb_typeof(model_usage->'total_tokens')='number'
			                  AND model_usage->>'total_tokens' ~ '^[0-9]+$'
			                  AND (model_usage->>'total_tokens')::numeric <= 9223372036854775807
			            THEN (model_usage->>'total_tokens')::numeric ELSE 0 END AS total_tokens,
			       latency_ms
			FROM agent_runs
		)
		SELECT outcome,error_class,count(*),
		       least(sum(model_calls),9223372036854775807)::bigint,
		       least(sum(input_tokens),9223372036854775807)::bigint,
		       least(sum(output_tokens),9223372036854775807)::bigint,
		       least(sum(total_tokens),9223372036854775807)::bigint,
		       (sum(latency_ms::numeric)/1000)::double precision
		FROM normalized
		GROUP BY outcome,error_class
		ORDER BY outcome,error_class
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []agentMetricRow{}
	for rows.Next() {
		var value agentMetricRow
		var count int64
		if err = rows.Scan(
			&value.outcome, &value.errorClass, &count,
			&value.usage[0], &value.usage[1], &value.usage[2], &value.usage[3],
			&value.latencySeconds,
		); err != nil {
			return nil, err
		}
		value.count = nonnegativeCount(count)
		result = append(result, value)
	}
	return result, rows.Err()
}

type operationalMetricsCollector struct {
	reader      metricsReader
	application *applicationMetrics
}

func (collector *operationalMetricsCollector) Describe(chan<- *prometheus.Desc) {}

func (collector *operationalMetricsCollector) Collect(channel chan<- prometheus.Metric) {
	values := metricValues{}
	if collector.reader != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		values, err = collector.reader.Read(ctx)
		cancel()
		if err != nil {
			channel <- prometheus.NewInvalidMetric(
				prometheus.NewDesc("ref0_metrics_collection_error", "Operational metrics collection failed.", nil, nil),
				errors.New("operational metrics are unavailable"),
			)
			return
		}
	}
	application := map[applicationMetricKey]applicationMetricValue{}
	if collector.application != nil {
		application = collector.application.Snapshot()
	}
	emitter := metricEmitter{channel: channel}
	emitter.values(values, application)
}

type metricEmitter struct{ channel chan<- prometheus.Metric }

func (emitter metricEmitter) metric(name, help string, valueType prometheus.ValueType, value float64, labels []string, labelValues ...string) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		value = 0
	}
	emitter.channel <- prometheus.MustNewConstMetric(prometheus.NewDesc(name, help, labels, nil), valueType, value, labelValues...)
}

func (emitter metricEmitter) summary(name, help string, count uint64, sum float64, labels []string, labelValues ...string) {
	if math.IsNaN(sum) || math.IsInf(sum, 0) || sum < 0 {
		sum = 0
	}
	emitter.channel <- prometheus.MustNewConstSummary(prometheus.NewDesc(name, help, labels, nil), count, sum, nil, labelValues...)
}

func (emitter metricEmitter) values(values metricValues, application map[applicationMetricKey]applicationMetricValue) {
	const prefix = "ref0_"
	for _, status := range queueStatuses {
		emitter.metric(prefix+"job_queue_depth", "Jobs currently eligible or leased.", prometheus.GaugeValue, float64(values.queue[status]), []string{"status"}, strings.ToLower(status))
	}
	emitter.metric(prefix+"job_lease_recoveries_total", "Retained lease expiry recoveries.", prometheus.CounterValue, float64(values.leaseRecoveries), nil)
	for _, status := range sourceStatuses {
		var count uint64
		var duration float64
		for _, row := range values.sourceSyncs {
			if row.status == status {
				count += row.count
				duration += row.duration
			}
		}
		emitter.summary(prefix+"source_sync_duration_seconds", "Retained source sync duration.", count, duration, []string{"status"}, strings.ToLower(status))
	}
	for _, kind := range sourceKinds {
		var count uint64
		for _, row := range values.sourceSyncs {
			if row.status == "FAILED" && row.kind == kind {
				count += row.count
			}
		}
		emitter.metric(prefix+"source_sync_failures_total", "Retained failed source syncs.", prometheus.CounterValue, float64(count), []string{"source_kind"}, strings.ToLower(kind))
	}
	for _, phase := range documentationPhases {
		var count uint64
		var duration float64
		for _, row := range values.documentation {
			if row.phase == phase {
				count += row.count
				duration += row.duration
			}
		}
		emitter.summary(prefix+"documentation_phase_duration_seconds", "Retained documentation job attempt duration.", count, duration, []string{"phase"}, strings.ToLower(phase))
	}
	for _, outcome := range []string{"COMPLETE", "SKIPPED"} {
		emitter.metric(prefix+"documentation_pages_total", "Documentation page outcomes.", prometheus.CounterValue, float64(values.pages[outcome]), []string{"outcome"}, strings.ToLower(outcome))
	}
	emitter.metric(prefix+"documentation_page_retries_total", "Documentation page retries.", prometheus.CounterValue, float64(values.pageRetries), nil)
	emitter.modelValues(prefix, values)
	agent := aggregateRetainedAgentMetrics(values.agentRuns)
	for _, outcome := range agentOutcomes {
		emitter.metric(prefix+"agent_retained_results", "Agent run outcomes in the current retained window.", prometheus.GaugeValue, float64(agent.outcomes[outcome]), []string{"outcome"}, strings.ToLower(outcome))
	}
	emitter.metric(prefix+"agent_retained_model_calls", "Model calls in the current retained Agent-run window.", prometheus.GaugeValue, float64(agent.calls), nil)
	for index, tokenType := range []string{"input", "output", "total"} {
		emitter.metric(prefix+"agent_retained_model_tokens", "Model tokens in the current retained Agent-run window.", prometheus.GaugeValue, float64(agent.tokens[index]), []string{"token_type"}, tokenType)
	}
	for _, class := range metricErrorClasses {
		emitter.metric(prefix+"agent_retained_errors", "Agent run errors in the current retained window.", prometheus.GaugeValue, float64(agent.errors[class]), []string{"error_class"}, class)
	}
	emitter.metric(prefix+"agent_retained_run_latency_count", "Completed Agent runs with latency in the current retained window.", prometheus.GaugeValue, float64(agent.latencyCount), nil)
	emitter.metric(prefix+"agent_retained_run_latency_seconds_sum", "Summed Agent run latency in the current retained window.", prometheus.GaugeValue, agent.latencySum, nil)
	for _, routeClass := range []string{"query", "job_control", "other"} {
		for _, statusClass := range []string{"1xx", "2xx", "3xx", "4xx", "5xx"} {
			value := application[applicationMetricKey{routeClass: routeClass, statusClass: statusClass}]
			emitter.summary(prefix+"application_request_duration_seconds", "API request time by bounded route and status class.", value.count, value.duration, []string{"route_class", "status_class"}, routeClass, statusClass)
		}
	}
	emitter.aggregateGauges(prefix, values)
}

func (emitter metricEmitter) modelValues(prefix string, values metricValues) {
	for _, role := range modelRoles {
		var calls uint64
		var tokens [3]uint64
		var latencyCount uint64
		var latencySum float64
		errorsByClass := map[string]uint64{}
		start, phase := 0, "PLAN_RUN"
		if role == "DOCUMENTATION_WRITER" {
			start, phase = 4, "GENERATE_PAGE"
		}
		calls = nonnegativeCount(values.documentationUsage[start])
		tokens = [3]uint64{
			nonnegativeCount(values.documentationUsage[start+1]),
			nonnegativeCount(values.documentationUsage[start+2]),
			nonnegativeCount(values.documentationUsage[start+3]),
		}
		for _, row := range values.documentation {
			if row.phase != phase {
				continue
			}
			latencyCount += row.count
			latencySum += row.duration
			if row.outcome != nil && *row.outcome != "SUCCEEDED" {
				errorsByClass[boundedMetricError(row.sanitizedError)] += row.count
			}
		}
		label := strings.ToLower(role)
		emitter.metric(prefix+"model_calls_total", "Retained model calls by role.", prometheus.CounterValue, float64(calls), []string{"role"}, label)
		for index, tokenType := range []string{"input", "output", "total"} {
			emitter.metric(prefix+"model_tokens_total", "Retained model tokens by role and token type.", prometheus.CounterValue, float64(tokens[index]), []string{"role", "token_type"}, label, tokenType)
		}
		emitter.summary(prefix+"model_latency_seconds", "Retained documentation model-phase latency by role.", latencyCount, latencySum, []string{"role"}, label)
		for _, class := range metricErrorClasses {
			emitter.metric(prefix+"model_errors_total", "Retained model errors by bounded class and role.", prometheus.CounterValue, float64(errorsByClass[class]), []string{"error_class", "role"}, class, label)
		}
	}
}

func aggregateRetainedAgentMetrics(rows []agentMetricRow) retainedAgentMetrics {
	result := retainedAgentMetrics{outcomes: map[string]uint64{}, errors: map[string]uint64{}}
	for _, row := range rows {
		result.outcomes[row.outcome] = saturatingAddUint64(result.outcomes[row.outcome], row.count)
		result.calls = saturatingAddUint64(result.calls, nonnegativeCount(row.usage[0]))
		for index := range result.tokens {
			result.tokens[index] = saturatingAddUint64(result.tokens[index], nonnegativeCount(row.usage[index+1]))
		}
		result.latencyCount = saturatingAddUint64(result.latencyCount, row.count)
		result.latencySum += row.latencySeconds
		if row.outcome == "FAILED" {
			result.errors[row.errorClass] = saturatingAddUint64(result.errors[row.errorClass], row.count)
		}
	}
	return result
}

func saturatingAddUint64(left, right uint64) uint64 {
	maximum := ^uint64(0)
	if maximum-left < right {
		return maximum
	}
	return left + right
}

func (emitter metricEmitter) aggregateGauges(prefix string, values metricValues) {
	ages := nonnegative(values.wikiAges)
	lags := nonnegative(values.revisionLags)
	emitter.metric(prefix+"published_wiki_age_seconds", "Published wiki age aggregates.", prometheus.GaugeValue, maximum(ages), []string{"statistic"}, "oldest")
	emitter.metric(prefix+"published_wiki_age_seconds", "Published wiki age aggregates.", prometheus.GaugeValue, minimum(ages), []string{"statistic"}, "newest")
	emitter.metric(prefix+"source_revision_lag_seconds", "Current source revision lag from the published wiki.", prometheus.GaugeValue, maximum(lags), []string{"statistic"}, "maximum")
	emitter.metric(prefix+"source_revision_lag_seconds", "Current source revision lag from the published wiki.", prometheus.GaugeValue, average(lags), []string{"statistic"}, "average")
	for _, state := range discordStates {
		emitter.metric(prefix+"discord_connections", "Discord connections by state.", prometheus.GaugeValue, float64(values.discordStates[state]), []string{"state"}, strings.ToLower(state))
	}
	emitter.metric(prefix+"discord_gateway_latency_seconds", "Discord gateway latency aggregates.", prometheus.GaugeValue, average(values.discordLatencies), []string{"statistic"}, "average")
	emitter.metric(prefix+"discord_gateway_latency_seconds", "Discord gateway latency aggregates.", prometheus.GaugeValue, maximum(values.discordLatencies), []string{"statistic"}, "maximum")
	emitter.metric(prefix+"discord_reconnects_total", "Retained Discord reconnect transitions.", prometheus.CounterValue, float64(values.discordReconnects), nil)
	emitter.metric(prefix+"discord_permission_failures", "Current unhealthy permission failures.", prometheus.GaugeValue, float64(values.permissionFailures), nil)
	for _, health := range bindingHealths {
		emitter.metric(prefix+"discord_bindings", "Discord bindings by health.", prometheus.GaugeValue, float64(values.bindingHealths[health]), []string{"health"}, strings.ToLower(health))
	}
}

func boundedMetricError(value *string) string {
	if value == nil {
		return "other"
	}
	normalized := strings.ToLower(*value)
	switch {
	case strings.Contains(normalized, "rate") && strings.Contains(normalized, "limit"):
		return "rate_limit"
	case strings.Contains(normalized, "timeout") || strings.Contains(normalized, "timed out"):
		return "timeout"
	case strings.Contains(normalized, "valid") || strings.Contains(normalized, "schema"):
		return "validation"
	case strings.Contains(normalized, "provider") || strings.Contains(normalized, "model"):
		return "provider"
	default:
		return "other"
	}
}

func nonnegative(values []float64) []float64 {
	result := make([]float64, len(values))
	for index, value := range values {
		if value > 0 {
			result[index] = value
		}
	}
	return result
}

func nonnegativeCount(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func maximum(values []float64) float64 {
	result := 0.0
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}

func minimum(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}
