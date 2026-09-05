package credentials

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const rotatedOracleKey = "v2:MjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjI"

func TestCredentialPostgresPersistenceRotationAndExactLease(t *testing.T) {
	pool := postgresCredentialPool(t)
	ctx := context.Background()
	firstVault, err := security.NewCredentialVault(oracleKey, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewService(pool, firstVault)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.OperatorID{1}
	command := CreateCommand{
		Kind: ProviderAPIKey, Label: "Provider key",
		Secret: credentialSecret(t, "provider-secret-one"),
	}
	created, err := first.Create(ctx, command, actor, "credential-create")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.SecretVersion != 1 || created.KeyID != "v1" || created.MaskedValue != MaskedValue {
		t.Fatalf("created = %#v", created)
	}
	replay, err := first.Create(ctx, command, actor, "credential-create")
	if err != nil || replay != created {
		t.Fatalf("create replay = %#v, %v", replay, err)
	}
	changed := command
	changed.Secret = credentialSecret(t, "different-provider-secret")
	if _, err = first.Create(ctx, changed, actor, "credential-create"); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("changed replay error = %v", err)
	}

	rotatedVault, err := security.NewCredentialVault(rotatedOracleKey, oracleKey)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewService(pool, rotatedVault)
	if err != nil {
		t.Fatal(err)
	}
	previousKeyReplay, err := second.Create(ctx, command, actor, "credential-create")
	if err != nil || previousKeyReplay != created {
		t.Fatalf("previous-key replay = %#v, %v", previousKeyReplay, err)
	}
	reader, err := NewSecretReader(pool, rotatedVault)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := reader.Read(ctx, created.ID, ProviderAPIKey, 1)
	if err != nil || leased.Reveal() != "provider-secret-one" {
		t.Fatalf("version-one lease = %v, %v", leased, err)
	}
	rotated, err := second.Rotate(ctx, RotateCommand{
		CredentialID: created.ID, Secret: credentialSecret(t, "provider-secret-two"),
	}, actor, "credential-rotate")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.SecretVersion != 2 || rotated.KeyID != "v2" || rotated.RotatedAt == nil {
		t.Fatalf("rotated = %#v", rotated)
	}
	if _, err = reader.Read(ctx, created.ID, ProviderAPIKey, 1); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("stale version lease error = %v", err)
	}
	if _, err = reader.Read(ctx, created.ID, RepositoryHTTPS, 2); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("wrong-kind lease error = %v", err)
	}
	leased, err = reader.Read(ctx, created.ID, ProviderAPIKey, 2)
	if err != nil || leased.Reveal() != "provider-secret-two" {
		t.Fatalf("version-two lease = %v, %v", leased, err)
	}
	if _, err = first.Create(ctx, command, actor, "credential-create"); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("stale create replay error = %v", err)
	}
	if _, err = second.Rotate(ctx, RotateCommand{
		CredentialID: created.ID, Secret: credentialSecret(t, "provider-secret-two"),
	}, actor, "credential-rotate"); err != nil {
		t.Fatalf("rotation replay: %v", err)
	}

	var ciphertext []byte
	var credentialCount, rotationCount, auditCount, eventCount, idempotencyCount int
	err = pool.QueryRow(ctx, `
		SELECT ciphertext,
		       (SELECT count(*) FROM credentials),
		       (SELECT count(*) FROM credential_rotation_attempts),
		       (SELECT count(*) FROM audit_events),
		       (SELECT count(*) FROM event_log),
		       (SELECT count(*) FROM idempotency_records)
		FROM credentials
	`).Scan(&ciphertext, &credentialCount, &rotationCount, &auditCount, &eventCount, &idempotencyCount)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("provider-secret")) ||
		credentialCount != 1 || rotationCount != 1 || auditCount != 2 || eventCount != 2 || idempotencyCount != 2 {
		t.Fatalf("stored counts=%d/%d/%d/%d/%d ciphertext=%x", credentialCount, rotationCount, auditCount, eventCount, idempotencyCount, ciphertext)
	}
	if _, err = pool.Exec(ctx, `UPDATE credentials SET deleted_at = clock_timestamp() WHERE id = $1`, pgUUID(created.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = second.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted metadata error = %v", err)
	}
	if _, err = reader.Read(ctx, created.ID, ProviderAPIKey, 2); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("deleted lease error = %v", err)
	}
}

func TestCredentialPostgresConcurrentRotationsSerializeVersions(t *testing.T) {
	pool := postgresCredentialPool(t)
	ctx := context.Background()
	vault, err := security.NewCredentialVault(oracleKey, "")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.OperatorID{2}
	created, err := service.Create(ctx, CreateCommand{
		Kind: ProviderAPIKey, Label: "Concurrent key", Secret: credentialSecret(t, "initial-secret"),
	}, actor, "create")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	versions := make(chan int32, 2)
	errorsFound := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index, value := range []string{"rotation-secret-a", "rotation-secret-b"} {
		index, value := index, value
		go func() {
			ready.Done()
			<-start
			metadata, rotateErr := service.Rotate(ctx, RotateCommand{
				CredentialID: created.ID, Secret: credentialSecret(t, value),
			}, actor, "rotate-"+string(rune('a'+index)))
			versions <- metadata.SecretVersion
			errorsFound <- rotateErr
		}()
	}
	ready.Wait()
	close(start)
	gotVersions := []int32{<-versions, <-versions}
	for range 2 {
		if err = <-errorsFound; err != nil {
			t.Fatalf("concurrent rotation: %v", err)
		}
	}
	sort.Slice(gotVersions, func(i, j int) bool { return gotVersions[i] < gotVersions[j] })
	if gotVersions[0] != 2 || gotVersions[1] != 3 {
		t.Fatalf("rotation versions = %v", gotVersions)
	}
	rows, err := pool.Query(ctx, `
		SELECT new_secret_version, started_at, finished_at
		FROM credential_rotation_attempts ORDER BY new_secret_version
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var previous time.Time
	for rows.Next() {
		var version int32
		var started, finished time.Time
		if err = rows.Scan(&version, &started, &finished); err != nil {
			t.Fatal(err)
		}
		if finished.Before(started) || (!previous.IsZero() && started.Before(previous)) {
			t.Fatalf("rotation timestamps version=%d started=%s finished=%s previous=%s", version, started, finished, previous)
		}
		previous = started
	}
}

func TestCredentialPostgresEventFailureRollsBackSecretAndIdempotency(t *testing.T) {
	pool := postgresCredentialPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE event_log ADD CONSTRAINT reject_events CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewCredentialVault(oracleKey, "")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	secret := "rollback-secret-sentinel"
	_, err = service.Create(ctx, CreateCommand{
		Kind: ProviderAPIKey, Label: "Rollback", Secret: credentialSecret(t, secret),
	}, auth.OperatorID{3}, "rollback")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("event failure = %v", err)
	}
	for _, table := range []string{"credentials", "credential_rotation_attempts", "audit_events", "event_log", "idempotency_records"} {
		var count int
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func postgresCredentialPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err = admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "credential_test_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
	})
	statements := []string{
		`CREATE TABLE credentials (
			id uuid PRIMARY KEY, kind varchar(32) NOT NULL, label varchar(255) NOT NULL,
			masked_value varchar(64) NOT NULL, key_id varchar(128) NOT NULL,
			nonce bytea NOT NULL CHECK (octet_length(nonce)=12), ciphertext bytea NOT NULL,
			secret_version integer NOT NULL CHECK (secret_version>0), created_at timestamptz NOT NULL,
			rotated_at timestamptz, deleted_at timestamptz
		)`,
		`CREATE TABLE credential_rotation_attempts (
			id uuid PRIMARY KEY, credential_id uuid NOT NULL REFERENCES credentials(id),
			old_secret_version integer NOT NULL, new_secret_version integer NOT NULL,
			new_key_id varchar(128) NOT NULL, status varchar(16) NOT NULL,
			sanitized_error text, actor_operator_id uuid NOT NULL,
			started_at timestamptz NOT NULL, finished_at timestamptz
		)`,
		`CREATE TABLE audit_events (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(), actor_type varchar(32) NOT NULL,
			actor_id uuid, action varchar(128) NOT NULL, target_type varchar(64) NOT NULL,
			target_id uuid, request_id uuid NOT NULL, details jsonb NOT NULL,
			created_at timestamptz NOT NULL
		)`,
		`CREATE TABLE event_log (
			sequence bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			event_type varchar(128) NOT NULL, resource_type varchar(64) NOT NULL,
			resource_id uuid NOT NULL, snapshot jsonb NOT NULL, created_at timestamptz NOT NULL
		)`,
		`CREATE TABLE idempotency_records (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(), scope varchar(255) NOT NULL,
			request_key varchar(255) NOT NULL, operation varchar(128) NOT NULL,
			request_digest bytea NOT NULL CHECK (octet_length(request_digest)=32),
			result_type varchar(64) NOT NULL, result_id uuid NOT NULL,
			created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
			UNIQUE(scope, request_key)
		)`,
	}
	for _, statement := range statements {
		if _, err = pool.Exec(ctx, statement); err != nil {
			t.Fatalf("create credential test schema: %v", err)
		}
	}
	return pool
}
