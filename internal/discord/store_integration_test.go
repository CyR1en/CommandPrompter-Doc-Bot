package discord

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/knowledgebases"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestStorePostgreSQLGatewaySemantics(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDiscordTestDatabase(t, ctx, databaseURL)
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "TRUNCATE credentials, discord_connections, event_log CASCADE"); err != nil {
		t.Fatal(err)
	}

	key := "active:" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	vault, err := security.NewCredentialVault(key, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	connectionID := ConnectionID(randomBytes16(t))
	credentialID := security.CredentialID(randomBytes16(t))
	secret, _ := security.NewSecretValue("discord-store-secret-sentinel")
	envelope, err := vault.Encrypt(credentialID, security.CredentialDiscordBotToken, 1, secret)
	if err != nil {
		t.Fatal(err)
	}
	credentialUUID := pgtype.UUID{Bytes: [16]byte(credentialID), Valid: true}
	connectionUUID := pgtype.UUID{Bytes: [16]byte(connectionID), Valid: true}
	if _, err := pool.Exec(ctx, `
		INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES($1,'DISCORD_BOT_TOKEN','Discord','••••',$2,$3,$4,1)
	`, credentialUUID, envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO discord_connections(
			id,display_name,display_key,credential_id,credential_version,lifecycle,state
		) VALUES($1,'Docs bot','docs bot',$2,1,'ENABLED','CONNECTING')
	`, connectionUUID, credentialUUID); err != nil {
		t.Fatal(err)
	}

	configs, err := store.EnabledConnections(ctx)
	if err != nil || len(configs) != 1 || configs[0].ID != connectionID ||
		configs[0].CredentialID != credentials.ID(credentialID) || configs[0].CredentialVersion != 1 ||
		configs[0].Token().Reveal() != "discord-store-secret-sentinel" {
		t.Fatalf("configs=%+v err=%v", configs, err)
	}
	capture := configs[0].Capture()

	first, err := store.AcquireOwnership(ctx, connectionID)
	if err != nil || !first.Owned() {
		t.Fatalf("first ownership=%v err=%v", first.Owned(), err)
	}
	second, err := store.AcquireOwnership(ctx, connectionID)
	if err != nil || second.Owned() {
		t.Fatalf("second ownership=%v err=%v", second.Owned(), err)
	}
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	third, err := store.AcquireOwnership(ctx, connectionID)
	if err != nil || !third.Owned() {
		t.Fatalf("reacquired ownership=%v err=%v", third.Owned(), err)
	}
	if err := third.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if err := store.Connecting(ctx, capture); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(ctx, capture, 42*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := store.EventReceived(ctx, capture, 43*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := store.Degraded(ctx, capture, strings.Repeat("x", 1001)); err != nil {
		t.Fatal(err)
	}
	if err := store.Degraded(ctx, capture, strings.Repeat("x", 1000)); err != nil {
		t.Fatal(err)
	}
	var state string
	var latency, errorLength, eventCount int
	var heartbeat, lastEvent time.Time
	if err := pool.QueryRow(ctx, `
		SELECT state,gateway_latency_ms,length(sanitized_error),last_heartbeat_at,last_event_at
		FROM discord_connections WHERE id=$1
	`, connectionUUID).Scan(&state, &latency, &errorLength, &heartbeat, &lastEvent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM event_log
		WHERE resource_type='discord_connection' AND resource_id=$1
	`, connectionUUID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if state != "DEGRADED" || latency != 43 || errorLength != 1000 || heartbeat.IsZero() || lastEvent.IsZero() || eventCount != 2 {
		t.Fatalf("state=%s latency=%d errorLength=%d heartbeat=%s event=%s events=%d", state, latency, errorLength, heartbeat, lastEvent, eventCount)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE discord_connections SET version=version+1,state='READY',sanitized_error=NULL
		WHERE id=$1
	`, connectionUUID); err != nil {
		t.Fatal(err)
	}
	for name, callback := range map[string]func() error{
		"connecting": func() error { return store.Connecting(ctx, capture) },
		"ready":      func() error { return store.Ready(ctx, capture, time.Millisecond) },
		"event":      func() error { return store.EventReceived(ctx, capture, time.Millisecond) },
		"degraded":   func() error { return store.Degraded(ctx, capture, "stale gateway") },
	} {
		if callbackErr := callback(); !errors.Is(callbackErr, ErrConflict) {
			t.Fatalf("stale %s callback error = %v", name, callbackErr)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT state,count(*) OVER() FROM discord_connections WHERE id=$1`,
		connectionUUID).Scan(&state, &eventCount); err != nil || state != "READY" {
		t.Fatalf("stale callback state=%q err=%v", state, err)
	}

	if _, err := pool.Exec(ctx, "UPDATE credentials SET deleted_at=clock_timestamp() WHERE id=$1", credentialUUID); err != nil {
		t.Fatal(err)
	}
	configs, err = store.EnabledConnections(ctx)
	if err != nil || len(configs) != 0 {
		t.Fatalf("deleted credential configs=%+v err=%v", configs, err)
	}
	var persisted string
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(string_agg(snapshot::text || coalesce(sanitized_error,''), ' '), '')
		FROM event_log LEFT JOIN discord_connections ON discord_connections.id=event_log.resource_id
		WHERE event_log.resource_id=$1
	`, connectionUUID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, "discord-store-secret-sentinel") {
		t.Fatal("Discord token reached persisted state or events")
	}
}

func TestStorePostgreSQLCredentialReuseIsStructurallyRaceSafe(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDiscordTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `TRUNCATE credentials,operators,idempotency_records,audit_events,event_log CASCADE`); err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewCredentialVault(
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{6}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.OperatorID(randomBytes16(t))
	if _, err = pool.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Credential Race','credential race','unused')
	`, pgDiscordUUID([16]byte(actor))); err != nil {
		t.Fatal(err)
	}
	credentialIDs := []credentials.ID{
		credentials.ID(randomBytes16(t)), credentials.ID(randomBytes16(t)),
		credentials.ID(randomBytes16(t)), credentials.ID(randomBytes16(t)),
	}
	for index, credentialID := range credentialIDs {
		secret, secretErr := security.NewSecretValue(fmt.Sprintf("discord-race-token-%d", index))
		if secretErr != nil {
			t.Fatal(secretErr)
		}
		envelope, encryptErr := vault.Encrypt(
			security.CredentialID(credentialID), security.CredentialDiscordBotToken, 1, secret,
		)
		if encryptErr != nil {
			t.Fatal(encryptErr)
		}
		if _, err = pool.Exec(ctx, `
			INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
			VALUES($1,'DISCORD_BOT_TOKEN',$2,'••••',$3,$4,$5,1)
		`, pgDiscordUUID([16]byte(credentialID)), fmt.Sprintf("Discord %d", index),
			envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext()); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	type createResult struct {
		connection Connection
		err        error
	}
	start := make(chan struct{})
	created := make(chan createResult, 2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			<-start
			value, createErr := store.CreateConnection(ctx, CreateConnection{
				DisplayName: fmt.Sprintf("Shared credential %d", index), CredentialID: credentialIDs[0],
			}, actor, fmt.Sprintf("shared-create-%d", index))
			created <- createResult{connection: value, err: createErr}
		}()
	}
	close(start)
	createSuccesses, createConflicts := 0, 0
	for range 2 {
		result := <-created
		switch {
		case result.err == nil:
			createSuccesses++
		case errors.Is(result.err, ErrConflict):
			createConflicts++
		default:
			t.Fatalf("concurrent create error=%v", result.err)
		}
	}
	var sharedCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM discord_connections WHERE credential_id=$1`,
		pgDiscordUUID([16]byte(credentialIDs[0]))).Scan(&sharedCount); err != nil {
		t.Fatal(err)
	}
	if createSuccesses != 1 || createConflicts != 1 || sharedCount != 1 {
		t.Fatalf("create successes=%d conflicts=%d rows=%d", createSuccesses, createConflicts, sharedCount)
	}
	left, err := store.CreateConnection(ctx, CreateConnection{DisplayName: "Left", CredentialID: credentialIDs[1]}, actor, "left-create")
	if err != nil {
		t.Fatal(err)
	}
	right, err := store.CreateConnection(ctx, CreateConnection{DisplayName: "Right", CredentialID: credentialIDs[2]}, actor, "right-create")
	if err != nil {
		t.Fatal(err)
	}
	rotated := make(chan error, 2)
	start = make(chan struct{})
	for _, connection := range []Connection{left, right} {
		connection := connection
		go func() {
			<-start
			_, rotateErr := store.RotateConnectionToken(ctx, RotateToken{
				ConnectionID: connection.ID, ExpectedVersion: connection.Version, CredentialID: credentialIDs[3],
			}, actor, "rotate-"+connection.ID.String())
			rotated <- rotateErr
		}()
	}
	close(start)
	rotateSuccesses, rotateConflicts := 0, 0
	for range 2 {
		switch rotateErr := <-rotated; {
		case rotateErr == nil:
			rotateSuccesses++
		case errors.Is(rotateErr, ErrConflict):
			rotateConflicts++
		default:
			t.Fatalf("concurrent rotate error=%v", rotateErr)
		}
	}
	var rotatedCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM discord_connections WHERE credential_id=$1`,
		pgDiscordUUID([16]byte(credentialIDs[3]))).Scan(&rotatedCount); err != nil {
		t.Fatal(err)
	}
	if rotateSuccesses != 1 || rotateConflicts != 1 || rotatedCount != 1 {
		t.Fatalf("rotate successes=%d conflicts=%d rows=%d", rotateSuccesses, rotateConflicts, rotatedCount)
	}
	queue := jobs.NewStore(pool, nil)
	connectionCommand := func(action, operationKey string, connection Connection) jobs.Command {
		return jobs.Command{
			Type: jobs.RefreshDiscord, TargetType: "discord_connection", TargetID: jobs.UUID(connection.ID),
			Payload: map[string]any{
				"action": action, "connection_id": connection.ID.String(), "connection_version": connection.Version,
				"credential_id": connection.CredentialID.String(), "credential_version": connection.CredentialVersion,
			},
			OperationKey: operationKey,
		}
	}
	left, err = store.GetConnection(ctx, left.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := queue.Enqueue(ctx, connectionCommand("validate", "discord:identity-race-left", left))
	if err != nil {
		t.Fatal(err)
	}
	permit, err := queue.Claim(ctx, "discord-identity-race", time.Minute)
	if err != nil || permit == nil || permit.JobID != jobID {
		t.Fatalf("identity permit=%+v err=%v", permit, err)
	}
	identity := Identity{ApplicationID: "900000000000000001", BotUserID: "900000000000000002", Username: "same-bot"}
	if err = store.CompleteIdentity(ctx, left.ID, identity, *permit); err != nil {
		t.Fatal(err)
	}
	right, err = store.GetConnection(ctx, right.ID)
	if err != nil {
		t.Fatal(err)
	}
	rightJobID, err := queue.Enqueue(ctx, connectionCommand("validate", "discord:identity-race-right", right))
	if err != nil {
		t.Fatal(err)
	}
	rightPermit, err := queue.Claim(ctx, "discord-identity-race-right", time.Minute)
	if err != nil || rightPermit == nil || rightPermit.JobID != rightJobID {
		t.Fatalf("right identity permit=%+v err=%v", rightPermit, err)
	}
	if err = store.CompleteIdentity(ctx, right.ID, identity, *rightPermit); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate bot identity error = %v", err)
	}
	rightRefreshJobID, err := queue.Enqueue(ctx, connectionCommand("refresh", "discord:refresh-race-right", right))
	if err != nil {
		t.Fatal(err)
	}
	rightRefreshPermit, err := queue.Claim(ctx, "discord-refresh-race-right", time.Minute)
	if err != nil || rightRefreshPermit == nil || rightRefreshPermit.JobID != rightRefreshJobID {
		t.Fatalf("right refresh permit=%+v err=%v", rightRefreshPermit, err)
	}
	if err = store.CompleteRefresh(ctx, right.ID, identity, nil, *rightRefreshPermit); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate bot refresh identity error = %v", err)
	}
	var identityCount int
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FROM discord_connections
		WHERE application_id=$1 OR bot_user_id=$2
	`, string(identity.ApplicationID), string(identity.BotUserID)).Scan(&identityCount); err != nil {
		t.Fatal(err)
	}
	if identityCount != 1 {
		t.Fatalf("Discord identity rows = %d", identityCount)
	}
}

func TestStorePostgreSQLControlPlaneIdempotencyAndRatePersistence(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDiscordTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		TRUNCATE credentials, operators, knowledge_bases, idempotency_records,
		         audit_events, event_log CASCADE
	`); err != nil {
		t.Fatal(err)
	}

	vault, err := security.NewCredentialVault(
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.OperatorID(randomBytes16(t))
	credentialID := credentials.ID(randomBytes16(t))
	knowledgeBaseID := knowledgebases.ID(randomBytes16(t))
	secret, err := security.NewSecretValue("discord-control-secret-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := vault.Encrypt(
		security.CredentialID(credentialID), security.CredentialDiscordBotToken, 1, secret,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Discord Operator','discord operator','unused')
	`, pgDiscordUUID([16]byte(actor))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES($1,'DISCORD_BOT_TOKEN','Discord','••••',$2,$3,$4,1)
	`, pgDiscordUUID([16]byte(credentialID)), envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext()); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language)
		VALUES($1,'Discord KB','discord kb','RESTRICTED','ACTIVE','','en')
	`, pgDiscordUUID([16]byte(knowledgeBaseID))); err != nil {
		t.Fatal(err)
	}
	agentID := seedDiscordAgent(t, ctx, fixture, [16]byte(knowledgeBaseID))
	if err = fixture.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateConnection(ctx, CreateConnection{
		DisplayName: "Docs bot", CredentialID: credentialID,
	}, actor, "connection-create")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.CreateConnection(ctx, CreateConnection{
		DisplayName: "Docs bot", CredentialID: credentialID,
	}, actor, "connection-create")
	if err != nil || replayed.ID != created.ID || replayed.CredentialVersion != 1 || replayed.Version != 1 {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	if _, err = store.CreateConnection(ctx, CreateConnection{
		DisplayName: "Different bot", CredentialID: credentialID,
	}, actor, "connection-create"); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("idempotency conflict=%v", err)
	}
	connections, err := store.ListConnections(ctx)
	if err != nil || len(connections) != 1 || connections[0].ID != created.ID {
		t.Fatalf("connections=%+v err=%v", connections, err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO discord_servers(connection_id,server_id,name,owner,refreshed_at)
		VALUES($1,'100','Guild',false,clock_timestamp())
	`, pgDiscordUUID([16]byte(created.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO discord_channels(
			connection_id,server_id,channel_id,name,channel_type,position,
			effective_bot_permissions,everyone_can_view,refreshed_at
		) VALUES($1,'100','200','docs',0,0,0,true,clock_timestamp())
	`, pgDiscordUUID([16]byte(created.ID))); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.RequestConnectionValidation(ctx, created.ID, created.Version, actor, "connection-validate")
	if err != nil {
		t.Fatal(err)
	}
	job, err := jobs.NewStore(pool, nil).Get(ctx, jobID)
	if err != nil || job.Type != jobs.RefreshDiscord || job.TargetID != jobs.UUID(created.ID) {
		t.Fatalf("job=%+v err=%v", job, err)
	}

	config := BindingConfiguration{
		ConnectionID: created.ID, ServerID: "100", ListenChannelID: "200",
		AgentID: agentID, Triggers: []TriggerType{TriggerMention, TriggerSlashCommand},
		ReplyPolicy: ReplySameChannel, AllowedRoleIDs: []Snowflake{"300"},
		AllowedUserIDs: []Snowflake{}, RatePolicy: RatePolicy{Requests: 3, WindowSeconds: 60},
	}
	binding, err := store.CreateBinding(ctx, CreateBinding{Configuration: config}, actor, "binding-create")
	if err != nil || binding.Health != BindingDraft || binding.Enabled {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	edited, err := store.UpdateBinding(ctx, UpdateBinding{
		BindingID: binding.ID, ExpectedVersion: binding.Version,
		Configuration: config, Enabled: false,
	}, actor, "binding-update")
	if err != nil || edited.Version != binding.Version+1 || edited.Health != BindingDraft {
		t.Fatalf("edited=%+v err=%v", edited, err)
	}
	userID := Snowflake("400")
	for attempt := 1; attempt <= 4; attempt++ {
		allowed, rateErr := store.ConsumeRate(ctx, edited, userID)
		if rateErr != nil || allowed != (attempt <= 3) {
			t.Fatalf("rate attempt %d allowed=%v err=%v", attempt, allowed, rateErr)
		}
	}
	restarted, err := NewStore(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, rateErr := restarted.ConsumeRate(ctx, edited, userID); rateErr != nil || allowed {
		t.Fatalf("restarted rate allowed=%v err=%v", allowed, rateErr)
	}
	var bindingEventsBeforeDisable, bindingAuditsBeforeDisable int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE resource_type='discord_binding' AND resource_id=$1`,
		pgDiscordUUID([16]byte(edited.ID))).Scan(&bindingEventsBeforeDisable); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_type='discord_binding' AND target_id=$1`,
		pgDiscordUUID([16]byte(edited.ID))).Scan(&bindingAuditsBeforeDisable); err != nil {
		t.Fatal(err)
	}
	disabled, err := store.UpdateConnection(ctx, UpdateConnection{
		ConnectionID: created.ID, ExpectedVersion: created.Version,
		DisplayName: created.DisplayName, Lifecycle: ConnectionDisabled,
	}, actor, "connection-disable")
	if err != nil || disabled.Lifecycle != ConnectionDisabled || disabled.State != StateDisabled || disabled.Version != 2 {
		t.Fatalf("disabled=%+v err=%v", disabled, err)
	}
	preserved, err := store.GetBinding(ctx, edited.ID)
	if err != nil || preserved.Version != edited.Version || preserved.Enabled != edited.Enabled {
		t.Fatalf("connection disable changed binding intent: before=%+v after=%+v err=%v", edited, preserved, err)
	}
	var bindingEventsAfterDisable, bindingAuditsAfterDisable int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE resource_type='discord_binding' AND resource_id=$1`,
		pgDiscordUUID([16]byte(edited.ID))).Scan(&bindingEventsAfterDisable); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE target_type='discord_binding' AND target_id=$1`,
		pgDiscordUUID([16]byte(edited.ID))).Scan(&bindingAuditsAfterDisable); err != nil {
		t.Fatal(err)
	}
	if bindingEventsAfterDisable != bindingEventsBeforeDisable || bindingAuditsAfterDisable != bindingAuditsBeforeDisable {
		t.Fatalf("connection disable fabricated binding events %d->%d audits %d->%d",
			bindingEventsBeforeDisable, bindingEventsAfterDisable, bindingAuditsBeforeDisable, bindingAuditsAfterDisable)
	}
	if err = store.DeleteBinding(ctx, edited.ID, edited.Version, actor, "binding-delete"); err != nil {
		t.Fatal(err)
	}
	if bindings, listErr := store.ListBindings(ctx); listErr != nil || len(bindings) != 0 {
		t.Fatalf("bindings=%+v err=%v", bindings, listErr)
	}
	for _, query := range []string{
		`SELECT snapshot FROM event_log WHERE event_type='discord.binding.deleted' ORDER BY sequence DESC LIMIT 1`,
		`SELECT details FROM audit_events WHERE action='discord.binding.delete' ORDER BY created_at DESC LIMIT 1`,
	} {
		var snapshot []byte
		if err = pool.QueryRow(ctx, query).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		var tombstone map[string]any
		if err = json.Unmarshal(snapshot, &tombstone); err != nil {
			t.Fatal(err)
		}
		if tombstone["enabled"] != false || tombstone["health"] != "DRAFT" ||
			tombstone["version"] != float64(edited.Version+1) {
			t.Fatalf("delete tombstone = %#v", tombstone)
		}
	}
	var auditCount, eventCount int
	var persisted string
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM event_log`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `
		SELECT coalesce(string_agg(details::text, ' '), '') FROM audit_events
	`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if auditCount != 6 || eventCount < auditCount || strings.Contains(persisted, secret.Reveal()) {
		t.Fatalf("audit=%d events=%d persisted=%q", auditCount, eventCount, persisted)
	}
}

func TestStorePostgreSQLDiscordExecutionIsPermitFencedAndAudienceSafe(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDiscordTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		TRUNCATE credentials, knowledge_bases, jobs, audit_events, event_log CASCADE
	`); err != nil {
		t.Fatal(err)
	}

	vault, err := security.NewCredentialVault(
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	credentialID := credentials.ID(randomBytes16(t))
	connectionID := ConnectionID(randomBytes16(t))
	knowledgeBaseID := knowledgebases.ID(randomBytes16(t))
	bindingID := BindingID(randomBytes16(t))
	secret, _ := security.NewSecretValue("discord-execution-secret-sentinel")
	envelope, err := vault.Encrypt(
		security.CredentialID(credentialID), security.CredentialDiscordBotToken, 1, secret,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fixture.Rollback(ctx) }()
	if _, err = fixture.Exec(ctx, `
		INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES($1,'DISCORD_BOT_TOKEN','Discord','••••',$2,$3,$4,1)
	`, pgDiscordUUID([16]byte(credentialID)), envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext()); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO discord_connections(
			id,display_name,display_key,credential_id,credential_version,lifecycle,state
		) VALUES($1,'Docs bot','docs bot',$2,1,'ENABLED','CONNECTING')
	`, pgDiscordUUID([16]byte(connectionID)), pgDiscordUUID([16]byte(credentialID))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language)
		VALUES($1,'Private docs','private docs','RESTRICTED','ACTIVE','','en')
	`, pgDiscordUUID([16]byte(knowledgeBaseID))); err != nil {
		t.Fatal(err)
	}
	agentID := seedDiscordAgent(t, ctx, fixture, [16]byte(knowledgeBaseID))
	if err = fixture.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewStore(pool, nil)
	command := jobs.Command{
		Type: jobs.RefreshDiscord, TargetType: "discord_connection", TargetID: jobs.UUID(connectionID),
		Payload: map[string]any{
			"action": "refresh", "connection_id": connectionID.String(), "connection_version": int32(1),
			"credential_id": credentialID.String(), "credential_version": int32(1),
		},
		OperationKey: "discord:execution:live",
	}
	jobID, err := queue.Enqueue(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := queue.Claim(ctx, "discord-live-worker", time.Minute)
	if err != nil || permit == nil || permit.JobID != jobID {
		t.Fatalf("permit=%+v err=%v", permit, err)
	}

	documentationRunID := randomBytes16(t)
	wikiVersionID := randomBytes16(t)
	fixture, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO documentation_runs(
			id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,
			instructions,language,completed_at
		) VALUES($1,$2,'PUBLISHED',$3,1,'','en',clock_timestamp())
	`, pgDiscordUUID(documentationRunID), pgDiscordUUID([16]byte(knowledgeBaseID)), pgDiscordUUID([16]byte(jobID))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO wiki_versions(
			id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count
		) VALUES($1,$2,$3,'knowledge-bases/live/wiki',decode(repeat('ab',32),'hex'),1)
	`, pgDiscordUUID(wikiVersionID), pgDiscordUUID([16]byte(knowledgeBaseID)), pgDiscordUUID(documentationRunID)); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `UPDATE documentation_runs SET published_wiki_version_id=$2 WHERE id=$1`,
		pgDiscordUUID(documentationRunID), pgDiscordUUID(wikiVersionID)); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `UPDATE knowledge_bases SET published_wiki_id=$2 WHERE id=$1`,
		pgDiscordUUID([16]byte(knowledgeBaseID)), pgDiscordUUID(wikiVersionID)); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO discord_servers(connection_id,server_id,name,owner,refreshed_at)
		VALUES($1,'100','Docs',false,clock_timestamp())
	`, pgDiscordUUID([16]byte(connectionID))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO discord_channels(
			connection_id,server_id,channel_id,name,channel_type,position,
			effective_bot_permissions,everyone_can_view,viewer_role_ids,viewer_user_ids,refreshed_at
		) VALUES($1,'100','500','private-docs',0,0,$2,false,'["300"]','[]',clock_timestamp())
	`, pgDiscordUUID([16]byte(connectionID)), int64(BasePermissions)); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO discord_roles(connection_id,server_id,role_id,name,position,refreshed_at)
		VALUES($1,'100','300','Readers',1,clock_timestamp())
	`, pgDiscordUUID([16]byte(connectionID))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO channel_bindings(
			id,connection_id,server_id,listen_channel_id,agent_id,
			reply_policy,allowed_role_ids,allowed_user_ids,
			rate_requests,rate_window_seconds,enabled,health,validated_at
		) VALUES($1,$2,'100','500',$3,'SAME_CHANNEL','["300"]','[]',5,60,true,'HEALTHY',clock_timestamp())
	`, pgDiscordUUID([16]byte(bindingID)), pgDiscordUUID([16]byte(connectionID)),
		pgDiscordUUID([16]byte(agentID))); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec(ctx, `
		INSERT INTO channel_binding_triggers(
			binding_id,connection_id,server_id,listen_channel_id,enabled,trigger_type
		) VALUES($1,$2,'100','500',true,'MENTION'),($1,$2,'100','500',true,'SLASH_COMMAND')
	`, pgDiscordUUID([16]byte(bindingID)), pgDiscordUUID([16]byte(connectionID))); err != nil {
		t.Fatal(err)
	}
	if err = fixture.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	connection, binding, err := store.AssertExecution(ctx, command, *permit)
	if err != nil || connection.ID != connectionID || binding != nil {
		t.Fatalf("connection=%+v binding=%+v err=%v", connection, binding, err)
	}
	identity := Identity{ApplicationID: "800", BotUserID: "801", Username: "ref0"}
	snapshot := ServerSnapshot{
		Server: ServerMetadata{ID: "100", Name: "Docs"},
		Channels: []ChannelMetadata{{
			ID: "500", ServerID: "100", Name: "private-docs", ChannelType: 0,
			EffectiveBotPermissions: BasePermissions, EveryoneCanView: false,
			ViewerRoleIDs: []Snowflake{"300"},
		}},
		Roles: []RoleMetadata{{ID: "300", Name: "Readers", Position: 1}},
	}
	if err = store.CompleteRefresh(ctx, connectionID, identity, []ServerSnapshot{snapshot}, *permit); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var health string
	var bindingVersion int32
	var viewerRoles string
	if err = pool.QueryRow(ctx, `
		SELECT cb.enabled,cb.health,cb.version,dc.viewer_role_ids::text
		FROM channel_bindings cb
		JOIN discord_channels dc ON dc.connection_id=cb.connection_id
		 AND dc.server_id=cb.server_id AND dc.channel_id=cb.listen_channel_id
		WHERE cb.id=$1
	`, pgDiscordUUID([16]byte(bindingID))).Scan(&enabled, &health, &bindingVersion, &viewerRoles); err != nil {
		t.Fatal(err)
	}
	if !enabled || health != "HEALTHY" || bindingVersion != 1 || viewerRoles != `["300"]` {
		t.Fatalf("enabled=%v health=%s version=%d audience=%s", enabled, health, bindingVersion, viewerRoles)
	}
	var connectionEvents, directoryEvents int
	if err = pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE event_type='discord.connection.updated'),
			count(*) FILTER (WHERE event_type='discord.directory.refreshed')
		FROM event_log
		WHERE resource_id=$1 AND snapshot->>'id'=$2
	`, pgDiscordUUID([16]byte(connectionID)), connectionID.String()).Scan(&connectionEvents, &directoryEvents); err != nil {
		t.Fatal(err)
	}
	if connectionEvents != 1 || directoryEvents != 1 {
		t.Fatalf("refresh events connection=%d directory=%d", connectionEvents, directoryEvents)
	}
	if err = store.FailExecution(ctx, connectionID, "Discord execution failed.", *permit, nil); err != nil {
		t.Fatal(err)
	}
	var failureEvents int
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FROM event_log
		WHERE event_type='discord.connection.state_changed'
		  AND resource_id=$1 AND snapshot->>'state'='DEGRADED'
	`, pgDiscordUUID([16]byte(connectionID))).Scan(&failureEvents); err != nil {
		t.Fatal(err)
	}
	if failureEvents != 1 {
		t.Fatalf("execution failure events = %d", failureEvents)
	}
	versionBumpedBindingID := BindingID(randomBytes16(t))
	postSnapshotBindingID := BindingID(randomBytes16(t))
	if _, err = pool.Exec(ctx, `
		INSERT INTO discord_channels(
			connection_id,server_id,channel_id,name,channel_type,position,
			effective_bot_permissions,everyone_can_view,viewer_role_ids,viewer_user_ids,refreshed_at
		) VALUES
			($1,'100','501','captured-binding',0,1,$2,false,'["300"]','[]',clock_timestamp()),
			($1,'100','502','post-snapshot-binding',0,2,$2,false,'["300"]','[]',clock_timestamp());
		INSERT INTO channel_bindings(
			id,connection_id,server_id,listen_channel_id,agent_id,reply_policy,
			allowed_role_ids,allowed_user_ids,rate_requests,rate_window_seconds,
			enabled,health,validated_at
		) VALUES($3,$1,'100','501',$4,'SAME_CHANNEL','["300"]','[]',5,60,true,'HEALTHY',clock_timestamp());
		INSERT INTO channel_binding_triggers(
			binding_id,connection_id,server_id,listen_channel_id,enabled,trigger_type
		) VALUES($3,$1,'100','501',true,'SLASH_COMMAND')
		`, pgx.QueryExecModeSimpleProtocol, pgDiscordUUID([16]byte(connectionID)), int64(BasePermissions),
		pgDiscordUUID([16]byte(versionBumpedBindingID)), pgDiscordUUID([16]byte(agentID))); err != nil {
		t.Fatal(err)
	}
	registrationCaptures := []BindingCapture{
		{ID: bindingID, Version: 1},
		{ID: versionBumpedBindingID, Version: 1},
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO channel_bindings(
			id,connection_id,server_id,listen_channel_id,agent_id,reply_policy,
			allowed_role_ids,allowed_user_ids,rate_requests,rate_window_seconds,
			enabled,health,validated_at
		) VALUES($1,$2,'100','502',$3,'SAME_CHANNEL','["300"]','[]',5,60,true,'HEALTHY',clock_timestamp());
		INSERT INTO channel_binding_triggers(
			binding_id,connection_id,server_id,listen_channel_id,enabled,trigger_type
		) VALUES($1,$2,'100','502',true,'SLASH_COMMAND');
		UPDATE channel_bindings SET version=version+1 WHERE id=$4
		`, pgx.QueryExecModeSimpleProtocol, pgDiscordUUID([16]byte(postSnapshotBindingID)),
		pgDiscordUUID([16]byte(connectionID)), pgDiscordUUID([16]byte(agentID)),
		pgDiscordUUID([16]byte(versionBumpedBindingID))); err != nil {
		t.Fatal(err)
	}
	if err = store.FailCommandRegistration(ctx, connectionID, "100", *permit, nil, registrationCaptures); err != nil {
		t.Fatal(err)
	}
	var bumpedHealth, createdHealth string
	var bumpedVersion, createdVersion int32
	if err = pool.QueryRow(ctx, `SELECT health,version FROM channel_bindings WHERE id=$1`,
		pgDiscordUUID([16]byte(versionBumpedBindingID))).Scan(&bumpedHealth, &bumpedVersion); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT health,version FROM channel_bindings WHERE id=$1`,
		pgDiscordUUID([16]byte(postSnapshotBindingID))).Scan(&createdHealth, &createdVersion); err != nil {
		t.Fatal(err)
	}
	if bumpedHealth != "HEALTHY" || bumpedVersion != 2 || createdHealth != "HEALTHY" || createdVersion != 1 {
		t.Fatalf("stale registration captures mutated edited/new bindings: bumped=%s/%d created=%s/%d",
			bumpedHealth, bumpedVersion, createdHealth, createdVersion)
	}
	var registrationEvents int
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FROM event_log
		WHERE event_type='discord.binding.unhealthy' AND resource_id=$1
		  AND snapshot->>'id'=$2
	`, pgDiscordUUID([16]byte(bindingID)), bindingID.String()).Scan(&registrationEvents); err != nil {
		t.Fatal(err)
	}
	if registrationEvents != 1 {
		t.Fatalf("registration events = %d", registrationEvents)
	}

	stale := *permit
	stale.LeaseGeneration++
	if err = store.CompleteIdentity(ctx, connectionID,
		Identity{ApplicationID: "900", BotUserID: "901", Username: "stale"}, stale,
	); !errors.Is(err, jobs.ErrStalePermit) {
		t.Fatalf("stale identity err = %v", err)
	}
	var username string
	if err = pool.QueryRow(ctx, "SELECT bot_username FROM discord_connections WHERE id=$1",
		pgDiscordUUID([16]byte(connectionID))).Scan(&username); err != nil || username != "ref0" {
		t.Fatalf("username=%q err=%v", username, err)
	}
	if _, _, err = store.AssertExecution(ctx, command, *permit); err != nil {
		t.Fatalf("pre-mutation execution assertion = %v", err)
	}
	var staleConnectionEvents, staleRegistrationEvents int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE resource_id=$1`,
		pgDiscordUUID([16]byte(connectionID))).Scan(&staleConnectionEvents); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE resource_id=$1`,
		pgDiscordUUID([16]byte(bindingID))).Scan(&staleRegistrationEvents); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE discord_connections SET version=version+1 WHERE id=$1", pgDiscordUUID([16]byte(connectionID))); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.AssertExecution(ctx, command, *permit); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale capture err = %v", err)
	}
	if err = store.CompleteRefresh(ctx, connectionID,
		Identity{ApplicationID: "910", BotUserID: "911", Username: "stale-refresh"}, nil, *permit,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale refresh completion err = %v", err)
	}
	if err = store.FailExecution(ctx, connectionID, "stale failure", *permit, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale execution failure err = %v", err)
	}
	if err = store.FailCommandRegistration(ctx, connectionID, "100", *permit, nil, registrationCaptures); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale registration failure err = %v", err)
	}
	var staleUsername string
	var unchangedBindingVersion int32
	var unchangedConnectionEvents, unchangedRegistrationEvents int
	if err = pool.QueryRow(ctx, `SELECT bot_username FROM discord_connections WHERE id=$1`,
		pgDiscordUUID([16]byte(connectionID))).Scan(&staleUsername); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT version FROM channel_bindings WHERE id=$1`,
		pgDiscordUUID([16]byte(bindingID))).Scan(&unchangedBindingVersion); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE resource_id=$1`,
		pgDiscordUUID([16]byte(connectionID))).Scan(&unchangedConnectionEvents); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM event_log WHERE resource_id=$1`,
		pgDiscordUUID([16]byte(bindingID))).Scan(&unchangedRegistrationEvents); err != nil {
		t.Fatal(err)
	}
	if staleUsername != "ref0" || unchangedBindingVersion != 2 ||
		unchangedConnectionEvents != staleConnectionEvents || unchangedRegistrationEvents != staleRegistrationEvents {
		t.Fatalf("stale completion mutated state: username=%q binding_version=%d events=%d/%d want=%d/%d",
			staleUsername, unchangedBindingVersion, unchangedConnectionEvents, unchangedRegistrationEvents,
			staleConnectionEvents, staleRegistrationEvents)
	}

	if _, err = pool.Exec(ctx, `UPDATE discord_connections SET version=1 WHERE id=$1`,
		pgDiscordUUID([16]byte(connectionID))); err != nil {
		t.Fatal(err)
	}
	validateCommand := jobs.Command{
		Type: jobs.RefreshDiscord, TargetType: "discord_connection", TargetID: jobs.UUID(connectionID),
		Payload: map[string]any{
			"action": "validate", "connection_id": connectionID.String(), "connection_version": int32(1),
			"credential_id": credentialID.String(), "credential_version": int32(1),
		},
		OperationKey: "discord:execution:validate-capture",
	}
	validateJobID, enqueueErr := queue.Enqueue(ctx, validateCommand)
	if enqueueErr != nil {
		t.Fatal(enqueueErr)
	}
	validatePermit, claimErr := queue.Claim(ctx, "discord-validate-worker", time.Minute)
	if claimErr != nil || validatePermit == nil || validatePermit.JobID != validateJobID {
		t.Fatalf("validate permit=%+v err=%v", validatePermit, claimErr)
	}
	if _, _, err = store.AssertExecution(ctx, validateCommand, *validatePermit); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE discord_connections SET credential_version=2 WHERE id=$1`,
		pgDiscordUUID([16]byte(connectionID))); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteIdentity(ctx, connectionID,
		Identity{ApplicationID: "920", BotUserID: "921", Username: "stale-identity"}, *validatePermit,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale identity completion err = %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT bot_username FROM discord_connections WHERE id=$1`,
		pgDiscordUUID([16]byte(connectionID))).Scan(&staleUsername); err != nil || staleUsername != "ref0" {
		t.Fatalf("stale identity username=%q err=%v", staleUsername, err)
	}

	if _, err = pool.Exec(ctx, `UPDATE discord_connections SET credential_version=1 WHERE id=$1`,
		pgDiscordUUID([16]byte(connectionID))); err != nil {
		t.Fatal(err)
	}
	bindingCommand := jobs.Command{
		Type: jobs.RefreshDiscord, TargetType: "discord_binding", TargetID: jobs.UUID(bindingID),
		Payload: map[string]any{
			"action": "register_command", "binding_id": bindingID.String(), "binding_version": unchangedBindingVersion,
			"connection_id": connectionID.String(), "connection_version": int32(1),
			"credential_id": credentialID.String(), "credential_version": int32(1),
		},
		OperationKey: "discord:execution:binding-capture",
	}
	bindingJobID, enqueueErr := queue.Enqueue(ctx, bindingCommand)
	if enqueueErr != nil {
		t.Fatal(enqueueErr)
	}
	bindingPermit, claimErr := queue.Claim(ctx, "discord-binding-worker", time.Minute)
	if claimErr != nil || bindingPermit == nil || bindingPermit.JobID != bindingJobID {
		t.Fatalf("binding permit=%+v err=%v", bindingPermit, claimErr)
	}
	if _, _, err = store.AssertExecution(ctx, bindingCommand, *bindingPermit); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE channel_bindings SET version=version+1 WHERE id=$1`,
		pgDiscordUUID([16]byte(bindingID))); err != nil {
		t.Fatal(err)
	}
	if err = store.FailExecution(ctx, connectionID, "stale binding failure", *bindingPermit, &bindingID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale binding failure err = %v", err)
	}
	if err = store.FailCommandRegistration(ctx, connectionID, "100", *bindingPermit, &bindingID,
		[]BindingCapture{{ID: bindingID, Version: unchangedBindingVersion}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale binding registration err = %v", err)
	}
	otherCredentialID := credentials.ID(randomBytes16(t))
	otherConnectionID := ConnectionID(randomBytes16(t))
	if _, err = pool.Exec(ctx, `
		INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES($1,'DISCORD_BOT_TOKEN','Other Discord','••••',$2,$3,$4,1)
	`, pgDiscordUUID([16]byte(otherCredentialID)), envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext()); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO discord_connections(
			id,display_name,display_key,credential_id,credential_version,lifecycle,state
		) VALUES($1,'Other bot','other bot',$2,1,'ENABLED','CONNECTING')
	`, pgDiscordUUID([16]byte(otherConnectionID)), pgDiscordUUID([16]byte(otherCredentialID))); err != nil {
		t.Fatal(err)
	}
	mismatchedCommand := bindingCommand
	mismatchedCommand.Payload = map[string]any{}
	for key, value := range bindingCommand.Payload {
		mismatchedCommand.Payload[key] = value
	}
	mismatchedCommand.Payload["connection_id"] = otherConnectionID.String()
	mismatchedCommand.Payload["credential_id"] = otherCredentialID.String()
	mismatchedCommand.Payload["binding_version"] = unchangedBindingVersion + 1
	captureTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _, captureErr := assertExecutionCapture(ctx, captureTx, mismatchedCommand)
	_ = captureTx.Rollback(ctx)
	if captureErr == nil || !strings.Contains(captureErr.Error(), "capture is invalid") {
		t.Fatalf("mismatched connection/binding capture error = %v", captureErr)
	}
	var persisted string
	if err = pool.QueryRow(ctx, `
		SELECT coalesce(string_agg(coalesce(snapshot::text,'') || coalesce(details::text,''), ' '), '')
		FROM event_log FULL JOIN audit_events ON false
	`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, secret.Reveal()) {
		t.Fatal("Discord execution persisted its token")
	}
}

func migrateDiscordTestDatabase(t *testing.T, ctx context.Context, databaseURL string) {
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

func randomBytes16(t *testing.T) [16]byte {
	t.Helper()
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return value
}

func seedDiscordAgent(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	knowledgeBaseID [16]byte,
) agents.AgentID {
	t.Helper()
	actorID := randomBytes16(t)
	endpointID := randomBytes16(t)
	profileID := randomBytes16(t)
	profileVersionID := randomBytes16(t)
	agentID := agents.AgentID(randomBytes16(t))
	agentVersionID := agents.VersionID(randomBytes16(t))
	unique := strings.ReplaceAll(jobs.UUID(actorID).String(), "-", "")
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO operators(id,username,username_key,password_hash) VALUES($1,$2,$2,'unused')`, []any{pgDiscordUUID(actorID), "agent-" + unique}},
		{`INSERT INTO provider_endpoints(id,display_name,display_key,base_url,lifecycle,health,health_checked_at) VALUES($1,$2,$2,'https://models.example.test','ACTIVE','HEALTHY',clock_timestamp())`, []any{pgDiscordUUID(endpointID), "endpoint-" + unique}},
		{`INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id) VALUES($1,$2,'discord-model','AVAILABLE',$3)`, []any{pgDiscordUUID(profileID), pgDiscordUUID(endpointID), pgDiscordUUID(profileVersionID)}},
		{`
			INSERT INTO model_profile_versions(
				id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,
				supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,
				timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id
			) VALUES($1,$2,1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,'NONE',30,0,1,'{}','{}','OPERATOR',$3)
		`, []any{pgDiscordUUID(profileVersionID), pgDiscordUUID(profileID), pgDiscordUUID(actorID)}},
		{`INSERT INTO agents(id,agent_key,lifecycle,current_version_id,activated_at) VALUES($1,$2,'ACTIVE',$3,clock_timestamp())`, []any{pgDiscordUUID([16]byte(agentID)), "discord-" + unique, pgDiscordUUID([16]byte(agentVersionID))}},
		{`
			INSERT INTO agent_versions(
				id,agent_id,version_number,display_name,response_language,identity_instructions,model_profile_id,
				reasoning_effort,answer_mode,evidence_access,refusal_markdown,max_tool_calls,max_answer_tokens,created_by_operator_id
			) VALUES($1,$2,1,'Discord Agent','en','Answer from the selected documentation.',$3,'NONE','SINGLE_PASS','WIKI_ONLY','Cannot answer.',0,1024,$4)
		`, []any{pgDiscordUUID([16]byte(agentVersionID)), pgDiscordUUID([16]byte(agentID)), pgDiscordUUID(profileID), pgDiscordUUID(actorID)}},
		{`INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id) VALUES($1,$2,0,$3)`, []any{pgDiscordUUID([16]byte(agentID)), pgDiscordUUID([16]byte(agentVersionID)), pgDiscordUUID(knowledgeBaseID)}},
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	return agentID
}
