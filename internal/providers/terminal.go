package providers

import (
	"context"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// TerminalCallback closes provider capture runs left pending or running when
// their durable job can no longer be retried.
func TerminalCallback(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	switch job.Type {
	case jobs.DiscoverEndpoint:
		return failDiscoveryJob(ctx, tx, job)
	case jobs.ProbeModel:
		return failProbeJob(ctx, tx, job)
	default:
		return nil
	}
}

func failDiscoveryJob(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	var runID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM discovery_runs WHERE job_id=$1`, uuid(job.ID)).Scan(&runID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	run, err := discoveryRunTx(ctx, tx, DiscoveryRunID(runID.Bytes), true)
	if err != nil || run.Status.Terminal() {
		return err
	}
	if job.TargetType != "provider_endpoint" || job.TargetID != jobs.UUID(run.EndpointID) {
		return errors.New("provider discovery terminal job target is invalid")
	}
	sanitized := "provider_discovery:job_failed"
	if job.Status == jobs.Cancelled {
		sanitized = "provider_discovery:cancelled"
	}
	if _, err = tx.Exec(ctx, `
		UPDATE discovery_runs
		SET status='FAILED', model_ids='[]'::jsonb, raw_response=NULL,
		    tls_verified=NULL, authentication_succeeded=NULL, http_status=NULL,
		    response_sha256=NULL, model_count=NULL, sanitized_error=$2,
		    started_at=coalesce(started_at,$3), completed_at=$3
		WHERE id=$1
	`, uuid(run.ID), sanitized, providerTerminalTime(job)); err != nil {
		return err
	}
	completed, err := discoveryRunTx(ctx, tx, run.ID, false)
	if err != nil {
		return err
	}
	return recordCaptureRun(ctx, tx, completed, nil, nil, "discovery.failed")
}

func failProbeJob(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	var runID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM probe_runs WHERE job_id=$1`, uuid(job.ID)).Scan(&runID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	run, err := probeRunTx(ctx, tx, ProbeRunID(runID.Bytes), true)
	if err != nil || run.Status.Terminal() {
		return err
	}
	if job.TargetType != "model_profile" || job.TargetID != jobs.UUID(run.ProfileID) {
		return errors.New("provider probe terminal job target is invalid")
	}
	sanitized := "provider_probe:job_failed"
	if job.Status == jobs.Cancelled {
		sanitized = "provider_probe:cancelled"
	}
	if _, err = tx.Exec(ctx, `
		UPDATE probe_runs
		SET status='FAILED', findings=NULL, raw_response=NULL,
		    sanitized_error=$2, resulting_version_id=NULL,
		    started_at=coalesce(started_at,$3), completed_at=$3
		WHERE id=$1
	`, uuid(run.ID), sanitized, providerTerminalTime(job)); err != nil {
		return err
	}
	completed, err := probeRunTx(ctx, tx, run.ID, false)
	if err != nil {
		return err
	}
	return recordCaptureRun(ctx, tx, DiscoveryRun{}, &completed, nil, "probe.failed")
}

func providerTerminalTime(job jobs.Snapshot) time.Time {
	if job.FinishedAt != nil {
		return *job.FinishedAt
	}
	return job.UpdatedAt
}
