package events

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestPostgreSQLCommitOrderingAndDurableReads(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateEventDatabase(t, ctx, databaseURL)
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
	if _, err = pool.Exec(ctx, `
		TRUNCATE event_log RESTART IDENTITY;
		UPDATE event_stream_state SET pruned_through=0,updated_at=clock_timestamp() WHERE id=1
	`); err != nil {
		t.Fatal(err)
	}

	firstTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(ctx, firstTx, testResourceEvent(t, "resource.first", 1)); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	secondDone := make(chan error, 1)
	secondEvent := testResourceEvent(t, "resource.second", 2)
	go func() {
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			secondDone <- beginErr
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		close(started)
		if appendErr := Append(ctx, tx, secondEvent); appendErr != nil {
			secondDone <- appendErr
			return
		}
		secondDone <- tx.Commit(ctx)
	}()
	<-started
	select {
	case secondErr := <-secondDone:
		t.Fatalf("second event bypassed the transaction lock: %v", secondErr)
	case <-time.After(50 * time.Millisecond):
	}
	if err = firstTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-secondDone; err != nil {
		t.Fatal(err)
	}

	rolledBack, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(ctx, rolledBack, testResourceEvent(t, "resource.rolled_back", 99)); err != nil {
		t.Fatal(err)
	}
	if err = rolledBack.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	committed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(ctx, committed, testResourceEvent(t, "resource.third", 3)); err != nil {
		t.Fatal(err)
	}
	if err = committed.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	reader, err := NewReader(pool)
	if err != nil {
		t.Fatal(err)
	}
	values, err := reader.ReadAfter(ctx, 0, MaxReadLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Fatalf("committed events=%d, want 3", len(values))
	}
	for index, want := range []string{"resource.first", "resource.second", "resource.third"} {
		if values[index].Type != want || (index > 0 && values[index-1].Sequence >= values[index].Sequence) {
			t.Fatalf("event[%d]=%+v", index, values[index])
		}
		var snapshot map[string]any
		if err = json.Unmarshal(values[index].Snapshot, &snapshot); err != nil || snapshot["order"] != float64(index+1) {
			t.Fatalf("snapshot[%d]=%s, err=%v", index, values[index].Snapshot, err)
		}
	}
	resumed, err := reader.ReadAfter(ctx, values[1].Sequence, 1)
	if err != nil || len(resumed) != 1 || resumed[0].Type != "resource.third" {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	window, err := reader.Window(ctx)
	if err != nil || window.Tail != values[2].Sequence || window.PrunedThrough != 0 {
		t.Fatalf("cursor window=%+v err=%v", window, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE event_stream_state SET pruned_through=$1 WHERE id=1`, values[1].Sequence); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ReadAfter(ctx, values[0].Sequence, MaxReadLimit); !errors.Is(err, ErrCursorPruned) {
		t.Fatalf("pruned cursor error=%v", err)
	}
	resumed, err = reader.ReadAfter(ctx, values[1].Sequence, MaxReadLimit)
	if err != nil || len(resumed) != 1 || resumed[0].Sequence != values[2].Sequence {
		t.Fatalf("retained cursor resume=%+v err=%v", resumed, err)
	}
}

func testResourceEvent(t *testing.T, eventType string, order int) ResourceEvent {
	t.Helper()
	var resourceID [16]byte
	if _, err := rand.Read(resourceID[:]); err != nil {
		t.Fatal(err)
	}
	return ResourceEvent{
		Type:         eventType,
		ResourceType: "test",
		ResourceID:   resourceID,
		Snapshot:     map[string]any{"order": order},
	}
}

func migrateEventDatabase(t *testing.T, ctx context.Context, databaseURL string) {
	t.Helper()
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	goose.SetBaseFS(migrations.FS)
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpContext(ctx, database, "."); err != nil {
		t.Fatal(err)
	}
}
