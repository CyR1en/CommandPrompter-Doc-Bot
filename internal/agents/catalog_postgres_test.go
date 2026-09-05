package agents

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestCatalogPostgreSQLVersionsReadinessAndAtomicEvents(t *testing.T) {
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
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.OperatorID(testUUID(t))
	if _, err = pool.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Agent Operator','agent operator','unused')
	`, pgUUID(ID(actor))); err != nil {
		t.Fatal(err)
	}
	profileID, endpointID := seedAgentModel(t, ctx, pool, actor)
	publicKB := seedAgentKnowledgeBase(t, ctx, pool, "Public Docs", Public, false)
	restrictedKB := seedAgentKnowledgeBase(t, ctx, pool, "Private Docs", Restricted, true)

	configuration := validConfiguration(publicKB, restrictedKB)
	configuration.ModelProfileID = profileID
	created, err := catalog.Create(ctx, CreateCommand{Key: "docs-support", Configuration: configuration}, actor, "create-main")
	if err != nil {
		t.Fatal(err)
	}
	if created.Lifecycle != Draft || created.Version != 1 || created.CurrentVersion.VersionNumber != 1 ||
		created.Selector() != "agent:docs-support" || len(created.CurrentVersion.Memberships) != 2 ||
		created.CurrentVersion.Memberships[0].KnowledgeBaseID != publicKB ||
		created.CurrentVersion.Memberships[1].KnowledgeBaseID != restrictedKB {
		t.Fatalf("created agent = %#v", created)
	}
	replay, err := catalog.Create(ctx, CreateCommand{Key: "docs-support", Configuration: configuration}, actor, "create-main")
	if err != nil || replay.ID != created.ID || replay.CurrentVersionID != created.CurrentVersionID || replay.Version != 1 {
		t.Fatalf("create replay = %#v, %v", replay, err)
	}
	changedCreate := configuration
	changedCreate.Description = "Different digest"
	if _, err = catalog.Create(ctx, CreateCommand{Key: "docs-support", Configuration: changedCreate}, actor, "create-main"); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("changed create replay error = %v", err)
	}

	secondaryConfiguration := configuration
	secondaryConfiguration.DisplayName = "Secondary Agent"
	secondaryConfiguration.KnowledgeBaseIDs = []KnowledgeBaseID{restrictedKB}
	secondary, err := catalog.Create(ctx, CreateCommand{Key: "secondary", Configuration: secondaryConfiguration}, actor, "create-secondary")
	if err != nil {
		t.Fatal(err)
	}
	page, err := catalog.ListPage(ctx, nil, 1)
	if err != nil || len(page.Agents) != 1 || page.Agents[0].ID != created.ID || page.NextCursor == nil {
		t.Fatalf("first Agent page = %#v, %v", page, err)
	}
	next, err := catalog.ListPage(ctx, page.NextCursor, 1)
	if err != nil || len(next.Agents) != 1 || next.Agents[0].ID != secondary.ID || next.NextCursor != nil {
		t.Fatalf("second Agent page = %#v, %v", next, err)
	}
	if _, err = catalog.ListPage(ctx, nil, MaxCatalogPageSize+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized page error = %v", err)
	}
	byKey, err := catalog.GetByKey(ctx, "docs-support")
	if err != nil || byKey.ID != created.ID {
		t.Fatalf("GetByKey = %#v, %v", byKey, err)
	}

	readiness, err := catalog.EvaluateReadiness(ctx, created.ID)
	if err != nil || readiness.Ready || readiness.EffectiveAccess != Restricted ||
		!hasReadinessIssue(readiness, IssueEndpointUnavailable, nil) ||
		!hasReadinessIssue(readiness, IssueKnowledgeBaseUnpublished, &publicKB) {
		t.Fatalf("initial readiness = %#v, %v", readiness, err)
	}
	if _, err = catalog.SetLifecycle(ctx, SetLifecycleCommand{
		AgentID: created.ID, ExpectedVersion: 1, Lifecycle: Active,
	}, actor, "activate-not-ready"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("unready activation error = %v", err)
	}
	if details, ok := NotReadyDetails(err); !ok || details.Ready {
		t.Fatalf("readiness error details = %#v, %t", details, ok)
	}
	unchanged, err := catalog.Get(ctx, created.ID)
	if err != nil || unchanged.Lifecycle != Draft || unchanged.Version != 1 {
		t.Fatalf("agent changed after rejected activation = %#v, %v", unchanged, err)
	}

	publishAgentKnowledgeBase(t, ctx, pool, publicKB)
	if _, err = pool.Exec(ctx, `
		UPDATE provider_endpoints SET health='HEALTHY',health_checked_at=clock_timestamp()
		WHERE id=$1
	`, pgUUID(ID(endpointID))); err != nil {
		t.Fatal(err)
	}
	readiness, err = catalog.EvaluateReadiness(ctx, created.ID)
	if err != nil || !readiness.Ready || readiness.ModelProfileVersionID == nil ||
		readiness.ProviderEndpointID == nil || *readiness.ProviderEndpointID != endpointID {
		t.Fatalf("ready Agent = %#v, %v", readiness, err)
	}
	activated, err := catalog.SetLifecycle(ctx, SetLifecycleCommand{
		AgentID: created.ID, ExpectedVersion: 1, Lifecycle: Active,
	}, actor, "activate")
	if err != nil || activated.Lifecycle != Active || activated.Version != 2 || activated.ActivatedAt == nil {
		t.Fatalf("activated Agent = %#v, %v", activated, err)
	}
	readyScoped, err := catalog.ListReadyScoped(ctx, []AgentID{secondary.ID, activated.ID})
	if err != nil || len(readyScoped) != 1 || readyScoped[0].ID != activated.ID {
		t.Fatalf("ready scoped Agents = %#v, %v", readyScoped, err)
	}
	resolvedScoped, err := catalog.ResolveReadyScoped(ctx, []AgentID{activated.ID}, activated.Key)
	if err != nil || resolvedScoped.ID != activated.ID {
		t.Fatalf("resolved scoped Agent = %#v, %v", resolvedScoped, err)
	}
	if _, err = catalog.ResolveReadyScoped(ctx, []AgentID{secondary.ID}, activated.Key); !errors.Is(err, ErrChatModelUnavailable) {
		t.Fatalf("unscoped selector error = %v", err)
	}
	if _, err = catalog.ResolveReadyScoped(ctx, []AgentID{secondary.ID}, secondary.Key); !errors.Is(err, ErrChatModelUnavailable) {
		t.Fatalf("inactive selector error = %v", err)
	}
	scopeDescriptions, err := catalog.DescribeScopes(ctx, []AgentID{secondary.ID, activated.ID})
	if err != nil || len(scopeDescriptions) != 2 || scopeDescriptions[0].AgentID != secondary.ID ||
		scopeDescriptions[0].Ready || scopeDescriptions[1].AgentID != activated.ID || !scopeDescriptions[1].Ready ||
		len(scopeDescriptions[1].KnowledgeBaseIDs) != 2 || scopeDescriptions[1].KnowledgeBaseIDs[0] != publicKB ||
		scopeDescriptions[1].KnowledgeBaseIDs[1] != restrictedKB || scopeDescriptions[1].EffectiveAccess != Restricted {
		t.Fatalf("scope descriptions = %#v, %v", scopeDescriptions, err)
	}
	if _, err = catalog.DescribeScopes(ctx, []AgentID{activated.ID, activated.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate scope description error = %v", err)
	}
	if _, err = catalog.DescribeScopes(ctx, []AgentID{AgentID(testUUID(t))}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown scope description error = %v", err)
	}

	replacement := configuration
	replacement.DisplayName = "Documentation Concierge"
	replacement.Description = "Second immutable configuration."
	replacement.KnowledgeBaseIDs = []KnowledgeBaseID{restrictedKB, publicKB}
	replaced, err := catalog.ReplaceConfiguration(ctx, ReplaceConfigurationCommand{
		AgentID: created.ID, ExpectedVersion: 2, Configuration: replacement,
	}, actor, "replace")
	if err != nil || replaced.Version != 3 || replaced.CurrentVersion.VersionNumber != 2 ||
		replaced.CurrentVersion.Configuration.DisplayName != replacement.DisplayName ||
		replaced.CurrentVersion.Memberships[0].KnowledgeBaseID != restrictedKB {
		t.Fatalf("replaced Agent = %#v, %v", replaced, err)
	}
	replacementReplay, err := catalog.ReplaceConfiguration(ctx, ReplaceConfigurationCommand{
		AgentID: created.ID, ExpectedVersion: 2, Configuration: replacement,
	}, actor, "replace")
	if err != nil || replacementReplay.Version != 3 || replacementReplay.CurrentVersionID != replaced.CurrentVersionID {
		t.Fatalf("replacement replay = %#v, %v", replacementReplay, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE knowledge_bases SET lifecycle='ARCHIVED' WHERE id=$1`, pgUUID(ID(publicKB))); err != nil {
		t.Fatal(err)
	}
	unreadyReplacement := replacement
	unreadyReplacement.DisplayName = "Candidate requiring archived knowledge"
	if _, err = catalog.ReplaceConfiguration(ctx, ReplaceConfigurationCommand{
		AgentID: created.ID, ExpectedVersion: 3, Configuration: unreadyReplacement,
	}, actor, "replace-unready-candidate"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("unready active replacement error = %v", err)
	} else if details, ok := NotReadyDetails(err); !ok || details.Ready ||
		!hasReadinessIssue(details, IssueKnowledgeBaseInactive, &publicKB) {
		t.Fatalf("unready active replacement details = %#v, %t", details, ok)
	}
	unchangedAfterUnready, err := catalog.Get(ctx, created.ID)
	if err != nil || unchangedAfterUnready.Version != replaced.Version ||
		unchangedAfterUnready.CurrentVersionID != replaced.CurrentVersionID ||
		unchangedAfterUnready.CurrentVersion.Configuration.DisplayName != replacement.DisplayName {
		t.Fatalf("unready active replacement changed current Agent = %#v, %v", unchangedAfterUnready, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE knowledge_bases SET lifecycle='ACTIVE' WHERE id=$1`, pgUUID(ID(publicKB))); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.ReplaceConfiguration(ctx, ReplaceConfigurationCommand{
		AgentID: created.ID, ExpectedVersion: 2, Configuration: configuration,
	}, actor, "replace-stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale replacement error = %v", err)
	}

	firstVersionPage, err := catalog.ListVersions(ctx, created.ID, nil, 1)
	if err != nil || len(firstVersionPage.Versions) != 1 || firstVersionPage.NextCursor == nil ||
		firstVersionPage.Versions[0].VersionNumber != 2 {
		t.Fatalf("first immutable version page = %#v, %v", firstVersionPage, err)
	}
	secondVersionPage, err := catalog.ListVersions(ctx, created.ID, firstVersionPage.NextCursor, 1)
	if err != nil || len(secondVersionPage.Versions) != 1 || secondVersionPage.NextCursor != nil ||
		secondVersionPage.Versions[0].VersionNumber != 1 ||
		secondVersionPage.Versions[0].Configuration.DisplayName != configuration.DisplayName ||
		secondVersionPage.Versions[0].Memberships[0].KnowledgeBaseID != publicKB {
		t.Fatalf("second immutable version page = %#v, %v", secondVersionPage, err)
	}
	if _, err = catalog.ListVersions(ctx, created.ID, nil, MaxVersionPageSize+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded version page error = %v", err)
	}
	scopeDescriptions, err = catalog.DescribeScopes(ctx, []AgentID{created.ID})
	if err != nil || len(scopeDescriptions) != 1 || !scopeDescriptions[0].Ready ||
		scopeDescriptions[0].EffectiveAccess != Restricted || len(scopeDescriptions[0].KnowledgeBaseIDs) != 2 ||
		scopeDescriptions[0].KnowledgeBaseIDs[0] != restrictedKB || scopeDescriptions[0].KnowledgeBaseIDs[1] != publicKB {
		t.Fatalf("restricted scope description = %#v, %v", scopeDescriptions, err)
	}
	beforeFailed := replaced
	badReplacement := replacement
	badReplacement.KnowledgeBaseIDs = []KnowledgeBaseID{KnowledgeBaseID(testUUID(t))}
	if _, err = catalog.ReplaceConfiguration(ctx, ReplaceConfigurationCommand{
		AgentID: created.ID, ExpectedVersion: 3, Configuration: badReplacement,
	}, actor, "replace-invalid-reference"); err == nil {
		t.Fatal("replacement with unknown knowledge base succeeded")
	}
	afterFailed, err := catalog.Get(ctx, created.ID)
	if err != nil || afterFailed.Version != beforeFailed.Version || afterFailed.CurrentVersionID != beforeFailed.CurrentVersionID {
		t.Fatalf("failed replacement was not atomic = %#v, %v", afterFailed, err)
	}
	versions, err := catalog.ListVersions(ctx, created.ID, nil, MaxVersionPageSize)
	if err != nil || len(versions.Versions) != 2 || versions.NextCursor != nil {
		t.Fatalf("failed replacement left an immutable version = %#v, %v", versions, err)
	}

	archived, err := catalog.SetLifecycle(ctx, SetLifecycleCommand{
		AgentID: created.ID, ExpectedVersion: 3, Lifecycle: Archived,
	}, actor, "archive")
	if err != nil || archived.Version != 4 || archived.Lifecycle != Archived || archived.ArchivedAt == nil {
		t.Fatalf("archived Agent = %#v, %v", archived, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE model_profiles SET availability='UNAVAILABLE' WHERE id=$1`, pgUUID(ID(profileID))); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.SetLifecycle(ctx, SetLifecycleCommand{
		AgentID: created.ID, ExpectedVersion: 4, Lifecycle: Active,
	}, actor, "reactivate-unavailable-model"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("unavailable-model activation error = %v", err)
	} else if details, ok := NotReadyDetails(err); !ok || !hasReadinessIssue(details, IssueModelUnavailable, nil) {
		t.Fatalf("unavailable-model readiness details = %#v, %t", details, ok)
	}

	var audits, events, idempotencyRecords, versionRows int
	err = pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM audit_events WHERE target_type='agent'),
		  (SELECT count(*) FROM event_log WHERE resource_type='agent'),
		  (SELECT count(*) FROM idempotency_records WHERE operation LIKE 'agent.%'),
		  (SELECT count(*) FROM agent_versions WHERE agent_id=$1)
	`, pgUUID(ID(created.ID))).Scan(&audits, &events, &idempotencyRecords, &versionRows)
	if err != nil || audits != 5 || events != 5 || idempotencyRecords != 5 || versionRows != 2 {
		t.Fatalf("catalog records audits=%d events=%d idempotency=%d versions=%d err=%v", audits, events, idempotencyRecords, versionRows, err)
	}
}

func seedAgentModel(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	actor auth.OperatorID,
) (ModelProfileID, ProviderEndpointID) {
	t.Helper()
	endpointID := ProviderEndpointID(testUUID(t))
	profileID := ModelProfileID(testUUID(t))
	profileVersionID := ModelProfileVersionID(testUUID(t))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO provider_endpoints(
			id,display_name,display_key,base_url,lifecycle,health,version,configuration_version
		) VALUES($1,'Agent Provider','agent-provider','https://models.example.test','ACTIVE','UNKNOWN',1,1)
	`, pgUUID(ID(endpointID))); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id,version)
		VALUES($1,$2,'answer-model','AVAILABLE',$3,1)
	`, pgUUID(ID(profileID)), pgUUID(ID(endpointID)), pgUUID(ID(profileVersionID))); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO model_profile_versions(
			id,profile_id,version_number,configuration_version,transport,
			context_window_tokens,max_output_tokens,supports_streaming,supports_tools,
			supports_structured_output,supports_temperature,reasoning_transport,
			timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,
			source,created_by_operator_id
		) VALUES($1,$2,1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,
		         'NONE',30,0,2,'{}'::jsonb,'{}'::jsonb,'OPERATOR',$3)
	`, pgUUID(ID(profileVersionID)), pgUUID(ID(profileID)), pgUUID(ID(actor))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return profileID, endpointID
}

func seedAgentKnowledgeBase(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	name string,
	access AccessPolicy,
	published bool,
) KnowledgeBaseID {
	t.Helper()
	id := KnowledgeBaseID(testUUID(t))
	if _, err := pool.Exec(ctx, `
		INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language)
		VALUES($1,$2,$3,$4,'ACTIVE','','en')
	`, pgUUID(ID(id)), name, id.String(), access); err != nil {
		t.Fatal(err)
	}
	if published {
		publishAgentKnowledgeBase(t, ctx, pool, id)
	}
	return id
}

func publishAgentKnowledgeBase(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id KnowledgeBaseID) {
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
	`, pgUUID(ID(jobID)), pgUUID(ID(id)), "agent-test-publish:"+id.String()); err != nil {
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
		"agent-test/wiki/"+ID(wikiID).String(), bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE documentation_runs SET published_wiki_version_id=$2 WHERE id=$1
	`, pgUUID(ID(runID)), pgUUID(ID(wikiID))); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE knowledge_bases SET published_wiki_id=$2,updated_at=clock_timestamp() WHERE id=$1
	`, pgUUID(ID(id)), pgUUID(ID(wikiID))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func hasReadinessIssue(readiness Readiness, code ReadinessIssueCode, knowledgeBaseID *KnowledgeBaseID) bool {
	for _, issue := range readiness.Issues {
		if issue.Code != code {
			continue
		}
		if knowledgeBaseID == nil && issue.KnowledgeBaseID == nil ||
			knowledgeBaseID != nil && issue.KnowledgeBaseID != nil && *knowledgeBaseID == *issue.KnowledgeBaseID {
			return true
		}
	}
	return false
}

func migrateAgentTestDatabase(t *testing.T, ctx context.Context, databaseURL string) {
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

func testUUID(t *testing.T) [16]byte {
	t.Helper()
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
