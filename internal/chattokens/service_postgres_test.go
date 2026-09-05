package chattokens

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestServicePostgreSQLIssueAuthenticateRevokeAndExpiry(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateChatTokenDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		TRUNCATE chat_access_tokens,agents,model_profiles,provider_endpoints,operators,
		         event_log,audit_events,idempotency_records RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewCredentialVault(
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{23}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	var ledgerIndex string
	if err = pool.QueryRow(ctx, `SELECT pg_get_indexdef('public.ix_chat_access_tokens_created'::regclass)`).Scan(&ledgerIndex); err != nil ||
		!strings.Contains(ledgerIndex, "(created_at DESC, id DESC)") || strings.Contains(ledgerIndex, " WHERE ") {
		t.Fatalf("chat token ledger pagination index=%q err=%v", ledgerIndex, err)
	}
	actor, firstAgent, secondAgent := seedChatTokenPrincipals(t, ctx, pool)
	expiresAt := time.Now().UTC().Add(2 * time.Hour)
	issued, err := service.Create(ctx, CreateCommand{
		Label: "Open WebUI", AgentIDs: []agents.AgentID{secondAgent, firstAgent}, ExpiresAt: expiresAt,
	}, actor, "issue-open-webui")
	if err != nil || issued.Secret == nil || issued.Token.ID == (ID{}) {
		t.Fatalf("Create()=%#v err=%v", issued, err)
	}
	plaintext := issued.Secret.Reveal()
	if !validSecret(plaintext) || issued.Token.Prefix == plaintext || len(issued.Token.AgentIDs) != 2 {
		t.Fatalf("issued metadata=%#v", issued.Token)
	}

	replay, err := service.Create(ctx, CreateCommand{
		Label: "Open WebUI", AgentIDs: []agents.AgentID{firstAgent, secondAgent}, ExpiresAt: expiresAt,
	}, actor, "issue-open-webui")
	if !errors.Is(err, ErrSecretAlreadyIssued) || replay.Secret != nil || replay.Token.ID != issued.Token.ID {
		t.Fatalf("sorted-scope replay=%#v err=%v", replay, err)
	}

	var storedDigest []byte
	var storedPrefix string
	var tokens, scopes, leakedAudit int
	if err = pool.QueryRow(ctx, `
		SELECT token_digest,token_prefix,
		       (SELECT count(*) FROM chat_access_tokens),
		       (SELECT count(*) FROM chat_access_token_agents WHERE token_id=$1),
		       (SELECT count(*) FROM audit_events WHERE details::text LIKE '%' || $2 || '%') +
		       (SELECT count(*) FROM event_log WHERE snapshot::text LIKE '%' || $2 || '%') +
		       (SELECT count(*) FROM idempotency_records WHERE request_digest::text LIKE '%' || $2 || '%')
		FROM chat_access_tokens WHERE id=$1
	`, pgUUID(issued.Token.ID), plaintext).Scan(&storedDigest, &storedPrefix, &tokens, &scopes, &leakedAudit); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte(plaintext))
	if !bytes.Equal(storedDigest, expectedDigest[:]) || storedPrefix != issued.Token.Prefix ||
		tokens != 1 || scopes != 2 || leakedAudit != 0 {
		t.Fatalf("storage digest=%x prefix=%q tokens=%d scopes=%d leaked=%d", storedDigest, storedPrefix, tokens, scopes, leakedAudit)
	}

	grant, err := service.Authenticate(ctx, plaintext)
	if err != nil || grant.Subject != "chat-token:"+issued.Token.ID.String() ||
		!grant.Allows(firstAgent) || !grant.Allows(secondAgent) {
		t.Fatalf("Authenticate()=%#v err=%v", grant, err)
	}
	futureLastUse := time.Now().UTC().Add(time.Hour).Round(time.Microsecond)
	if _, err = pool.Exec(ctx, `UPDATE chat_access_tokens SET last_used_at=$2 WHERE id=$1`, pgUUID(issued.Token.ID), futureLastUse); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Authenticate(ctx, plaintext); err != nil {
		t.Fatal(err)
	}
	var lastUsed time.Time
	if err = pool.QueryRow(ctx, `SELECT last_used_at FROM chat_access_tokens WHERE id=$1`, pgUUID(issued.Token.ID)).Scan(&lastUsed); err != nil || !lastUsed.Equal(futureLastUse) {
		t.Fatalf("monotonic last use=%s err=%v", lastUsed, err)
	}

	page, err := service.List(ctx, nil, 1)
	if err != nil || len(page.Summaries) != 1 || page.Summaries[0].ID != issued.Token.ID ||
		page.Summaries[0].AgentCount != 2 || page.NextCursor != nil {
		t.Fatalf("List()=%#v err=%v", page, err)
	}
	revoked, err := service.Revoke(ctx, issued.Token.ID, actor, "revoke-open-webui")
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("Revoke()=%#v err=%v", revoked, err)
	}
	revokedReplay, err := service.Revoke(ctx, issued.Token.ID, actor, "revoke-open-webui")
	if err != nil || revokedReplay.RevokedAt == nil {
		t.Fatalf("Revoke() replay=%#v err=%v", revokedReplay, err)
	}
	if _, err = service.Authenticate(ctx, plaintext); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked Authenticate error=%v", err)
	}

	expired, err := service.Create(ctx, CreateCommand{
		Label: "Expiring", AgentIDs: []agents.AgentID{firstAgent}, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, actor, "issue-expiring")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE chat_access_tokens
		SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour',last_used_at=NULL
		WHERE id=$1
	`, pgUUID(expired.Token.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Authenticate(ctx, expired.Secret.Reveal()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired Authenticate error=%v", err)
	}

	lockExpiry, err := service.Create(ctx, CreateCommand{
		Label: "Lock expiry", AgentIDs: []agents.AgentID{firstAgent}, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, actor, "issue-lock-expiry")
	if err != nil {
		t.Fatal(err)
	}
	var exactExpiry time.Time
	if err = pool.QueryRow(ctx, `
		UPDATE chat_access_tokens SET expires_at=clock_timestamp()+interval '1500 milliseconds'
		WHERE id=$1 RETURNING expires_at
	`, pgUUID(lockExpiry.Token.ID)).Scan(&exactExpiry); err != nil {
		t.Fatal(err)
	}
	locker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback(ctx)
	var lockedID pgtype.UUID
	if err = locker.QueryRow(ctx, `SELECT id FROM chat_access_tokens WHERE id=$1 FOR UPDATE`, pgUUID(lockExpiry.Token.ID)).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	authenticated := make(chan error, 1)
	go func() {
		_, authenticateErr := service.Authenticate(ctx, lockExpiry.Secret.Reveal())
		authenticated <- authenticateErr
	}()
	waitDeadline := time.Now().Add(time.Second)
	blocked := false
	for time.Now().Before(waitDeadline) {
		if err = pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database() AND wait_event_type='Lock'
				  AND query LIKE '%FROM chat_access_tokens%' AND query LIKE '%FOR UPDATE%'
			)
		`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("Authenticate did not block on the locked digest row")
	}
	if delay := time.Until(exactExpiry.Add(50 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	if err = locker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-authenticated; !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("lock-across-expiry Authenticate error=%v", err)
	}

	tiedCreatedAt := time.Now().UTC().Add(-24 * time.Hour)
	if _, err = pool.Exec(ctx, `UPDATE chat_access_tokens SET created_at=$1`, tiedCreatedAt); err != nil {
		t.Fatal(err)
	}
	seenTokens := map[ID]bool{}
	orderedTokens := make([]ID, 0, 3)
	var cursor *PageCursor
	for {
		page, listErr := service.List(ctx, cursor, 1)
		if listErr != nil || len(page.Summaries) != 1 || seenTokens[page.Summaries[0].ID] {
			t.Fatalf("token page=%#v err=%v seen=%v", page, listErr, seenTokens)
		}
		seenTokens[page.Summaries[0].ID] = true
		orderedTokens = append(orderedTokens, page.Summaries[0].ID)
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}
	if len(seenTokens) != 3 {
		t.Fatalf("paginated token count=%d", len(seenTokens))
	}
	wantOrder := append([]ID(nil), orderedTokens...)
	sort.Slice(wantOrder, func(left, right int) bool { return wantOrder[left].String() > wantOrder[right].String() })
	if !reflect.DeepEqual(orderedTokens, wantOrder) {
		t.Fatalf("tied token order=%v want=%v", orderedTokens, wantOrder)
	}
}

func seedChatTokenPrincipals(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (auth.OperatorID, agents.AgentID, agents.AgentID) {
	t.Helper()
	actorID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	endpointID, _ := newUUID()
	profileID, _ := newUUID()
	profileVersionID, _ := newUUID()
	firstAgentID, _ := newUUID()
	firstVersionID, _ := newUUID()
	secondAgentID, _ := newUUID()
	secondVersionID, _ := newUUID()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	batch := &pgx.Batch{}
	batch.Queue(`
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Token Operator','token operator','unused')
	`, pgUUID(actorID))
	batch.Queue(`
		INSERT INTO provider_endpoints(id,display_name,display_key,base_url,lifecycle,health,health_checked_at,version,configuration_version)
		VALUES($1,'Token Provider','token-provider','https://models.example.test','ACTIVE','HEALTHY',clock_timestamp(),1,1)
	`, pgUUID(endpointID))
	batch.Queue(`
		INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id,version)
		VALUES($1,$2,'token-model','AVAILABLE',$3,1)
	`, pgUUID(profileID), pgUUID(endpointID), pgUUID(profileVersionID))
	batch.Queue(`
		INSERT INTO model_profile_versions(
			id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,
			supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,
			timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id
		) VALUES($1,$2,1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,'NONE',30,0,2,'{}','{}','OPERATOR',$3)
	`, pgUUID(profileVersionID), pgUUID(profileID), pgUUID(actorID))
	batch.Queue(`
		INSERT INTO agents(id,agent_key,lifecycle,current_version_id,version) VALUES
			($1,'token-first','DRAFT',$2,1),($3,'token-second','DRAFT',$4,1)
	`, pgUUID(firstAgentID), pgUUID(firstVersionID), pgUUID(secondAgentID), pgUUID(secondVersionID))
	batch.Queue(`
		INSERT INTO agent_versions(
			id,agent_id,version_number,display_name,description,response_language,identity_instructions,
			model_profile_id,reasoning_effort,answer_mode,behavioral_instructions,evidence_access,
			refusal_markdown,max_tool_calls,max_answer_tokens,created_by_operator_id
		) VALUES
			($1,$2,1,'First','','en','Answer docs.',$3,'NONE','TOOL_CALLING','','WIKI_ONLY','Cannot answer.',1,1024,$4),
			($5,$6,1,'Second','','en','Answer docs.',$3,'NONE','TOOL_CALLING','','WIKI_ONLY','Cannot answer.',1,1024,$4)
	`, pgUUID(firstVersionID), pgUUID(firstAgentID), pgUUID(profileID), pgUUID(actorID), pgUUID(secondVersionID), pgUUID(secondAgentID))
	results := tx.SendBatch(ctx, batch)
	if err = results.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return auth.OperatorID(actorID), agents.AgentID(firstAgentID), agents.AgentID(secondAgentID)
}

func migrateChatTokenDatabase(t *testing.T, ctx context.Context, databaseURL string) {
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
