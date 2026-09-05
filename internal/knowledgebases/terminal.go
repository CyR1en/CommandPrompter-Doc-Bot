package knowledgebases

import (
	"context"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const purgeRecoveryDelay = time.Minute

// TerminalCallback recovers a knowledge base whose purge never committed.
// A committed deletion intent is irreversible because filesystem deletion may
// already be partial, so it remains pending and receives a durable recovery
// job. Before that point, the previous active/archived lifecycle is restored.
func TerminalCallback(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	if job.Type != jobs.PurgeKnowledgeBase {
		return nil
	}
	if job.TargetType != "knowledge_base" {
		return errors.New("purge job target is invalid")
	}
	id := ID(job.TargetID)
	var operationKey string
	if err := tx.QueryRow(ctx, `SELECT operation_key FROM jobs WHERE id=$1`, pgUUID(ID(job.ID))).Scan(&operationKey); err != nil {
		return err
	}
	if operationKey != purgeOperationKey(id) {
		return errors.New("purge job target is invalid")
	}
	var latest pgtype.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM jobs WHERE operation_key=$1
		ORDER BY created_at DESC,id DESC LIMIT 1
	`, operationKey).Scan(&latest); err != nil {
		return err
	}
	if !latest.Valid || latest.Bytes != [16]byte(job.ID) {
		return nil
	}
	row, err := readRow(ctx, tx, id, true, true)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if Lifecycle(row.lifecycle) != PendingDelete {
		return nil
	}
	var intentExists bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM artifact_deletion_intents
			WHERE kind='KNOWLEDGE_BASE' AND resource_id=$1
		)
	`, pgUUID(id)).Scan(&intentExists); err != nil {
		return err
	}
	now := job.UpdatedAt
	if job.FinishedAt != nil {
		now = *job.FinishedAt
	}
	if intentExists {
		notBefore := now.Add(purgeRecoveryDelay)
		if _, err = jobs.NewStore(nil, nil).EnqueueTxAt(ctx, tx, jobs.Command{
			Type: jobs.PurgeKnowledgeBase, TargetType: "knowledge_base",
			TargetID: jobs.UUID(id), Payload: map[string]any{},
			OperationKey: purgeOperationKey(id), MaxAttempts: 3,
			NotBefore: &notBefore,
		}, now); err != nil {
			return err
		}
		return recordChange(ctx, tx, row.value(), nil,
			"knowledge_base.purge_recovery_queued", "knowledge_base.purge_recovery_queued")
	}
	archivedAt := timePointer(row.archivedAt)
	target := RestoreLifecycle(archivedAt)
	if _, err = Transition(PendingDelete, target); err != nil {
		return err
	}
	if target == Active {
		archivedAt = nil
	}
	if _, err = tx.Exec(ctx, `
		UPDATE knowledge_bases
		SET lifecycle=$2, archived_at=$3, delete_requested_at=NULL,
		    purge_after=NULL, deleted_at=NULL, updated_at=$4, version=version+1
		WHERE id=$1
	`, pgUUID(id), string(target), archivedAt, now); err != nil {
		return err
	}
	updated, err := readRow(ctx, tx, id, true, false)
	if err != nil {
		return err
	}
	suffix := "failed"
	if job.Status == jobs.Cancelled {
		suffix = "cancelled"
	}
	return recordChange(ctx, tx, updated.value(), nil,
		"knowledge_base.purge_"+suffix, "knowledge_base.purge_"+suffix)
}
