package sources

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTerminalCallbackClosesSourceCaptures(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateSourceDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tests := []struct {
		name       string
		jobType    jobs.Type
		jobStatus  jobs.Status
		syncKind   SyncKind
		wantHealth Health
		wantError  string
	}{
		{"validation failure", jobs.ValidateSource, jobs.Failed, Validation, Unhealthy, "source_validation:job_failed"},
		{"sync cancellation", jobs.SyncSource, jobs.Cancelled, Synchronization, Healthy, "source_sync:cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err = pool.Exec(ctx, `TRUNCATE operators,knowledge_bases,sources,jobs,event_log,audit_events RESTART IDENTITY CASCADE`); err != nil {
				t.Fatal(err)
			}
			kbID := terminalSourceID(t, "11111111-1111-4111-8111-111111111111")
			sourceID := terminalSourceID(t, "22222222-2222-4222-8222-222222222222")
			jobID := terminalSourceID(t, "33333333-3333-4333-8333-333333333333")
			syncID := terminalSourceID(t, "44444444-4444-4444-8444-444444444444")
			candidateID := terminalSourceID(t, "55555555-5555-4555-8555-555555555555")
			now := time.Now().UTC().Truncate(time.Microsecond)
			if _, err = pool.Exec(ctx, `INSERT INTO knowledge_bases(id,name,name_key) VALUES($1,'Source terminal','source terminal')`, pgUUID(kbID)); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO sources(
					id,knowledge_base_id,kind,display_name,display_key,privacy,lifecycle,
					health,checked_at,version,configuration_version,validated_configuration_version
				) VALUES($1,$2,'REPOSITORY','Repository','repository','PUBLIC','ACTIVE',
				         'HEALTHY',$3,1,1,1)
			`, pgUUID(sourceID), pgUUID(kbID), now); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO repository_sources(
					source_id,remote_url,ref_kind,ref_value,include_patterns,exclude_patterns
				) VALUES($1,'https://example.test/repository.git','BRANCH','main','[]'::jsonb,'[]'::jsonb)
			`, pgUUID(sourceID)); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO jobs(
					id,job_type,target_type,target_id,payload,operation_key,status,
					attempt_count,max_attempts,created_at,updated_at,finished_at
				) VALUES($1,$2,'source',$3,'{}'::jsonb,$4,$5,1,3,$6,$6,$6)
			`, pgUUID(jobID), string(test.jobType), pgUUID(sourceID),
				"terminal-source:"+syncID.String(), string(test.jobStatus), now); err != nil {
				t.Fatal(err)
			}
			candidate := any(nil)
			if test.syncKind == Synchronization {
				candidate = pgUUID(candidateID)
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO source_syncs(
					id,source_id,job_id,sync_kind,captured_source_version,
					captured_configuration_version,captured_privacy,captured_remote_url,
					captured_ref_kind,captured_ref_value,captured_include_patterns,
					captured_exclude_patterns,candidate_revision_id,status,created_at,
					captured_source_kind
				) VALUES($1,$2,$3,$4,1,1,'PUBLIC','https://example.test/repository.git',
				         'BRANCH','main','[]'::jsonb,'[]'::jsonb,$5,'PENDING',$6,'REPOSITORY')
			`, pgUUID(syncID), pgUUID(sourceID), pgUUID(jobID), string(test.syncKind), candidate, now); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			callbackErr := TerminalCallback(ctx, tx, jobs.Snapshot{
				ID: jobs.JobID(jobID), Type: test.jobType, TargetType: "source",
				TargetID: jobs.UUID(sourceID), Status: test.jobStatus,
				UpdatedAt: now, FinishedAt: &now,
			})
			if callbackErr != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(callbackErr)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var syncStatus, syncError, health string
			var started, completed *time.Time
			if err = pool.QueryRow(ctx, `
				SELECT ss.status,ss.sanitized_error,ss.started_at,ss.completed_at,s.health
				FROM source_syncs ss JOIN sources s ON s.id=ss.source_id WHERE ss.id=$1
			`, pgUUID(syncID)).Scan(&syncStatus, &syncError, &started, &completed, &health); err != nil {
				t.Fatal(err)
			}
			if SyncStatus(syncStatus) != SyncFailed || syncError != test.wantError ||
				started == nil || completed == nil || Health(health) != test.wantHealth {
				t.Fatalf("sync=%s error=%q started=%v completed=%v health=%s",
					syncStatus, syncError, started, completed, health)
			}
		})
	}
}

func terminalSourceID(t *testing.T, raw string) ID {
	t.Helper()
	id, err := jobs.ParseUUID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ID(id)
}
