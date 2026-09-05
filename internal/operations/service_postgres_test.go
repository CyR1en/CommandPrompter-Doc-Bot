package operations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

type exportSnapshotTracer struct {
	reached     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func (tracer *exportSnapshotTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM channel_bindings AS binding") {
		tracer.once.Do(func() { close(tracer.reached) })
		<-tracer.release
	}
	return ctx
}

func (*exportSnapshotTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tracer *exportSnapshotTracer) unblock() {
	tracer.releaseOnce.Do(func() { close(tracer.release) })
}

func TestServicePostgreSQLExportRedactionAndOverviewProjection(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateOperationsDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `TRUNCATE credentials, knowledge_bases, event_log, jobs, operators CASCADE`); err != nil {
		t.Fatal(err)
	}

	const (
		credentialID = "00000000-0000-4000-8000-000000000001"
		knowledgeID  = "00000000-0000-4000-8000-000000000002"
		sourceID     = "00000000-0000-4000-8000-000000000003"
		providerID   = "00000000-0000-4000-8000-000000000004"
		failedJobID  = "00000000-0000-4000-8000-000000000005"
		freshID      = "00000000-0000-4000-8000-000000000006"
	)
	if _, err = pool.Exec(ctx, `
		INSERT INTO credentials(
		  id, kind, label, masked_value, key_id, nonce, ciphertext, secret_version
		) VALUES('00000000-0000-4000-8000-000000000001', 'REPOSITORY_HTTPS', 'Repository credential', '••••',
		  'key-secret-sentinel', decode(repeat('6e', 12), 'hex'),
		  convert_to('ciphertext-secret-sentinel', 'utf8'), 4);

		INSERT INTO knowledge_bases(
		  id, name, name_key, access_policy, lifecycle, instructions, language, version
		) VALUES('00000000-0000-4000-8000-000000000002', 'Product docs', 'product docs', 'RESTRICTED', 'ACTIVE',
		  'Document the public API.', 'en', 1);
		INSERT INTO knowledge_bases(
		  id, name, name_key, access_policy, lifecycle, instructions, language, version
		) VALUES('00000000-0000-4000-8000-000000000006', 'Fresh docs', 'fresh docs',
		  'PUBLIC', 'ACTIVE', '', 'en', 1);

		INSERT INTO sources(
		  id, knowledge_base_id, kind, display_name, display_key, privacy,
		  lifecycle, health, sanitized_error, checked_at, version,
		  configuration_version, validated_configuration_version
		) VALUES('00000000-0000-4000-8000-000000000003', '00000000-0000-4000-8000-000000000002', 'REPOSITORY', 'Product repository', 'product repository',
		  'PRIVATE', 'ACTIVE', 'UNHEALTHY', 'Repository access failed.',
		  clock_timestamp(), 1, 1, 1);

		INSERT INTO repository_sources(
		  source_id, remote_url, credential_username, credential_id, ref_kind,
		  ref_value, include_patterns, exclude_patterns, poll_interval_seconds
		) VALUES('00000000-0000-4000-8000-000000000003', 'https://git.example/product.git', 'git-user', '00000000-0000-4000-8000-000000000001', 'BRANCH',
		  'main', '["src/**"]', '["vendor/**"]', 300);

		INSERT INTO provider_endpoints(
		  id, display_name, display_key, base_url, credential_id, headers,
		  lifecycle, health
		) VALUES('00000000-0000-4000-8000-000000000004', 'Primary provider', 'primary provider', 'https://model.example',
		  '00000000-0000-4000-8000-000000000001', '{"Authorization":"provider-header-secret-sentinel","X-Tenant":"docs","nested":{"api_key":"nested-secret-sentinel","safe":"kept"}}',
		  'ACTIVE', 'UNKNOWN');

		INSERT INTO jobs(
		  id, job_type, target_type, target_id, payload, operation_key, status,
		  attempt_count, max_attempts, progress, sanitized_error, started_at, finished_at
		) VALUES('00000000-0000-4000-8000-000000000005', 'SYNC_SOURCE', 'source', '00000000-0000-4000-8000-000000000003', '{}', 'operations-failed',
		  'FAILED', 3, 3, 50, 'Source sync failed.', clock_timestamp(), clock_timestamp());

		INSERT INTO jobs(
		  id, job_type, target_type, target_id, payload, operation_key, status
		) VALUES('00000000-0000-4000-8000-000000000007', 'PREPARE_RUN',
		  'knowledge_base', '00000000-0000-4000-8000-000000000006', '{}',
		  'operations-fresh-prepare', 'PENDING');
		INSERT INTO documentation_runs(
		  id, knowledge_base_id, status, prepare_job_id, knowledge_base_version,
		  instructions, language
		) VALUES('00000000-0000-4000-8000-000000000008',
		  '00000000-0000-4000-8000-000000000006', 'PREPARING',
		  '00000000-0000-4000-8000-000000000007', 1, '', 'en');
		INSERT INTO wiki_versions(
		  id, knowledge_base_id, documentation_run_id, artifact_key,
		  manifest_sha256, page_count
		) VALUES('00000000-0000-4000-8000-000000000009',
		  '00000000-0000-4000-8000-000000000006',
		  '00000000-0000-4000-8000-000000000008', 'wiki/fresh',
		  decode(repeat('11', 32), 'hex'), 1);
		UPDATE knowledge_bases
		SET published_wiki_id='00000000-0000-4000-8000-000000000009', version=2
		WHERE id='00000000-0000-4000-8000-000000000006';
	`); err != nil {
		t.Fatal(err)
	}

	service, err := NewService(pool)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := service.ExportConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"key-secret-sentinel", "ciphertext-secret-sentinel",
		"provider-header-secret-sentinel", "nested-secret-sentinel",
	} {
		if strings.Contains(string(serialized), secret) {
			t.Fatalf("export disclosed %q: %s", secret, serialized)
		}
	}
	if len(exported.Providers) != 1 || exported.Providers[0].Headers["X-Tenant"] != "docs" ||
		len(exported.Providers[0].HeaderNames) != 3 || len(exported.Sources) != 1 ||
		len(exported.KnowledgeBases) != 2 || exported.KnowledgeBases[0].WikiExportURL != nil {
		t.Fatalf("unexpected export projection: %+v", exported)
	}

	overview, err := service.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.UnhealthySources) != 1 || overview.UnhealthySources[0].ID != sourceID ||
		len(overview.FailedJobs) != 1 || overview.FailedJobs[0].ID != failedJobID ||
		len(overview.KnowledgeBaseIssues) != 1 || overview.KnowledgeBaseIssues[0].ID != knowledgeID ||
		overview.KnowledgeBaseIssues[0].Kind != "unpublished" ||
		len(overview.ProviderErrors) != 0 || len(overview.AgentFailures) != 0 {
		t.Fatalf("unexpected overview projection: %+v", overview)
	}
	if _, err = pool.Exec(ctx, `UPDATE knowledge_bases SET version=3 WHERE id=$1`, freshID); err != nil {
		t.Fatal(err)
	}
	drifted, err := service.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundStale := false
	for _, issue := range drifted.KnowledgeBaseIssues {
		if issue.ID == freshID && issue.Kind == "stale" && issue.PublishedWikiID != nil {
			foundStale = true
		}
	}
	if !foundStale {
		t.Fatalf("published version drift was not reported: %+v", drifted.KnowledgeBaseIssues)
	}

	if _, err = pool.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES('00000000-0000-4000-8000-000000000010','Overview Operator','overview operator','unused');
		INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id)
		VALUES('00000000-0000-4000-8000-000000000011',$1,'overview-model','AVAILABLE','00000000-0000-4000-8000-000000000012');
		INSERT INTO model_profile_versions(
			id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,
			supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,
			timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id
		) VALUES('00000000-0000-4000-8000-000000000012','00000000-0000-4000-8000-000000000011',1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,'NONE',30,0,1,'{}','{}','OPERATOR','00000000-0000-4000-8000-000000000010');
		INSERT INTO agents(id,agent_key,lifecycle,current_version_id,activated_at)
		VALUES('00000000-0000-4000-8000-000000000013','overview-agent','ACTIVE','00000000-0000-4000-8000-000000000014',clock_timestamp());
		INSERT INTO agent_versions(
			id,agent_id,version_number,display_name,response_language,identity_instructions,model_profile_id,
			reasoning_effort,answer_mode,evidence_access,refusal_markdown,max_tool_calls,max_answer_tokens,created_by_operator_id
		) VALUES('00000000-0000-4000-8000-000000000014','00000000-0000-4000-8000-000000000013',1,'Overview Agent','en','Report operations failures.','00000000-0000-4000-8000-000000000011','NONE','SINGLE_PASS','WIKI_ONLY','Cannot answer.',0,1024,'00000000-0000-4000-8000-000000000010');
		INSERT INTO agent_runs(
			agent_id,agent_version_id,agent_resource_version,agent_version_number,
			model_profile_id,model_profile_version_id,model_profile_version_number,
			provider_endpoint_id,captured_endpoint_configuration_version,origin,subject,
			request_digest,effective_access_policy,outcome,model_usage,latency_ms,sanitized_error,
			created_at,completed_at
		)
		SELECT '00000000-0000-4000-8000-000000000013','00000000-0000-4000-8000-000000000014',1,1,
		       '00000000-0000-4000-8000-000000000011','00000000-0000-4000-8000-000000000012',1,
		       $1,1,'HTTP','overview-fixture',decode(repeat('cd',32),'hex'),'PUBLIC',
		       CASE WHEN value%2=0 THEN 'FAILED' ELSE 'ANSWERED' END,'{}',10,
		       CASE WHEN value%2=0 THEN 'agent_execution:provider_request_failed' ELSE NULL END,
		       clock_timestamp()-(value||' seconds')::interval,
		       clock_timestamp()-(value||' seconds')::interval
		FROM generate_series(1,1000) AS value;
		ANALYZE agent_runs
	`, pgx.QueryExecModeSimpleProtocol, providerID); err != nil {
		t.Fatal(err)
	}
	populated, err := service.Overview(ctx)
	if err != nil || len(populated.AgentFailures) != overviewLimit || populated.AgentFailures[0].AgentKey != "overview-agent" {
		t.Fatalf("populated Agent failures = %#v, %v", populated.AgentFailures, err)
	}
	planTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer planTx.Rollback(ctx)
	if _, err = planTx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	var plan []byte
	if err = planTx.QueryRow(ctx, `
		EXPLAIN (FORMAT JSON,COSTS OFF)
		SELECT run.id,run.agent_id,agent.agent_key,version.display_name,
		       run.agent_version_number,lower(run.origin),run.sanitized_error,
		       run.created_at,run.completed_at
		FROM agent_runs AS run
		JOIN agents AS agent ON agent.id=run.agent_id
		JOIN agent_versions AS version
		  ON version.agent_id=run.agent_id AND version.id=run.agent_version_id
		WHERE run.outcome='FAILED' AND run.sanitized_error IS NOT NULL
		ORDER BY run.created_at DESC,run.id
		LIMIT $1
	`, overviewLimit).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plan, []byte("ix_agent_runs_failed_created")) {
		t.Fatalf("FAILED Agent overview plan does not use bounded index: %s", plan)
	}
}

func TestConfigurationExportUsesOneRepeatableReadSnapshot(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateOperationsDatabase(t, ctx, databaseURL)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err = admin.Exec(ctx, `
		TRUNCATE credentials,operators,knowledge_bases,jobs,idempotency_records,
		         audit_events,event_log CASCADE
	`); err != nil {
		t.Fatal(err)
	}
	const (
		operatorID       = "10000000-0000-4000-8000-000000000001"
		credentialID     = "10000000-0000-4000-8000-000000000002"
		knowledgeBaseID  = "10000000-0000-4000-8000-000000000003"
		endpointID       = "10000000-0000-4000-8000-000000000004"
		profileID        = "10000000-0000-4000-8000-000000000005"
		profileVersionID = "10000000-0000-4000-8000-000000000006"
		agentID          = "10000000-0000-4000-8000-000000000007"
		agentVersionID   = "10000000-0000-4000-8000-000000000008"
		connectionID     = "10000000-0000-4000-8000-000000000009"
		bindingID        = "10000000-0000-4000-8000-00000000000a"
		replacementID    = "10000000-0000-4000-8000-00000000000b"
	)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Export Operator','export operator','unused');
		INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES($2,'DISCORD_BOT_TOKEN','Export Discord','••••','test',decode(repeat('01',12),'hex'),decode('01','hex'),1);
		INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language)
		VALUES($3,'Export KB','export kb','PUBLIC','ACTIVE','','en');
		INSERT INTO provider_endpoints(id,display_name,display_key,base_url,lifecycle,health,health_checked_at)
		VALUES($4,'Export Endpoint','export endpoint','https://models.example.test','ACTIVE','HEALTHY',clock_timestamp());
		INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id)
		VALUES($5,$4,'export-model','AVAILABLE',$6);
		INSERT INTO model_profile_versions(
			id,profile_id,version_number,configuration_version,transport,context_window_tokens,
			max_output_tokens,supports_streaming,supports_tools,supports_structured_output,
			supports_temperature,reasoning_transport,timeout_seconds,max_retries,
			max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id
		) VALUES($6,$5,1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,'NONE',30,0,1,'{}','{}','OPERATOR',$1);
		INSERT INTO agents(id,agent_key,lifecycle,current_version_id,activated_at)
		VALUES($7,'export-agent','ACTIVE',$8,clock_timestamp());
		INSERT INTO agent_versions(
			id,agent_id,version_number,display_name,response_language,identity_instructions,
			model_profile_id,reasoning_effort,answer_mode,evidence_access,refusal_markdown,
			max_tool_calls,max_answer_tokens,created_by_operator_id
		) VALUES($8,$7,1,'Export Agent','en','Answer documentation.',$5,'NONE','SINGLE_PASS','WIKI_ONLY','Cannot answer.',0,1024,$1);
		INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id)
		VALUES($7,$8,0,$3);
		INSERT INTO discord_connections(
			id,display_name,display_key,credential_id,credential_version,lifecycle,state
		) VALUES($9,'Export Bot','export bot',$2,1,'ENABLED','CONNECTING');
		INSERT INTO discord_servers(connection_id,server_id,name,owner,refreshed_at)
		VALUES($9,'100','Export Guild',false,clock_timestamp());
		INSERT INTO discord_channels(
			connection_id,server_id,channel_id,name,channel_type,position,
			effective_bot_permissions,everyone_can_view,refreshed_at
		) VALUES($9,'100','200','Export Channel',0,0,0,true,clock_timestamp())
	`, pgx.QueryExecModeSimpleProtocol, operatorID, credentialID, knowledgeBaseID, endpointID, profileID, profileVersionID,
		agentID, agentVersionID, connectionID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	tracer := &exportSnapshotTracer{reached: make(chan struct{}), release: make(chan struct{})}
	defer tracer.unblock()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Tracer = tracer
	exportPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(exportPool.Close)
	service, err := NewService(exportPool)
	if err != nil {
		t.Fatal(err)
	}
	type exportResult struct {
		value ConfigurationExport
		err   error
	}
	result := make(chan exportResult, 1)
	go func() {
		value, exportErr := service.ExportConfiguration(ctx)
		result <- exportResult{value: value, err: exportErr}
	}()
	select {
	case <-tracer.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not reach the Discord binding snapshot boundary")
	}
	mutation, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mutation.Exec(ctx, `
		INSERT INTO agent_versions(
			id,agent_id,version_number,display_name,description,response_language,
			identity_instructions,model_profile_id,reasoning_effort,answer_mode,
			behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
			max_answer_tokens,created_by_operator_id
		)
		SELECT $2,agent_id,2,'Replacement Agent',description,response_language,
		       identity_instructions,model_profile_id,reasoning_effort,answer_mode,
		       behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
		       max_answer_tokens,created_by_operator_id
		FROM agent_versions WHERE id=$1;
		INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id)
		VALUES($3,$2,0,$4);
		UPDATE agents SET current_version_id=$2,version=version+1 WHERE id=$3;
		INSERT INTO channel_bindings(
			id,connection_id,server_id,listen_channel_id,agent_id,reply_policy,
			allowed_role_ids,allowed_user_ids,rate_requests,rate_window_seconds
		) VALUES($5,$6,'100','200',$3,'SAME_CHANNEL','[]','[]',5,60);
		INSERT INTO channel_binding_triggers(
			binding_id,connection_id,server_id,listen_channel_id,enabled,trigger_type
		) VALUES($5,$6,'100','200',false,'MENTION')
	`, pgx.QueryExecModeSimpleProtocol, agentVersionID, replacementID, agentID, knowledgeBaseID, bindingID, connectionID); err != nil {
		_ = mutation.Rollback(ctx)
		t.Fatal(err)
	}
	if err = mutation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tracer.unblock()
	var exported exportResult
	select {
	case exported = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("export did not finish")
	}
	if exported.err != nil || len(exported.value.Agents) != 1 ||
		exported.value.Agents[0].CurrentVersionNumber != 1 || len(exported.value.DiscordBindings) != 0 {
		t.Fatalf("repeatable-read export = %+v, %v", exported.value, exported.err)
	}
	currentService, err := NewService(admin)
	if err != nil {
		t.Fatal(err)
	}
	current, err := currentService.ExportConfiguration(ctx)
	if err != nil || len(current.Agents) != 1 || current.Agents[0].CurrentVersionNumber != 2 ||
		len(current.DiscordBindings) != 1 || current.DiscordBindings[0].AgentID != agentID {
		t.Fatalf("current export = %+v, %v", current, err)
	}
}

func migrateOperationsDatabase(t *testing.T, ctx context.Context, databaseURL string) {
	t.Helper()
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	goose.SetBaseFS(migrations.FS)
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpContext(ctx, database, "."); err != nil {
		t.Fatal(err)
	}
}
