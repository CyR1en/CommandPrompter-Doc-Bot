package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/chattokens"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCompatibilityRoutePersistsOnlyAgentRunRowsPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDocumentationAPIDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		TRUNCATE agent_runs,agents,knowledge_bases,model_profiles,provider_endpoints,operators,
		         jobs,event_log,audit_events,idempotency_records
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatal(err)
	}
	capture := seedCompatibilityExecutionCapture(t, ctx, pool)
	vault := testCompatibilityVault(t)
	postgresStore, err := agents.NewPostgresExecutionStore(pool, compatibilityArtifactResolver{}, vault)
	if err != nil {
		t.Fatal(err)
	}
	repository := &compatibilityExecutionRepository{capture: capture, store: postgresStore}
	engine, err := agents.NewEngine(repository, compatibilityDigester{}, compatibilityCitationModel{}, agents.EngineOptions{
		Clock: func() time.Time { return capture.CapturedAt.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenID, err := chattokens.ParseID(testedTokenID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := chattokens.NewGrant(tokenID, []agents.AgentID{capture.Agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	compatibility, err := newCompatibilityHandler(
		fakeCompatibilityTokens{grant: grant},
		&fakeScopedAgents{selected: capture.Agent, list: []agents.Agent{capture.Agent}},
		engine,
		func() time.Time { return capture.CapturedAt.Add(2 * time.Second) },
	)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	compatibility.register(mux)
	handler := instrumentRequests(problemBoundary(mux), newApplicationMetrics())
	response := compatibilityRequest(handler, http.MethodPost, compatibilityCompletionsPath,
		`{"model":"agent:db-docs","messages":[{"role":"user","content":"What is verified?"}],"stream":false}`,
		map[string]string{"Authorization": "Bearer token", "Content-Type": "application/json"})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"x_ref0":{"run_id":"`+capture.RunID.String()+`"`) {
		t.Fatalf("completion=%d %s", response.Code, response.Body.String())
	}
	var runs, scopes int
	var subject string
	if err = pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM agent_runs),
		       (SELECT count(*) FROM agent_run_knowledge_bases),
		       (SELECT subject FROM agent_runs LIMIT 1)
	`).Scan(&runs, &scopes, &subject); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || scopes != 1 || subject != grant.Subject {
		t.Fatalf("rows runs=%d scopes=%d subject=%q", runs, scopes, subject)
	}
}

type compatibilityExecutionRepository struct {
	capture agents.ExecutionCapture
	store   *agents.PostgresExecutionStore
}

func (repository *compatibilityExecutionRepository) Capture(context.Context, string) (agents.ExecutionCapture, error) {
	return repository.capture, nil
}

func (repository *compatibilityExecutionRepository) ReleaseCapture(context.Context, agents.ExecutionCapture) error {
	return nil
}

func (*compatibilityExecutionRepository) AssertFresh(context.Context, agents.ExecutionCapture) error {
	return nil
}

func (*compatibilityExecutionRepository) AssertSecurityFresh(context.Context, agents.ExecutionCapture) error {
	return nil
}

func (repository *compatibilityExecutionRepository) RecordRun(ctx context.Context, record agents.RunRecord) (agents.RunID, error) {
	return repository.store.RecordRun(ctx, record)
}

func (*compatibilityExecutionRepository) SearchWiki(_ context.Context, _ agents.CapturedKnowledgeBase, _ string, _ int) ([]agents.WikiSearchHit, error) {
	return []agents.WikiSearchHit{{Slug: "home", Title: "Home", Rank: 1}}, nil
}

func (*compatibilityExecutionRepository) ReadWikiPage(_ context.Context, captured agents.CapturedKnowledgeBase, slug string, start int, _ *int) (agents.WikiPassage, error) {
	path, end := slug, start
	return agents.WikiPassage{
		Slug: slug, Title: "Home", StartLine: start, EndLine: end, Text: "Verified database evidence.",
		Citation: agents.EvidenceCitation{
			Label: "Home", Resource: "wiki://" + captured.WikiVersionID.String() + "/" + slug,
			Path: &path, StartLine: &start, EndLine: &end,
		},
	}, nil
}

func (*compatibilityExecutionRepository) GetClaim(context.Context, agents.CapturedKnowledgeBase, string) (agents.Claim, error) {
	return agents.Claim{}, agents.ErrEvidence
}

type compatibilityDigester struct{}

func (compatibilityDigester) DigestRequest(agents.ExecutionCapture, agents.ExecuteRequest) ([32]byte, error) {
	return [32]byte{9}, nil
}

type compatibilityCitationModel struct{}

func (compatibilityCitationModel) Complete(_ context.Context, request agents.ModelRequest) (agents.ModelTurn, error) {
	if len(request.Messages) != 2 {
		return agents.ModelTurn{}, errors.New("unexpected prompt")
	}
	const marker = `citation_id\":\"`
	content := request.Messages[1].Content
	start := strings.Index(content, marker)
	if start < 0 {
		return agents.ModelTurn{}, errors.New("citation absent")
	}
	start += len(marker)
	end := strings.Index(content[start:], `\"`)
	if end < 0 {
		return agents.ModelTurn{}, errors.New("citation malformed")
	}
	return agents.ModelTurn{Draft: &agents.AnswerDraft{
		Status: agents.DraftAnswered,
		Spans:  []agents.DraftSpan{{Markdown: "Verified answer.", CitationIDs: []string{content[start : start+end]}}},
	}, Usage: map[string]int{"input_tokens": 4, "output_tokens": 2}}, nil
}

type compatibilityArtifactResolver struct{}

func (compatibilityArtifactResolver) ResolveArtifactKey(string) (string, error) {
	return "", errors.New("unexpected artifact lookup")
}

func seedCompatibilityExecutionCapture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) agents.ExecutionCapture {
	t.Helper()
	ids := []string{
		"41000000-0000-4000-8000-000000000001", "42000000-0000-4000-8000-000000000002",
		"43000000-0000-4000-8000-000000000003", "44000000-0000-4000-8000-000000000004",
		"45000000-0000-4000-8000-000000000005", "46000000-0000-4000-8000-000000000006",
		"47000000-0000-4000-8000-000000000007", "48000000-0000-4000-8000-000000000008",
		"49000000-0000-4000-8000-000000000009", "4a000000-0000-4000-8000-00000000000a",
	}
	parsed := make([]agents.ID, len(ids))
	for index, raw := range ids {
		value, err := agents.ParseID(raw)
		if err != nil {
			t.Fatal(err)
		}
		parsed[index] = value
	}
	actorID, endpointID, profileID, profileVersionID := parsed[0], parsed[1], parsed[2], parsed[3]
	agentID, agentVersionID, knowledgeBaseID := parsed[4], parsed[5], parsed[6]
	jobID, documentationRunID, wikiID := parsed[7], parsed[8], parsed[9]
	runID := agents.RunID{91}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO operators(id,username,username_key,password_hash) VALUES($1,'Compatibility Operator','compatibility operator','unused')`, compatPGUUID(actorID))
	batch.Queue(`
		INSERT INTO provider_endpoints(id,display_name,display_key,base_url,lifecycle,health,health_checked_at,version,configuration_version)
		VALUES($1,'Compatibility Provider','compatibility-provider','https://models.example.test','ACTIVE','HEALTHY',clock_timestamp(),1,1)
	`, compatPGUUID(endpointID))
	batch.Queue(`INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id,version) VALUES($1,$2,'compat-model','AVAILABLE',$3,1)`, compatPGUUID(profileID), compatPGUUID(endpointID), compatPGUUID(profileVersionID))
	batch.Queue(`
		INSERT INTO model_profile_versions(
			id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,
			supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,
			timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id
		) VALUES($1,$2,1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,'NONE',30,0,2,'{}','{}','OPERATOR',$3)
	`, compatPGUUID(profileVersionID), compatPGUUID(profileID), compatPGUUID(actorID))
	batch.Queue(`INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language,version) VALUES($1,'Compatibility Docs','compatibility docs','PUBLIC','ACTIVE','','en',1)`, compatPGUUID(knowledgeBaseID))
	batch.Queue(`INSERT INTO jobs(id,job_type,target_type,target_id,operation_key) VALUES($1,'PREPARE_RUN','knowledge_base',$2,'compatibility-run')`, compatPGUUID(jobID), compatPGUUID(knowledgeBaseID))
	batch.Queue(`
		INSERT INTO documentation_runs(id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,instructions,language,completed_at)
		VALUES($1,$2,'PUBLISHED',$3,1,'','en',clock_timestamp())
	`, compatPGUUID(documentationRunID), compatPGUUID(knowledgeBaseID), compatPGUUID(jobID))
	batch.Queue(`
		INSERT INTO wiki_versions(id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count)
		VALUES($1,$2,$3,'compatibility/wiki',decode(repeat('01',32),'hex'),1)
	`, compatPGUUID(wikiID), compatPGUUID(knowledgeBaseID), compatPGUUID(documentationRunID))
	batch.Queue(`UPDATE documentation_runs SET published_wiki_version_id=$1 WHERE id=$2`, compatPGUUID(wikiID), compatPGUUID(documentationRunID))
	batch.Queue(`UPDATE knowledge_bases SET published_wiki_id=$1 WHERE id=$2`, compatPGUUID(wikiID), compatPGUUID(knowledgeBaseID))
	batch.Queue(`
		INSERT INTO agent_run_scope_reservations(run_id,position,knowledge_base_id,wiki_version_id,expires_at)
		VALUES($1,0,$2,$3,clock_timestamp()+interval '24 hours')
	`, compatPGUUID(agents.ID(runID)), compatPGUUID(knowledgeBaseID), compatPGUUID(wikiID))
	batch.Queue(`INSERT INTO agents(id,agent_key,lifecycle,current_version_id,version,activated_at) VALUES($1,'db-docs','ACTIVE',$2,1,clock_timestamp())`, compatPGUUID(agentID), compatPGUUID(agentVersionID))
	batch.Queue(`
		INSERT INTO agent_versions(
			id,agent_id,version_number,display_name,description,response_language,identity_instructions,model_profile_id,
			reasoning_effort,answer_mode,behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
			max_answer_tokens,created_by_operator_id
		) VALUES($1,$2,1,'DB Docs','','en','Answer from the database docs.',$3,'NONE','SINGLE_PASS','','WIKI_ONLY','Cannot answer.',0,1024,$4)
	`, compatPGUUID(agentVersionID), compatPGUUID(agentID), compatPGUUID(profileID), compatPGUUID(actorID))
	batch.Queue(`INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id) VALUES($1,$2,0,$3)`, compatPGUUID(agentID), compatPGUUID(agentVersionID), compatPGUUID(knowledgeBaseID))
	results := tx.SendBatch(ctx, batch)
	if err = results.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(time.Microsecond)
	contextTokens, outputTokens := int32(16000), int32(4096)
	configuration := agents.Configuration{
		DisplayName: "DB Docs", ResponseLanguage: "en", IdentityInstructions: "Answer from the database docs.",
		ModelProfileID: agents.ModelProfileID(profileID), ReasoningEffort: agents.ReasoningNone,
		AnswerMode: agents.SinglePass, EvidenceAccess: agents.WikiOnly, RefusalMarkdown: "Cannot answer.",
		MaxToolCalls: 0, MaxAnswerTokens: 1024, KnowledgeBaseIDs: []agents.KnowledgeBaseID{agents.KnowledgeBaseID(knowledgeBaseID)},
	}
	version := agents.Version{
		ID: agents.VersionID(agentVersionID), AgentID: agents.AgentID(agentID), VersionNumber: 1,
		Configuration: configuration, Memberships: []agents.Membership{{Position: 0, KnowledgeBaseID: agents.KnowledgeBaseID(knowledgeBaseID)}},
		CreatedByOperator: [16]byte(actorID), CreatedAt: now,
	}
	agent := agents.Agent{
		ID: agents.AgentID(agentID), Key: "db-docs", Lifecycle: agents.Active,
		CurrentVersionID: agents.VersionID(agentVersionID), CurrentVersion: version, Version: 1,
		CreatedAt: now, UpdatedAt: now, ActivatedAt: &now,
	}
	return agents.ExecutionCapture{
		RunID: runID, Agent: agent,
		Model: agents.CapturedModel{
			Endpoint: providers.Endpoint{ID: providers.EndpointID(endpointID), Lifecycle: providers.Active, Health: providers.Healthy, Version: 1, ConfigurationVersion: 1},
			Profile: providers.Profile{
				ID: providers.ProfileID(profileID), EndpointID: providers.EndpointID(endpointID), Availability: providers.Available, Version: 1,
				CurrentVersion: providers.ProfileVersion{
					ID: providers.ProfileVersionID(profileVersionID), ProfileID: providers.ProfileID(profileID), VersionNumber: 1, ConfigurationVersion: 1,
					Settings: providers.Settings{Transport: providers.ChatCompletions, ContextWindowTokens: &contextTokens, MaxOutputTokens: &outputTokens},
				},
			},
			ProfileVersionID: agents.ModelProfileVersionID(profileVersionID), ProfileVersionNumber: 1,
			ReasoningEffort: agents.ReasoningNone, AnswerMode: agents.SinglePass,
		},
		KnowledgeBases: []agents.CapturedKnowledgeBase{{
			Position: 0, ID: agents.KnowledgeBaseID(knowledgeBaseID), ResourceVersion: 1, AccessPolicy: agents.Public,
			WikiVersionID: agents.WikiVersionID(wikiID), DocumentationRunID: agents.DocumentationRunID(documentationRunID),
			SourceScopeDigest: [32]byte{7},
		}},
		EffectiveAccess: agents.Public, CapturedAt: now,
	}
}

func testCompatibilityVault(t *testing.T) *security.CredentialVault {
	t.Helper()
	vault, err := security.NewCredentialVault(
		"active:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

func compatPGUUID(id agents.ID) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte(id), Valid: true}
}
