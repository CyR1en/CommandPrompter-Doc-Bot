package agents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/retention"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCapturedWikiReservationFencesRetentionUntilReceiptSettlement(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	for _, test := range []struct {
		name        string
		recordFirst bool
	}{
		{name: "receipt commits before intent application", recordFirst: true},
		{name: "intent application runs before receipt settlement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReservationRaceFixture(t, databaseURL)
			if test.recordFirst {
				fixture.record(t)
			}
			result, err := fixture.retention.Apply(fixture.ctx, fixture.permit)
			if err != nil || result["old_wikis"] != 0 {
				t.Fatalf("Apply() = %#v, %v", result, err)
			}
			if !test.recordFirst {
				fixture.record(t)
			}
			fixture.assertPreserved(t)
		})
	}
}

func TestFailedSettlementKeepsReservationForExactReplay(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	fixture := newReservationRaceFixture(t, databaseURL)
	blocker, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })
	if _, err = blocker.Exec(fixture.ctx, `
		SELECT id FROM wiki_versions WHERE id=$1 FOR UPDATE
	`, pgUUID(ID(fixture.capture.KnowledgeBases[0].WikiVersionID))); err != nil {
		t.Fatal(err)
	}
	repository := &settlementReplayRepository{
		ExecutionRepository: fixture.store,
		capture:             fixture.capture,
	}
	engine, err := NewEngine(repository, staticDigester{}, &fakeModel{}, EngineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := ExecuteRequest{
		Selector: fixture.capture.Agent.Selector(), Origin: OriginHTTP, Subject: "reservation-reader",
		Messages: []Message{{Role: RoleUser, Content: "What was captured?"}},
	}
	started := time.Now()
	if _, err = engine.Execute(fixture.ctx, request, authorizerFunc(func(AuthorizationScope) error {
		return ErrExecutionForbidden
	})); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked settlement error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < receiptSettlementTimeout {
		t.Fatalf("blocked settlement returned before timeout: %s", elapsed)
	}
	var reservations int
	if err = fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM agent_run_scope_reservations WHERE run_id=$1
	`, pgUUID(ID(fixture.capture.RunID))).Scan(&reservations); err != nil || reservations != len(fixture.capture.KnowledgeBases) {
		t.Fatalf("reservation after settlement timeout = %d, %v", reservations, err)
	}
	if repository.record.Capture.RunID != fixture.capture.RunID {
		t.Fatalf("captured replay receipt = %#v", repository.record)
	}
	if err = blocker.Rollback(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	runID, err := fixture.store.RecordRun(fixture.ctx, repository.record)
	if err != nil || runID != fixture.capture.RunID {
		t.Fatalf("exact settlement replay = %s, %v", runID.String(), err)
	}
	var runs int
	if err = fixture.pool.QueryRow(fixture.ctx, `
		SELECT (SELECT count(*) FROM agent_runs WHERE id=$1),
		       (SELECT count(*) FROM agent_run_scope_reservations WHERE run_id=$1)
	`, pgUUID(ID(fixture.capture.RunID))).Scan(&runs, &reservations); err != nil || runs != 1 || reservations != 0 {
		t.Fatalf("settled replay state runs=%d reservations=%d, %v", runs, reservations, err)
	}
}

type settlementReplayRepository struct {
	ExecutionRepository
	capture ExecutionCapture
	record  RunRecord
}

func (repository *settlementReplayRepository) Capture(context.Context, string) (ExecutionCapture, error) {
	return repository.capture, nil
}

func (repository *settlementReplayRepository) RecordRun(ctx context.Context, record RunRecord) (RunID, error) {
	repository.record = record
	return repository.ExecutionRepository.RecordRun(ctx, record)
}

func TestExpiredCapturedWikiReservationIsBoundedlyReclaimed(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	fixture := newReservationRaceFixture(t, databaseURL)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE agent_run_scope_reservations
		SET created_at=clock_timestamp()-interval '2 days',
		    expires_at=clock_timestamp()-interval '1 day'
		WHERE run_id=$1
	`, pgUUID(ID(fixture.capture.RunID))); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.retention.Apply(fixture.ctx, fixture.permit)
	if err != nil || result["old_wikis"] != 1 {
		t.Fatalf("Apply() = %#v, %v", result, err)
	}
	if _, err = fixture.store.RecordRun(fixture.ctx, fixture.runRecord); !errors.Is(err, ErrExecutionUnavailable) {
		t.Fatalf("expired capture RecordRun error = %v", err)
	}
	if _, err = fixture.wiki.ReadPage(
		artifacts.ID(fixture.knowledgeBaseID), artifacts.ID(fixture.capture.KnowledgeBases[0].WikiVersionID), "overview",
	); err == nil {
		t.Fatal("expired captured wiki artifact remains readable")
	}
}

func TestCaptureCannotReturnWikiDiscardedByRolledBackRetention(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	fixture := newReservationRaceFixture(t, databaseURL)
	oldWikiID := fixture.capture.KnowledgeBases[0].WikiVersionID
	var replacementWiki pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT published_wiki_id FROM knowledge_bases WHERE id=$1
	`, pgUUID(ID(fixture.knowledgeBaseID))).Scan(&replacementWiki); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		DELETE FROM agent_run_scope_reservations WHERE run_id=$1;
		DELETE FROM artifact_deletion_intents WHERE kind='WIKI_VERSION' AND resource_id=$2;
		UPDATE knowledge_bases SET published_wiki_id=$2,version=version+1 WHERE id=$3
	`, pgx.QueryExecModeSimpleProtocol, pgUUID(ID(fixture.capture.RunID)), pgUUID(ID(oldWikiID)), pgUUID(ID(fixture.knowledgeBaseID))); err != nil {
		t.Fatal(err)
	}

	tracer := &capturedWikiLockTracer{reached: make(chan struct{}), release: make(chan struct{})}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Tracer = tracer
	capturePool, err := pgxpool.NewWithConfig(fixture.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capturePool.Close)
	captureStore, err := NewPostgresExecutionStore(capturePool, rejectingArtifactResolver{}, fixture.store.vault)
	if err != nil {
		t.Fatal(err)
	}
	type captureResult struct {
		capture ExecutionCapture
		err     error
	}
	captured := make(chan captureResult, 1)
	go func() {
		value, captureErr := captureStore.Capture(fixture.ctx, fixture.capture.Agent.Selector())
		captured <- captureResult{capture: value, err: captureErr}
	}()
	select {
	case <-tracer.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("Capture did not reach the published-wiki lock")
	}

	if _, err = fixture.pool.Exec(fixture.ctx, `
		UPDATE knowledge_bases SET published_wiki_id=$1,version=version+1 WHERE id=$2;
		INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
		VALUES('WIKI_VERSION',$3,$2,$2)
	`, pgx.QueryExecModeSimpleProtocol, replacementWiki, pgUUID(ID(fixture.knowledgeBaseID)), pgUUID(ID(oldWikiID))); err != nil {
		close(tracer.release)
		t.Fatal(err)
	}
	failedArtifacts := &failAfterWikiDiscard{delegate: fixture.wiki}
	service, err := retention.NewService(fixture.pool, reservationRetentionPolicy(),
		reservationSourceArtifacts{}, reservationRunArtifacts{}, failedArtifacts)
	if err != nil {
		close(tracer.release)
		t.Fatal(err)
	}
	if _, err = service.Apply(fixture.ctx, fixture.permit); err == nil || !failedArtifacts.discarded {
		close(tracer.release)
		t.Fatalf("rolled-back wiki finalization error=%v discarded=%v", err, failedArtifacts.discarded)
	}
	close(tracer.release)
	select {
	case result := <-captured:
		if result.err == nil || result.capture.RunID != (RunID{}) {
			t.Fatalf("Capture returned discarded wiki: %#v, %v", result.capture, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Capture remained blocked after retention rollback")
	}
	if _, err = fixture.wiki.ReadPage(
		artifacts.ID(fixture.knowledgeBaseID), artifacts.ID(oldWikiID), "overview",
	); err == nil {
		t.Fatal("post-discard failure did not remove the old wiki artifact")
	}
	var reservations int
	if err = fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM agent_run_scope_reservations
		WHERE knowledge_base_id=$1 AND wiki_version_id=$2
	`, pgUUID(ID(fixture.knowledgeBaseID)), pgUUID(ID(oldWikiID))).Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("discarded wiki reservations=%d, %v", reservations, err)
	}
}

type capturedWikiLockTracer struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (tracer *capturedWikiLockTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT membership.position,kb.id,kb.version") {
		tracer.once.Do(func() { close(tracer.reached) })
		<-tracer.release
	}
	return ctx
}

func (*capturedWikiLockTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type failAfterWikiDiscard struct {
	delegate  *artifacts.WikiStore
	discarded bool
}

func (failure *failAfterWikiDiscard) Discard(knowledgeBaseID, wikiID artifacts.ID) error {
	if err := failure.delegate.Discard(knowledgeBaseID, wikiID); err != nil {
		return err
	}
	failure.discarded = true
	return errors.New("forced failure after wiki artifact removal")
}

type reservationRaceFixture struct {
	ctx             context.Context
	pool            *pgxpool.Pool
	store           *PostgresExecutionStore
	wiki            *artifacts.WikiStore
	retention       *retention.Service
	permit          jobs.Permit
	capture         ExecutionCapture
	runRecord       RunRecord
	knowledgeBaseID KnowledgeBaseID
	wikiContent     []byte
}

func newReservationRaceFixture(t *testing.T, databaseURL string) reservationRaceFixture {
	t.Helper()
	ctx := context.Background()
	migrateAgentTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		TRUNCATE agents,knowledge_bases,model_profiles,provider_endpoints,operators,
		         jobs,event_log,audit_events,idempotency_records RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewCredentialVault(
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{31}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.OperatorID(testUUID(t))
	if _, err = pool.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Reservation Operator','reservation operator','unused')
	`, pgUUID(ID(actor))); err != nil {
		t.Fatal(err)
	}
	profileID, endpointID := seedAgentModel(t, ctx, pool, actor)
	if _, err = pool.Exec(ctx, `
		UPDATE provider_endpoints SET health='HEALTHY',health_checked_at=clock_timestamp()
		WHERE id=$1
	`, pgUUID(ID(endpointID))); err != nil {
		t.Fatal(err)
	}
	knowledgeBaseID := seedAgentKnowledgeBase(t, ctx, pool, "Reservation Docs", Public, true)
	configuration := validConfiguration(knowledgeBaseID)
	configuration.ModelProfileID = profileID
	configuration.AnswerMode = SinglePass
	configuration.MaxToolCalls = 0
	configuration.EvidenceAccess = WikiOnly
	catalog, err := NewCatalog(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := catalog.Create(ctx, CreateCommand{Key: "reservation-docs", Configuration: configuration}, actor, "reservation-create")
	if err != nil {
		t.Fatal(err)
	}
	agent, err = catalog.SetLifecycle(ctx, SetLifecycleCommand{
		AgentID: agent.ID, ExpectedVersion: agent.Version, Lifecycle: Active,
	}, actor, "reservation-activate")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresExecutionStore(pool, rejectingArtifactResolver{}, vault)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := store.Capture(ctx, agent.Selector())
	if err != nil {
		t.Fatal(err)
	}
	wiki, err := artifacts.NewWikiStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("# Reserved wiki\n\nThis captured evidence must remain readable.\n")
	claims := []byte(`{"claims":[]}`)
	page := artifacts.Page{
		Slug: "overview", Title: "Overview", Description: "Reservation proof", PageType: "Concept",
		Markdown: string(content), ContentSHA256: sha256.Sum256(content), ClaimsJSON: claims, ClaimsSHA256: sha256.Sum256(claims),
	}
	if _, err = wiki.Publish(
		artifacts.ID(knowledgeBaseID), artifacts.ID(capture.KnowledgeBases[0].DocumentationRunID),
		artifacts.ID(capture.KnowledgeBases[0].WikiVersionID), []artifacts.Page{page}, nil,
	); err != nil {
		t.Fatal(err)
	}
	republishAgentKnowledgeBase(t, ctx, pool, knowledgeBaseID)
	if _, err = pool.Exec(ctx, `
		UPDATE wiki_versions SET published_at=clock_timestamp()-interval '60 days'
		WHERE id=$1
	`, pgUUID(ID(capture.KnowledgeBases[0].WikiVersionID))); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
		VALUES('WIKI_VERSION',$1,$2,$2)
	`, pgUUID(ID(capture.KnowledgeBases[0].WikiVersionID)), pgUUID(ID(knowledgeBaseID))); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE jobs
		SET status='SUCCEEDED',attempt_count=1,progress=100,result='{}',
		    started_at=clock_timestamp(),finished_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE job_type='PREPARE_RUN' AND status='PENDING'
	`); err != nil {
		t.Fatal(err)
	}
	digest, err := store.DigestRequest(capture, ExecuteRequest{
		Selector: agent.Selector(), Origin: OriginHTTP, Subject: "reservation-reader",
		Messages: []Message{{Role: RoleUser, Content: "What was captured?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runRecord := RunRecord{
		Capture: capture, Origin: OriginHTTP, Subject: "reservation-reader", RequestDigest: digest,
		Outcome: CompletionAnswered, Usage: map[string]int{"model_calls": 1}, LatencyMS: 25,
		CompletedAt: capture.CapturedAt.Add(time.Second),
	}
	service, err := retention.NewService(pool, reservationRetentionPolicy(),
		reservationSourceArtifacts{}, reservationRunArtifacts{}, wiki)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := service.Schedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := jobs.NewStore(pool, nil).Claim(ctx, "reservation-worker", time.Minute)
	if err != nil || permit == nil || permit.JobID != jobID {
		t.Fatalf("retention permit = %#v, %v", permit, err)
	}
	return reservationRaceFixture{
		ctx: ctx, pool: pool, store: store, wiki: wiki, retention: service, permit: *permit,
		capture: capture, runRecord: runRecord, knowledgeBaseID: knowledgeBaseID, wikiContent: content,
	}
}

func reservationRetentionPolicy() retention.Policy {
	return retention.Policy{
		SourceSnapshots: 30 * 24 * time.Hour, FailedDrafts: 30 * 24 * time.Hour,
		JobLogs: 30 * 24 * time.Hour, EventLog: 30 * 24 * time.Hour,
		AgentRuns: 90 * 24 * time.Hour, DiscordContext: 30 * 24 * time.Hour,
		OldWikis: 30 * 24 * time.Hour, BatchSize: 20,
	}
}

func (fixture reservationRaceFixture) record(t *testing.T) {
	t.Helper()
	runID, err := fixture.store.RecordRun(fixture.ctx, fixture.runRecord)
	if err != nil || runID != fixture.capture.RunID {
		t.Fatalf("RecordRun() = %s, %v", runID.String(), err)
	}
}

func (fixture reservationRaceFixture) assertPreserved(t *testing.T) {
	t.Helper()
	content, err := fixture.wiki.ReadPage(
		artifacts.ID(fixture.knowledgeBaseID), artifacts.ID(fixture.capture.KnowledgeBases[0].WikiVersionID), "overview",
	)
	if err != nil || !bytes.Equal(content, fixture.wikiContent) {
		t.Fatalf("captured wiki artifact = %q, %v", content, err)
	}
	var intents, wikis, runs, reservations int
	if err = fixture.pool.QueryRow(fixture.ctx, `
		SELECT
		  (SELECT count(*) FROM artifact_deletion_intents WHERE kind='WIKI_VERSION' AND resource_id=$1),
		  (SELECT count(*) FROM wiki_versions WHERE id=$1),
		  (SELECT count(*) FROM agent_runs WHERE id=$2),
		  (SELECT count(*) FROM agent_run_scope_reservations WHERE run_id=$2)
	`, pgUUID(ID(fixture.capture.KnowledgeBases[0].WikiVersionID)), pgUUID(ID(fixture.capture.RunID))).Scan(
		&intents, &wikis, &runs, &reservations,
	); err != nil {
		t.Fatal(err)
	}
	if intents != 0 || wikis != 1 || runs != 1 || reservations != 0 {
		t.Fatalf("preserved state intents=%d wikis=%d runs=%d reservations=%d", intents, wikis, runs, reservations)
	}
}

type reservationSourceArtifacts struct{}

func (reservationSourceArtifacts) DiscardSnapshot(sourcefiles.ID, sourcefiles.ID) error { return nil }

type reservationRunArtifacts struct{}

func (reservationRunArtifacts) DiscardRun(artifacts.ID, artifacts.ID) error { return nil }
