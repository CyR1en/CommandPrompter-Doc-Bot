package jobs

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestStorePostgreSQLSemantics(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateJobsTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	t.Run("concurrent claim is singular", func(t *testing.T) {
		resetJobs(t, ctx, pool)
		store := NewStore(pool, nil)
		id := enqueueTestJob(t, ctx, store, "claim:singular", 3)

		var wait sync.WaitGroup
		permits := make(chan *Permit, 2)
		errorsFound := make(chan error, 2)
		for _, worker := range []WorkerID{"worker-a", "worker-b"} {
			wait.Add(1)
			go func(worker WorkerID) {
				defer wait.Done()
				permit, err := store.Claim(ctx, worker, time.Minute)
				permits <- permit
				errorsFound <- err
			}(worker)
		}
		wait.Wait()
		close(permits)
		close(errorsFound)
		claimed := 0
		for err := range errorsFound {
			if err != nil {
				t.Fatal(err)
			}
		}
		for permit := range permits {
			if permit != nil {
				claimed++
				if permit.JobID != id || permit.LeaseGeneration != 1 {
					t.Fatalf("unexpected permit: %+v", permit)
				}
			}
		}
		if claimed != 1 {
			t.Fatalf("claimed %d permits", claimed)
		}
		var attempts int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM job_attempts").Scan(&attempts); err != nil || attempts != 1 {
			t.Fatalf("attempt count=%d err=%v", attempts, err)
		}
	})

	t.Run("model concurrency admission is atomic and does not block other models", func(t *testing.T) {
		resetJobs(t, ctx, pool)
		store := NewStore(pool, nil)
		enqueueConcurrentTestJob(t, ctx, store, "limited:first", "model-profile:a", 1)
		enqueueConcurrentTestJob(t, ctx, store, "limited:second", "model-profile:a", 1)

		var wait sync.WaitGroup
		permits := make(chan *Permit, 2)
		errorsFound := make(chan error, 2)
		for _, worker := range []WorkerID{"worker-a", "worker-b"} {
			wait.Add(1)
			go func(worker WorkerID) {
				defer wait.Done()
				permit, claimErr := store.Claim(ctx, worker, time.Minute)
				permits <- permit
				errorsFound <- claimErr
			}(worker)
		}
		wait.Wait()
		close(permits)
		close(errorsFound)
		for claimErr := range errorsFound {
			if claimErr != nil {
				t.Fatal(claimErr)
			}
		}
		var active *Permit
		for permit := range permits {
			if permit != nil {
				if active != nil {
					t.Fatal("two jobs exceeded a model concurrency limit of one")
				}
				active = permit
			}
		}
		if active == nil {
			t.Fatal("no limited job was admitted")
		}

		otherID := enqueueConcurrentTestJob(t, ctx, store, "other:first", "model-profile:b", 1)
		other, err := store.Claim(ctx, "worker-c", time.Minute)
		if err != nil || other == nil || other.JobID != otherID {
			t.Fatalf("other model was blocked: permit=%+v err=%v", other, err)
		}
		if err = store.Succeed(ctx, *active, map[string]any{}); err != nil {
			t.Fatal(err)
		}
		remaining, err := store.Claim(ctx, "worker-d", time.Minute)
		if err != nil || remaining == nil || remaining.JobID == otherID {
			t.Fatalf("released model capacity was not reused: permit=%+v err=%v", remaining, err)
		}
	})

	t.Run("operator reads and transaction-bound cancellation", func(t *testing.T) {
		resetJobs(t, ctx, pool)
		store := NewStore(pool, nil)
		first := enqueueTestJob(t, ctx, store, "read:first", 3)
		second := enqueueTestJob(t, ctx, store, "read:second", 3)
		pending := Pending
		jobType := SyncSource
		values, err := store.List(ctx, ListOptions{Limit: 1, Offset: 1, Status: &pending, Type: &jobType})
		if err != nil || len(values) != 1 || values[0].ID != second {
			t.Fatalf("list=%+v err=%v", values, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status, err := store.RequestCancelTx(ctx, tx, first); err != nil || status != Cancelled {
			t.Fatalf("transaction cancel status=%s err=%v", status, err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		rolledBack, err := store.Get(ctx, first)
		if err != nil || rolledBack.Status != Pending {
			t.Fatalf("rolled-back cancel=%+v err=%v", rolledBack, err)
		}
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status, err := store.RequestCancelTx(ctx, tx, first); err != nil || status != Cancelled {
			t.Fatalf("committed cancel status=%s err=%v", status, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		cancelled, err := store.Get(ctx, first)
		if err != nil || cancelled.Status != Cancelled {
			t.Fatalf("committed cancel=%+v err=%v", cancelled, err)
		}
	})

	t.Run("service cancellation is atomic idempotent and convergent", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `
			TRUNCATE audit_events, idempotency_records, job_attempts,
			         job_events, event_log, jobs RESTART IDENTITY CASCADE
		`); err != nil {
			t.Fatal(err)
		}
		vault, err := security.NewCredentialVault(
			"active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		service, err := NewService(pool, vault, nil)
		if err != nil {
			t.Fatal(err)
		}
		actor := ActorID(randomUUID(t))

		rolledBack, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.Queue().EnqueueTx(ctx, rolledBack, Command{
			Type: SyncSource, TargetType: "source", TargetID: randomUUID(t),
			Payload: map[string]any{"secret": "rollback-sentinel"}, OperationKey: "service:rollback",
		}); err != nil {
			t.Fatal(err)
		}
		if err = rolledBack.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		var rolledBackJobs int
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM jobs").Scan(&rolledBackJobs); err != nil || rolledBackJobs != 0 {
			t.Fatalf("rolled-back jobs=%d err=%v", rolledBackJobs, err)
		}

		firstID := enqueueTestJob(t, ctx, service.Queue(), "service:first", 3)
		otherID := enqueueTestJob(t, ctx, service.Queue(), "service:other", 3)
		first, err := service.Cancel(ctx, firstID, actor, "cancel-key")
		if err != nil || first.Status != Cancelled {
			t.Fatalf("first cancel=%+v err=%v", first, err)
		}
		replay, err := service.Cancel(ctx, firstID, actor, "cancel-key")
		if err != nil || !reflect.DeepEqual(first, replay) {
			t.Fatalf("replay=%+v err=%v", replay, err)
		}
		if _, err = service.Cancel(ctx, otherID, actor, "cancel-key"); !errors.Is(err, idempotency.ErrConflict) {
			t.Fatalf("key conflict=%v", err)
		}
		var idempotencyCount, auditCount int
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM idempotency_records").Scan(&idempotencyCount); err != nil {
			t.Fatal(err)
		}
		if err = pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='job.cancel'").Scan(&auditCount); err != nil {
			t.Fatal(err)
		}
		if idempotencyCount != 1 || auditCount != 1 {
			t.Fatalf("idempotency=%d audit=%d", idempotencyCount, auditCount)
		}
		var persisted string
		if err = pool.QueryRow(ctx, "SELECT details::text FROM audit_events WHERE action='job.cancel'").Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(persisted, "rollback-sentinel") || strings.Contains(persisted, "operation_key") || strings.Contains(persisted, "payload") {
			t.Fatalf("audit disclosed private job input: %s", persisted)
		}

		leasedID := enqueueTestJob(t, ctx, service.Queue(), "service:leased", 3)
		permit, err := service.Queue().Claim(ctx, "service-worker", time.Minute)
		if err != nil || permit == nil || permit.JobID != otherID {
			t.Fatalf("claim other pending job first=%+v err=%v", permit, err)
		}
		if err = service.Queue().Succeed(ctx, *permit, map[string]any{}); err != nil {
			t.Fatal(err)
		}
		permit, err = service.Queue().Claim(ctx, "service-worker", time.Minute)
		if err != nil || permit == nil || permit.JobID != leasedID {
			t.Fatalf("claim leased job=%+v err=%v", permit, err)
		}
		requested, err := service.Cancel(ctx, leasedID, actor, "leased-key")
		if err != nil || requested.Status != CancelRequested {
			t.Fatalf("requested cancel=%+v err=%v", requested, err)
		}
		if err = service.Queue().AcknowledgeCancel(ctx, *permit); err != nil {
			t.Fatal(err)
		}
		converged, err := service.Cancel(ctx, leasedID, actor, "leased-key")
		if err != nil || converged.Status != Cancelled {
			t.Fatalf("converged cancel=%+v err=%v", converged, err)
		}
	})

	t.Run("reclamation fences every old mutation", func(t *testing.T) {
		resetJobs(t, ctx, pool)
		store := NewStore(pool, nil)
		id := enqueueTestJob(t, ctx, store, "lease:reclaim", 3)
		first, err := store.Claim(ctx, "worker-a", time.Minute)
		if err != nil || first == nil {
			t.Fatalf("first claim: permit=%v err=%v", first, err)
		}
		if _, err := pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", toPGJobID(id)); err != nil {
			t.Fatal(err)
		}
		second, err := store.Claim(ctx, "worker-b", time.Minute)
		if err != nil || second == nil || second.LeaseGeneration != first.LeaseGeneration+1 {
			t.Fatalf("reclaim: permit=%v err=%v", second, err)
		}
		checks := []func() error{
			func() error { return store.Heartbeat(ctx, *first, time.Minute) },
			func() error { return store.Succeed(ctx, *first, map[string]any{"revision": "stale"}) },
			func() error { _, err := store.RetryAfter(ctx, *first, "retry", time.Second); return err },
			func() error { return store.Fail(ctx, *first, "failure") },
			func() error { return store.CompleteAcceptedResult(ctx, *first, map[string]any{"revision": "stale"}) },
		}
		for index, check := range checks {
			if err := check(); !errors.Is(err, ErrStalePermit) {
				t.Fatalf("stale mutation %d: %v", index, err)
			}
		}
		var outcomes []string
		rows, err := pool.Query(ctx, "SELECT COALESCE(outcome, '') FROM job_attempts ORDER BY attempt_number")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var outcome string
			if err := rows.Scan(&outcome); err != nil {
				t.Fatal(err)
			}
			outcomes = append(outcomes, outcome)
		}
		if !reflect.DeepEqual(outcomes, []string{"LEASE_EXPIRED", ""}) {
			t.Fatalf("attempt outcomes=%v", outcomes)
		}
	})

	t.Run("database clock rejects a permit after lock wait", func(t *testing.T) {
		resetJobs(t, ctx, pool)
		store := NewStore(pool, nil)
		id := enqueueTestJob(t, ctx, store, "lease:lock-wait", 3)
		permit, err := store.Claim(ctx, "worker-a", 150*time.Millisecond)
		if err != nil || permit == nil {
			t.Fatalf("claim: permit=%v err=%v", permit, err)
		}
		blocker, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blocker.Exec(ctx, "SELECT id FROM jobs WHERE id=$1 FOR UPDATE", toPGJobID(id)); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { result <- store.Heartbeat(ctx, *permit, time.Minute) }()
		time.Sleep(200 * time.Millisecond)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-result; !errors.Is(err, ErrStalePermit) {
			t.Fatalf("heartbeat after lock wait: %v", err)
		}
	})

	t.Run("retry schedule and accepted-result cancellation race", func(t *testing.T) {
		resetJobs(t, ctx, pool)
		store := NewStore(pool, nil)
		retryID := enqueueTestJob(t, ctx, store, "retry:later", 3)
		permit, err := store.Claim(ctx, "worker-a", time.Minute)
		if err != nil || permit == nil {
			t.Fatalf("claim: permit=%v err=%v", permit, err)
		}
		if status, err := store.RetryAfter(ctx, *permit, "provider unavailable", time.Minute); err != nil || status != RetryWait {
			t.Fatalf("retry: status=%s err=%v", status, err)
		}
		if claimed, err := store.Claim(ctx, "worker-b", time.Minute); err != nil || claimed != nil {
			t.Fatalf("early claim: permit=%v err=%v", claimed, err)
		}
		if _, err := pool.Exec(ctx, "UPDATE jobs SET not_before=clock_timestamp()-interval '1 second' WHERE id=$1", toPGJobID(retryID)); err != nil {
			t.Fatal(err)
		}
		ready, err := store.Claim(ctx, "worker-b", time.Minute)
		if err != nil || ready == nil {
			t.Fatalf("ready claim: permit=%v err=%v", ready, err)
		}
		if status, err := store.RequestCancel(ctx, retryID); err != nil || status != CancelRequested {
			t.Fatalf("cancel request: status=%s err=%v", status, err)
		}
		if err := store.CompleteAcceptedResult(ctx, *ready, map[string]any{"outcome": "accepted"}); err != nil {
			t.Fatal(err)
		}
		snapshot, err := store.Get(ctx, retryID)
		if err != nil || snapshot.Status != Succeeded || snapshot.Result["outcome"] != "accepted" {
			t.Fatalf("accepted snapshot=%+v err=%v", snapshot, err)
		}
	})

	t.Run("cancellation acknowledgment and final-attempt expiry", func(t *testing.T) {
		resetJobs(t, ctx, pool)
		store := NewStore(pool, nil)
		cancelID := enqueueTestJob(t, ctx, store, "cancel:ack", 3)
		permit, err := store.Claim(ctx, "worker-a", time.Minute)
		if err != nil || permit == nil {
			t.Fatalf("claim: permit=%v err=%v", permit, err)
		}
		if status, err := store.RequestCancel(ctx, cancelID); err != nil || status != CancelRequested {
			t.Fatalf("cancel request: status=%s err=%v", status, err)
		}
		if err := store.Heartbeat(ctx, *permit, time.Minute); !errors.Is(err, ErrStalePermit) {
			t.Fatalf("cancel-request heartbeat: %v", err)
		}
		if err := store.AcknowledgeCancel(ctx, *permit); err != nil {
			t.Fatal(err)
		}
		cancelled, err := store.Get(ctx, cancelID)
		if err != nil || cancelled.Status != Cancelled {
			t.Fatalf("cancelled snapshot=%+v err=%v", cancelled, err)
		}

		exhaustedID := enqueueTestJob(t, ctx, store, "lease:exhausted", 1)
		exhausted, err := store.Claim(ctx, "worker-a", time.Minute)
		if err != nil || exhausted == nil || exhausted.JobID != exhaustedID {
			t.Fatalf("exhausted claim: permit=%v err=%v", exhausted, err)
		}
		if _, err := pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", toPGJobID(exhaustedID)); err != nil {
			t.Fatal(err)
		}
		nextID := enqueueTestJob(t, ctx, store, "lease:next", 3)
		next, err := store.Claim(ctx, "worker-b", time.Minute)
		if err != nil || next == nil || next.JobID != nextID {
			t.Fatalf("next claim: permit=%v err=%v", next, err)
		}
		failedSnapshot, err := store.Get(ctx, exhaustedID)
		if err != nil || failedSnapshot.Status != Failed || failedSnapshot.SanitizedError == nil || *failedSnapshot.SanitizedError != "lease expired after final attempt" {
			t.Fatalf("expired snapshot=%+v err=%v", failedSnapshot, err)
		}
	})

	t.Run("terminal callback and fenced domain work roll back atomically", func(t *testing.T) {
		resetJobs(t, ctx, pool)
		callbackError := errors.New("callback rejected terminal state")
		store := NewStore(pool, func(ctx context.Context, tx pgx.Tx, _ Snapshot) error {
			if _, err := tx.Exec(ctx, "UPDATE jobs SET progress=77"); err != nil {
				return err
			}
			return callbackError
		})
		id := enqueueTestJob(t, ctx, store, "callback:rollback", 3)
		permit, err := store.Claim(ctx, "worker-a", time.Minute)
		if err != nil || permit == nil {
			t.Fatalf("claim: permit=%v err=%v", permit, err)
		}
		if err := store.Fail(ctx, *permit, "safe failure"); !errors.Is(err, callbackError) {
			t.Fatalf("callback failure: %v", err)
		}
		snapshot, err := store.Get(ctx, id)
		if err != nil || snapshot.Status != Leased || snapshot.Progress != 0 {
			t.Fatalf("rolled-back snapshot=%+v err=%v", snapshot, err)
		}

		if _, err := pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", toPGJobID(id)); err != nil {
			t.Fatal(err)
		}
		second, err := NewStore(pool, nil).Claim(ctx, "worker-b", time.Minute)
		if err != nil || second == nil {
			t.Fatalf("reclaim: permit=%v err=%v", second, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "UPDATE jobs SET progress=33 WHERE id=$1", toPGJobID(id)); err != nil {
			t.Fatal(err)
		}
		if err := store.AssertPermit(ctx, tx, *permit); !errors.Is(err, ErrStalePermit) {
			t.Fatalf("stale transaction permit: %v", err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		snapshot, err = store.Get(ctx, id)
		if err != nil || snapshot.Progress != 0 {
			t.Fatalf("domain rollback snapshot=%+v err=%v", snapshot, err)
		}
	})
}

func migrateJobsTestDatabase(t *testing.T, ctx context.Context, databaseURL string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatal(err)
	}
}

func resetJobs(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, "TRUNCATE job_attempts, job_events, event_log, jobs RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}
}

func enqueueTestJob(t *testing.T, ctx context.Context, store *Store, operation string, maxAttempts int32) JobID {
	t.Helper()
	id, err := store.Enqueue(ctx, Command{
		Type: SyncSource, TargetType: "source", TargetID: randomUUID(t),
		Payload: map[string]any{}, OperationKey: operation, MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func enqueueConcurrentTestJob(t *testing.T, ctx context.Context, store *Store, operation, concurrencyKey string, concurrencyLimit int32) JobID {
	t.Helper()
	id, err := store.Enqueue(ctx, Command{
		Type: SyncSource, TargetType: "source", TargetID: randomUUID(t),
		Payload: map[string]any{}, OperationKey: operation, MaxAttempts: 3,
		ConcurrencyKey: concurrencyKey, ConcurrencyLimit: concurrencyLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func randomUUID(t *testing.T) UUID {
	t.Helper()
	var id UUID
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}
