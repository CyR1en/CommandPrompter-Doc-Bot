package sources

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestSourceStorePostgreSQLStateMachine(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateSourceDatabase(t, ctx, databaseURL)
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `
		TRUNCATE audit_events,idempotency_records,website_revision_pages,
			source_syncs,source_revisions,repository_sources,website_sources,sources,
			job_attempts,job_events,event_log,jobs,credentials,operators,knowledge_bases
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatal(err)
	}

	vault, err := security.NewCredentialVault(
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{11}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewStore(pool, nil)
	store, err := NewStore(pool, queue, vault)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := NewID()
	restrictedKB, _ := NewID()
	publicKB, _ := NewID()
	credentialID, _ := NewID()
	if _, err := pool.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Source Operator','source operator','unused')
	`, pgUUID(actor)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language)
		VALUES($1,'Restricted sources','restricted sources','RESTRICTED','ACTIVE','','en'),
		      ($2,'Public sources','public sources','PUBLIC','ACTIVE','','en')
	`, pgUUID(restrictedKB), pgUUID(publicKB)); err != nil {
		t.Fatal(err)
	}
	secret, _ := security.NewSecretValue("source-secret-sentinel")
	envelope, err := vault.Encrypt(security.CredentialID(credentialID), security.CredentialRepositoryHTTPS, 1, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES($1,'REPOSITORY_HTTPS','Git','masked',$2,$3,$4,1)
	`, pgUUID(credentialID), envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext()); err != nil {
		t.Fatal(err)
	}
	secrets, err := credentials.NewSecretReader(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	leasedSecret, err := secrets.Read(ctx, credentials.ID(credentialID), credentials.RepositoryHTTPS, 1)
	if err != nil || leasedSecret.Reveal() != secret.Reveal() {
		t.Fatalf("leased source credential=%v err=%v", leasedSecret, err)
	}
	if _, err := secrets.Read(ctx, credentials.ID(credentialID), credentials.RepositoryHTTPS, 2); !errors.Is(err, credentials.ErrSecretUnavailable) {
		t.Fatalf("wrong credential lease version error=%v", err)
	}

	name, _ := ParseName("Docs repository")
	remote, _ := ParseRepositoryRemote("https://git.example.test/team/docs.git")
	reference, _ := ParseReference(Branch, "main")
	username := "git"
	poll := 300
	config := RepositoryConfiguration{
		Name: name, Privacy: Private, Remote: remote, Reference: reference,
		CredentialUsername: &username, CredentialID: &credentialID,
		IncludePatterns: []string{"src/**", "README.md"}, ExcludePatterns: []string{"**/*.min.js"},
		PollIntervalSeconds: &poll,
	}
	created, err := store.CreateRepository(ctx, CreateRepository{KnowledgeBaseID: restrictedKB, Configuration: config}, actor, "source-create")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.CreateRepository(ctx, CreateRepository{KnowledgeBaseID: restrictedKB, Configuration: config}, actor, "source-create")
	if err != nil || !reflect.DeepEqual(created, replay) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := config
	changed.Name, _ = ParseName("Other")
	if _, err := store.CreateRepository(ctx, CreateRepository{KnowledgeBaseID: restrictedKB, Configuration: changed}, actor, "source-create"); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("idempotency conflict=%v", err)
	}
	var sourcesCount, syncCount, jobCount, requestCount int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM sources),(SELECT count(*) FROM source_syncs),
		       (SELECT count(*) FROM jobs),(SELECT count(*) FROM idempotency_records)
	`).Scan(&sourcesCount, &syncCount, &jobCount, &requestCount); err != nil {
		t.Fatal(err)
	}
	if sourcesCount != 1 || syncCount != 1 || jobCount != 1 || requestCount != 1 {
		t.Fatalf("sources=%d syncs=%d jobs=%d requests=%d", sourcesCount, syncCount, jobCount, requestCount)
	}

	permit := claimSourceJob(t, ctx, queue, created.Validation.JobID)
	running, err := store.Begin(ctx, created.Validation.ID, permit)
	if err != nil || running.Status != SyncRunning {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE credentials SET secret_version=2 WHERE id=$1`, pgUUID(credentialID)); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	superseded, err := store.CompleteValidation(ctx, ValidationCompletion{SyncID: running.ID, ResolvedNativeVersion: &commit}, permit)
	if err != nil || superseded.Status != SyncSuperseded {
		t.Fatalf("superseded=%+v err=%v", superseded, err)
	}
	if err := queue.CompleteAcceptedResult(ctx, permit, syncResult(superseded)); err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(ctx, created.Source.ID)
	if err != nil || current.Lifecycle != Draft || current.Health != Unknown || current.Version != 1 {
		t.Fatalf("current=%+v err=%v", current, err)
	}

	validation, err := store.RequestValidation(ctx, RequestOperation{SourceID: current.ID, ExpectedVersion: current.Version}, actor, "validate-rotated")
	if err != nil || validation.Repository.CredentialVersion == nil || *validation.Repository.CredentialVersion != 2 {
		t.Fatalf("validation=%+v err=%v", validation, err)
	}
	permit = claimSourceJob(t, ctx, queue, validation.JobID)
	if _, err := store.Begin(ctx, validation.ID, permit); err != nil {
		t.Fatal(err)
	}
	validated, err := store.CompleteValidation(ctx, ValidationCompletion{SyncID: validation.ID, ResolvedNativeVersion: &commit}, permit)
	if err != nil || validated.Status != SyncSucceeded {
		t.Fatalf("validated=%+v err=%v", validated, err)
	}
	if err := queue.CompleteAcceptedResult(ctx, permit, syncResult(validated)); err != nil {
		t.Fatal(err)
	}
	active, _ := store.Get(ctx, current.ID)
	if active.Lifecycle != Active || active.Health != Healthy || active.Version != 2 || active.ValidatedConfigurationVersion == nil || *active.ValidatedConfigurationVersion != 1 {
		t.Fatalf("active=%+v", active)
	}

	first, err := store.RequestSync(ctx, RequestOperation{SourceID: active.ID, ExpectedVersion: active.Version}, actor, "sync-one")
	if err != nil {
		t.Fatal(err)
	}
	coalesced, err := store.RequestSync(ctx, RequestOperation{SourceID: active.ID, ExpectedVersion: active.Version}, actor, "sync-two")
	if err != nil || coalesced.ID != first.ID || coalesced.JobID != first.JobID {
		t.Fatalf("coalesced=%+v first=%+v err=%v", coalesced, first, err)
	}
	permit = claimSourceJob(t, ctx, queue, first.JobID)
	first, err = store.Begin(ctx, first.ID, permit)
	if err != nil || first.CandidateRevisionID == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	files := newSourceFileStore(t)
	stored, err := files.StoreSnapshot(sourcefiles.ID(active.ID), sourcefiles.ID(*first.CandidateRevisionID), sourcefiles.Files(sourcefiles.File{Path: "README.md", Content: []byte("same content")}), nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate := RevisionCandidate{NativeVersion: strings.Repeat("b", 40), Fingerprint: stored.Fingerprint.Digest, ArtifactKey: stored.ArtifactKey, FileCount: stored.Fingerprint.FileCount, ByteCount: stored.Fingerprint.ByteCount, IgnoredPaths: []string{"vendor/generated.js"}}
	completion := SyncCompletion{SyncID: first.ID, Revision: &candidate}
	var wait sync.WaitGroup
	results := make(chan Sync, 2)
	errorsFound := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := store.CompleteSync(ctx, completion, permit)
			results <- value
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	for value := range results {
		if value.Status != SyncSucceeded || value.ResultRevisionID == nil || *value.ResultRevisionID != *first.CandidateRevisionID {
			t.Fatalf("completion=%+v", value)
		}
	}
	if err := queue.CompleteAcceptedResult(ctx, permit, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	revisions, err := store.ListRevisions(ctx, active.ID)
	if err != nil || len(revisions) != 1 || revisions[0].NativeVersion != candidate.NativeVersion {
		t.Fatalf("revisions=%+v err=%v", revisions, err)
	}

	afterFirst, _ := store.Get(ctx, active.ID)
	second, err := store.RequestSync(ctx, RequestOperation{SourceID: afterFirst.ID, ExpectedVersion: afterFirst.Version}, actor, "sync-three")
	if err != nil {
		t.Fatal(err)
	}
	permit = claimSourceJob(t, ctx, queue, second.JobID)
	second, err = store.Begin(ctx, second.ID, permit)
	if err != nil || second.CandidateRevisionID == nil {
		t.Fatal(err)
	}
	secondStored, err := files.StoreSnapshot(sourcefiles.ID(active.ID), sourcefiles.ID(*second.CandidateRevisionID), sourcefiles.Files(sourcefiles.File{Path: "README.md", Content: []byte("same content")}), nil)
	if err != nil {
		t.Fatal(err)
	}
	reusedCandidate := candidate
	reusedCandidate.ArtifactKey = secondStored.ArtifactKey
	reused, err := store.CompleteSync(ctx, SyncCompletion{SyncID: second.ID, Revision: &reusedCandidate}, permit)
	if err != nil || reused.ResultRevisionID == nil || *reused.ResultRevisionID == *second.CandidateRevisionID {
		t.Fatalf("reused=%+v err=%v", reused, err)
	}
	if err := files.DiscardSnapshot(sourcefiles.ID(active.ID), sourcefiles.ID(*second.CandidateRevisionID)); err != nil {
		t.Fatal(err)
	}
	if err := queue.CompleteAcceptedResult(ctx, permit, syncResult(reused)); err != nil {
		t.Fatal(err)
	}

	websiteName, _ := ParseName("Product docs")
	websiteRemote, _ := ParseWebsiteRemote("https://docs.example.test/product/")
	websiteConfig := WebsiteConfiguration{
		Name: websiteName, Privacy: Public, Remote: websiteRemote,
		Limits: DefaultCrawlLimits(), AcquisitionMode: BuiltinCrawl,
	}
	websiteCreated, err := store.CreateWebsite(ctx, CreateWebsite{KnowledgeBaseID: publicKB, Configuration: websiteConfig}, actor, "website-create")
	if err != nil {
		t.Fatal(err)
	}
	permit = claimSourceJob(t, ctx, queue, websiteCreated.Validation.JobID)
	if _, err := store.Begin(ctx, websiteCreated.Validation.ID, permit); err != nil {
		t.Fatal(err)
	}
	websiteVersion := strings.Repeat("a", 64)
	websiteValidated, err := store.CompleteValidation(ctx, ValidationCompletion{SyncID: websiteCreated.Validation.ID, ResolvedNativeVersion: &websiteVersion}, permit)
	if err != nil || websiteValidated.Status != SyncSucceeded {
		t.Fatalf("website validation=%+v err=%v", websiteValidated, err)
	}
	if err := queue.CompleteAcceptedResult(ctx, permit, syncResult(websiteValidated)); err != nil {
		t.Fatal(err)
	}
	websiteActive, _ := store.Get(ctx, websiteCreated.Source.ID)
	websiteSync, err := store.RequestSync(ctx, RequestOperation{SourceID: websiteActive.ID, ExpectedVersion: websiteActive.Version}, actor, "website-sync")
	if err != nil {
		t.Fatal(err)
	}
	permit = claimSourceJob(t, ctx, queue, websiteSync.JobID)
	websiteSync, err = store.Begin(ctx, websiteSync.ID, permit)
	if err != nil || websiteSync.CandidateRevisionID == nil {
		t.Fatalf("website sync=%+v err=%v", websiteSync, err)
	}
	websiteStored, err := files.StoreSnapshot(sourcefiles.ID(websiteActive.ID), sourcefiles.ID(*websiteSync.CandidateRevisionID), sourcefiles.Files(
		sourcefiles.File{Path: "pages/guide.md", Content: []byte("# Guide\n")},
	), nil)
	if err != nil {
		t.Fatal(err)
	}
	var pageDigest [32]byte
	copy(pageDigest[:], bytes.Repeat([]byte{0xcc}, 32))
	websiteCandidate := RevisionCandidate{
		NativeVersion: strings.Repeat("b", 64), Fingerprint: websiteStored.Fingerprint.Digest,
		ArtifactKey: websiteStored.ArtifactKey, FileCount: websiteStored.Fingerprint.FileCount,
		ByteCount: websiteStored.Fingerprint.ByteCount,
		WebsitePages: []PageCapture{{
			CanonicalURL: "https://docs.example.test/product/guide?language=en",
			ContentPath:  "pages/guide.md", ContentSHA256: pageDigest,
			EvidenceURI: "web://" + websiteActive.ID.String() + "@" + strings.Repeat("b", 64) + "/guide",
			Freshness:   "fresh",
		}},
	}
	websiteCompleted, err := store.CompleteSync(ctx, SyncCompletion{SyncID: websiteSync.ID, Revision: &websiteCandidate}, permit)
	if err != nil || websiteCompleted.Status != SyncSucceeded {
		t.Fatalf("website completion=%+v err=%v", websiteCompleted, err)
	}
	if err := queue.CompleteAcceptedResult(ctx, permit, syncResult(websiteCompleted)); err != nil {
		t.Fatal(err)
	}
	websiteRevisions, err := store.ListRevisions(ctx, websiteActive.ID)
	if err != nil || len(websiteRevisions) != 1 || len(websiteRevisions[0].WebsitePages) != 1 || websiteRevisions[0].WebsitePages[0].CanonicalURL != websiteCandidate.WebsitePages[0].CanonicalURL {
		t.Fatalf("website revisions=%+v err=%v", websiteRevisions, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE source_syncs SET created_at=clock_timestamp()-interval '10 minutes' WHERE source_id=$1 AND sync_kind='SYNC'`, pgUUID(active.ID)); err != nil {
		t.Fatal(err)
	}
	dueResults := make(chan []Sync, 2)
	dueErrors := make(chan error, 2)
	for range 2 {
		go func() {
			values, err := store.ScheduleDue(ctx, 50)
			dueResults <- values
			dueErrors <- err
		}()
	}
	dueCount := 0
	for range 2 {
		dueCount += len(<-dueResults)
		if err := <-dueErrors; err != nil {
			t.Fatal(err)
		}
	}
	if dueCount != 1 {
		t.Fatalf("concurrent polling scheduled %d captures", dueCount)
	}

	var persisted string
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(string_agg(value,' '),'') FROM (
			SELECT details::text AS value FROM audit_events
			UNION ALL SELECT snapshot::text FROM event_log
		) persisted
	`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, secret.Reveal()) {
		t.Fatal("source secret reached audit or resource events")
	}
}

func claimSourceJob(t *testing.T, ctx context.Context, queue *jobs.Store, expected jobs.JobID) jobs.Permit {
	t.Helper()
	permit, err := queue.Claim(ctx, "source-worker", time.Minute)
	if err != nil || permit == nil || permit.JobID != expected {
		t.Fatalf("permit=%+v expected=%s err=%v", permit, expected.String(), err)
	}
	return *permit
}

func migrateSourceDatabase(t *testing.T, ctx context.Context, databaseURL string) {
	t.Helper()
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, database, "."); err != nil {
		t.Fatal(err)
	}
}
