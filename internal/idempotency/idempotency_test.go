package idempotency

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestExecutePostgreSQLSemantics(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	t.Run("concurrent replay executes once", func(t *testing.T) {
		resetRecords(t, ctx, pool)
		request := testRequest("operator:one", "request-one", "same")
		expected := Result{Type: "knowledge_base", ID: randomID(t)}
		var calls atomic.Int32
		start := make(chan struct{})
		results := make(chan Result, 2)
		errorsFound := make(chan error, 2)
		var wait sync.WaitGroup
		for range 2 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				result, err := inTransaction(ctx, pool, request, func(context.Context, pgx.Tx) (Result, error) {
					calls.Add(1)
					time.Sleep(50 * time.Millisecond)
					return expected, nil
				})
				results <- result
				errorsFound <- err
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		close(errorsFound)
		for err := range errorsFound {
			if err != nil {
				t.Fatal(err)
			}
		}
		for result := range results {
			if result != expected {
				t.Fatalf("result=%+v", result)
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("operation called %d times", calls.Load())
		}
	})

	t.Run("operation and digest conflicts", func(t *testing.T) {
		resetRecords(t, ctx, pool)
		request := testRequest("operator:one", "request-one", "first")
		expected := Result{Type: "knowledge_base", ID: randomID(t)}
		if _, err := inTransaction(ctx, pool, request, constant(expected)); err != nil {
			t.Fatal(err)
		}
		changedDigest := request
		changedDigest.Digest = sha256.Sum256([]byte("second"))
		if _, err := inTransaction(ctx, pool, changedDigest, constant(expected)); !errors.Is(err, ErrConflict) {
			t.Fatalf("digest conflict=%v", err)
		}
		changedOperation := request
		changedOperation.Operation = "knowledge_base.update"
		if _, err := inTransaction(ctx, pool, changedOperation, constant(expected)); !errors.Is(err, ErrConflict) {
			t.Fatalf("operation conflict=%v", err)
		}
		rotated := changedDigest
		rotated.AcceptedDigests = []Digest{request.Digest}
		if result, err := inTransaction(ctx, pool, rotated, constant(Result{})); err != nil || result != expected {
			t.Fatalf("accepted previous digest: result=%+v err=%v", result, err)
		}
	})

	t.Run("database clock is sampled after lock wait", func(t *testing.T) {
		resetRecords(t, ctx, pool)
		request := testRequest("operator:clock", "post-lock-clock", "post-lock")
		request.TTL = 100 * time.Millisecond
		lockKey := "14:operator:clockpost-lock-clock"
		expected := Result{Type: "knowledge_base", ID: randomID(t)}
		blocker, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blocker.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", lockKey); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, executionErr := inTransaction(ctx, pool, request, constant(expected))
			done <- executionErr
		}()
		time.Sleep(200 * time.Millisecond)
		var releasedAfter time.Time
		if err := blocker.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&releasedAfter); err != nil {
			t.Fatal(err)
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		var createdAt, expiresAt time.Time
		if err := pool.QueryRow(ctx, `
			SELECT created_at, expires_at FROM idempotency_records
			WHERE scope=$1 AND request_key=$2
		`, request.Scope, request.Key).Scan(&createdAt, &expiresAt); err != nil {
			t.Fatal(err)
		}
		if createdAt.Before(releasedAfter) || expiresAt.Sub(createdAt) != request.TTL {
			t.Fatalf("created=%s released=%s ttl=%s", createdAt, releasedAfter, expiresAt.Sub(createdAt))
		}
	})

	t.Run("callback failure rolls back domain and record", func(t *testing.T) {
		resetRecords(t, ctx, pool)
		request := testRequest("operator:rollback", "one", "same")
		callbackError := errors.New("domain rejected")
		_, err := inTransaction(ctx, pool, request, func(ctx context.Context, tx pgx.Tx) (Result, error) {
			if _, err := tx.Exec(ctx, "INSERT INTO event_log(event_type,resource_type,resource_id,snapshot) VALUES('TEST','test',gen_random_uuid(),'{}')"); err != nil {
				return Result{}, err
			}
			return Result{}, callbackError
		})
		if !errors.Is(err, callbackError) {
			t.Fatalf("callback error=%v", err)
		}
		var records, events int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM idempotency_records").Scan(&records); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_log WHERE event_type='TEST'").Scan(&events); err != nil {
			t.Fatal(err)
		}
		if records != 0 || events != 0 {
			t.Fatalf("records=%d events=%d", records, events)
		}
	})
}

func TestRejectsUnrepresentableTTL(t *testing.T) {
	request := testRequest("scope", "key", "content")
	request.TTL = time.Nanosecond
	_, err := Execute(context.Background(), nil, request, constant(Result{}))
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func testRequest(scope, key, input string) Request {
	return Request{Scope: scope, Key: key, Operation: "knowledge_base.create", Digest: sha256.Sum256([]byte(input)), TTL: time.Hour}
}

func constant(result Result) Operation {
	return func(context.Context, pgx.Tx) (Result, error) { return result, nil }
}

func randomID(t *testing.T) [16]byte {
	t.Helper()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id
}

func inTransaction(ctx context.Context, pool *pgxpool.Pool, request Request, operation Operation) (Result, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := Execute(ctx, tx, request, operation)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result, nil
}

func migrateTestDatabase(t *testing.T, ctx context.Context, databaseURL string) {
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

func resetRecords(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, "TRUNCATE idempotency_records, event_log"); err != nil {
		t.Fatal(err)
	}
}
