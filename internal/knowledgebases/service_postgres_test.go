package knowledgebases

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestKnowledgeBasePostgresLifecycleAndAtomicEvents(t *testing.T) {
	pool := postgresKnowledgeBasePool(t)
	ctx := context.Background()
	service := newPostgresKnowledgeBaseService(t, pool, nil)
	actor := auth.OperatorID{1}
	name, err := ParseName("Plugin Docs")
	if err != nil {
		t.Fatal(err)
	}
	command := CreateCommand{
		Name: name, Access: Restricted, Instructions: "Document public commands.", Language: "en-US",
	}
	created, err := service.Create(ctx, command, actor, "create")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Version != 1 || created.Lifecycle != Active || created.Access != Restricted {
		t.Fatalf("created = %#v", created)
	}
	replay, err := service.Create(ctx, command, actor, "create")
	if err != nil || replay != created {
		t.Fatalf("create replay = %#v, %v", replay, err)
	}
	changed := command
	changed.Instructions = "different"
	if _, err = service.Create(ctx, changed, actor, "create"); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("changed replay error = %v", err)
	}
	archive := Archived
	updated, err := service.Update(ctx, UpdateCommand{
		KnowledgeBaseID: created.ID, ExpectedVersion: 1, Lifecycle: &archive,
	}, actor, "archive")
	if err != nil || updated.Version != 2 || updated.Lifecycle != Archived || updated.ArchivedAt == nil {
		t.Fatalf("archive = %#v, %v", updated, err)
	}
	wrongDelete := DeleteCommand{
		KnowledgeBaseID: created.ID, ExpectedVersion: 2, ConfirmationName: "plugin docs",
	}
	if _, err = service.RequestDelete(ctx, wrongDelete, actor, "wrong-delete"); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("wrong confirmation error = %v", err)
	}
	deletionCommand := wrongDelete
	deletionCommand.ConfirmationName = "Plugin Docs"
	deletion, err := service.RequestDelete(ctx, deletionCommand, actor, "delete")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deletion.KnowledgeBase.Version != 3 || deletion.KnowledgeBase.Lifecycle != PendingDelete ||
		deletion.KnowledgeBase.DeleteRequestedAt == nil || deletion.KnowledgeBase.PurgeAfter == nil ||
		deletion.KnowledgeBase.PurgeAfter.Sub(*deletion.KnowledgeBase.DeleteRequestedAt) != 72*time.Hour {
		t.Fatalf("deletion = %#v", deletion)
	}
	var jobCreatedAt, jobUpdatedAt time.Time
	if err = pool.QueryRow(ctx, `SELECT created_at, updated_at FROM jobs WHERE id = $1`,
		pgUUID(ID(deletion.PurgeJobID)),
	).Scan(&jobCreatedAt, &jobUpdatedAt); err != nil ||
		!jobCreatedAt.Equal(*deletion.KnowledgeBase.DeleteRequestedAt) ||
		!jobUpdatedAt.Equal(*deletion.KnowledgeBase.DeleteRequestedAt) {
		t.Fatalf("purge job time = %s/%s, delete time = %s, err=%v", jobCreatedAt, jobUpdatedAt, deletion.KnowledgeBase.DeleteRequestedAt, err)
	}
	deletionReplay, err := service.RequestDelete(ctx, deletionCommand, actor, "delete")
	if err != nil || !reflect.DeepEqual(deletionReplay, deletion) {
		t.Fatalf("delete replay = %#v, %v", deletionReplay, err)
	}
	restored, err := service.Restore(ctx, RestoreCommand{
		KnowledgeBaseID: created.ID, ExpectedVersion: 3,
	}, actor, "restore")
	if err != nil || restored.Version != 4 || restored.Lifecycle != Archived ||
		restored.ArchivedAt == nil || restored.DeleteRequestedAt != nil || restored.PurgeAfter != nil {
		t.Fatalf("restore = %#v, %v", restored, err)
	}
	var jobStatus string
	if err = pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`,
		pgUUID(ID(deletion.PurgeJobID)),
	).Scan(&jobStatus); err != nil || jobs.Status(jobStatus) != jobs.Cancelled {
		t.Fatalf("purge job status = %s, %v", jobStatus, err)
	}
	if _, err = service.RequestDelete(ctx, deletionCommand, actor, "delete"); !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("stale deletion replay error = %v", err)
	}

	values, err := service.List(ctx)
	if err != nil || len(values) != 1 || values[0].ID != created.ID {
		t.Fatalf("list = %#v, %v", values, err)
	}
	var audits, knowledgeEvents, idempotencyRecords int
	err = pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM audit_events WHERE target_type='knowledge_base'),
		       (SELECT count(*) FROM event_log WHERE resource_type='knowledge_base'),
		       (SELECT count(*) FROM idempotency_records)
	`).Scan(&audits, &knowledgeEvents, &idempotencyRecords)
	if err != nil || audits != 4 || knowledgeEvents != 4 || idempotencyRecords != 4 {
		t.Fatalf("records audit=%d events=%d idempotency=%d err=%v", audits, knowledgeEvents, idempotencyRecords, err)
	}
}

func TestKnowledgeBasePostgresPrivatePolicyAndNormalizedNameRaces(t *testing.T) {
	pool := postgresKnowledgeBasePool(t)
	ctx := context.Background()
	service := newPostgresKnowledgeBaseService(t, pool, nil)
	actor := auth.OperatorID{2}

	start := make(chan struct{})
	type result struct {
		value KnowledgeBase
		err   error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index, raw := range []string{"Straße Docs", "STRASSE DOCS"} {
		index, raw := index, raw
		go func() {
			ready.Done()
			name, parseErr := ParseName(raw)
			if parseErr != nil {
				results <- result{err: parseErr}
				return
			}
			<-start
			value, createErr := service.Create(ctx, CreateCommand{
				Name: name, Access: Restricted, Language: "en", Instructions: "",
			}, actor, "race-create-"+string(rune('a'+index)))
			results <- result{value: value, err: createErr}
		}()
	}
	ready.Wait()
	close(start)
	createResults := []result{<-results, <-results}
	var winner KnowledgeBase
	var success, conflicts int
	for _, value := range createResults {
		if value.err == nil {
			success++
			winner = value.value
		} else if errors.Is(value.err, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("duplicate create error = %v", value.err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("duplicate create success=%d conflict=%d", success, conflicts)
	}

	sourceID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `
		INSERT INTO sources (id, knowledge_base_id, privacy, lifecycle)
		VALUES ($1, $2, 'PRIVATE', 'ENABLED')
	`, pgUUID(sourceID), pgUUID(winner.ID)); err != nil {
		t.Fatal(err)
	}
	public := Public
	if _, err = service.Update(ctx, UpdateCommand{
		KnowledgeBaseID: winner.ID, ExpectedVersion: 1, Access: &public,
	}, actor, "make-public"); err == nil || !errors.Is(err, ErrConflict) || err.Error() != "private sources require a restricted knowledge base" {
		t.Fatalf("private-source policy error = %v", err)
	}

	created := make([]KnowledgeBase, 0, 2)
	for index, raw := range []string{"Rename alpha", "Rename beta"} {
		name, parseErr := ParseName(raw)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		value, createErr := service.Create(ctx, CreateCommand{
			Name: name, Access: Restricted, Language: "en", Instructions: "",
		}, actor, "rename-create-"+string(rune('a'+index)))
		if createErr != nil {
			t.Fatal(createErr)
		}
		created = append(created, value)
	}
	ready = sync.WaitGroup{}
	ready.Add(2)
	start = make(chan struct{})
	updateErrors := make(chan error, 2)
	for index, raw := range []string{"Ｄuplicate", "Duplicate"} {
		index, raw := index, raw
		go func() {
			ready.Done()
			name, parseErr := ParseName(raw)
			if parseErr != nil {
				updateErrors <- parseErr
				return
			}
			<-start
			_, updateErr := service.Update(ctx, UpdateCommand{
				KnowledgeBaseID: created[index].ID, ExpectedVersion: 1, Name: &name,
			}, actor, "rename-"+string(rune('a'+index)))
			updateErrors <- updateErr
		}()
	}
	ready.Wait()
	close(start)
	errorsSeen := []error{<-updateErrors, <-updateErrors}
	var renameSuccess, renameConflict int
	for _, updateErr := range errorsSeen {
		if updateErr == nil {
			renameSuccess++
		} else if errors.Is(updateErr, ErrConflict) {
			renameConflict++
		} else {
			t.Fatalf("rename error = %v", updateErr)
		}
	}
	if renameSuccess != 1 || renameConflict != 1 {
		t.Fatalf("duplicate rename success=%d conflict=%d", renameSuccess, renameConflict)
	}
}

func TestKnowledgeBasePostgresRepeatedDeleteRestoreSelectsLatestPurgeJob(t *testing.T) {
	pool := postgresKnowledgeBasePool(t)
	ctx := context.Background()
	service := newPostgresKnowledgeBaseService(t, pool, nil)
	actor := auth.OperatorID{5}
	name, err := ParseName("Repeat restore")
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.Create(ctx, CreateCommand{Name: name, Access: Restricted, Language: "en"}, actor, "create")
	if err != nil {
		t.Fatal(err)
	}
	var firstJob jobs.JobID
	for cycle := 0; cycle < 2; cycle++ {
		deletion, deleteErr := service.RequestDelete(ctx, DeleteCommand{
			KnowledgeBaseID: value.ID, ExpectedVersion: value.Version, ConfirmationName: value.Name,
		}, actor, fmt.Sprintf("delete-%d", cycle))
		if deleteErr != nil {
			t.Fatalf("delete cycle %d: %v", cycle, deleteErr)
		}
		if cycle == 0 {
			firstJob = deletion.PurgeJobID
		} else if deletion.PurgeJobID == firstJob {
			t.Fatal("second delete reused the terminal purge job")
		}
		value, err = service.Restore(ctx, RestoreCommand{
			KnowledgeBaseID: value.ID, ExpectedVersion: deletion.KnowledgeBase.Version,
		}, actor, fmt.Sprintf("restore-%d", cycle))
		if err != nil || value.Lifecycle != Active {
			t.Fatalf("restore cycle %d = %#v, %v", cycle, value, err)
		}
	}
	var cancelled int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status='CANCELLED'`).Scan(&cancelled); err != nil || cancelled != 2 {
		t.Fatalf("cancelled purge jobs = %d, %v", cancelled, err)
	}
}

func TestKnowledgeBasePostgresPurgePermitAndArtifactBoundary(t *testing.T) {
	pool := postgresKnowledgeBasePool(t)
	ctx := context.Background()
	purger := &recordingPurger{}
	service := newPostgresKnowledgeBaseService(t, pool, purger)
	actor := auth.OperatorID{3}
	name, err := ParseName("Deferred purge")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateCommand{
		Name: name, Access: Restricted, Language: "en", Instructions: "",
	}, actor, "create")
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO sources (id, knowledge_base_id, privacy, lifecycle) VALUES ($1,$2,'PUBLIC','ENABLED')`, pgUUID(sourceID), pgUUID(created.ID)); err != nil {
		t.Fatal(err)
	}
	deletion, err := service.RequestDelete(ctx, DeleteCommand{
		KnowledgeBaseID: created.ID, ExpectedVersion: 1, ConfirmationName: created.Name,
	}, actor, "delete")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE jobs SET not_before=clock_timestamp()-interval '1 second' WHERE id=$1`, pgUUID(ID(deletion.PurgeJobID))); err != nil {
		t.Fatal(err)
	}
	jobStore := jobs.NewStore(pool, nil)
	permit, err := jobStore.Claim(ctx, "purger", time.Minute)
	if err != nil || permit == nil {
		t.Fatalf("claim purge = %#v, %v", permit, err)
	}
	if _, err = service.Purge(ctx, created.ID, *permit); !errors.Is(err, ErrPurgeNotReady) {
		t.Fatalf("early purge error = %v", err)
	}
	if _, err = pool.Exec(ctx, `
		UPDATE knowledge_bases
		SET delete_requested_at=clock_timestamp()-interval '2 seconds',
		    purge_after=clock_timestamp()-interval '1 second'
		WHERE id=$1
	`, pgUUID(created.ID)); err != nil {
		t.Fatal(err)
	}
	purged, err := service.Purge(ctx, created.ID, *permit)
	if err != nil || purged.Lifecycle != Deleted || purged.DeletedAt == nil || purged.Version != 3 {
		t.Fatalf("purged = %#v, %v", purged, err)
	}
	if purger.knowledgeBaseID != created.ID || len(purger.sourceIDs) != 1 || purger.sourceIDs[0] != sourceID {
		t.Fatalf("purger call = %s / %v", purger.knowledgeBaseID, purger.sourceIDs)
	}
	replayed, err := service.Purge(ctx, created.ID, *permit)
	if err != nil || replayed.Version != purged.Version || purger.calls != 1 {
		t.Fatalf("purge replay = %#v, calls=%d, err=%v", replayed, purger.calls, err)
	}
	if _, err = service.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purged get error = %v", err)
	}
	var audits, events, idempotencyRecords int
	err = pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM audit_events WHERE target_type='knowledge_base'),
		       (SELECT count(*) FROM event_log WHERE resource_type='knowledge_base'),
		       (SELECT count(*) FROM idempotency_records)
	`).Scan(&audits, &events, &idempotencyRecords)
	if err != nil || audits != 3 || events != 3 || idempotencyRecords != 3 {
		t.Fatalf("purge records audit=%d events=%d idempotency=%d err=%v", audits, events, idempotencyRecords, err)
	}
}

func TestKnowledgeBasePurgeIntentSurvivesArtifactFailureAndServiceRestart(t *testing.T) {
	pool := postgresKnowledgeBasePool(t)
	ctx := context.Background()
	purger := &recordingPurger{failures: 1}
	service := newPostgresKnowledgeBaseService(t, pool, purger)
	actor := auth.OperatorID{13}
	name, err := ParseName("Restartable purge")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateCommand{
		Name: name, Access: Restricted, Language: "en", Instructions: "",
	}, actor, "create")
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := service.RequestDelete(ctx, DeleteCommand{
		KnowledgeBaseID: created.ID, ExpectedVersion: created.Version, ConfirmationName: created.Name,
	}, actor, "delete")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE knowledge_bases SET purge_after=clock_timestamp()-interval '1 second' WHERE id=$1`, pgUUID(created.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE jobs SET not_before=clock_timestamp()-interval '1 second' WHERE id=$1`, pgUUID(ID(deletion.PurgeJobID))); err != nil {
		t.Fatal(err)
	}
	permit, err := jobs.NewStore(pool, nil).Claim(ctx, "restartable-purger", time.Minute)
	if err != nil || permit == nil || permit.JobID != deletion.PurgeJobID {
		t.Fatalf("permit=%#v err=%v", permit, err)
	}
	if _, err = service.Purge(ctx, created.ID, *permit); err == nil || err.Error() != "injected purge failure" {
		t.Fatalf("first purge error=%v", err)
	}
	var lifecycle string
	var intents, deletedEvents int
	if err = pool.QueryRow(ctx, `
		SELECT lifecycle,
		       (SELECT count(*) FROM artifact_deletion_intents WHERE kind='KNOWLEDGE_BASE' AND resource_id=$1),
		       (SELECT count(*) FROM event_log WHERE event_type='knowledge_base.deleted' AND resource_id=$1)
		FROM knowledge_bases WHERE id=$1
	`, pgUUID(created.ID)).Scan(&lifecycle, &intents, &deletedEvents); err != nil {
		t.Fatal(err)
	}
	if Lifecycle(lifecycle) != PendingDelete || intents != 1 || deletedEvents != 0 {
		t.Fatalf("after failure lifecycle=%s intents=%d deleted events=%d", lifecycle, intents, deletedEvents)
	}

	restarted := newPostgresKnowledgeBaseService(t, pool, purger)
	purged, err := restarted.Purge(ctx, created.ID, *permit)
	if err != nil || purged.Lifecycle != Deleted || purger.calls != 2 {
		t.Fatalf("restart purge=%#v calls=%d err=%v", purged, purger.calls, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM artifact_deletion_intents WHERE scope_id=$1`, pgUUID(created.ID)).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("remaining intents=%d err=%v", intents, err)
	}
}

func TestKnowledgeBaseTerminalPurgeBeforeIntentRestoresRetryableLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		archived  bool
		terminate func(context.Context, *testing.T, *pgxpool.Pool, *jobs.Store, jobs.JobID)
		status    jobs.Status
	}{
		{
			name: "failed purge restores active",
			terminate: func(ctx context.Context, t *testing.T, pool *pgxpool.Pool, queue *jobs.Store, id jobs.JobID) {
				makePurgeClaimable(t, ctx, pool, id)
				permit, err := queue.Claim(ctx, "purge-failure", time.Minute)
				if err != nil || permit == nil || permit.JobID != id {
					t.Fatalf("claim purge: %#v, %v", permit, err)
				}
				if err = queue.Fail(ctx, *permit, "purge failed"); err != nil {
					t.Fatalf("fail purge: %v", err)
				}
			},
			status: jobs.Failed,
		},
		{
			name:     "cancelled purge restores archived",
			archived: true,
			terminate: func(ctx context.Context, t *testing.T, _ *pgxpool.Pool, queue *jobs.Store, id jobs.JobID) {
				status, err := queue.RequestCancel(ctx, id)
				if err != nil || status != jobs.Cancelled {
					t.Fatalf("cancel purge: %s, %v", status, err)
				}
			},
			status: jobs.Cancelled,
		},
		{
			name: "exhausted purge restores active",
			terminate: func(ctx context.Context, t *testing.T, pool *pgxpool.Pool, queue *jobs.Store, id jobs.JobID) {
				makePurgeClaimable(t, ctx, pool, id)
				permit, err := queue.Claim(ctx, "expired-purge", time.Minute)
				if err != nil || permit == nil || permit.JobID != id {
					t.Fatalf("claim purge: %#v, %v", permit, err)
				}
				if _, err = pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, pgUUID(ID(id))); err != nil {
					t.Fatal(err)
				}
				if next, claimErr := queue.Claim(ctx, "purge-reaper", time.Minute); claimErr != nil || next != nil {
					t.Fatalf("expire purge: %#v, %v", next, claimErr)
				}
			},
			status: jobs.Failed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := postgresKnowledgeBasePool(t)
			ctx := context.Background()
			service := newPostgresKnowledgeBaseService(t, pool, nil)
			actor := auth.OperatorID{7}
			name, err := ParseName("Recoverable purge")
			if err != nil {
				t.Fatal(err)
			}
			created, err := service.Create(ctx, CreateCommand{
				Name: name, Access: Restricted, Instructions: "Recover safely.", Language: "en",
			}, actor, "create")
			if err != nil {
				t.Fatal(err)
			}
			current := created
			if test.archived {
				lifecycle := Archived
				current, err = service.Update(ctx, UpdateCommand{
					KnowledgeBaseID: created.ID, ExpectedVersion: created.Version, Lifecycle: &lifecycle,
				}, actor, "archive")
				if err != nil {
					t.Fatal(err)
				}
			}
			deletion, err := service.RequestDelete(ctx, DeleteCommand{
				KnowledgeBaseID: current.ID, ExpectedVersion: current.Version, ConfirmationName: current.Name,
			}, actor, "delete")
			if err != nil {
				t.Fatal(err)
			}
			queue := jobs.NewStore(pool, TerminalCallback)
			test.terminate(ctx, t, pool, queue, deletion.PurgeJobID)

			recovered, err := service.Get(ctx, current.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantLifecycle := Active
			if test.archived {
				wantLifecycle = Archived
			}
			if recovered.Lifecycle != wantLifecycle || recovered.Version != deletion.KnowledgeBase.Version+1 ||
				recovered.DeleteRequestedAt != nil || recovered.PurgeAfter != nil ||
				(test.archived && recovered.ArchivedAt == nil) {
				t.Fatalf("recovered = %#v", recovered)
			}
			var status string
			if err = pool.QueryRow(ctx, `SELECT status FROM jobs WHERE id=$1`, pgUUID(ID(deletion.PurgeJobID))).Scan(&status); err != nil || jobs.Status(status) != test.status {
				t.Fatalf("purge job status=%s err=%v", status, err)
			}
			var intents int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM artifact_deletion_intents WHERE resource_id=$1`, pgUUID(current.ID)).Scan(&intents); err != nil || intents != 0 {
				t.Fatalf("artifact deletion intents=%d err=%v", intents, err)
			}
			retry, err := service.RequestDelete(ctx, DeleteCommand{
				KnowledgeBaseID: recovered.ID, ExpectedVersion: recovered.Version, ConfirmationName: recovered.Name,
			}, actor, "delete-again")
			if err != nil || retry.PurgeJobID == deletion.PurgeJobID {
				t.Fatalf("retry deletion=%#v err=%v", retry, err)
			}
		})
	}
}

func TestKnowledgeBaseCommittedPurgeIntentSurvivesPartialFailureAndRestart(t *testing.T) {
	pool := postgresKnowledgeBasePool(t)
	ctx := context.Background()
	purger := &recordingPurger{failures: 1}
	service := newPostgresKnowledgeBaseService(t, pool, purger)
	actor := auth.OperatorID{8}
	name, err := ParseName("Committed purge recovery")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateCommand{
		Name: name, Access: Restricted, Instructions: "Delete durably.", Language: "en",
	}, actor, "create")
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := service.RequestDelete(ctx, DeleteCommand{
		KnowledgeBaseID: created.ID, ExpectedVersion: created.Version, ConfirmationName: created.Name,
	}, actor, "delete")
	if err != nil {
		t.Fatal(err)
	}
	childID := ID{15: 9}
	if _, err = pool.Exec(ctx, `
		INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
		VALUES('FAILED_DRAFT',$1,$2,$2)
	`, pgUUID(childID), pgUUID(created.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE knowledge_bases SET purge_after=clock_timestamp()-interval '1 second' WHERE id=$1`, pgUUID(created.ID)); err != nil {
		t.Fatal(err)
	}
	makePurgeClaimable(t, ctx, pool, deletion.PurgeJobID)
	queue := jobs.NewStore(pool, TerminalCallback)
	permit, err := queue.Claim(ctx, "partial-purge", time.Minute)
	if err != nil || permit == nil || permit.JobID != deletion.PurgeJobID {
		t.Fatalf("claim purge: %#v, %v", permit, err)
	}
	if _, err = service.Purge(ctx, created.ID, *permit); err == nil || err.Error() != "injected purge failure" || !purger.partial {
		t.Fatalf("partial purge error=%v partial=%t", err, purger.partial)
	}
	if err = queue.Fail(ctx, *permit, "purge failed after partial artifact deletion"); err != nil {
		t.Fatal(err)
	}

	pending, err := service.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Lifecycle != PendingDelete || pending.Version != deletion.KnowledgeBase.Version ||
		pending.DeleteRequestedAt == nil || pending.PurgeAfter == nil {
		t.Fatalf("pending recovery = %#v", pending)
	}
	var intents int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM artifact_deletion_intents WHERE scope_id=$1`, pgUUID(created.ID)).Scan(&intents); err != nil || intents != 2 {
		t.Fatalf("durable intents=%d err=%v", intents, err)
	}
	if _, err = service.Restore(ctx, RestoreCommand{
		KnowledgeBaseID: created.ID, ExpectedVersion: pending.Version,
	}, actor, "restore-after-intent"); !errors.Is(err, ErrConflict) || err.Error() != "knowledge base purge has started" {
		t.Fatalf("restore after committed intent error=%v", err)
	}
	var recoveryID pgtype.UUID
	var recoveryStatus string
	if err = pool.QueryRow(ctx, `
		SELECT id,status FROM jobs WHERE operation_key=$1
		ORDER BY created_at DESC,id DESC LIMIT 1
	`, purgeOperationKey(created.ID)).Scan(&recoveryID, &recoveryStatus); err != nil ||
		!recoveryID.Valid || recoveryID.Bytes == [16]byte(deletion.PurgeJobID) || jobs.Status(recoveryStatus) != jobs.Pending {
		t.Fatalf("recovery job=%v status=%s err=%v", recoveryID, recoveryStatus, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE jobs SET not_before=clock_timestamp()-interval '1 second' WHERE id=$1`, recoveryID); err != nil {
		t.Fatal(err)
	}

	restarted := newPostgresKnowledgeBaseService(t, pool, purger)
	recoveryPermit, err := queue.Claim(ctx, "restart-purge", time.Minute)
	if err != nil || recoveryPermit == nil || recoveryPermit.JobID != jobs.JobID(recoveryID.Bytes) {
		t.Fatalf("claim recovery: %#v, %v", recoveryPermit, err)
	}
	purged, err := restarted.Purge(ctx, created.ID, *recoveryPermit)
	if err != nil || purged.Lifecycle != Deleted || purger.calls != 2 {
		t.Fatalf("restarted purge=%#v calls=%d err=%v", purged, purger.calls, err)
	}
	if err = queue.CompleteAcceptedResult(ctx, *recoveryPermit, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM artifact_deletion_intents WHERE scope_id=$1`, pgUUID(created.ID)).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("remaining intents=%d err=%v", intents, err)
	}
}

func makePurgeClaimable(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id jobs.JobID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE jobs SET not_before=clock_timestamp()-interval '1 second', max_attempts=1 WHERE id=$1
	`, pgUUID(ID(id))); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeBasePostgresEventFailureRollsBackDeleteAndJob(t *testing.T) {
	pool := postgresKnowledgeBasePool(t)
	ctx := context.Background()
	service := newPostgresKnowledgeBaseService(t, pool, nil)
	actor := auth.OperatorID{4}
	name, err := ParseName("Rollback lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateCommand{Name: name, Access: Restricted, Language: "en"}, actor, "create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE event_log ADD CONSTRAINT reject_delete_event CHECK (event_type <> 'knowledge_base.pending_delete')`); err != nil {
		t.Fatal(err)
	}
	_, err = service.RequestDelete(ctx, DeleteCommand{
		KnowledgeBaseID: created.ID, ExpectedVersion: 1, ConfirmationName: created.Name,
	}, actor, "delete")
	if err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	value, err := service.Get(ctx, created.ID)
	if err != nil || value.Version != 1 || value.Lifecycle != Active {
		t.Fatalf("rolled-back value = %#v, %v", value, err)
	}
	var jobsCount, audits, idempotencyRecords int
	if err = pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM jobs),
		       (SELECT count(*) FROM audit_events),
		       (SELECT count(*) FROM idempotency_records)
	`).Scan(&jobsCount, &audits, &idempotencyRecords); err != nil || jobsCount != 0 || audits != 1 || idempotencyRecords != 1 {
		t.Fatalf("rollback counts jobs=%d audit=%d idempotency=%d err=%v", jobsCount, audits, idempotencyRecords, err)
	}
}

type recordingPurger struct {
	knowledgeBaseID ID
	sourceIDs       []ID
	calls           int
	failures        int
	partial         bool
}

func (purger *recordingPurger) Purge(_ context.Context, id ID, sourceIDs []ID) error {
	purger.calls++
	purger.knowledgeBaseID = id
	purger.sourceIDs = append([]ID(nil), sourceIDs...)
	if purger.failures > 0 {
		purger.failures--
		purger.partial = true
		return errors.New("injected purge failure")
	}
	return nil
}

func newPostgresKnowledgeBaseService(t *testing.T, pool *pgxpool.Pool, purger ArtifactPurger) *Service {
	t.Helper()
	vault, err := security.NewCredentialVault(oracleKey, "")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(pool, vault, 72*time.Hour, purger)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func postgresKnowledgeBasePool(t *testing.T) *pgxpool.Pool {
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
	schema := "knowledge_base_test_" + hex.EncodeToString(random[:])
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
	config.MaxConns = 12
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
		`CREATE TABLE model_profiles (id uuid PRIMARY KEY, endpoint_id uuid NOT NULL, current_version_id uuid)`,
		`CREATE TABLE model_profile_versions (id uuid PRIMARY KEY, max_concurrent_tasks integer NOT NULL)`,
		`CREATE TABLE provider_call_leases (id uuid PRIMARY KEY, endpoint_id uuid NOT NULL, expires_at timestamptz NOT NULL)`,
		`CREATE TABLE knowledge_bases (
			id uuid PRIMARY KEY, name varchar(255) NOT NULL, name_key varchar(255) NOT NULL,
			access_policy varchar(16) NOT NULL, lifecycle varchar(24) NOT NULL,
			instructions text NOT NULL, language varchar(35) NOT NULL, published_wiki_id uuid,
			archived_at timestamptz, delete_requested_at timestamptz, purge_after timestamptz,
			deleted_at timestamptz, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
			version integer NOT NULL
		)`,
		`CREATE UNIQUE INDEX knowledge_bases_live_name ON knowledge_bases(name_key) WHERE deleted_at IS NULL`,
		`CREATE TABLE sources (
			id uuid PRIMARY KEY, knowledge_base_id uuid NOT NULL,
			privacy varchar(16) NOT NULL, lifecycle varchar(16) NOT NULL
		)`,
		`CREATE TABLE artifact_deletion_intents (
			kind varchar(32) NOT NULL, resource_id uuid NOT NULL,
			owner_id uuid NOT NULL, scope_id uuid NOT NULL,
			created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
			PRIMARY KEY(kind, resource_id)
		)`,
		`CREATE TABLE jobs (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(), job_type varchar(32) NOT NULL,
			target_type varchar(64) NOT NULL, target_id uuid NOT NULL, payload jsonb NOT NULL DEFAULT '{}',
			operation_key varchar(512) NOT NULL, concurrency_key varchar(512) NOT NULL DEFAULT '',
			concurrency_limit integer NOT NULL DEFAULT 0, status varchar(24) NOT NULL DEFAULT 'PENDING',
			attempt_count integer NOT NULL DEFAULT 0, max_attempts integer NOT NULL DEFAULT 3,
			progress integer NOT NULL DEFAULT 0, lease_owner varchar(255), lease_expires_at timestamptz,
			lease_generation bigint NOT NULL DEFAULT 0, not_before timestamptz, result jsonb,
			sanitized_error text, created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
			started_at timestamptz, finished_at timestamptz
		)`,
		`CREATE UNIQUE INDEX jobs_active_operation ON jobs(operation_key) WHERE status IN ('PENDING','LEASED','RETRY_WAIT','CANCEL_REQUESTED')`,
		`CREATE TABLE job_attempts (
			job_id uuid NOT NULL, attempt_number integer NOT NULL, lease_generation bigint NOT NULL,
			worker_id varchar(255) NOT NULL, heartbeat_at timestamptz NOT NULL, outcome varchar(32),
			sanitized_error text, started_at timestamptz NOT NULL, finished_at timestamptz,
			PRIMARY KEY(job_id, attempt_number), UNIQUE(job_id, lease_generation)
		)`,
		`CREATE TABLE job_events (
			sequence bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, job_id uuid NOT NULL,
			attempt_number integer, event_kind varchar(32) NOT NULL, status varchar(24) NOT NULL,
			payload jsonb NOT NULL, created_at timestamptz NOT NULL
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
			request_digest bytea NOT NULL, result_type varchar(64) NOT NULL,
			result_id uuid NOT NULL, created_at timestamptz NOT NULL,
			expires_at timestamptz NOT NULL, UNIQUE(scope, request_key)
		)`,
	}
	for _, statement := range statements {
		if _, err = pool.Exec(ctx, statement); err != nil {
			t.Fatalf("create knowledge-base test schema: %v", err)
		}
	}
	return pool
}
