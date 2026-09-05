package docgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestDocumentationStorePostgreSQLPublishesAndReads(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(fmt.Sprintf("content-drift=%t", drift), func(t *testing.T) { testDocumentationPublication(t, drift) })
	}
}
func testDocumentationPublication(t *testing.T, drift bool) {
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
	if _, err = pool.Exec(ctx, `TRUNCATE operators,knowledge_bases,sources,source_revisions,provider_endpoints,model_profiles,model_profile_versions,model_assignments,jobs,event_log,audit_events,idempotency_records RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	actorID := mustID(t, "77777777-6666-4555-8444-333333333333")
	kbID := mustID(t, "00112233-4455-6677-8899-aabbccddeeff")
	sourceID := mustID(t, "22222222-3333-4444-8555-666666666666")
	revisionID := mustID(t, "33333333-4444-4555-8666-777777777777")
	endpointID := mustID(t, "44444444-5555-4666-8777-888888888888")
	profileID := mustID(t, "55555555-6666-4777-8888-999999999999")
	profileVersionID := mustID(t, "66666666-7777-4888-8999-aaaaaaaaaaaa")
	fixtureTx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureTx.Rollback(ctx)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO operators(id,username,username_key,password_hash) VALUES($1,'Documentation Operator','documentation operator','unused')`, []any{pgUUID(actorID)}},
		{`INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language,version) VALUES($1,'Documentation KB','documentation kb','RESTRICTED','ACTIVE','Use exact evidence.','en',1)`, []any{pgUUID(kbID)}},
		{`INSERT INTO sources(id,knowledge_base_id,kind,display_name,display_key,privacy,lifecycle,health,checked_at,version,configuration_version,validated_configuration_version) VALUES($1,$2,'REPOSITORY','Docs','docs','PUBLIC','ACTIVE','HEALTHY',clock_timestamp(),2,1,1)`, []any{pgUUID(sourceID), pgUUID(kbID)}},
		{`INSERT INTO source_revisions(id,source_id,observed_ref_kind,observed_ref,native_version,fingerprint,artifact_key,file_count,byte_count,ignored_paths) VALUES($1,$2,'BRANCH','main',$3,$4,$5,1,24,'[]'::jsonb)`, []any{pgUUID(revisionID), pgUUID(sourceID), strings.Repeat("a", 40), bytes.Repeat([]byte{'f'}, 32), "sources/" + sourceID.String() + "/snapshots/" + revisionID.String()}},
		{`UPDATE sources SET current_revision_id=$2 WHERE id=$1`, []any{pgUUID(sourceID), pgUUID(revisionID)}},
		{`INSERT INTO provider_endpoints(id,display_name,display_key,base_url,headers,lifecycle,health,version,configuration_version) VALUES($1,'Docs model','docs model','https://models.example/v1','{}'::jsonb,'ACTIVE','UNKNOWN',1,1)`, []any{pgUUID(endpointID)}},
		{`INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id,version) VALUES($1,$2,'docs-model','MANUAL',$3,1)`, []any{pgUUID(profileID), pgUUID(endpointID), pgUUID(profileVersionID)}},
		{`INSERT INTO model_profile_versions(id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id) VALUES($1,$2,1,1,'CHAT_COMPLETIONS',16000,2000,true,true,true,true,'NONE',30,0,2,'{}'::jsonb,'{}'::jsonb,'OPERATOR',$3)`, []any{pgUUID(profileVersionID), pgUUID(profileID), pgUUID(actorID)}},
	} {
		if _, err = fixtureTx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for index, role := range []string{"DOCUMENTATION_PLANNER", "DOCUMENTATION_WRITER"} {
		assignmentID := actorID
		assignmentID[15] = byte(index + 1)
		if _, err = fixtureTx.Exec(ctx, `INSERT INTO model_assignments(id,knowledge_base_id,role,model_profile_id,reasoning_effort,answer_mode,version) VALUES($1,$2,$3,$4,'NONE','TOOL_CALLING',1)`, pgUUID(assignmentID), pgUUID(kbID), role, pgUUID(profileID)); err != nil {
			t.Fatal(err)
		}
	}
	if err = fixtureTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewCredentialVault("active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	runArtifacts, err := artifacts.NewRunStore(root)
	if err != nil {
		t.Fatal(err)
	}
	wikiArtifacts, err := artifacts.NewWikiStore(root)
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewStore(pool, TerminalCallback)
	evidenceArtifacts, err := sourcefiles.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, queue, vault, runArtifacts, wikiArtifacts, evidenceArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	storedSource, err := evidenceArtifacts.StoreSnapshot(sourcefiles.ID(sourceID), sourcefiles.ID(revisionID), sourcefiles.Files(sourcefiles.File{Path: "main.go", Content: []byte("package main\nexact captured evidence\n")}), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := evidenceArtifacts.DiscardSnapshot(sourcefiles.ID(sourceID), sourcefiles.ID(revisionID)); err != nil {
			t.Error(err)
		}
	})
	if _, err = pool.Exec(ctx, `UPDATE source_revisions SET fingerprint=$2 WHERE id=$1`, pgUUID(revisionID), storedSource.Fingerprint.Digest[:]); err != nil {
		t.Fatal(err)
	}
	prepareJobID, err := store.RequestGeneration(ctx, kbID, 1, actorID, "publish-flow")
	if err != nil {
		t.Fatal(err)
	}
	permit := claimExpected(t, ctx, queue, prepareJobID)
	prepared, err := store.Prepare(ctx, kbID, permit)
	if err != nil || prepared.Run.Status != RunPlanning {
		t.Fatalf("prepared=%+v err=%v", prepared.Run, err)
	}
	if err = queue.CompleteAcceptedResult(ctx, permit, runResult(prepared)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RequestGeneration(ctx, kbID, 1, actorID, "concurrent-publish-flow"); err == nil || !strings.Contains(err.Error(), "documentation run is already active") {
		t.Fatalf("concurrent request err=%v", err)
	}
	latestRevision := revisionID
	if drift {
		for range 2 {
			latestRevision[15]++
			if _, err = pool.Exec(ctx, `INSERT INTO source_revisions(id,source_id,observed_ref_kind,observed_ref,native_version,fingerprint,artifact_key,file_count,byte_count)
	   VALUES($1,$2,'BRANCH','main',$3,$4,$5,1,24)`, pgUUID(latestRevision), pgUUID(sourceID), strings.Repeat("b", 40), bytes.Repeat([]byte{latestRevision[15]}, 32), "sources/"+sourceID.String()+"/snapshots/"+latestRevision.String()); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `UPDATE sources SET current_revision_id=$2 WHERE id=$1`, pgUUID(sourceID), pgUUID(latestRevision)); err != nil {
				t.Fatal(err)
			}
		}
		for _, mutation := range []string{"configuration_version=configuration_version+1,validated_configuration_version=validated_configuration_version+1", "lifecycle='DISABLED',disabled_at=clock_timestamp()"} {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `UPDATE sources SET `+mutation+` WHERE id=$1`, pgUUID(sourceID)); err != nil {
				t.Fatal(err)
			}
			current, err := store.configurationCurrentTx(ctx, tx, prepared.Run)
			if err != nil || current {
				t.Fatalf("configuration change accepted: %s %t %v", mutation, current, err)
			}
			if err = tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	planPermit := claimType(t, ctx, queue, jobs.PlanRun)
	planCommand, err := queue.GetCommand(ctx, planPermit)
	if err != nil || planCommand.ConcurrencyKey != "model-profile:"+profileID.String() || planCommand.ConcurrencyLimit != 2 {
		t.Fatalf("planner queue admission=%+v err=%v", planCommand, err)
	}
	plan := PagePlan{Pages: []PlannedPage{{Slug: "overview", Title: "Overview", Purpose: "Explain the system."}}}
	plannerUsage := ModelUsage{ModelCalls: 1, InputTokens: 2, OutputTokens: 3, TotalTokens: 5, TruncatedToolResults: 6}
	generating, err := store.AcceptPlan(ctx, prepared.Run.ID, plan, planPermit, plannerUsage)
	if err != nil || generating.Run.Status != RunGenerating || len(generating.Pages) != 1 || generating.Run.PlannerUsage != plannerUsage {
		t.Fatalf("generating=%+v err=%v", generating, err)
	}
	if err = queue.CompleteAcceptedResult(ctx, planPermit, runResult(generating)); err != nil {
		t.Fatal(err)
	}
	pagePermit := claimType(t, ctx, queue, jobs.GeneratePage)
	pageCommand, err := queue.GetCommand(ctx, pagePermit)
	if err != nil || pageCommand.ConcurrencyKey != "model-profile:"+profileID.String() || pageCommand.ConcurrencyLimit != 2 {
		t.Fatalf("writer queue admission=%+v err=%v", pageCommand, err)
	}
	page := generating.Pages[0]
	running, err := store.BeginPage(ctx, page, pagePermit)
	if err != nil || running.Pages[0].Status != PageRunning || running.Pages[0].AttemptCount != 1 {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	claimsJSON := []byte(fmt.Sprintf(`{"claims":[{"id":"entry","statement":"The entry point is documented.","evidence":[{"id":"entry-source","resource":"repo://%s@%s/main.go#L2-L2","source_revision_id":"%s","source_version":"%x"}]}]}`, sourceID.String(), strings.Repeat("a", 40), revisionID.String(), storedSource.Fingerprint.Digest))
	accepted := artifacts.Page{Slug: "overview", Title: "Overview", Description: "System overview.", PageType: "Concept", Markdown: "# Overview\n", ContentSHA256: sha256.Sum256([]byte("# Overview\n")), ClaimsJSON: claimsJSON, ClaimsSHA256: sha256.Sum256(claimsJSON)}
	if err = runArtifacts.SavePage(artifacts.ID(kbID), artifacts.ID(prepared.Run.ID), accepted); err != nil {
		t.Fatal(err)
	}
	writerUsage := ModelUsage{ModelCalls: 1, InputTokens: 4, OutputTokens: 5, TotalTokens: 9, TruncatedToolResults: 10}
	finalizing, err := store.CompletePage(ctx, running.Pages[0], accepted, pagePermit, writerUsage)
	if err != nil || finalizing.Run.Status != RunFinalizing || len(finalizing.Pages) != 1 || finalizing.Pages[0].Usage != writerUsage {
		t.Fatalf("finalizing=%+v err=%v", finalizing, err)
	}
	if err = queue.CompleteAcceptedResult(ctx, pagePermit, runResult(finalizing)); err != nil {
		t.Fatal(err)
	}
	finalPermit := claimType(t, ctx, queue, jobs.FinalizeRun)
	ready, err := store.BeginFinalization(ctx, prepared.Run.ID, finalPermit)
	if err != nil || ready.Run.Status != RunFinalizing {
		t.Fatalf("ready=%+v err=%v", ready.Run, err)
	}
	wikiVersionID := deterministicWikiVersionID(prepared.Run.ID)
	bundle, err := wikiArtifacts.Publish(artifacts.ID(kbID), artifacts.ID(prepared.Run.ID), artifacts.ID(wikiVersionID), []artifacts.Page{accepted}, []artifacts.SourceRevision{{"source_id": sourceID.String(), "revision_id": revisionID.String(), "fingerprint": fmt.Sprintf("%x", storedSource.Fingerprint.Digest), "commit": strings.Repeat("a", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	published, err := store.Publish(ctx, prepared.Run.ID, wikiVersionID, bundle, []artifacts.Page{accepted}, finalPermit)
	if err != nil || published.Run.Status != RunPublished {
		t.Fatalf("published=%+v err=%v", published.Run, err)
	}
	if _, err = store.Publish(ctx, prepared.Run.ID, wikiVersionID, bundle, []artifacts.Page{accepted}, finalPermit); err != nil {
		t.Fatalf("publication replay: %v", err)
	}
	if err = queue.CompleteAcceptedResult(ctx, finalPermit, runResult(published)); err != nil {
		t.Fatal(err)
	}
	if drift {
		var active int
		var capturedRevision, priorWiki string
		if err = pool.QueryRow(ctx, `SELECT count(*),min(captured.source_revision_id::text),min(run.prior_wiki_version_id::text)
	  FROM documentation_runs run JOIN documentation_run_sources captured ON captured.run_id=run.id
	  WHERE run.knowledge_base_id=$1 AND run.status='PREPARING'`, pgUUID(kbID)).Scan(&active, &capturedRevision, &priorWiki); err != nil {
			t.Fatal(err)
		}
		if active != 1 || capturedRevision != latestRevision.String() || priorWiki != wikiVersionID.String() {
			t.Fatalf("follow-up was not coalesced to latest content: %d %s %s", active, capturedRevision, priorWiki)
		}
		if published.Run.Sources[0].RevisionID != revisionID {
			t.Fatal("publication lost its captured revision")
		}
	}
	view, err := store.GetWiki(ctx, kbID, nil, stringPointer("overview"))
	if err != nil || view.Page == nil || view.Page.Markdown != accepted.Markdown || len(view.Pages) != 1 {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	excerpt, err := store.GetWikiEvidence(ctx, kbID, &wikiVersionID, "overview", "entry", "entry-source")
	if err != nil || excerpt.Text != "exact captured evidence" || excerpt.StartLine != 2 || excerpt.EndLine != 2 {
		t.Fatalf("captured excerpt=%+v err=%v", excerpt, err)
	}
	if _, err = store.GetWikiEvidence(ctx, kbID, &wikiVersionID, "overview", "other-claim", "entry-source"); err == nil {
		t.Fatal("evidence escaped its claim")
	}
	if _, err = store.GetWikiEvidence(ctx, ID{1}, &wikiVersionID, "overview", "entry", "entry-source"); err == nil {
		t.Fatal("evidence escaped its knowledge base")
	}
	exported, err := store.ExportWiki(ctx, kbID, nil)
	if err != nil || len(exported) < 4 || string(exported[:2]) != "PK" {
		t.Fatalf("export bytes=%d err=%v", len(exported), err)
	}
	replay, err := store.RequestGeneration(ctx, kbID, 1, actorID, "publish-flow")
	if err != nil || replay != prepareJobID {
		t.Fatalf("replay=%s want=%s err=%v", replay.String(), prepareJobID.String(), err)
	}
	if !drift {
		jobID, err := store.RequestGeneration(ctx, kbID, 2, actorID, "no-op-with-new-content")
		if err != nil {
			t.Fatal(err)
		}
		latestRevision[15]++
		if _, err = pool.Exec(ctx, `INSERT INTO source_revisions(id,source_id,observed_ref_kind,observed_ref,native_version,fingerprint,artifact_key,file_count,byte_count)
		 VALUES($1,$2,'BRANCH','main',$3,$4,$5,1,24)`, pgUUID(latestRevision), pgUUID(sourceID), strings.Repeat("b", 40), bytes.Repeat([]byte{9}, 32), "sources/"+sourceID.String()+"/snapshots/"+latestRevision.String()); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `UPDATE sources SET current_revision_id=$2 WHERE id=$1`, pgUUID(sourceID), pgUUID(latestRevision)); err != nil {
			t.Fatal(err)
		}
		permit := claimExpected(t, ctx, queue, jobID)
		for range 2 {
			noOp, err := store.Prepare(ctx, kbID, permit)
			if err != nil || noOp.Run.Status != RunNoOp {
				t.Fatalf("captured no-op=%s %v", noOp.Run.Status, err)
			}
		}
		var active int
		var capturedRevision string
		if err = pool.QueryRow(ctx, `SELECT count(*),min(captured.source_revision_id::text)
		 FROM documentation_runs run JOIN documentation_run_sources captured ON captured.run_id=run.id
		 WHERE run.knowledge_base_id=$1 AND run.status='PREPARING'`, pgUUID(kbID)).Scan(&active, &capturedRevision); err != nil || active != 1 || capturedRevision != latestRevision.String() {
			t.Fatalf("no-op follow-up=%d %s %v", active, capturedRevision, err)
		}
	}
}

func claimExpected(t *testing.T, ctx context.Context, queue *jobs.Store, expected jobs.JobID) jobs.Permit {
	t.Helper()
	permit, err := queue.Claim(ctx, "documentation-test", 5*time.Second)
	if err != nil || permit == nil || permit.JobID != expected {
		t.Fatalf("permit=%+v expected=%s err=%v", permit, expected.String(), err)
	}
	return *permit
}

func claimType(t *testing.T, ctx context.Context, queue *jobs.Store, expected jobs.Type) jobs.Permit {
	t.Helper()
	permit, err := queue.Claim(ctx, "documentation-test", 5*time.Second)
	if err != nil || permit == nil {
		t.Fatalf("permit=%+v err=%v", permit, err)
	}
	snapshot, err := queue.Get(ctx, permit.JobID)
	if err != nil || snapshot.Type != expected {
		t.Fatalf("snapshot=%+v expected=%s err=%v", snapshot, expected, err)
	}
	return *permit
}

func stringPointer(value string) *string { return &value }

func TestTerminalCallbackPostgreSQLRaceIsExactlyOnce(t *testing.T) {
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
	if _, err = pool.Exec(ctx, `TRUNCATE knowledge_bases,jobs,event_log,audit_events,idempotency_records RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	kbID := mustID(t, "00112233-4455-6677-8899-aabbccddeeff")
	jobID := jobs.JobID(mustID(t, "99999999-8888-4777-8666-555555555555"))
	runID := RunID(mustID(t, "11111111-2222-3333-4444-555555555555"))
	if _, err = pool.Exec(ctx, `INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language,version) VALUES($1,'Docs','docs','PUBLIC','ACTIVE','','en',1)`, pgUUID(kbID)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO jobs(id,job_type,target_type,target_id,payload,operation_key,status,attempt_count,max_attempts,progress,finished_at) VALUES($1,'PREPARE_RUN','knowledge_base',$2,$3::jsonb,'prepare-race','FAILED',3,3,0,clock_timestamp())`, pgUUID(ID(jobID)), pgUUID(kbID), `{"run_id":"`+runID.String()+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO documentation_runs(id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,instructions,language) VALUES($1,$2,'PREPARING',$3,1,'','en')`, pgUUID(ID(runID)), pgUUID(kbID), pgUUID(ID(jobID))); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snapshot := jobs.Snapshot{ID: jobID, Type: jobs.PrepareRun, TargetType: "knowledge_base", TargetID: jobs.UUID(kbID), Status: jobs.Failed, UpdatedAt: now, FinishedAt: &now}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			tx, beginErr := pool.BeginTx(ctx, pgx.TxOptions{})
			if beginErr != nil {
				results <- beginErr
				return
			}
			defer tx.Rollback(ctx)
			if callbackErr := TerminalCallback(ctx, tx, snapshot); callbackErr != nil {
				results <- callbackErr
				return
			}
			results <- tx.Commit(ctx)
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for callbackErr := range results {
		if callbackErr != nil {
			t.Fatal(callbackErr)
		}
	}
	var status, sanitized string
	var completed time.Time
	if err = pool.QueryRow(ctx, `SELECT status,sanitized_error,completed_at FROM documentation_runs WHERE id=$1`, pgUUID(ID(runID))).Scan(&status, &sanitized, &completed); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || sanitized != "documentation:preparation_failed" || completed.IsZero() {
		t.Fatalf("run status=%s error=%s completed=%v", status, sanitized, completed)
	}
	var resourceEvents, auditEvents int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM event_log WHERE resource_id=$1),(SELECT count(*) FROM audit_events WHERE target_id=$1)`, pgUUID(ID(runID))).Scan(&resourceEvents, &auditEvents); err != nil {
		t.Fatal(err)
	}
	if resourceEvents != 1 || auditEvents != 1 {
		t.Fatalf("resource events=%d audit events=%d", resourceEvents, auditEvents)
	}
}

func migrateDocumentationDatabase(t *testing.T, ctx context.Context, databaseURL string) {
	t.Helper()
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err = database.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(migrations.FS)
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpContext(ctx, database, "."); err != nil {
		t.Fatal(err)
	}
}
