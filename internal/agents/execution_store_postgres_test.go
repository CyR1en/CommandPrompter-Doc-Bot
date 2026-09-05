package agents

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExecutionStoreCapturesFreshScopeAndSettlesExactReceipts(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
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
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.OperatorID(testUUID(t))
	if _, err = pool.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Execution Operator','execution operator','unused')
	`, pgUUID(ID(actor))); err != nil {
		t.Fatal(err)
	}
	profileID, endpointID := seedAgentModel(t, ctx, pool, actor)
	t.Run("answer calls and durable jobs share provider admission", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE model_profile_versions SET max_concurrent_tasks=1 WHERE profile_id=$1`, pgUUID(ID(profileID))); err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(ctx, `UPDATE model_profile_versions SET max_concurrent_tasks=2 WHERE profile_id=$1`, pgUUID(ID(profileID)))
		first, second := jobs.NewStore(pool, nil), jobs.NewStore(pool, nil)
		release, err := first.AcquireModelCall(ctx, jobs.UUID(profileID), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		waiting, stop := context.WithTimeout(ctx, 30*time.Millisecond)
		defer stop()
		if other, err := second.AcquireModelCall(waiting, jobs.UUID(profileID), time.Second); err == nil {
			other()
			t.Fatal("two answer callers exceeded provider limit")
		}
		jobID, err := first.Enqueue(ctx, jobs.Command{Type: jobs.ProbeModel, TargetType: "model_profile", TargetID: jobs.UUID(profileID), OperationKey: "admission-test", ConcurrencyKey: "model-profile:" + profileID.String(), ConcurrencyLimit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if permit, err := second.Claim(ctx, "admission-test", time.Minute); err != nil || permit != nil {
			t.Fatalf("job bypassed answer admission: %+v %v", permit, err)
		}
		release()
		permit, err := second.Claim(ctx, "admission-test", time.Minute)
		if err != nil || permit == nil || permit.JobID != jobID {
			t.Fatalf("job did not acquire released capacity: %+v %v", permit, err)
		}
		waiting2, stop2 := context.WithTimeout(ctx, 30*time.Millisecond)
		defer stop2()
		if other, err := first.AcquireModelCall(waiting2, jobs.UUID(profileID), time.Second); err == nil {
			other()
			t.Fatal("answer bypassed active job")
		}
		if err := second.Succeed(ctx, *permit, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO provider_call_leases(endpoint_id,expires_at) VALUES($1,clock_timestamp()-interval '1 second')`, pgUUID(ID(endpointID))); err != nil {
			t.Fatal(err)
		}
		after, err := first.AcquireModelCall(ctx, jobs.UUID(profileID), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		after()
	})
	credentialA, credentialB := CredentialID(testUUID(t)), CredentialID(testUUID(t))
	if _, err = pool.Exec(ctx, `
		INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES($1,'PROVIDER_API_KEY','Agent credential A','••••','test',decode(repeat('01',12),'hex'),decode('01','hex'),1),
		      ($2,'PROVIDER_API_KEY','Agent credential B','••••','test',decode(repeat('02',12),'hex'),decode('02','hex'),1)
	`, pgUUID(ID(credentialA)), pgUUID(ID(credentialB))); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE provider_endpoints SET credential_id=$2,health='HEALTHY',health_checked_at=clock_timestamp()
		WHERE id=$1
	`, pgUUID(ID(endpointID)), pgUUID(ID(credentialA))); err != nil {
		t.Fatal(err)
	}
	knowledgeBaseID := seedAgentKnowledgeBase(t, ctx, pool, "Execution Docs", Public, true)
	secondKnowledgeBaseID := seedAgentKnowledgeBase(t, ctx, pool, "Execution Reference", Public, true)
	configuration := validConfiguration(knowledgeBaseID, secondKnowledgeBaseID)
	configuration.ModelProfileID = profileID
	configuration.EvidenceAccess = WikiOnly
	catalog, err := NewCatalog(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := catalog.Create(ctx, CreateCommand{Key: "execution-docs", Configuration: configuration}, actor, "execution-create")
	if err != nil {
		t.Fatal(err)
	}
	agent, err = catalog.SetLifecycle(ctx, SetLifecycleCommand{AgentID: agent.ID, ExpectedVersion: 1, Lifecycle: Active}, actor, "execution-activate")
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
	if capture.Agent.ID != agent.ID || len(capture.KnowledgeBases) != 2 ||
		capture.KnowledgeBases[0].ID != knowledgeBaseID || capture.KnowledgeBases[1].ID != secondKnowledgeBaseID ||
		capture.KnowledgeBases[0].SourceScopeDigest == ([32]byte{}) ||
		capture.KnowledgeBases[0].SourceScopeDigest == capture.KnowledgeBases[1].SourceScopeDigest ||
		capture.Model.ProfileVersionID == (ModelProfileVersionID{}) || capture.RunID == (RunID{}) {
		t.Fatalf("capture = %#v", capture)
	}
	if capture.Model.CapturedCredentialID == nil || *capture.Model.CapturedCredentialID != credentialA ||
		capture.Model.CapturedCredentialVersion == nil || *capture.Model.CapturedCredentialVersion != 1 {
		t.Fatalf("captured credential = %#v v%v", capture.Model.CapturedCredentialID, capture.Model.CapturedCredentialVersion)
	}
	t.Run("natural questions retrieve matching passages inside the captured wiki", func(t *testing.T) {
		member := capture.KnowledgeBases[0]
		body := strings.Repeat("Unrelated introduction.\nUnrelated introduction.\r\nUnrelated introduction.\rUnrelated introduction.\u2028", 35) + "Set session_ttl to control session expiration.\n"
		for _, wiki := range []WikiVersionID{member.WikiVersionID, capture.KnowledgeBases[1].WikiVersionID} {
			if _, err := pool.Exec(ctx, `INSERT INTO wiki_pages(wiki_version_id,slug,title,description,page_type,content_sha256,claims_sha256,body)
			VALUES($1,'sessions','Sessions','','concept',$2,$2,$3)`, pgUUID(ID(wiki)), bytes.Repeat([]byte{1}, 32), body); err != nil {
				t.Fatal(err)
			}
		}
		for _, question := range []string{"How do I configure session_ttl?", "session expiration", "Where can I change session expiration?"} {
			hits, err := store.SearchWiki(ctx, member, question, 8)
			if err != nil || len(hits) != 1 || hits[0].Slug != "sessions" || hits[0].StartLine <= 100 {
				t.Fatalf("%q: hits=%+v err=%v", question, hits, err)
			}
		}
		hits, err := store.SearchWiki(ctx, member, "unfindableword", 8)
		if err != nil || len(hits) != 0 {
			t.Fatalf("irrelevant evidence: %+v %v", hits, err)
		}
		tools, err := NewToolRuntime(store, capture)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := tools.InitialEvidence(ctx, "How do I configure session_ttl?", SinglePass)
		if err != nil || len(evidence) == 0 || !strings.Contains(evidence[0], "Set session_ttl") {
			t.Fatalf("matching passage absent: %v %v", evidence, err)
		}
		for _, citation := range tools.Citations() {
			if citation.StartLine == nil || *citation.StartLine <= 100 {
				t.Fatalf("incorrect passage citation: %+v", citation)
			}
		}
	})
	digest, err := store.DigestRequest(capture, ExecuteRequest{
		Selector: agent.Selector(), Origin: OriginHTTP, Subject: "reader-1",
		Messages: []Message{{Role: RoleUser, Content: "How is it published?"}},
	})
	if err != nil || digest == ([32]byte{}) {
		t.Fatalf("request digest = %x, %v", digest, err)
	}
	record := RunRecord{
		Capture: capture, Origin: OriginHTTP, Subject: "reader-1", RequestDigest: digest,
		Outcome: CompletionInsufficientEvidence, Usage: map[string]int{
			"model_calls": 1, "exact_integer": int(9007199254740992),
		}, LatencyMS: 12,
		ToolCalls: []string{"initial_search_wiki"},
	}
	record.Capture.CapturedAt = time.Date(2026, 8, 31, 12, 30, 0, 123456789, time.UTC)
	record.CompletedAt = record.Capture.CapturedAt.Add(-time.Second)
	zeroDigest := record
	zeroDigest.RequestDigest = [32]byte{}
	if _, err = store.RecordRun(ctx, zeroDigest); !errors.Is(err, ErrExecutionInvalid) {
		t.Fatalf("zero digest receipt error = %v", err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE provider_endpoints
		SET credential_id=$2,configuration_version=configuration_version+1
		WHERE id=$1
	`, pgUUID(ID(endpointID)), pgUUID(ID(credentialB))); err != nil {
		t.Fatal(err)
	}
	runID, err := store.RecordRun(ctx, record)
	if err != nil || runID != capture.RunID {
		t.Fatalf("RecordRun = %s, %v", runID.String(), err)
	}
	replayID, err := store.RecordRun(ctx, record)
	if err != nil || replayID != runID {
		t.Fatalf("RecordRun replay = %s, %v", replayID.String(), err)
	}
	var storedCapturedAt, storedCompletedAt time.Time
	if err = pool.QueryRow(ctx, `SELECT created_at,completed_at FROM agent_runs WHERE id=$1`, pgUUID(ID(runID))).Scan(&storedCapturedAt, &storedCompletedAt); err != nil {
		t.Fatal(err)
	}
	if !storedCapturedAt.Equal(postgresTimestamp(record.Capture.CapturedAt)) || !storedCompletedAt.Equal(postgresTimestamp(record.Capture.CapturedAt)) {
		t.Fatalf("stored receipt timestamps captured=%s completed=%s", storedCapturedAt, storedCompletedAt)
	}
	detail, err := catalog.GetRun(ctx, agent.ID, runID)
	if err != nil || detail.CapturedCredentialID == nil || *detail.CapturedCredentialID != credentialA ||
		detail.CapturedCredentialVersion == nil || *detail.CapturedCredentialVersion != 1 {
		t.Fatalf("stored credential attribution = %#v v%v, %v", detail.CapturedCredentialID, detail.CapturedCredentialVersion, err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE provider_endpoints
		SET credential_id=$2,configuration_version=$3
		WHERE id=$1
	`, pgUUID(ID(endpointID)), pgUUID(ID(credentialA)), capture.Model.Endpoint.ConfigurationVersion); err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.Usage = map[string]int{"model_calls": 2}
	if _, err = store.RecordRun(ctx, changed); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("changed receipt error = %v", err)
	}
	changedExactInteger := record
	changedExactInteger.Usage = map[string]int{
		"model_calls": 1, "exact_integer": int(9007199254740993),
	}
	if _, err = store.RecordRun(ctx, changedExactInteger); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("changed exact integer receipt error = %v", err)
	}
	var runs, scopes int
	if err = pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM agent_runs),
		       (SELECT count(*) FROM agent_run_knowledge_bases)
	`).Scan(&runs, &scopes); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || scopes != 2 {
		t.Fatalf("stored rows runs=%d scopes=%d", runs, scopes)
	}
	republishAgentKnowledgeBase(t, ctx, pool, knowledgeBaseID)
	if err = store.AssertFresh(ctx, capture); err != nil {
		t.Fatalf("new immutable publication invalidated capture: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE model_profiles SET availability='UNAVAILABLE' WHERE id=$1`, pgUUID(ID(profileID))); err != nil {
		t.Fatal(err)
	}
	if err = store.AssertFresh(ctx, capture); !errors.Is(err, ErrExecutionUnavailable) {
		t.Fatalf("model mutation freshness error = %v", err)
	}
	if err = store.AssertSecurityFresh(ctx, capture); err != nil {
		t.Fatalf("model mutation affected delivery security freshness: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE model_profiles SET availability='AVAILABLE' WHERE id=$1`, pgUUID(ID(profileID))); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE provider_endpoints SET configuration_version=configuration_version+1 WHERE id=$1`, pgUUID(ID(endpointID))); err != nil {
		t.Fatal(err)
	}
	if err = store.AssertFresh(ctx, capture); !errors.Is(err, ErrExecutionUnavailable) {
		t.Fatalf("endpoint mutation freshness error = %v", err)
	}
	if err = store.AssertSecurityFresh(ctx, capture); err != nil {
		t.Fatalf("endpoint mutation affected delivery security freshness: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE provider_endpoints SET configuration_version=$2 WHERE id=$1`, pgUUID(ID(endpointID)), capture.Model.Endpoint.ConfigurationVersion); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE knowledge_bases SET access_policy='RESTRICTED' WHERE id=$1`, pgUUID(ID(knowledgeBaseID))); err != nil {
		t.Fatal(err)
	}
	if err = store.AssertSecurityFresh(ctx, capture); !errors.Is(err, ErrExecutionUnavailable) {
		t.Fatalf("access mutation freshness error = %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE knowledge_bases SET access_policy='PUBLIC' WHERE id=$1`, pgUUID(ID(knowledgeBaseID))); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE agents SET version=version+1 WHERE id=$1`, pgUUID(ID(agent.ID))); err != nil {
		t.Fatal(err)
	}
	if err = store.AssertSecurityFresh(ctx, capture); !errors.Is(err, ErrExecutionUnavailable) {
		t.Fatalf("Agent mutation freshness error = %v", err)
	}
	failureCapture, err := store.Capture(ctx, agent.Selector())
	if err != nil {
		t.Fatal(err)
	}
	failureDigest, err := store.DigestRequest(failureCapture, ExecuteRequest{
		Selector: agent.Selector(), Origin: OriginDiscord, Subject: "discord-user",
		Messages: []Message{{Role: RoleUser, Content: "Unknown question"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sanitized := "agent_execution:provider_request_failed"
	failureRecord := RunRecord{
		Capture: failureCapture, Origin: OriginDiscord, Subject: "discord-user", RequestDigest: failureDigest,
		Outcome: CompletionFailed, Usage: map[string]int{"model_calls": 1}, LatencyMS: 4,
		SanitizedError: &sanitized, CompletedAt: failureCapture.CapturedAt.Add(time.Second),
	}
	if _, err = store.RecordRun(ctx, failureRecord); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RecordRun(ctx, failureRecord); err != nil {
		t.Fatal(err)
	}
	changedFailure := failureRecord
	changedSanitized := "agent_execution:tool_call_failed"
	changedFailure.SanitizedError = &changedSanitized
	if _, err = store.RecordRun(ctx, changedFailure); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("changed failure receipt error = %v", err)
	}
	var failureError string
	if err = pool.QueryRow(ctx, `SELECT sanitized_error FROM agent_runs WHERE id=$1`, pgUUID(ID(failureCapture.RunID))).Scan(&failureError); err != nil || failureError != sanitized {
		t.Fatalf("failure receipt = %q, %v", failureError, err)
	}
}

type rejectingArtifactResolver struct{}

func (rejectingArtifactResolver) ResolveArtifactKey(string) (string, error) {
	return "", errors.New("unexpected source artifact lookup")
}

func republishAgentKnowledgeBase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id KnowledgeBaseID) {
	t.Helper()
	jobID, runID, wikiID := testUUID(t), testUUID(t), testUUID(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO jobs(id,job_type,target_type,target_id,operation_key)
		VALUES($1,'PREPARE_RUN','knowledge_base',$2,$3)
	`, pgUUID(ID(jobID)), pgUUID(ID(id)), "agent-test-republish:"+ID(wikiID).String()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO documentation_runs(
			id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,
			instructions,language,completed_at
		) VALUES($1,$2,'PUBLISHED',$3,1,'','en',clock_timestamp())
	`, pgUUID(ID(runID)), pgUUID(ID(id)), pgUUID(ID(jobID))); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO wiki_versions(
			id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count
		) VALUES($1,$2,$3,$4,$5,1)
	`, pgUUID(ID(wikiID)), pgUUID(ID(id)), pgUUID(ID(runID)),
		"agent-test/wiki/"+ID(wikiID).String(), bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET published_wiki_version_id=$2 WHERE id=$1`, pgUUID(ID(runID)), pgUUID(ID(wikiID))); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE knowledge_bases
		SET published_wiki_id=$2,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1
	`, pgUUID(ID(id)), pgUUID(ID(wikiID))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
