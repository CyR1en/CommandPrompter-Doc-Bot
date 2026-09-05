package providers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestStorePostgreSQLVersionAndCaptureRaces(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateProviderTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `
		TRUNCATE operators, credentials, provider_endpoints, event_log,
		         audit_events, idempotency_records, jobs RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatal(err)
	}

	vault, err := security.NewCredentialVault(
		"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)), "",
	)
	if err != nil {
		t.Fatal(err)
	}
	actor := ActorID(randomProviderUUID(t))
	credentialID := credentials.ID(randomProviderUUID(t))
	secret, err := security.NewSecretValue("provider-secret-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := vault.Encrypt(security.CredentialID(credentialID), security.CredentialProviderAPIKey, 1, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operators (id,username,username_key,password_hash)
		VALUES ($1,'Provider Operator','provider operator','unused')
	`, uuid(actor)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO credentials (id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
		VALUES ($1,'PROVIDER_API_KEY','Provider key',$2,$3,$4,$5,1)
	`, uuid(credentialID), credentials.MaskedValue, envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext()); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	configuration := testEndpoint(&credentialID).Configuration
	endpoint, err := store.CreateEndpoint(ctx, CreateEndpoint{Configuration: configuration}, actor, "endpoint-create")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.CreateEndpoint(ctx, CreateEndpoint{Configuration: configuration}, actor, "endpoint-create")
	if err != nil || replay.ID != endpoint.ID {
		t.Fatalf("create replay=%+v err=%v", replay, err)
	}

	profile, err := store.CreateProfile(ctx, CreateProfile{
		EndpointID: endpoint.ID, ModelID: "manual-model", Settings: validTestSettings(),
	}, actor, "profile-create")
	if err != nil {
		t.Fatal(err)
	}
	firstSettings, secondSettings := validTestSettings(), validTestSettings()
	firstOutput, secondOutput := int32(3_000), int32(4_000)
	firstSettings.MaxOutputTokens, secondSettings.MaxOutputTokens = &firstOutput, &secondOutput
	commands := []EditProfile{
		{ProfileID: profile.ID, ExpectedVersion: 1, Settings: firstSettings},
		{ProfileID: profile.ID, ExpectedVersion: 1, Settings: secondSettings},
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for index, command := range commands {
		wait.Add(1)
		go func(index int, command EditProfile) {
			defer wait.Done()
			_, err := store.EditProfile(ctx, command, actor, []string{"edit-one", "edit-two"}[index])
			errorsFound <- err
		}(index, command)
	}
	wait.Wait()
	close(errorsFound)
	succeeded, conflicted := 0, 0
	for err := range errorsFound {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected edit error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("edit outcomes succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	current, err := store.GetProfile(ctx, profile.ID)
	if err != nil || current.Version != 2 || current.CurrentVersion.VersionNumber != 2 {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	var versionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM model_profile_versions WHERE profile_id=$1`, uuid(profile.ID)).Scan(&versionCount); err != nil || versionCount != 2 {
		t.Fatalf("version count=%d err=%v", versionCount, err)
	}

	discovery, err := store.ScheduleDiscovery(ctx, ScheduleDiscovery{EndpointID: endpoint.ID, ExpectedVersion: endpoint.Version}, actor, "discovery")
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewStore(pool, nil)
	permit, err := queue.Claim(ctx, "provider-test-worker", 60_000_000_000)
	if err != nil || permit == nil || permit.JobID != discovery.JobID {
		t.Fatalf("permit=%+v err=%v", permit, err)
	}
	if _, err := store.BeginDiscovery(ctx, discovery.ID, *permit); err != nil {
		t.Fatal(err)
	}
	tlsVerified, authenticated, status := true, true, int32(200)
	completion := CompleteDiscovery{
		RunID: discovery.ID, ModelIDs: []string{"manual-model", "discovered-model"},
		RawResponse: map[string]any{"data": []any{map[string]any{"id": "manual-model"}, map[string]any{"id": "discovered-model"}}},
		TLSVerified: &tlsVerified, AuthenticationSucceeded: &authenticated, HTTPStatus: &status,
	}
	completions := make(chan DiscoveryRun, 2)
	completionErrors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := store.CompleteDiscovery(ctx, completion, *permit)
			completions <- value
			completionErrors <- err
		}()
	}
	wait.Wait()
	close(completions)
	close(completionErrors)
	for err := range completionErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for value := range completions {
		if value.Status != CaptureSucceeded || value.ModelCount == nil || *value.ModelCount != 2 {
			t.Fatalf("completion=%+v", value)
		}
	}
	var completionEvents int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM event_log WHERE resource_id=$1 AND event_type='discovery.succeeded'
	`, uuid(discovery.ID)).Scan(&completionEvents); err != nil || completionEvents != 1 {
		t.Fatalf("completion events=%d err=%v", completionEvents, err)
	}
	endpoint, err = store.GetEndpoint(ctx, endpoint.ID)
	if err != nil || endpoint.Health != Healthy {
		t.Fatalf("endpoint=%+v err=%v", endpoint, err)
	}
	profiles, err := store.ListProfiles(ctx, &endpoint.ID)
	if err != nil || len(profiles) != 2 {
		t.Fatalf("profiles=%+v err=%v", profiles, err)
	}
	probe, err := store.ScheduleProbe(ctx, ScheduleProbe{
		ProfileID: profile.ID, ExpectedVersion: current.Version,
		SelectedChecks: []ProbeCheck{ProbeChat}, AcknowledgeCost: true,
	}, actor, "probe-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	var concurrencyKey string
	var concurrencyLimit int32
	if err = pool.QueryRow(ctx, `SELECT concurrency_key,concurrency_limit FROM jobs WHERE id=$1`, uuid(probe.JobID)).Scan(&concurrencyKey, &concurrencyLimit); err != nil || concurrencyKey != "model-profile:"+profile.ID.String() || concurrencyLimit != current.CurrentVersion.Settings.MaxConcurrentTasks {
		t.Fatalf("probe queue admission=%q/%d err=%v", concurrencyKey, concurrencyLimit, err)
	}
	var persisted string
	if err := pool.QueryRow(ctx, `SELECT coalesce(string_agg(details::text, ''), '') FROM audit_events`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, "provider-secret-sentinel") {
		t.Fatalf("audit leaked provider secret: %s", persisted)
	}
}

func migrateProviderTestDatabase(t *testing.T, ctx context.Context, databaseURL string) {
	t.Helper()
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, database, "."); err != nil {
		t.Fatal(err)
	}
}

func randomProviderUUID(t *testing.T) [16]byte {
	t.Helper()
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
