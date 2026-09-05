package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServicePostgresRotationRestartAndTokenContainment(t *testing.T) {
	pool := postgresAuthPool(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 30, 12, 0, 0, 123456000, time.UTC)
	service := newTestService(t, pool, time.Hour)
	service.clock = func() time.Time { return now }
	oldToken := testSecret(t, "old-bootstrap-token")
	newToken := testSecret(t, "new-bootstrap-token")

	if err := service.InitializeBootstrap(ctx, oldToken, time.Minute); err != nil {
		t.Fatalf("initialize old bootstrap: %v", err)
	}
	original := readBootstrapRow(t, pool)
	now = now.Add(2 * time.Minute)
	if err := service.InitializeBootstrap(ctx, oldToken, 30*time.Minute); err != nil {
		t.Fatalf("reinitialize same bootstrap: %v", err)
	}
	unchanged := readBootstrapRow(t, pool)
	if !bytes.Equal(unchanged.digest, original.digest) ||
		!unchanged.createdAt.Equal(original.createdAt) ||
		!unchanged.expiresAt.Equal(original.expiresAt) {
		t.Fatal("same expired bootstrap token was silently extended")
	}

	if err := service.InitializeBootstrap(ctx, newToken, 30*time.Minute); err != nil {
		t.Fatalf("rotate bootstrap: %v", err)
	}
	replaced := readBootstrapRow(t, pool)
	wantNewDigest := DigestToken(newToken.Reveal())
	if !bytes.Equal(replaced.digest, wantNewDigest[:]) ||
		!replaced.createdAt.Equal(now) ||
		!replaced.expiresAt.Equal(now.Add(30*time.Minute)) {
		t.Fatal("changed unconsumed bootstrap token was not rotated")
	}

	username, err := ParseUsername("  Ｏperator  ")
	if err != nil {
		t.Fatal(err)
	}
	password := testSecret(t, "operator-password-sentinel")
	oldCommand := BootstrapCommand{
		Username: username, Password: password, BootstrapToken: oldToken,
	}
	if _, err = service.Bootstrap(ctx, oldCommand); !errors.Is(err, ErrBootstrapDenied) {
		t.Fatalf("old bootstrap token error = %v", err)
	}
	command := oldCommand
	command.BootstrapToken = newToken
	authenticated, err := service.Bootstrap(ctx, command)
	if err != nil {
		t.Fatalf("bootstrap operator: %v", err)
	}

	var storedPassword string
	var storedSessionDigest []byte
	var storedBootstrapDigest []byte
	if err = pool.QueryRow(ctx, `SELECT password_hash FROM operators`).Scan(&storedPassword); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT token_digest FROM operator_sessions`).Scan(&storedSessionDigest); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT token_digest FROM bootstrap_tokens`).Scan(&storedBootstrapDigest); err != nil {
		t.Fatal(err)
	}
	wantSessionDigest := DigestToken(authenticated.Token.Reveal())
	if !security.VerifyPassword(password.Reveal(), storedPassword) ||
		strings.Contains(storedPassword, password.Reveal()) ||
		!bytes.Equal(storedSessionDigest, wantSessionDigest[:]) ||
		!bytes.Equal(storedBootstrapDigest, wantNewDigest[:]) {
		t.Fatal("database did not contain only password and token digests")
	}

	changedAfterConsumption := testSecret(t, "changed-after-consumption")
	if err = service.InitializeBootstrap(ctx, changedAfterConsumption, time.Hour); err != nil {
		t.Fatalf("restart initialization: %v", err)
	}
	if after := readBootstrapRow(t, pool); !bytes.Equal(after.digest, wantNewDigest[:]) || !after.createdAt.Equal(now) {
		t.Fatal("consumed bootstrap token changed during restart")
	}

	recreated := newTestService(t, pool, time.Hour)
	recreated.clock = func() time.Time { return now }
	session, err := recreated.Authenticate(ctx, authenticated.Token)
	if err != nil || session.Operator.Username != "Operator" {
		t.Fatalf("persisted session authentication = %#v, %v", session, err)
	}
	if err = recreated.Logout(ctx, session.ID); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err = recreated.Authenticate(ctx, authenticated.Token); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("revoked session error = %v", err)
	}

	login, err := recreated.Login(ctx, LoginCommand{
		Username: username,
		Password: password,
	})
	if err != nil {
		t.Fatalf("login after restart: %v", err)
	}
	now = now.Add(2 * time.Hour)
	expiredService := newTestService(t, pool, time.Hour)
	expiredService.clock = func() time.Time { return now }
	if _, err = expiredService.Authenticate(ctx, login.Token); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expired session error = %v", err)
	}
}

func TestServicePostgresBootstrapRaceAndPasswordConcurrencyCap(t *testing.T) {
	pool := postgresAuthPool(t)
	ctx := context.Background()
	service := newTestService(t, pool, time.Hour)
	bootstrapToken := testSecret(t, "bootstrap-token")
	if err := service.InitializeBootstrap(ctx, bootstrapToken, time.Hour); err != nil {
		t.Fatal(err)
	}
	username, err := ParseUsername("Operator")
	if err != nil {
		t.Fatal(err)
	}
	command := BootstrapCommand{
		Username:       username,
		Password:       testSecret(t, "operator-password"),
		BootstrapToken: bootstrapToken,
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, bootstrapErr := service.Bootstrap(ctx, command)
			results <- bootstrapErr
		}()
	}
	close(start)
	var successes, denied int
	for range 2 {
		switch result := <-results; {
		case result == nil:
			successes++
		case errors.Is(result, ErrBootstrapDenied):
			denied++
		default:
			t.Fatalf("bootstrap race error = %v", result)
		}
	}
	if successes != 1 || denied != 1 {
		t.Fatalf("bootstrap race successes=%d denied=%d", successes, denied)
	}
	var operators, sessions int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM operators`).Scan(&operators); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM operator_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if operators != 1 || sessions != 1 {
		t.Fatalf("bootstrap persisted operators=%d sessions=%d", operators, sessions)
	}

	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	var active atomic.Int32
	var maximum atomic.Int32
	service.verifyPassword = func(string, string) bool {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return true
	}
	loginCommand := LoginCommand{Username: username, Password: testSecret(t, "any-password")}
	loginResults := make(chan error, 3)
	for range 3 {
		go func() {
			_, loginErr := service.Login(ctx, loginCommand)
			loginResults <- loginErr
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("two password verifications did not overlap")
		}
	}
	select {
	case <-entered:
		t.Fatal("third password verification exceeded the concurrency cap")
	case <-time.After(100 * time.Millisecond):
	}
	releaseAll()
	for range 3 {
		if err = <-loginResults; err != nil {
			t.Fatalf("concurrent login: %v", err)
		}
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum password concurrency = %d", maximum.Load())
	}

	observedHash := make(chan string, 1)
	service.verifyPassword = func(_ string, encoded string) bool {
		observedHash <- encoded
		return false
	}
	unknown, err := ParseUsername("Nobody")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Login(ctx, LoginCommand{
		Username: unknown,
		Password: testSecret(t, "incorrect"),
	}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("unknown login error = %v", err)
	}
	if encoded := <-observedHash; encoded != dummyPasswordHash {
		t.Fatal("unknown username did not use the fixed dummy password hash")
	}
}

type bootstrapRow struct {
	digest    []byte
	createdAt time.Time
	expiresAt time.Time
}

func readBootstrapRow(t *testing.T, pool *pgxpool.Pool) bootstrapRow {
	t.Helper()
	var row bootstrapRow
	if err := pool.QueryRow(context.Background(), `
		SELECT token_digest, created_at, expires_at FROM bootstrap_tokens WHERE id = 1
	`).Scan(&row.digest, &row.createdAt, &row.expiresAt); err != nil {
		t.Fatal(err)
	}
	return row
}

func newTestService(t *testing.T, pool *pgxpool.Pool, ttl time.Duration) *Service {
	t.Helper()
	service, err := NewService(pool, ttl, DefaultPasswordConcurrency)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testSecret(t *testing.T, value string) *security.SecretValue {
	t.Helper()
	secret, err := security.NewSecretValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func postgresAuthPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	adminConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	admin, err := pgxpool.NewWithConfig(context.Background(), adminConfig)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	if err = admin.Ping(context.Background()); err != nil {
		admin.Close()
		t.Fatalf("ping PostgreSQL: %v", err)
	}

	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "auth_test_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(context.Background(), "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create auth schema: %v", err)
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
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
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
		`CREATE TABLE bootstrap_tokens (
			id smallint PRIMARY KEY DEFAULT 1,
			token_digest bytea NOT NULL,
			created_at timestamptz NOT NULL,
			expires_at timestamptz NOT NULL,
			consumed_at timestamptz
		)`,
		`CREATE TABLE operators (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			username varchar(255) NOT NULL,
			username_key varchar(255) NOT NULL UNIQUE,
			password_hash text NOT NULL,
			disabled_at timestamptz,
			created_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL,
			version integer NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE operator_sessions (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			operator_id uuid NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
			token_digest bytea NOT NULL UNIQUE,
			created_at timestamptz NOT NULL,
			last_seen_at timestamptz NOT NULL,
			expires_at timestamptz NOT NULL,
			revoked_at timestamptz
		)`,
	}
	for _, statement := range statements {
		if _, err = pool.Exec(context.Background(), statement); err != nil {
			t.Fatalf("create auth table: %v", err)
		}
	}
	return pool
}
