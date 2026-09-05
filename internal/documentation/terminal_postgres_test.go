package docgen

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTerminalCallbackClosesEveryDocumentationStage(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDocumentationDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tests := []struct {
		name      string
		jobType   jobs.Type
		jobStatus jobs.Status
		runStatus RunStatus
		wantRun   RunStatus
		wantPages PageStatus
		wantError string
	}{
		{"prepare failure", jobs.PrepareRun, jobs.Failed, RunPreparing, RunFailed, "", "documentation:preparation_failed"},
		{"prepare cancellation", jobs.PrepareRun, jobs.Cancelled, RunPreparing, RunInterrupted, "", "documentation:cancelled"},
		{"plan failure", jobs.PlanRun, jobs.Failed, RunPlanning, RunFailed, "", "documentation:planning_failed"},
		{"plan cancellation", jobs.PlanRun, jobs.Cancelled, RunPlanning, RunInterrupted, "", "documentation:cancelled"},
		{"page failure", jobs.GeneratePage, jobs.Failed, RunGenerating, RunInterrupted, PageSkipped, "documentation:page_skipped"},
		{"page cancellation", jobs.GeneratePage, jobs.Cancelled, RunGenerating, RunInterrupted, PageSkipped, "documentation:cancelled"},
		{"finalize failure", jobs.FinalizeRun, jobs.Failed, RunFinalizing, RunFailed, "", "documentation:publication_failed"},
		{"finalize cancellation", jobs.FinalizeRun, jobs.Cancelled, RunFinalizing, RunInterrupted, "", "documentation:cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err = pool.Exec(ctx, `TRUNCATE operators,knowledge_bases,jobs,event_log,audit_events RESTART IDENTITY CASCADE`); err != nil {
				t.Fatal(err)
			}
			kbID := mustID(t, "11111111-1111-4111-8111-111111111111")
			runID := mustID(t, "22222222-2222-4222-8222-222222222222")
			prepareID := mustID(t, "33333333-3333-4333-8333-333333333333")
			activeID := mustID(t, "44444444-4444-4444-8444-444444444444")
			pageID := mustID(t, "55555555-5555-4555-8555-555555555555")
			siblingID := mustID(t, "66666666-6666-4666-8666-666666666666")
			now := time.Now().UTC().Truncate(time.Microsecond)
			if _, err = pool.Exec(ctx, `INSERT INTO knowledge_bases(id,name,name_key) VALUES($1,'Terminal docs','terminal docs')`, pgUUID(kbID)); err != nil {
				t.Fatal(err)
			}
			jobID := activeID
			if test.jobType == jobs.PrepareRun {
				jobID = prepareID
				insertDocumentationTerminalJob(t, ctx, pool, prepareID, jobs.PrepareRun,
					"knowledge_base", kbID, test.jobStatus, now)
			} else {
				insertDocumentationTerminalJob(t, ctx, pool, prepareID, jobs.PrepareRun,
					"knowledge_base", kbID, jobs.Succeeded, now)
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO documentation_runs(
					id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,
					instructions,language,created_at,updated_at
				) VALUES($1,$2,$3,$4,1,'','en',$5,$5)
			`, pgUUID(runID), pgUUID(kbID), string(test.runStatus), pgUUID(prepareID), now); err != nil {
				t.Fatal(err)
			}
			targetType, targetID := "documentation_run", ID(runID)
			if test.jobType == jobs.PrepareRun {
				targetType, targetID = "knowledge_base", kbID
			}
			if test.jobType == jobs.GeneratePage {
				targetType, targetID = "documentation_page", pageID
			}
			if test.jobType != jobs.PrepareRun {
				insertDocumentationTerminalJob(t, ctx, pool, jobID, test.jobType,
					targetType, targetID, test.jobStatus, now)
			}
			if test.jobType == jobs.GeneratePage {
				insertDocumentationTerminalJob(t, ctx, pool, siblingID, jobs.GeneratePage,
					"documentation_page", siblingID, jobs.Pending, now)
				for position, page := range []struct {
					id, job ID
				}{{pageID, jobID}, {siblingID, siblingID}} {
					if _, err = pool.Exec(ctx, `
						INSERT INTO documentation_pages(
							id,run_id,job_id,position,slug,title,purpose,
							related_pages,source_seed_paths,status,created_at,updated_at
						) VALUES($1,$2,$3,$4,$5,$6,'Explain.','[]'::jsonb,'[]'::jsonb,'PENDING',$7,$7)
					`, pgUUID(page.id), pgUUID(runID), pgUUID(page.job), position,
						"page-"+page.id.String(), "Page", now); err != nil {
						t.Fatal(err)
					}
				}
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			callbackErr := TerminalCallback(ctx, tx, jobs.Snapshot{
				ID: jobs.JobID(jobID), Type: test.jobType, TargetType: targetType,
				TargetID: jobs.UUID(targetID), Status: test.jobStatus,
				UpdatedAt: now, FinishedAt: &now,
			})
			if callbackErr != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(callbackErr)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var status, sanitized string
			var completed *time.Time
			if err = pool.QueryRow(ctx, `SELECT status,sanitized_error,completed_at FROM documentation_runs WHERE id=$1`, pgUUID(runID)).Scan(&status, &sanitized, &completed); err != nil {
				t.Fatal(err)
			}
			if RunStatus(status) != test.wantRun || sanitized != test.wantError || completed == nil {
				t.Fatalf("run status=%s error=%q completed=%v", status, sanitized, completed)
			}
			if test.wantPages != "" {
				var nonterminal int
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM documentation_pages WHERE status<>$1`, string(test.wantPages)).Scan(&nonterminal); err != nil || nonterminal != 0 {
					t.Fatalf("nonterminal pages=%d err=%v", nonterminal, err)
				}
			}
			var audits, events int
			if err = pool.QueryRow(ctx, `
				SELECT (SELECT count(*) FROM audit_events WHERE target_type='documentation_run'),
				       (SELECT count(*) FROM event_log WHERE resource_type='documentation_run')
			`).Scan(&audits, &events); err != nil || audits != 1 || events != 1 {
				t.Fatalf("terminal records audits=%d events=%d err=%v", audits, events, err)
			}
		})
	}
}

func insertDocumentationTerminalJob(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	id ID,
	jobType jobs.Type,
	targetType string,
	targetID ID,
	status jobs.Status,
	now time.Time,
) {
	t.Helper()
	finished := any(nil)
	if status == jobs.Succeeded || status == jobs.Failed || status == jobs.Cancelled {
		finished = now
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO jobs(
			id,job_type,target_type,target_id,payload,operation_key,status,
			attempt_count,max_attempts,created_at,updated_at,finished_at
		) VALUES($1,$2,$3,$4,'{}'::jsonb,$5,$6,0,3,$7,$7,$8)
	`, pgUUID(id), string(jobType), targetType, pgUUID(targetID),
		"terminal-test:"+id.String(), string(status), now, finished); err != nil {
		t.Fatal(err)
	}
}
