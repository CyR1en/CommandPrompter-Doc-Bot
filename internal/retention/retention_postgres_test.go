package retention

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

type fakeSourceArtifacts struct {
	discarded [][2]sourcefiles.ID
	failures  int
}

func (fake *fakeSourceArtifacts) DiscardSnapshot(sourceID, revisionID sourcefiles.ID) error {
	fake.discarded = append(fake.discarded, [2]sourcefiles.ID{sourceID, revisionID})
	if fake.failures > 0 {
		fake.failures--
		return errors.New("injected source artifact failure")
	}
	return nil
}

type fakeRunArtifacts struct {
	discarded [][2]artifacts.ID
	failures  int
}

func (fake *fakeRunArtifacts) DiscardRun(knowledgeBaseID, runID artifacts.ID) error {
	fake.discarded = append(fake.discarded, [2]artifacts.ID{knowledgeBaseID, runID})
	if fake.failures > 0 {
		fake.failures--
		return errors.New("injected run artifact failure")
	}
	return nil
}

type fakeWikiArtifacts struct {
	discarded [][2]artifacts.ID
	failures  int
}

func (fake *fakeWikiArtifacts) Discard(knowledgeBaseID, wikiID artifacts.ID) error {
	fake.discarded = append(fake.discarded, [2]artifacts.ID{knowledgeBaseID, wikiID})
	if fake.failures > 0 {
		fake.failures--
		return errors.New("injected wiki artifact failure")
	}
	return nil
}

func TestRetentionPostgreSQLIsFencedReplaySafeAndPreservesCurrentContent(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateRetentionDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `TRUNCATE credentials,knowledge_bases,jobs,audit_events CASCADE`); err != nil {
		t.Fatal(err)
	}

	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	now := time.Now().UTC()
	knowledgeBaseID := retentionTestID(t)
	sourceID := retentionTestID(t)
	revisionID := retentionTestID(t)
	failedRunID := retentionTestID(t)
	oldRunID := retentionTestID(t)
	currentRunID := retentionTestID(t)
	protectedRunID := retentionTestID(t)
	oldWikiID := retentionTestID(t)
	currentWikiID := retentionTestID(t)
	protectedWikiID := retentionTestID(t)
	oldJobID := retentionTestID(t)
	failedPrepareJob := retentionTestID(t)
	oldPrepareJob := retentionTestID(t)
	currentPrepareJob := retentionTestID(t)
	protectedPrepareJob := retentionTestID(t)
	discordCredentialID := retentionTestID(t)
	discordConnectionID := retentionTestID(t)
	discordBindingID := retentionTestID(t)
	expiredDiscordContextID := retentionTestID(t)
	overageDiscordContextID := retentionTestID(t)
	liveDiscordContextID := retentionTestID(t)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `
		INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language)
		VALUES($1,'Retention','retention','RESTRICTED','ACTIVE','','en')
	`, retentionPG(knowledgeBaseID)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO sources(
			id,knowledge_base_id,kind,display_name,display_key,privacy,lifecycle,health,created_at,updated_at
		) VALUES($1,$2,'REPOSITORY','Old source','old source','PUBLIC','DRAFT','UNKNOWN',$3,$3)
	`, retentionPG(sourceID), retentionPG(knowledgeBaseID), old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO source_revisions(
			id,source_id,observed_ref_kind,observed_ref,native_version,fingerprint,
			artifact_key,file_count,byte_count,ignored_paths,created_at
		) VALUES($1,$2,'BRANCH','main','old',decode(repeat('ab',32),'hex'),$3,1,3,'[]',$4)
	`, retentionPG(revisionID), retentionPG(sourceID),
		"sources/"+sourcefiles.ID(sourceID).String()+"/snapshots/"+sourcefiles.ID(revisionID).String(), old); err != nil {
		t.Fatal(err)
	}
	for _, jobID := range [][16]byte{failedPrepareJob, oldPrepareJob, currentPrepareJob, protectedPrepareJob} {
		if _, err = tx.Exec(ctx, `
			INSERT INTO jobs(
				id,job_type,target_type,target_id,payload,operation_key,status,
				attempt_count,max_attempts,progress,lease_generation,result,
				created_at,updated_at,started_at,finished_at
			) VALUES($1,'PREPARE_RUN','knowledge_base',$2,'{}',$3,'SUCCEEDED',1,3,100,1,'{}',$4,$4,$4,$4)
		`, retentionPG(jobID), retentionPG(knowledgeBaseID), "fixture:"+jobs.UUID(jobID).String(), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO jobs(
			id,job_type,target_type,target_id,payload,operation_key,status,
			attempt_count,max_attempts,progress,lease_generation,result,
			created_at,updated_at,started_at,finished_at
		) VALUES($1,'SYNC_SOURCE','source',$2,'{}','retention:old-job','SUCCEEDED',1,3,100,1,'{}',$3,$3,$3,$3)
	`, retentionPG(oldJobID), retentionPG(sourceID), old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO job_attempts(
			job_id,attempt_number,lease_generation,worker_id,heartbeat_at,outcome,started_at,finished_at
		) VALUES($1,1,1,'old-worker',$2,'SUCCEEDED',$2,$2)
	`, retentionPG(oldJobID), old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO job_events(job_id,attempt_number,event_kind,status,payload,created_at)
		VALUES($1,1,'SUCCEEDED','SUCCEEDED','{}',$2)
	`, retentionPG(oldJobID), old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO documentation_runs(
			id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,
			instructions,language,sanitized_error,created_at,updated_at,completed_at
		) VALUES($1,$2,'FAILED',$3,1,'','en','Documentation failed.',$4,$4,$4)
	`, retentionPG(failedRunID), retentionPG(knowledgeBaseID), retentionPG(failedPrepareJob), old); err != nil {
		t.Fatal(err)
	}
	for _, value := range []struct {
		runID, prepareJob, wikiID [16]byte
		published                 time.Time
		artifact                  string
	}{
		{oldRunID, oldPrepareJob, oldWikiID, old, "old"},
		{protectedRunID, protectedPrepareJob, protectedWikiID, old, "agent-run-protected"},
		{currentRunID, currentPrepareJob, currentWikiID, now, "current"},
	} {
		if _, err = tx.Exec(ctx, `
			INSERT INTO documentation_runs(
				id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,
				instructions,language,created_at,updated_at,completed_at
			) VALUES($1,$2,'PUBLISHED',$3,1,'','en',$4,$4,$4)
		`, retentionPG(value.runID), retentionPG(knowledgeBaseID), retentionPG(value.prepareJob), value.published); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO wiki_versions(
				id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,
				page_count,created_at,published_at
			) VALUES($1,$2,$3,$4,decode(repeat('cd',32),'hex'),1,$5,$5)
		`, retentionPG(value.wikiID), retentionPG(knowledgeBaseID), retentionPG(value.runID), value.artifact, value.published); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET published_wiki_version_id=$2 WHERE id=$1`, retentionPG(value.runID), retentionPG(value.wikiID)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE knowledge_bases SET published_wiki_id=$2 WHERE id=$1`, retentionPG(knowledgeBaseID), retentionPG(currentWikiID)); err != nil {
		t.Fatal(err)
	}
	agentID, agentVersionID, retainedAgentRunID := seedRetentionAgentRun(t, ctx, tx, knowledgeBaseID, protectedRunID, protectedWikiID)
	_, _, expiredAgentRunID := seedRetentionAgentRun(t, ctx, tx, knowledgeBaseID, oldRunID, oldWikiID)
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET created_at=$2,completed_at=$2 WHERE id=$1`, retentionPG(expiredAgentRunID), old); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES($1,'DISCORD_BOT_TOKEN','Retention Discord','••••','test',decode(repeat('01',12),'hex'),decode('01','hex'),1);
		INSERT INTO discord_connections(
			id,display_name,display_key,credential_id,credential_version,lifecycle,state
		) VALUES($2,'Retention Discord','retention discord',$1,1,'ENABLED','CONNECTING');
		INSERT INTO discord_servers(connection_id,server_id,name,owner,refreshed_at)
		VALUES($2,'100','Retention Guild',false,clock_timestamp());
		INSERT INTO discord_channels(
			connection_id,server_id,channel_id,name,channel_type,position,
			effective_bot_permissions,everyone_can_view,refreshed_at
		) VALUES($2,'100','200','Retention Channel',0,0,0,true,clock_timestamp());
		INSERT INTO channel_bindings(
			id,connection_id,server_id,listen_channel_id,agent_id,reply_policy,
			allowed_role_ids,allowed_user_ids,rate_requests,rate_window_seconds
		) VALUES($3,$2,'100','200',$4,'SAME_CHANNEL','[]','[]',5,60);
		INSERT INTO channel_binding_triggers(
			binding_id,connection_id,server_id,listen_channel_id,enabled,trigger_type
		) VALUES($3,$2,'100','200',false,'MENTION')
	`, pgx.QueryExecModeSimpleProtocol, retentionPG(discordCredentialID), retentionPG(discordConnectionID),
		retentionPG(discordBindingID), retentionPG(agentID)); err != nil {
		t.Fatal(err)
	}
	for _, conversation := range []struct {
		id                        [16]byte
		user                      string
		created, activity, expiry time.Time
	}{
		{id: expiredDiscordContextID, user: "400", created: now.Add(-48 * time.Hour), activity: now.Add(-2 * time.Hour), expiry: now.Add(-time.Hour)},
		{id: overageDiscordContextID, user: "401", created: now.Add(-31 * 24 * time.Hour), activity: now, expiry: now.Add(time.Hour)},
		{id: liveDiscordContextID, user: "402", created: now, activity: now, expiry: now.Add(time.Hour)},
	} {
		if _, err = tx.Exec(ctx, `
			INSERT INTO discord_conversations(
				id,binding_id,agent_id,agent_version_id,external_user_id,destination_id,
				created_at,updated_at,last_activity_at,expires_at
			) VALUES($1,$2,$3,$4,$5,'200',$6,$7,$7,$8);
			INSERT INTO discord_conversation_messages(
				conversation_id,sequence,role,markdown,estimated_tokens,created_at
			) VALUES($1,1,'USER','retained context',4,$7)
		`, pgx.QueryExecModeSimpleProtocol, retentionPG(conversation.id), retentionPG(discordBindingID),
			retentionPG(agentID), retentionPG(agentVersionID), conversation.user,
			conversation.created, conversation.activity, conversation.expiry); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertAgentRunRetentionPlan(t, ctx, pool, retainedAgentRunID)

	sourceArtifacts := &fakeSourceArtifacts{}
	runArtifacts := &fakeRunArtifacts{}
	wikiArtifacts := &fakeWikiArtifacts{}
	service, err := NewService(pool, Policy{
		SourceSnapshots: 30 * 24 * time.Hour, FailedDrafts: 30 * 24 * time.Hour,
		JobLogs: 30 * 24 * time.Hour, EventLog: 30 * 24 * time.Hour, AgentRuns: 30 * 24 * time.Hour,
		DiscordContext: 30 * 24 * time.Hour,
		OldWikis:       30 * 24 * time.Hour, BatchSize: 20,
	}, sourceArtifacts, runArtifacts, wikiArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := service.Schedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := jobs.NewStore(pool, nil).Claim(ctx, "retention-worker", time.Minute)
	if err != nil || permit == nil || permit.JobID != scheduled {
		t.Fatalf("permit=%+v err=%v", permit, err)
	}
	result, err := service.Apply(ctx, *permit)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"agent_runs": 1, "discord_context": 2, "old_wikis": 1, "failed_drafts": 1,
		"job_logs": 1, "source_snapshots": 1,
	} {
		if result[key] != want {
			t.Fatalf("result[%s]=%v want %v; all=%v", key, result[key], want, result)
		}
	}
	if len(sourceArtifacts.discarded) != 1 || len(runArtifacts.discarded) != 1 || len(wikiArtifacts.discarded) != 1 ||
		wikiArtifacts.discarded[0][1] != artifacts.ID(oldWikiID) {
		t.Fatalf("source=%v run=%v wiki=%v", sourceArtifacts.discarded, runArtifacts.discarded, wikiArtifacts.discarded)
	}
	var expiredAgentRunCount, retainedAgentRunCount, oldWikiCount, currentWikiCount, protectedWikiCount, attempts, jobEvents int
	var purged pgtype.Timestamptz
	if err = pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM agent_runs WHERE id=$1),
			(SELECT count(*) FROM agent_runs WHERE id=$2)
	`, retentionPG(expiredAgentRunID), retentionPG(retainedAgentRunID)).Scan(&expiredAgentRunCount, &retainedAgentRunCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM wiki_versions WHERE id=$1`, retentionPG(oldWikiID)).Scan(&oldWikiCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM wiki_versions WHERE id=$1`, retentionPG(currentWikiID)).Scan(&currentWikiCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM wiki_versions WHERE id=$1`, retentionPG(protectedWikiID)).Scan(&protectedWikiCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT artifact_purged_at FROM source_revisions WHERE id=$1`, retentionPG(revisionID)).Scan(&purged); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM job_attempts WHERE job_id=$1`, retentionPG(oldJobID)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM job_events WHERE job_id=$1`, retentionPG(oldJobID)).Scan(&jobEvents); err != nil {
		t.Fatal(err)
	}
	if expiredAgentRunCount != 0 || retainedAgentRunCount != 1 || oldWikiCount != 0 || currentWikiCount != 1 || protectedWikiCount != 1 ||
		!purged.Valid || attempts != 0 || jobEvents != 0 {
		t.Fatalf("expiredAgentRun=%d retainedAgentRun=%d oldWiki=%d currentWiki=%d protectedWiki=%d purged=%v attempts=%d events=%d", expiredAgentRunCount, retainedAgentRunCount, oldWikiCount, currentWikiCount, protectedWikiCount, purged.Valid, attempts, jobEvents)
	}
	var deletedDiscordContexts, deletedDiscordMessages, liveDiscordContexts, liveDiscordMessages int
	if err = pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM discord_conversations WHERE id=ANY($1::uuid[])),
			(SELECT count(*) FROM discord_conversation_messages WHERE conversation_id=ANY($1::uuid[])),
			(SELECT count(*) FROM discord_conversations WHERE id=$2),
			(SELECT count(*) FROM discord_conversation_messages WHERE conversation_id=$2)
	`, []pgtype.UUID{retentionPG(expiredDiscordContextID), retentionPG(overageDiscordContextID)},
		retentionPG(liveDiscordContextID)).Scan(
		&deletedDiscordContexts, &deletedDiscordMessages, &liveDiscordContexts, &liveDiscordMessages,
	); err != nil {
		t.Fatal(err)
	}
	if deletedDiscordContexts != 0 || deletedDiscordMessages != 0 || liveDiscordContexts != 1 || liveDiscordMessages != 1 {
		t.Fatalf("Discord retention deleted contexts=%d messages=%d live contexts=%d messages=%d",
			deletedDiscordContexts, deletedDiscordMessages, liveDiscordContexts, liveDiscordMessages)
	}
	var completedAudits int
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action='retention.completed' AND request_id=$1 AND details=$2::jsonb
	`, retentionPG([16]byte(permit.JobID)), `{"agent_runs":1,"discord_context":2,"event_log":0,"failed_drafts":1,"job_logs":1,"old_wikis":1,"source_snapshots":1}`).Scan(&completedAudits); err != nil {
		t.Fatal(err)
	}
	if completedAudits != 1 {
		t.Fatalf("completed audits = %d", completedAudits)
	}
	var agentRunAudits int
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action='retention.agent_run_deleted' AND target_type='agent_run'
		  AND target_id=$1 AND details='{"retention_days":30}'::jsonb
	`, retentionPG(expiredAgentRunID)).Scan(&agentRunAudits); err != nil || agentRunAudits != 1 {
		t.Fatalf("Agent run deletion audits = %d, %v", agentRunAudits, err)
	}
	stale := *permit
	stale.LeaseGeneration++
	if _, err = service.Apply(ctx, stale); !errors.Is(err, jobs.ErrStalePermit) {
		t.Fatalf("stale retention err = %v", err)
	}
}

func assertAgentRunRetentionPlan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, templateID [16]byte) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_runs(
			id,agent_id,agent_version_id,agent_resource_version,agent_version_number,
			model_profile_id,model_profile_version_id,model_profile_version_number,
			provider_endpoint_id,captured_endpoint_configuration_version,captured_credential_id,captured_credential_version,
			origin,subject,request_digest,effective_access_policy,outcome,model_usage,latency_ms,
			tool_calls,citations,sanitized_error,created_at,completed_at
		)
		SELECT gen_random_uuid(),agent_id,agent_version_id,agent_resource_version,agent_version_number,
		       model_profile_id,model_profile_version_id,model_profile_version_number,
		       provider_endpoint_id,captured_endpoint_configuration_version,captured_credential_id,captured_credential_version,
		       origin,'retention-plan',decode(repeat('ef',32),'hex'),effective_access_policy,outcome,model_usage,latency_ms,
		       tool_calls,citations,sanitized_error,
		       clock_timestamp()-(value||' seconds')::interval,
		       clock_timestamp()-(value||' seconds')::interval
		FROM agent_runs CROSS JOIN generate_series(1,1000) AS value
		WHERE id=$1;
		INSERT INTO agent_run_knowledge_bases(
			run_id,position,knowledge_base_id,knowledge_base_version,access_policy,
			wiki_version_id,documentation_run_id,source_revision_ids,source_scope_digest
		)
		SELECT run.id,scope.position,scope.knowledge_base_id,scope.knowledge_base_version,scope.access_policy,
		       scope.wiki_version_id,scope.documentation_run_id,scope.source_revision_ids,scope.source_scope_digest
		FROM agent_runs AS run
		CROSS JOIN agent_run_knowledge_bases AS scope
		WHERE run.subject='retention-plan' AND scope.run_id=$1;
		ANALYZE agent_runs;
		ANALYZE agent_run_knowledge_bases
	`, pgx.QueryExecModeSimpleProtocol, retentionPG(templateID)); err != nil {
		t.Fatal(err)
	}
	var wikiVersionID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		SELECT wiki_version_id
		FROM agent_run_knowledge_bases WHERE run_id=$1
	`, retentionPG(templateID)).Scan(&wikiVersionID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	var plan []byte
	if err = tx.QueryRow(ctx, `
		EXPLAIN (FORMAT JSON,COSTS OFF)
		SELECT id FROM agent_runs
		WHERE completed_at <= $1
		ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
	`, time.Now().UTC().Add(time.Hour), 20).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plan, []byte("ix_agent_runs_completed")) {
		t.Fatalf("Agent run retention plan does not use completed index: %s", plan)
	}
	if err = tx.QueryRow(ctx, `
		EXPLAIN (FORMAT JSON,COSTS OFF)
		SELECT 1 FROM agent_run_knowledge_bases
		WHERE wiki_version_id=$1 LIMIT 1
	`, wikiVersionID).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plan, []byte("ix_agent_run_knowledge_bases_wiki")) {
		t.Fatalf("old-wiki Agent run scope probe does not use wiki index: %s", plan)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM agent_runs WHERE subject='retention-plan'`); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionDeletionIntentsSurviveFailureAndServiceRestart(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateRetentionDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	for _, test := range []struct {
		name string
		kind deletionKind
	}{
		{name: "wiki version", kind: wikiVersionIntent},
		{name: "failed draft", kind: failedDraftIntent},
		{name: "source snapshot", kind: sourceSnapshotIntent},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err = pool.Exec(ctx, `TRUNCATE knowledge_bases,jobs,audit_events,event_log,artifact_deletion_intents CASCADE`); err != nil {
				t.Fatal(err)
			}
			knowledgeBaseID := retentionTestID(t)
			resourceID := retentionTestID(t)
			ownerID := knowledgeBaseID
			if _, err = pool.Exec(ctx, `
				INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language)
				VALUES($1,'Intent recovery','intent recovery','RESTRICTED','ACTIVE','','en')
			`, retentionPG(knowledgeBaseID)); err != nil {
				t.Fatal(err)
			}

			switch test.kind {
			case wikiVersionIntent, failedDraftIntent:
				prepareJobID := retentionTestID(t)
				status := "FAILED"
				if test.kind == wikiVersionIntent {
					status = "PUBLISHED"
				}
				if _, err = pool.Exec(ctx, `
					INSERT INTO jobs(
						id,job_type,target_type,target_id,payload,operation_key,status,
						attempt_count,max_attempts,progress,lease_generation,result,
						created_at,updated_at,started_at,finished_at
					) VALUES($1,'PREPARE_RUN','knowledge_base',$2,'{}',$3,'SUCCEEDED',1,3,100,1,'{}',clock_timestamp(),clock_timestamp(),clock_timestamp(),clock_timestamp())
				`, retentionPG(prepareJobID), retentionPG(knowledgeBaseID), "fixture:"+jobs.UUID(prepareJobID).String()); err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, `
					INSERT INTO documentation_runs(
						id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,
						instructions,language,sanitized_error,created_at,updated_at,completed_at
					) VALUES($1,$2,$3::varchar,$4,1,'','en',CASE WHEN $3::text='FAILED' THEN 'failed' ELSE NULL END,clock_timestamp(),clock_timestamp(),clock_timestamp())
				`, retentionPG(resourceID), retentionPG(knowledgeBaseID), status, retentionPG(prepareJobID)); err != nil {
					t.Fatal(err)
				}
				if test.kind == wikiVersionIntent {
					wikiID := retentionTestID(t)
					if _, err = pool.Exec(ctx, `
						INSERT INTO wiki_versions(id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count)
						VALUES($1,$2,$3,'recoverable',decode(repeat('ab',32),'hex'),1)
					`, retentionPG(wikiID), retentionPG(knowledgeBaseID), retentionPG(resourceID)); err != nil {
						t.Fatal(err)
					}
					resourceID = wikiID
				}
			case sourceSnapshotIntent:
				ownerID = retentionTestID(t)
				if _, err = pool.Exec(ctx, `
					INSERT INTO sources(id,knowledge_base_id,kind,display_name,display_key,privacy,lifecycle,health)
					VALUES($1,$2,'REPOSITORY','Recovery source','recovery source','PUBLIC','DRAFT','UNKNOWN')
				`, retentionPG(ownerID), retentionPG(knowledgeBaseID)); err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, `
					INSERT INTO source_revisions(
						id,source_id,observed_ref_kind,observed_ref,native_version,fingerprint,
						artifact_key,file_count,byte_count,ignored_paths
					) VALUES($1,$2,'BRANCH','main','recoverable',decode(repeat('cd',32),'hex'),'recoverable',1,1,'[]')
				`, retentionPG(resourceID), retentionPG(ownerID)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
				VALUES($1,$2,$3,$4)
			`, string(test.kind), retentionPG(resourceID), retentionPG(ownerID), retentionPG(knowledgeBaseID)); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
				VALUES('KNOWLEDGE_BASE',$1,$1,$1)
			`, retentionPG(knowledgeBaseID)); err != nil {
				t.Fatal(err)
			}

			sourceArtifacts := &fakeSourceArtifacts{}
			runArtifacts := &fakeRunArtifacts{}
			wikiArtifacts := &fakeWikiArtifacts{}
			switch test.kind {
			case wikiVersionIntent:
				wikiArtifacts.failures = 1
			case failedDraftIntent:
				runArtifacts.failures = 1
			case sourceSnapshotIntent:
				sourceArtifacts.failures = 1
			}
			policy := Policy{
				SourceSnapshots: 30 * 24 * time.Hour, FailedDrafts: 30 * 24 * time.Hour,
				JobLogs: 30 * 24 * time.Hour, EventLog: 30 * 24 * time.Hour,
				AgentRuns: 30 * 24 * time.Hour, DiscordContext: 30 * 24 * time.Hour,
				OldWikis:  30 * 24 * time.Hour,
				BatchSize: 20,
			}
			service, err := NewService(pool, policy, sourceArtifacts, runArtifacts, wikiArtifacts)
			if err != nil {
				t.Fatal(err)
			}
			jobID, err := service.Schedule(ctx)
			if err != nil {
				t.Fatal(err)
			}
			permit, err := jobs.NewStore(pool, nil).Claim(ctx, "intent-worker", time.Minute)
			if err != nil || permit == nil || permit.JobID != jobID {
				t.Fatalf("permit=%#v err=%v", permit, err)
			}
			if counts, applyErr := service.applyDeletionIntents(ctx, *permit); applyErr == nil || counts[test.kind] != 0 {
				t.Fatalf("first deletion counts=%v err=%v", counts, applyErr)
			}
			var pending int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM artifact_deletion_intents WHERE kind=$1 AND resource_id=$2`, string(test.kind), retentionPG(resourceID)).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("pending intent=%d err=%v", pending, err)
			}

			restarted, err := NewService(pool, policy, sourceArtifacts, runArtifacts, wikiArtifacts)
			if err != nil {
				t.Fatal(err)
			}
			counts, err := restarted.applyDeletionIntents(ctx, *permit)
			if err != nil || counts[test.kind] != 1 {
				t.Fatalf("restart deletion counts=%v err=%v", counts, err)
			}
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM artifact_deletion_intents WHERE kind=$1 AND resource_id=$2`, string(test.kind), retentionPG(resourceID)).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("remaining intent=%d err=%v", pending, err)
			}
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM artifact_deletion_intents WHERE kind='KNOWLEDGE_BASE' AND resource_id=$1`, retentionPG(knowledgeBaseID)).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("parent purge intent=%d err=%v", pending, err)
			}
			switch test.kind {
			case wikiVersionIntent:
				var remaining int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM wiki_versions WHERE id=$1`, retentionPG(resourceID)).Scan(&remaining); err != nil || remaining != 0 || len(wikiArtifacts.discarded) != 2 {
					t.Fatalf("wiki remaining=%d calls=%d err=%v", remaining, len(wikiArtifacts.discarded), err)
				}
			case failedDraftIntent:
				var audits int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='retention.failed_draft_deleted' AND target_id=$1`, retentionPG(resourceID)).Scan(&audits); err != nil || audits != 1 || len(runArtifacts.discarded) != 2 {
					t.Fatalf("draft audits=%d calls=%d err=%v", audits, len(runArtifacts.discarded), err)
				}
			case sourceSnapshotIntent:
				var purged pgtype.Timestamptz
				if err = pool.QueryRow(ctx, `SELECT artifact_purged_at FROM source_revisions WHERE id=$1`, retentionPG(resourceID)).Scan(&purged); err != nil || !purged.Valid || len(sourceArtifacts.discarded) != 2 {
					t.Fatalf("source purged=%v calls=%d err=%v", purged.Valid, len(sourceArtifacts.discarded), err)
				}
			}
		})
	}
}

func TestEventLogRetentionAdvancesOnlyAContiguousCursorPrefix(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateRetentionDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		TRUNCATE event_log RESTART IDENTITY;
		UPDATE event_stream_state SET pruned_through=0,updated_at=clock_timestamp() WHERE id=1
	`); err != nil {
		t.Fatal(err)
	}
	resourceID := retentionTestID(t)
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	recent := time.Now().UTC()
	for _, createdAt := range []time.Time{old, recent, old} {
		if _, err = pool.Exec(ctx, `
			INSERT INTO event_log(event_type,resource_type,resource_id,snapshot,created_at)
			VALUES('fixture.changed','fixture',$1,'{}',$2)
		`, retentionPG(resourceID), createdAt); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{pool: pool, policy: Policy{BatchSize: 100}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := service.deleteEventLog(ctx, tx, recent.Add(-30*24*time.Hour))
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var watermark int64
	var sequences []int64
	rows, err := pool.Query(ctx, `SELECT sequence FROM event_log ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sequence int64
		if err = rows.Scan(&sequence); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		sequences = append(sequences, sequence)
	}
	rows.Close()
	if err = pool.QueryRow(ctx, `SELECT pruned_through FROM event_stream_state WHERE id=1`).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || watermark != 1 || len(sequences) != 2 || sequences[0] != 2 || sequences[1] != 3 {
		t.Fatalf("deleted=%d watermark=%d sequences=%v", deleted, watermark, sequences)
	}
}

func seedRetentionAgentRun(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	knowledgeBaseID, documentationRunID, wikiVersionID [16]byte,
) ([16]byte, [16]byte, [16]byte) {
	t.Helper()
	actorID := retentionTestID(t)
	endpointID := retentionTestID(t)
	profileID := retentionTestID(t)
	profileVersionID := retentionTestID(t)
	agentID := retentionTestID(t)
	agentVersionID := retentionTestID(t)
	agentRunID := retentionTestID(t)
	unique := jobs.UUID(actorID).String()
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO operators(id,username,username_key,password_hash) VALUES($1,$2,$2,'unused')`, []any{retentionPG(actorID), "retention-" + unique}},
		{`INSERT INTO provider_endpoints(id,display_name,display_key,base_url,lifecycle,health,health_checked_at) VALUES($1,$2,$2,'https://models.example.test','ACTIVE','HEALTHY',clock_timestamp())`, []any{retentionPG(endpointID), "retention-endpoint-" + unique}},
		{`INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id) VALUES($1,$2,'retention-model','AVAILABLE',$3)`, []any{retentionPG(profileID), retentionPG(endpointID), retentionPG(profileVersionID)}},
		{`
			INSERT INTO model_profile_versions(
				id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,
				supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,
				timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id
			) VALUES($1,$2,1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,'NONE',30,0,1,'{}','{}','OPERATOR',$3)
		`, []any{retentionPG(profileVersionID), retentionPG(profileID), retentionPG(actorID)}},
		{`INSERT INTO agents(id,agent_key,lifecycle,current_version_id,activated_at) VALUES($1,$2,'ACTIVE',$3,clock_timestamp())`, []any{retentionPG(agentID), "retention-" + unique, retentionPG(agentVersionID)}},
		{`
			INSERT INTO agent_versions(
				id,agent_id,version_number,display_name,response_language,identity_instructions,model_profile_id,
				reasoning_effort,answer_mode,evidence_access,refusal_markdown,max_tool_calls,max_answer_tokens,created_by_operator_id
			) VALUES($1,$2,1,'Retention Agent','en','Answer from retained evidence.',$3,'NONE','SINGLE_PASS','WIKI_ONLY','Cannot answer.',0,1024,$4)
		`, []any{retentionPG(agentVersionID), retentionPG(agentID), retentionPG(profileID), retentionPG(actorID)}},
		{`INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id) VALUES($1,$2,0,$3)`, []any{retentionPG(agentID), retentionPG(agentVersionID), retentionPG(knowledgeBaseID)}},
		{`
			INSERT INTO agent_runs(
				id,agent_id,agent_version_id,agent_resource_version,agent_version_number,
				model_profile_id,model_profile_version_id,model_profile_version_number,
				provider_endpoint_id,captured_endpoint_configuration_version,origin,subject,
				request_digest,effective_access_policy,outcome,latency_ms
			) VALUES($1,$2,$3,1,1,$4,$5,1,$6,1,'HTTP','retention-test',
				decode(repeat('ab',32),'hex'),'RESTRICTED','ANSWERED',0)
		`, []any{retentionPG(agentRunID), retentionPG(agentID), retentionPG(agentVersionID), retentionPG(profileID), retentionPG(profileVersionID), retentionPG(endpointID)}},
		{`
			INSERT INTO agent_run_knowledge_bases(
				run_id,position,knowledge_base_id,knowledge_base_version,access_policy,
				wiki_version_id,documentation_run_id,source_scope_digest
			) VALUES($1,0,$2,1,'RESTRICTED',$3,$4,decode(repeat('cd',32),'hex'))
		`, []any{retentionPG(agentRunID), retentionPG(knowledgeBaseID), retentionPG(wikiVersionID), retentionPG(documentationRunID)}},
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	return agentID, agentVersionID, agentRunID
}

func retentionTestID(t *testing.T) [16]byte {
	t.Helper()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id
}

func retentionPG(id [16]byte) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func migrateRetentionDatabase(t *testing.T, ctx context.Context, databaseURL string) {
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
