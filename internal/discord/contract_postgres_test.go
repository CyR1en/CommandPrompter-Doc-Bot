package discord

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type discordBindingRaceResult struct {
	binding Binding
	err     error
}

type mutableDeliveryREST struct {
	state   *LiveDeliveryState
	failure *error
}

func (client *mutableDeliveryREST) RefreshDelivery(
	context.Context,
	string,
	Snowflake,
	Snowflake,
	Snowflake,
	Snowflake,
	Snowflake,
) (LiveDeliveryState, error) {
	if client.failure != nil && *client.failure != nil {
		return LiveDeliveryState{}, *client.failure
	}
	state := *client.state
	state.CallerRoleIDs = cloneSnowflakeSet(state.CallerRoleIDs)
	state.Listen.ViewerRoleIDs = append([]Snowflake(nil), state.Listen.ViewerRoleIDs...)
	state.Listen.ViewerUserIDs = append([]Snowflake(nil), state.Listen.ViewerUserIDs...)
	state.Destination.ViewerRoleIDs = append([]Snowflake(nil), state.Destination.ViewerRoleIDs...)
	state.Destination.ViewerUserIDs = append([]Snowflake(nil), state.Destination.ViewerUserIDs...)
	return state, nil
}

func (*mutableDeliveryREST) Close() {}

func cloneSnowflakeSet(values map[Snowflake]struct{}) map[Snowflake]struct{} {
	result := make(map[Snowflake]struct{}, len(values))
	for value := range values {
		result[value] = struct{}{}
	}
	return result
}

type discordContractFixture struct {
	store          *Store
	pool           *pgxpool.Pool
	vault          *security.CredentialVault
	actor          auth.OperatorID
	credentialID   credentials.ID
	connectionID   ConnectionID
	knowledgeBase  agents.KnowledgeBaseID
	documentRunID  agents.DocumentationRunID
	wikiVersionID  agents.WikiVersionID
	agentIDs       [2]agents.AgentID
	agentVersionID [2]agents.VersionID
}

func setupDiscordContractFixture(t *testing.T) discordContractFixture {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDiscordTestDatabase(t, ctx, databaseURL)
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		TRUNCATE credentials,operators,knowledge_bases,jobs,idempotency_records,
		         audit_events,event_log CASCADE
	`); err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewCredentialVault(
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{13}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture := discordContractFixture{
		pool: pool, vault: vault, actor: auth.OperatorID(randomBytes16(t)),
		credentialID: credentials.ID(randomBytes16(t)), connectionID: ConnectionID(randomBytes16(t)),
		knowledgeBase: agents.KnowledgeBaseID(randomBytes16(t)),
		documentRunID: agents.DocumentationRunID(randomBytes16(t)),
		wikiVersionID: agents.WikiVersionID(randomBytes16(t)),
	}
	secret, err := security.NewSecretValue("discord-contract-secret")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := vault.Encrypt(
		security.CredentialID(fixture.credentialID), security.CredentialDiscordBotToken, 1, secret,
	)
	if err != nil {
		t.Fatal(err)
	}
	prepareJobID := jobs.JobID(randomBytes16(t))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	queries := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO operators(id,username,username_key,password_hash) VALUES($1,'Discord Contract','discord contract','unused')`, []any{pgDiscordUUID([16]byte(fixture.actor))}},
		{`INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version) VALUES($1,'DISCORD_BOT_TOKEN','Discord Contract','••••',$2,$3,$4,1)`, []any{pgDiscordUUID([16]byte(fixture.credentialID)), envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext()}},
		{`INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language) VALUES($1,'Contract KB','contract kb','RESTRICTED','ACTIVE','','en')`, []any{pgDiscordUUID([16]byte(fixture.knowledgeBase))}},
		{`INSERT INTO jobs(id,job_type,target_type,target_id,operation_key,status) VALUES($1,'PREPARE_RUN','knowledge_base',$2,$3,'SUCCEEDED')`, []any{pgDiscordUUID([16]byte(prepareJobID)), pgDiscordUUID([16]byte(fixture.knowledgeBase)), "discord-contract-prepare:" + prepareJobID.String()}},
		{`INSERT INTO documentation_runs(id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,instructions,language,completed_at) VALUES($1,$2,'PUBLISHED',$3,1,'','en',clock_timestamp())`, []any{pgDiscordUUID([16]byte(fixture.documentRunID)), pgDiscordUUID([16]byte(fixture.knowledgeBase)), pgDiscordUUID([16]byte(prepareJobID))}},
		{`INSERT INTO wiki_versions(id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count) VALUES($1,$2,$3,$4,decode(repeat('cd',32),'hex'),1)`, []any{pgDiscordUUID([16]byte(fixture.wikiVersionID)), pgDiscordUUID([16]byte(fixture.knowledgeBase)), pgDiscordUUID([16]byte(fixture.documentRunID)), "discord-contract/wiki/" + fixture.wikiVersionID.String()}},
		{`UPDATE documentation_runs SET published_wiki_version_id=$2 WHERE id=$1`, []any{pgDiscordUUID([16]byte(fixture.documentRunID)), pgDiscordUUID([16]byte(fixture.wikiVersionID))}},
		{`UPDATE knowledge_bases SET published_wiki_id=$2 WHERE id=$1`, []any{pgDiscordUUID([16]byte(fixture.knowledgeBase)), pgDiscordUUID([16]byte(fixture.wikiVersionID))}},
		{`INSERT INTO discord_connections(id,display_name,display_key,credential_id,credential_version,application_id,bot_user_id,bot_username,lifecycle,state) VALUES($1,'Contract Bot','contract bot',$2,1,'900000000000000001','900000000000000002','contract-bot','ENABLED','READY')`, []any{pgDiscordUUID([16]byte(fixture.connectionID)), pgDiscordUUID([16]byte(fixture.credentialID))}},
		{`INSERT INTO discord_servers(connection_id,server_id,name,owner,refreshed_at) VALUES($1,'100','Contract Guild',false,clock_timestamp())`, []any{pgDiscordUUID([16]byte(fixture.connectionID))}},
		{`INSERT INTO discord_roles(connection_id,server_id,role_id,name,position,refreshed_at) VALUES($1,'100','300','Readers',1,clock_timestamp()),($1,'100','301','Editors',2,clock_timestamp())`, []any{pgDiscordUUID([16]byte(fixture.connectionID))}},
	}
	for _, query := range queries {
		if _, err = tx.Exec(ctx, query.query, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, channel := range []struct {
		id, parent string
		kind       int32
	}{
		{id: "200"}, {id: "201"}, {id: "202"}, {id: "203"}, {id: "204"},
		{id: "210", parent: "200", kind: 11},
	} {
		var parent any
		if channel.parent != "" {
			parent = channel.parent
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO discord_channels(
				connection_id,server_id,channel_id,parent_id,name,channel_type,position,
				effective_bot_permissions,everyone_can_view,viewer_role_ids,viewer_user_ids,refreshed_at
			) VALUES($1,'100',$2,$3,$4,$5,0,$6,false,'["300"]','[]',clock_timestamp())
		`, pgDiscordUUID([16]byte(fixture.connectionID)), channel.id, parent, "channel-"+channel.id,
			channel.kind, int64(BasePermissions|ThreadPermissions)); err != nil {
			t.Fatal(err)
		}
	}
	for index := range fixture.agentIDs {
		fixture.agentIDs[index] = seedDiscordAgent(t, ctx, tx, [16]byte(fixture.knowledgeBase))
		var versionID pgtype.UUID
		if err = tx.QueryRow(ctx, `SELECT current_version_id FROM agents WHERE id=$1`,
			pgDiscordUUID([16]byte(fixture.agentIDs[index]))).Scan(&versionID); err != nil || !versionID.Valid {
			t.Fatal(err)
		}
		fixture.agentVersionID[index] = agents.VersionID(versionID.Bytes)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.store, err = NewStore(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture discordContractFixture) bindingConfiguration(agentID agents.AgentID, channel Snowflake, trigger TriggerType) BindingConfiguration {
	return BindingConfiguration{
		ConnectionID: fixture.connectionID, ServerID: "100", ListenChannelID: channel,
		AgentID: agentID, Triggers: []TriggerType{trigger}, ReplyPolicy: ReplySameChannel,
		AllowedRoleIDs: []Snowflake{"300"}, AllowedUserIDs: []Snowflake{}, RatePolicy: DefaultRatePolicy(),
	}
}

func (fixture discordContractFixture) gatewayCapture() GatewayCapture {
	return GatewayCapture{
		ConnectionID: fixture.connectionID, ConnectionVersion: 1,
		CredentialID: fixture.credentialID, CredentialVersion: 1,
	}
}

func TestBindingRouteUniquenessIsFinalConcurrentAuthority(t *testing.T) {
	fixture := setupDiscordContractFixture(t)
	ctx := context.Background()
	createStart := make(chan struct{})
	createResults := make(chan discordBindingRaceResult, 2)
	for index := range 2 {
		index := index
		go func() {
			<-createStart
			binding, err := fixture.store.CreateBinding(ctx, CreateBinding{
				Configuration: fixture.bindingConfiguration(fixture.agentIDs[index], "200", TriggerMention),
				Enabled:       true,
			}, fixture.actor, fmt.Sprintf("binding-route-create-%d", index))
			createResults <- discordBindingRaceResult{binding: binding, err: err}
		}()
	}
	close(createStart)
	assertOneDiscordSuccessAndConflict(t, createResults)

	coexisting, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: fixture.bindingConfiguration(fixture.agentIDs[1], "200", TriggerSlashCommand), Enabled: true,
	}, fixture.actor, "binding-route-coexist")
	if err != nil || !coexisting.Enabled {
		t.Fatalf("different trigger coexistence = %+v, %v", coexisting, err)
	}

	drafts := make([]Binding, 2)
	for index, channel := range []Snowflake{"201", "202"} {
		drafts[index], err = fixture.store.CreateBinding(ctx, CreateBinding{
			Configuration: fixture.bindingConfiguration(fixture.agentIDs[0], channel, TriggerMention),
		}, fixture.actor, fmt.Sprintf("binding-route-draft-%d", index))
		if err != nil {
			t.Fatal(err)
		}
	}
	updateStart := make(chan struct{})
	updateResults := make(chan discordBindingRaceResult, 2)
	for index := range drafts {
		index := index
		go func() {
			<-updateStart
			binding, updateErr := fixture.store.UpdateBinding(ctx, UpdateBinding{
				BindingID: drafts[index].ID, ExpectedVersion: drafts[index].Version,
				Configuration: fixture.bindingConfiguration(fixture.agentIDs[0], "203", TriggerMention),
				Enabled:       true,
			}, fixture.actor, fmt.Sprintf("binding-route-update-%d", index))
			updateResults <- discordBindingRaceResult{binding: binding, err: updateErr}
		}()
	}
	close(updateStart)
	assertOneDiscordSuccessAndConflict(t, updateResults)

	direct := make([]Binding, 2)
	for index := range direct {
		direct[index], err = fixture.store.CreateBinding(ctx, CreateBinding{
			Configuration: fixture.bindingConfiguration(fixture.agentIDs[index], "204", TriggerMention),
		}, fixture.actor, fmt.Sprintf("binding-route-direct-%d", index))
		if err != nil {
			t.Fatal(err)
		}
	}
	directStart := make(chan struct{})
	directResults := make(chan error, 2)
	for _, binding := range direct {
		binding := binding
		go func() {
			tx, beginErr := fixture.pool.Begin(ctx)
			if beginErr != nil {
				directResults <- beginErr
				return
			}
			defer tx.Rollback(ctx)
			<-directStart
			_, updateErr := tx.Exec(ctx, `
				UPDATE channel_bindings
				SET enabled=true,health='HEALTHY',validated_at=clock_timestamp()
				WHERE id=$1
			`, pgDiscordUUID([16]byte(binding.ID)))
			if updateErr == nil {
				updateErr = tx.Commit(ctx)
			}
			directResults <- updateErr
		}()
	}
	close(directStart)
	directSuccess, directConflict := 0, 0
	for range 2 {
		switch err = <-directResults; {
		case err == nil:
			directSuccess++
		case postgresUniqueViolation(err):
			directConflict++
		default:
			t.Fatalf("direct route race error = %v", err)
		}
	}
	if directSuccess != 1 || directConflict != 1 {
		t.Fatalf("direct route race successes=%d conflicts=%d", directSuccess, directConflict)
	}
	var enabledRoutes int
	if err = fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM channel_binding_triggers
		WHERE connection_id=$1 AND server_id='100' AND listen_channel_id='204'
		  AND trigger_type='MENTION' AND enabled
	`, pgDiscordUUID([16]byte(fixture.connectionID))).Scan(&enabledRoutes); err != nil {
		t.Fatal(err)
	}
	if enabledRoutes != 1 {
		t.Fatalf("direct enabled routes = %d", enabledRoutes)
	}
}

func TestStaleGatewayCaptureCannotAuthorizeAfterConnectionRotation(t *testing.T) {
	fixture := setupDiscordContractFixture(t)
	ctx := context.Background()
	if _, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: fixture.bindingConfiguration(fixture.agentIDs[0], "200", TriggerMention), Enabled: true,
	}, fixture.actor, "stale-gateway-binding"); err != nil {
		t.Fatal(err)
	}
	stale := fixture.gatewayCapture()
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE discord_connections SET version=version+1 WHERE id=$1
	`, pgDiscordUUID([16]byte(fixture.connectionID))); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.AuthorizeInvocation(
		ctx, stale, "100", "200", nil, "400", map[Snowflake]struct{}{"300": {}}, false,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale gateway invocation error = %v", err)
	}
}

func TestDiscordContextIsBoundedAndAgentVersionIsolated(t *testing.T) {
	fixture := setupDiscordContractFixture(t)
	ctx := context.Background()
	binding, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: fixture.bindingConfiguration(fixture.agentIDs[0], "200", TriggerMention),
	}, fixture.actor, "context-binding")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreWithOptions(fixture.pool, fixture.vault, StoreOptions{Context: ContextOptions{
		IdleExpiry: time.Hour, MaxMessages: 4, MaxTokens: 64,
	}})
	if err != nil {
		t.Fatal(err)
	}
	key := ContextKey{
		BindingID: binding.ID, AgentID: binding.AgentID, AgentVersionID: fixture.agentVersionID[0],
		UserID: "400", DestinationID: "200",
	}
	for turn := 1; turn <= 3; turn++ {
		if err = store.AppendContext(ctx, key, fmt.Sprintf("user-%d", turn), fmt.Sprintf("assistant-%d", turn)); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := store.LoadContext(ctx, key)
	if err != nil || len(messages) != 4 || messages[0].Content != "user-2" || messages[3].Content != "assistant-3" {
		t.Fatalf("bounded context = %#v, %v", messages, err)
	}

	assertContextForeignKeyViolation(t, fixture, binding.ID, fixture.agentIDs[1], fixture.agentVersionID[1], "401")
	assertContextForeignKeyViolation(t, fixture, binding.ID, fixture.agentIDs[0], fixture.agentVersionID[1], "402")

	newVersionID := agents.VersionID(randomBytes16(t))
	if _, err = fixture.pool.Exec(ctx, `
		INSERT INTO agent_versions(
			id,agent_id,version_number,display_name,description,response_language,
			identity_instructions,model_profile_id,reasoning_effort,answer_mode,
			behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
			max_answer_tokens,created_by_operator_id
		)
		SELECT $2,agent_id,2,display_name,description,response_language,
		       identity_instructions,model_profile_id,reasoning_effort,answer_mode,
		       behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
		       max_answer_tokens,created_by_operator_id
		FROM agent_versions WHERE id=$1
	`, pgDiscordUUID([16]byte(fixture.agentVersionID[0])), pgDiscordUUID([16]byte(newVersionID))); err != nil {
		t.Fatal(err)
	}
	newKey := key
	newKey.AgentVersionID = newVersionID
	if err = store.AppendContext(ctx, newKey, "new-user", "new-assistant"); err != nil {
		t.Fatal(err)
	}
	newMessages, err := store.LoadContext(ctx, newKey)
	if err != nil || len(newMessages) != 2 || newMessages[0].Content != "new-user" {
		t.Fatalf("new-version context = %#v, %v", newMessages, err)
	}
	if messages, err = store.LoadContext(ctx, key); err != nil || len(messages) != 4 {
		t.Fatalf("old-version context = %#v, %v", messages, err)
	}
	concurrentStore, err := NewStoreWithOptions(fixture.pool, fixture.vault, StoreOptions{Context: ContextOptions{
		IdleExpiry: time.Hour, MaxMessages: 20, MaxTokens: 1_024,
	}})
	if err != nil {
		t.Fatal(err)
	}
	concurrentKey := key
	concurrentKey.DestinationID = "201"
	appendStart := make(chan struct{})
	appendErrors := make(chan error, 5)
	for turn := 1; turn <= 5; turn++ {
		turn := turn
		go func() {
			<-appendStart
			appendErrors <- concurrentStore.AppendContext(
				ctx, concurrentKey, fmt.Sprintf("concurrent-user-%d", turn), fmt.Sprintf("concurrent-assistant-%d", turn),
			)
		}()
	}
	close(appendStart)
	for range 5 {
		if appendErr := <-appendErrors; appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	concurrentMessages, err := concurrentStore.LoadContext(ctx, concurrentKey)
	if err != nil || len(concurrentMessages) != 10 {
		t.Fatalf("concurrent context = %#v, %v", concurrentMessages, err)
	}
	for index := 0; index < len(concurrentMessages); index += 2 {
		user := concurrentMessages[index]
		assistant := concurrentMessages[index+1]
		if user.Role != agents.RoleUser || assistant.Role != agents.RoleAssistant ||
			strings.TrimPrefix(user.Content, "concurrent-user-") != strings.TrimPrefix(assistant.Content, "concurrent-assistant-") {
			t.Fatalf("incomplete concurrent turn at %d: %#v %#v", index, user, assistant)
		}
	}
	tokenStore, err := NewStoreWithOptions(fixture.pool, fixture.vault, StoreOptions{Context: ContextOptions{
		IdleExpiry: time.Hour, MaxMessages: 20, MaxTokens: 8,
	}})
	if err != nil {
		t.Fatal(err)
	}
	tokenKey := key
	tokenKey.DestinationID = "202"
	for turn := 1; turn <= 3; turn++ {
		if err = tokenStore.AppendContext(ctx, tokenKey,
			fmt.Sprintf("user%04d", turn), fmt.Sprintf("asst%04d", turn)); err != nil {
			t.Fatal(err)
		}
	}
	tokenMessages, err := tokenStore.LoadContext(ctx, tokenKey)
	if err != nil || len(tokenMessages) != 4 || tokenMessages[0].Content != "user0002" || tokenMessages[3].Content != "asst0003" {
		t.Fatalf("token-pruned context = %#v, %v", tokenMessages, err)
	}

	if _, err = fixture.pool.Exec(ctx, `
		UPDATE discord_conversations
		SET created_at=clock_timestamp()-interval '3 hours',
		    last_activity_at=clock_timestamp()-interval '2 hours',
		    updated_at=clock_timestamp()-interval '2 hours',
		    expires_at=clock_timestamp()-interval '1 hour'
		WHERE binding_id=$1 AND agent_version_id=$2
	`, pgDiscordUUID([16]byte(binding.ID)), pgDiscordUUID([16]byte(newVersionID))); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendContext(ctx, newKey, "fresh-user", "fresh-assistant"); err != nil {
		t.Fatal(err)
	}
	if newMessages, err = store.LoadContext(ctx, newKey); err != nil || len(newMessages) != 2 || newMessages[0].Content != "fresh-user" {
		t.Fatalf("expired context reset = %#v, %v", newMessages, err)
	}

	updatedConfig := fixture.bindingConfiguration(fixture.agentIDs[1], "200", TriggerMention)
	if _, err = fixture.store.UpdateBinding(ctx, UpdateBinding{
		BindingID: binding.ID, ExpectedVersion: binding.Version, Configuration: updatedConfig,
	}, fixture.actor, "context-agent-target-change"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = fixture.pool.QueryRow(ctx, `SELECT count(*) FROM discord_conversations WHERE binding_id=$1`,
		pgDiscordUUID([16]byte(binding.ID))).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("Agent target change retained %d Discord contexts", remaining)
	}
}

func TestInvocationAuthorizesTheEntireTwoKnowledgeBaseCorpus(t *testing.T) {
	fixture := setupDiscordContractFixture(t)
	ctx := context.Background()
	secondKnowledgeBaseID := agents.KnowledgeBaseID(randomBytes16(t))
	secondRunID := agents.DocumentationRunID(randomBytes16(t))
	secondWikiID := agents.WikiVersionID(randomBytes16(t))
	jobID := jobs.JobID(randomBytes16(t))
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language)
		VALUES($1,'Second Contract KB','second contract kb','PUBLIC','ACTIVE','','en');
		INSERT INTO jobs(id,job_type,target_type,target_id,operation_key,status)
		VALUES($2,'PREPARE_RUN','knowledge_base',$1,$3,'SUCCEEDED');
		INSERT INTO documentation_runs(
			id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,instructions,language,completed_at
		) VALUES($4,$1,'PUBLISHED',$2,1,'','en',clock_timestamp());
		INSERT INTO wiki_versions(
			id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count
		) VALUES($5,$1,$4,$6,decode(repeat('de',32),'hex'),1);
		UPDATE documentation_runs SET published_wiki_version_id=$5 WHERE id=$4;
		UPDATE knowledge_bases SET published_wiki_id=$5 WHERE id=$1;
		INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id)
		VALUES($7,$8,1,$1)
	`, pgx.QueryExecModeSimpleProtocol, pgDiscordUUID([16]byte(secondKnowledgeBaseID)),
		pgDiscordUUID([16]byte(jobID)), "two-kb:"+jobID.String(), pgDiscordUUID([16]byte(secondRunID)),
		pgDiscordUUID([16]byte(secondWikiID)), "discord-contract/wiki/"+secondWikiID.String(),
		pgDiscordUUID([16]byte(fixture.agentIDs[0])), pgDiscordUUID([16]byte(fixture.agentVersionID[0]))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	binding, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: fixture.bindingConfiguration(fixture.agentIDs[0], "200", TriggerMention), Enabled: true,
	}, fixture.actor, "two-kb-binding")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.AuthorizeInvocation(
		ctx, fixture.gatewayCapture(), "100", "200", nil, "400", nil, false,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("two-KB restricted corpus authorized without allowlist match: %v", err)
	}
	invocation, err := fixture.store.AuthorizeInvocation(
		ctx, fixture.gatewayCapture(), "100", "200", nil, "400", map[Snowflake]struct{}{"300": {}}, false,
	)
	if err != nil || invocation.Binding.ID != binding.ID || invocation.EffectiveAccess != AccessRestricted ||
		len(invocation.Corpus) != 2 || invocation.Corpus[0].Position != 0 || invocation.Corpus[1].Position != 1 ||
		invocation.Corpus[1].KnowledgeBaseID != secondKnowledgeBaseID || invocation.Corpus[1].AccessPolicy != AccessPublic {
		t.Fatalf("two-KB invocation = %+v, %v", invocation, err)
	}
	if _, err = fixture.pool.Exec(ctx, `UPDATE knowledge_bases SET lifecycle='ARCHIVED',archived_at=clock_timestamp() WHERE id=$1`,
		pgDiscordUUID([16]byte(secondKnowledgeBaseID))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.AuthorizeInvocation(
		ctx, fixture.gatewayCapture(), "100", "200", nil, "400", map[Snowflake]struct{}{"300": {}}, false,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("two-KB corpus partially authorized with inactive member: %v", err)
	}
}

func TestInvocationThreadShadowingAndFinalDeliveryMutationMatrix(t *testing.T) {
	fixture := setupDiscordContractFixture(t)
	ctx := context.Background()
	withMemberDeny, err := audienceOverwriteDigest([]any{
		map[string]any{"id": "100", "type": json.Number("0"), "allow": "0", "deny": "1024"},
		map[string]any{"id": "300", "type": json.Number("0"), "allow": "1024", "deny": "0"},
		map[string]any{"id": "400", "type": json.Number("1"), "allow": "0", "deny": "1024"},
	})
	if err != nil {
		t.Fatal(err)
	}
	withoutMemberDeny, err := audienceOverwriteDigest([]any{
		map[string]any{"id": "100", "type": json.Number("0"), "allow": "0", "deny": "1024"},
		map[string]any{"id": "300", "type": json.Number("0"), "allow": "1024", "deny": "0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.pool.Exec(ctx, `
		UPDATE discord_channels SET audience_overwrite_sha256=$2
		WHERE connection_id=$1 AND channel_id='200'
	`, pgDiscordUUID([16]byte(fixture.connectionID)), withMemberDeny[:]); err != nil {
		t.Fatal(err)
	}
	parentConfig := fixture.bindingConfiguration(fixture.agentIDs[0], "200", TriggerMention)
	parentConfig.ReplyPolicy = ReplyThread
	parentConfig.AllowedRoleIDs = []Snowflake{"300", "301"}
	parentConfig.AllowedUserIDs = []Snowflake{"400"}
	parent, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: parentConfig, Enabled: true,
	}, fixture.actor, "thread-parent-binding")
	if err != nil {
		t.Fatal(err)
	}
	parentID := Snowflake("200")
	roles := map[Snowflake]struct{}{"300": {}, "301": {}}
	invocation, err := fixture.store.AuthorizeInvocation(
		ctx, fixture.gatewayCapture(), "100", "210", &parentID, "400", roles, false,
	)
	if err != nil || invocation.Binding.ID != parent.ID || invocation.InvocationChannelID != "210" ||
		invocation.InvocationParentID == nil || *invocation.InvocationParentID != "200" {
		t.Fatalf("inherited invocation = %+v, %v", invocation, err)
	}

	exactConfig := fixture.bindingConfiguration(fixture.agentIDs[1], "210", TriggerMention)
	exactConfig.AllowedRoleIDs = []Snowflake{}
	exactConfig.AllowedUserIDs = []Snowflake{"401"}
	disabledExact, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: exactConfig,
	}, fixture.actor, "thread-disabled-exact-shadow")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.AuthorizeInvocation(
		ctx, fixture.gatewayCapture(), "100", "210", &parentID, "400", roles, false,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled exact route fell back to parent: %v", err)
	}
	if err = fixture.store.DeleteBinding(
		ctx, disabledExact.ID, disabledExact.Version, fixture.actor, "thread-disabled-exact-delete",
	); err != nil {
		t.Fatal(err)
	}
	exact, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: exactConfig, Enabled: true,
	}, fixture.actor, "thread-exact-shadow")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.store.AuthorizeInvocation(
		ctx, fixture.gatewayCapture(), "100", "210", &parentID, "400", roles, false,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("denied exact route fell back to parent: %v", err)
	}
	exactInvocation, err := fixture.store.AuthorizeInvocation(
		ctx, fixture.gatewayCapture(), "100", "210", &parentID, "401", nil, false,
	)
	if err != nil || exactInvocation.Binding.ID != exact.ID || exactInvocation.Binding.AgentID != fixture.agentIDs[1] {
		t.Fatalf("authorized exact invocation = %+v, %v", exactInvocation, err)
	}
	if err = fixture.store.DeleteBinding(ctx, exact.ID, exact.Version, fixture.actor, "thread-exact-delete"); err != nil {
		t.Fatal(err)
	}

	runID := recordDiscordRunForInvocation(t, fixture, invocation)
	live := LiveDeliveryState{
		CallerRoleIDs: cloneSnowflakeSet(roles),
		Listen:        invocation.CapturedListen,
		Destination:   invocation.CapturedReply,
	}
	live.Destination.ChannelID = "210"
	live.Destination.ParentID = &parentID
	var liveFailure error
	deliveryStore, err := NewStoreWithOptions(fixture.pool, fixture.vault, StoreOptions{
		DeliveryRESTFactory: func() (DeliveryREST, error) {
			return &mutableDeliveryREST{state: &live, failure: &liveFailure}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	permit := DeliveryPermit{Invocation: invocation, RunID: runID, DestinationID: "210"}
	if err = deliveryStore.ReauthorizeDelivery(ctx, permit); err != nil {
		t.Fatalf("valid inherited delivery = %v", err)
	}
	takeoverConfig := fixture.bindingConfiguration(fixture.agentIDs[1], "210", TriggerMention)
	disabledTakeover, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: takeoverConfig,
	}, fixture.actor, "thread-disabled-exact-takeover")
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryConflict(t, deliveryStore, permit, "disabled exact route takeover")
	if err = fixture.store.DeleteBinding(
		ctx, disabledTakeover.ID, disabledTakeover.Version, fixture.actor, "thread-disabled-exact-takeover-delete",
	); err != nil {
		t.Fatal(err)
	}

	takeover, err := fixture.store.CreateBinding(ctx, CreateBinding{
		Configuration: takeoverConfig, Enabled: true,
	}, fixture.actor, "thread-exact-takeover")
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryConflict(t, deliveryStore, permit, "exact route takeover")
	if err = fixture.store.DeleteBinding(ctx, takeover.ID, takeover.Version, fixture.actor, "thread-exact-takeover-delete"); err != nil {
		t.Fatal(err)
	}
	if err = deliveryStore.ReauthorizeDelivery(ctx, permit); err != nil {
		t.Fatalf("parent route after takeover removal = %v", err)
	}
	if _, err = fixture.pool.Exec(ctx, `UPDATE channel_bindings SET agent_id=$2 WHERE id=$1`,
		pgDiscordUUID([16]byte(parent.ID)), pgDiscordUUID([16]byte(fixture.agentIDs[1]))); err != nil {
		t.Fatal(err)
	}
	assertDeliveryConflict(t, deliveryStore, permit, "binding Agent target")
	if _, err = fixture.pool.Exec(ctx, `UPDATE channel_bindings SET agent_id=$2 WHERE id=$1`,
		pgDiscordUUID([16]byte(parent.ID)), pgDiscordUUID([16]byte(fixture.agentIDs[0]))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.pool.Exec(ctx, `DELETE FROM channel_binding_triggers WHERE binding_id=$1`,
		pgDiscordUUID([16]byte(parent.ID))); err != nil {
		t.Fatal(err)
	}
	assertDeliveryConflict(t, deliveryStore, permit, "binding trigger removed")
	if _, err = fixture.pool.Exec(ctx, `
		INSERT INTO channel_binding_triggers(
			binding_id,connection_id,server_id,listen_channel_id,enabled,trigger_type
		) VALUES($1,$2,'100','200',true,'MENTION')
	`, pgDiscordUUID([16]byte(parent.ID)), pgDiscordUUID([16]byte(fixture.connectionID))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.pool.Exec(ctx, `
		DELETE FROM agent_version_knowledge_bases
		WHERE agent_version_id=$1 AND knowledge_base_id=$2
	`, pgDiscordUUID([16]byte(invocation.AgentVersionID)), pgDiscordUUID([16]byte(fixture.knowledgeBase))); err != nil {
		t.Fatal(err)
	}
	assertDeliveryConflict(t, deliveryStore, permit, "Agent corpus membership")
	if _, err = fixture.pool.Exec(ctx, `
		INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id)
		VALUES($1,$2,0,$3)
	`, pgDiscordUUID([16]byte(parent.AgentID)), pgDiscordUUID([16]byte(invocation.AgentVersionID)),
		pgDiscordUUID([16]byte(fixture.knowledgeBase))); err != nil {
		t.Fatal(err)
	}
	newCurrentVersionID := agents.VersionID(randomBytes16(t))
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO agent_versions(
			id,agent_id,version_number,display_name,description,response_language,
			identity_instructions,model_profile_id,reasoning_effort,answer_mode,
			behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
			max_answer_tokens,created_by_operator_id
		)
		SELECT $2,agent_id,2,display_name,description,response_language,
		       identity_instructions,model_profile_id,reasoning_effort,answer_mode,
		       behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
		       max_answer_tokens,created_by_operator_id
		FROM agent_versions WHERE id=$1
	`, pgDiscordUUID([16]byte(invocation.AgentVersionID)), pgDiscordUUID([16]byte(newCurrentVersionID))); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id)
		VALUES($1,$2,0,$3)
	`, pgDiscordUUID([16]byte(parent.AgentID)), pgDiscordUUID([16]byte(newCurrentVersionID)),
		pgDiscordUUID([16]byte(fixture.knowledgeBase))); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE agents SET current_version_id=$2,version=version+1 WHERE id=$1`,
		pgDiscordUUID([16]byte(parent.AgentID)), pgDiscordUUID([16]byte(newCurrentVersionID))); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertDeliveryConflict(t, deliveryStore, permit, "Agent current version")
	if _, err = fixture.pool.Exec(ctx, `UPDATE agents SET current_version_id=$2,version=version-1 WHERE id=$1`,
		pgDiscordUUID([16]byte(parent.AgentID)), pgDiscordUUID([16]byte(invocation.AgentVersionID))); err != nil {
		t.Fatal(err)
	}

	databaseMutations := []struct {
		name, mutate, restore string
		args                  []any
	}{
		{
			name: "binding version", mutate: `UPDATE channel_bindings SET version=version+1 WHERE id=$1`,
			restore: `UPDATE channel_bindings SET version=version-1 WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(parent.ID))},
		},
		{
			name: "binding caller allowlist", mutate: `UPDATE channel_bindings SET allowed_role_ids='["301"]' WHERE id=$1`,
			restore: `UPDATE channel_bindings SET allowed_role_ids='["300","301"]' WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(parent.ID))},
		},
		{
			name: "binding disabled route", mutate: `UPDATE channel_bindings SET enabled=false,health='DRAFT',validated_at=NULL WHERE id=$1`,
			restore: `UPDATE channel_bindings SET enabled=true,health='HEALTHY',validated_at=clock_timestamp() WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(parent.ID))},
		},
		{
			name: "Agent resource version", mutate: `UPDATE agents SET version=version+1 WHERE id=$1`,
			restore: `UPDATE agents SET version=version-1 WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(parent.AgentID))},
		},
		{
			name: "Agent lifecycle", mutate: `UPDATE agents SET lifecycle='ARCHIVED',archived_at=clock_timestamp() WHERE id=$1`,
			restore: `UPDATE agents SET lifecycle='ACTIVE',archived_at=NULL WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(parent.AgentID))},
		},
		{
			name: "KB lifecycle", mutate: `UPDATE knowledge_bases SET lifecycle='ARCHIVED',archived_at=clock_timestamp() WHERE id=$1`,
			restore: `UPDATE knowledge_bases SET lifecycle='ACTIVE',archived_at=NULL WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(fixture.knowledgeBase))},
		},
		{
			name: "KB access", mutate: `UPDATE knowledge_bases SET access_policy='PUBLIC' WHERE id=$1`,
			restore: `UPDATE knowledge_bases SET access_policy='RESTRICTED' WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(fixture.knowledgeBase))},
		},
		{
			name: "KB publication removed", mutate: `UPDATE knowledge_bases SET published_wiki_id=NULL WHERE id=$1 AND $2::uuid IS NOT NULL`,
			restore: `UPDATE knowledge_bases SET published_wiki_id=$2 WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(fixture.knowledgeBase)), pgDiscordUUID([16]byte(fixture.wikiVersionID))},
		},
		{
			name: "connection state", mutate: `UPDATE discord_connections SET state='DEGRADED' WHERE id=$1`,
			restore: `UPDATE discord_connections SET state='READY' WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(fixture.connectionID))},
		},
		{
			name: "credential capture", mutate: `UPDATE discord_connections SET credential_version=credential_version+1 WHERE id=$1`,
			restore: `UPDATE discord_connections SET credential_version=credential_version-1 WHERE id=$1`, args: []any{pgDiscordUUID([16]byte(fixture.connectionID))},
		},
	}
	for _, test := range databaseMutations {
		t.Run(test.name, func(t *testing.T) {
			if _, mutationErr := fixture.pool.Exec(ctx, test.mutate, test.args...); mutationErr != nil {
				t.Fatal(mutationErr)
			}
			assertDeliveryConflict(t, deliveryStore, permit, test.name)
			if _, restoreErr := fixture.pool.Exec(ctx, test.restore, test.args...); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			if restoreErr := deliveryStore.ReauthorizeDelivery(ctx, permit); restoreErr != nil {
				t.Fatalf("%s restore = %v", test.name, restoreErr)
			}
		})
	}

	originalRoles := live.CallerRoleIDs
	live.CallerRoleIDs = map[Snowflake]struct{}{"300": {}}
	assertDeliveryConflict(t, deliveryStore, permit, "caller lost one of two allowed roles")
	live.CallerRoleIDs = map[Snowflake]struct{}{"300": {}, "301": {}, "999": {}}
	assertDeliveryConflict(t, deliveryStore, permit, "unrelated caller role churn despite direct-user allow")
	live.CallerRoleIDs = originalRoles
	originalAudience := append([]Snowflake(nil), live.Destination.ViewerRoleIDs...)
	live.Destination.ViewerRoleIDs = []Snowflake{"301"}
	assertDeliveryConflict(t, deliveryStore, permit, "reply audience")
	live.Destination.ViewerRoleIDs = originalAudience
	originalDigest := live.Destination.AudienceOverwriteSHA256
	live.Destination.AudienceOverwriteSHA256 = withoutMemberDeny
	assertDeliveryConflict(t, deliveryStore, permit, "member VIEW_CHANNEL deny removal")
	live.Destination.AudienceOverwriteSHA256 = originalDigest
	originalPermissions := live.Destination.EffectiveBotPermissions
	live.Destination.EffectiveBotPermissions &^= PermissionSendMessagesInThread
	assertDeliveryConflict(t, deliveryStore, permit, "bot permissions")
	live.Destination.EffectiveBotPermissions = originalPermissions
	originalParent := live.Destination.ParentID
	unrelatedParent := Snowflake("201")
	live.Destination.ParentID = &unrelatedParent
	assertDeliveryConflict(t, deliveryStore, permit, "thread parent")
	live.Destination.ParentID = originalParent
	liveFailure = errors.New("Discord destination no longer exists")
	assertDeliveryConflict(t, deliveryStore, permit, "channel existence")
	liveFailure = nil

	republishDiscordKnowledgeBase(t, fixture)
	if err = deliveryStore.ReauthorizeDelivery(ctx, permit); err != nil {
		t.Fatalf("new immutable wiki publication invalidated delivery: %v", err)
	}
}

func assertDeliveryConflict(t *testing.T, store *Store, permit DeliveryPermit, mutation string) {
	t.Helper()
	if err := store.ReauthorizeDelivery(context.Background(), permit); !errors.Is(err, ErrConflict) {
		t.Fatalf("%s delivery error = %v", mutation, err)
	}
}

func recordDiscordRunForInvocation(
	t *testing.T,
	fixture discordContractFixture,
	invocation Invocation,
) agents.RunID {
	t.Helper()
	ctx := context.Background()
	var profileID, profileVersionID, endpointID pgtype.UUID
	var profileVersionNumber, endpointConfigurationVersion int32
	if err := fixture.pool.QueryRow(ctx, `
		SELECT version.model_profile_id,profile.current_version_id,
		       profile_version.version_number,endpoint.id,endpoint.configuration_version
		FROM agent_versions AS version
		JOIN model_profiles AS profile ON profile.id=version.model_profile_id
		JOIN model_profile_versions AS profile_version ON profile_version.id=profile.current_version_id
		JOIN provider_endpoints AS endpoint ON endpoint.id=profile.endpoint_id
		WHERE version.id=$1
	`, pgDiscordUUID([16]byte(invocation.AgentVersionID))).Scan(
		&profileID, &profileVersionID, &profileVersionNumber, &endpointID, &endpointConfigurationVersion,
	); err != nil {
		t.Fatal(err)
	}
	runID := agents.RunID(randomBytes16(t))
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO agent_runs(
			id,agent_id,agent_version_id,agent_resource_version,agent_version_number,
			model_profile_id,model_profile_version_id,model_profile_version_number,
			provider_endpoint_id,captured_endpoint_configuration_version,origin,subject,
			request_digest,effective_access_policy,outcome,model_usage,latency_ms,tool_calls,citations
		) VALUES($1,$2,$3,$4,1,$5,$6,$7,$8,$9,'DISCORD',$10,
		         decode(repeat('ef',32),'hex'),$11,'ANSWERED','{}',1,'[]','[]')
	`, pgDiscordUUID([16]byte(runID)), pgDiscordUUID([16]byte(invocation.Binding.AgentID)),
		pgDiscordUUID([16]byte(invocation.AgentVersionID)), invocation.AgentResourceVersion,
		profileID, profileVersionID, profileVersionNumber, endpointID, endpointConfigurationVersion,
		invocation.Subject, string(invocation.EffectiveAccess)); err != nil {
		t.Fatal(err)
	}
	for _, member := range invocation.Corpus {
		if _, err = tx.Exec(ctx, `
			INSERT INTO agent_run_knowledge_bases(
				run_id,position,knowledge_base_id,knowledge_base_version,access_policy,
				wiki_version_id,documentation_run_id,source_revision_ids,source_scope_digest
			) VALUES($1,$2,$3,$4,$5,$6,$7,'{}',decode(repeat('ab',32),'hex'))
		`, pgDiscordUUID([16]byte(runID)), member.Position, pgDiscordUUID([16]byte(member.KnowledgeBaseID)),
			member.KnowledgeBaseVersion, string(member.AccessPolicy), pgDiscordUUID([16]byte(fixture.wikiVersionID)),
			pgDiscordUUID([16]byte(fixture.documentRunID))); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return runID
}

func republishDiscordKnowledgeBase(t *testing.T, fixture discordContractFixture) {
	t.Helper()
	ctx := context.Background()
	jobID := jobs.JobID(randomBytes16(t))
	runID := agents.DocumentationRunID(randomBytes16(t))
	wikiID := agents.WikiVersionID(randomBytes16(t))
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO jobs(id,job_type,target_type,target_id,operation_key,status)
		VALUES($1,'PREPARE_RUN','knowledge_base',$2,$3,'SUCCEEDED')
	`, pgDiscordUUID([16]byte(jobID)), pgDiscordUUID([16]byte(fixture.knowledgeBase)),
		"discord-republish:"+wikiID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO documentation_runs(
			id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,
			instructions,language,completed_at
		) SELECT $1,id,'PUBLISHED',$2,version,instructions,language,clock_timestamp()
		  FROM knowledge_bases WHERE id=$3
	`, pgDiscordUUID([16]byte(runID)), pgDiscordUUID([16]byte(jobID)),
		pgDiscordUUID([16]byte(fixture.knowledgeBase))); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO wiki_versions(
			id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count
		) VALUES($1,$2,$3,$4,decode(repeat('bc',32),'hex'),1)
	`, pgDiscordUUID([16]byte(wikiID)), pgDiscordUUID([16]byte(fixture.knowledgeBase)),
		pgDiscordUUID([16]byte(runID)), "discord-contract/wiki/"+wikiID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET published_wiki_version_id=$2 WHERE id=$1`,
		pgDiscordUUID([16]byte(runID)), pgDiscordUUID([16]byte(wikiID))); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE knowledge_bases
		SET published_wiki_id=$2,version=version+1,updated_at=clock_timestamp()
		WHERE id=$1
	`, pgDiscordUUID([16]byte(fixture.knowledgeBase)), pgDiscordUUID([16]byte(wikiID))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertContextForeignKeyViolation(
	t *testing.T,
	fixture discordContractFixture,
	bindingID BindingID,
	agentID agents.AgentID,
	versionID agents.VersionID,
	userID string,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO discord_conversations(
			binding_id,agent_id,agent_version_id,external_user_id,destination_id,expires_at
		) VALUES($1,$2,$3,$4,'200',clock_timestamp()+interval '1 hour')
	`, pgDiscordUUID([16]byte(bindingID)), pgDiscordUUID([16]byte(agentID)),
		pgDiscordUUID([16]byte(versionID)), userID); err == nil {
		err = tx.Commit(ctx)
	}
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "23503" {
		t.Fatalf("context FK error = %v", err)
	}
}

func assertOneDiscordSuccessAndConflict(t *testing.T, results <-chan discordBindingRaceResult) {
	t.Helper()
	successes, conflicts := 0, 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil && result.binding.ID != (BindingID{}):
			successes++
		case errors.Is(result.err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("route race result=%+v error=%v", result.binding, result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("route race successes=%d conflicts=%d", successes, conflicts)
	}
}

func postgresUniqueViolation(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "23505"
}
