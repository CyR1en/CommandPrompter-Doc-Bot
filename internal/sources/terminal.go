package sources

import (
	"context"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// TerminalCallback closes a source capture that could not reach its normal
// completion path. Retry-wait jobs do not invoke this callback and remain
// restartable through Begin.
func TerminalCallback(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	if job.Type != jobs.ValidateSource && job.Type != jobs.SyncSource {
		return nil
	}
	var syncID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM source_syncs WHERE job_id=$1`, pgUUID(ID(job.ID))).Scan(&syncID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	run, err := getSync(ctx, tx, ID(syncID.Bytes), true)
	if err != nil {
		return err
	}
	expectedKind := Validation
	if job.Type == jobs.SyncSource {
		expectedKind = Synchronization
	}
	if job.TargetType != "source" || job.TargetID != jobs.UUID(run.SourceID) || run.Kind != expectedKind {
		return errors.New("source terminal job target is invalid")
	}
	if run.Status == SyncSucceeded || run.Status == SyncFailed || run.Status == SyncSuperseded {
		return nil
	}
	now := terminalTime(job)
	sanitized := "source_sync:job_failed"
	if job.Type == jobs.ValidateSource {
		sanitized = "source_validation:job_failed"
	}
	if job.Status == jobs.Cancelled {
		sanitized = "source_sync:cancelled"
		if job.Type == jobs.ValidateSource {
			sanitized = "source_validation:cancelled"
		}
	}
	if _, err = tx.Exec(ctx, `
		UPDATE source_syncs
		SET status='FAILED', result_revision_id=NULL, resolved_native_version=NULL,
		    sanitized_error=$2, started_at=coalesce(started_at,$3), completed_at=$3
		WHERE id=$1
	`, pgUUID(run.ID), sanitized, now); err != nil {
		return err
	}
	if job.Status == jobs.Failed {
		updated, updateErr := tx.Exec(ctx, `
			UPDATE sources
			SET health='UNHEALTHY', sanitized_error=$2, checked_at=$3,
			    updated_at=$3, version=version+1
			WHERE id=$1 AND version=$4
		`, pgUUID(run.SourceID), sanitized, now, run.CapturedSourceVersion)
		if updateErr != nil {
			return updateErr
		}
		if updated.RowsAffected() == 1 {
			source, readErr := getSource(ctx, tx, run.SourceID, false)
			if readErr != nil {
				return readErr
			}
			if err = recordSource(ctx, tx, source, nil, "source_sync.failed", "source.health_updated"); err != nil {
				return err
			}
		}
	}
	completed, err := getSync(ctx, tx, run.ID, false)
	if err != nil {
		return err
	}
	return recordSync(ctx, tx, completed, nil, "source_sync.failed")
}

func terminalTime(job jobs.Snapshot) time.Time {
	if job.FinishedAt != nil {
		return *job.FinishedAt
	}
	return job.UpdatedAt
}
