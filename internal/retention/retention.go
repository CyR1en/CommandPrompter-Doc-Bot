package retention

import (
	"context"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/cyr1en/ref0/internal/worker"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var TargetID = jobs.UUID{15: 1}

const OperationKey = "retention:apply"

type Policy struct {
	SourceSnapshots time.Duration
	FailedDrafts    time.Duration
	JobLogs         time.Duration
	EventLog        time.Duration
	AgentRuns       time.Duration
	DiscordContext  time.Duration
	OldWikis        time.Duration
	BatchSize       int
}

func (policy Policy) Validate() error {
	if policy.SourceSnapshots <= 0 || policy.FailedDrafts <= 0 || policy.JobLogs <= 0 || policy.EventLog <= 0 ||
		policy.AgentRuns <= 0 || policy.DiscordContext <= 0 || policy.OldWikis <= 0 {
		return errors.New("retention durations must be positive")
	}
	if policy.BatchSize < 1 || policy.BatchSize > 1_000 {
		return errors.New("retention batch size must be between 1 and 1000")
	}
	return nil
}

type SourceArtifacts interface {
	DiscardSnapshot(sourcefiles.ID, sourcefiles.ID) error
}

type RunArtifacts interface {
	DiscardRun(artifacts.ID, artifacts.ID) error
}

type WikiArtifacts interface {
	Discard(artifacts.ID, artifacts.ID) error
}

type Service struct {
	pool            *pgxpool.Pool
	policy          Policy
	sourceArtifacts SourceArtifacts
	runArtifacts    RunArtifacts
	wikiArtifacts   WikiArtifacts
	queue           *jobs.Store
}

func NewService(
	pool *pgxpool.Pool,
	policy Policy,
	sourceArtifacts SourceArtifacts,
	runArtifacts RunArtifacts,
	wikiArtifacts WikiArtifacts,
) (*Service, error) {
	if pool == nil || sourceArtifacts == nil || runArtifacts == nil || wikiArtifacts == nil {
		return nil, errors.New("retention dependencies are incomplete")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Service{
		pool: pool, policy: policy, sourceArtifacts: sourceArtifacts,
		runArtifacts: runArtifacts, wikiArtifacts: wikiArtifacts,
		queue: jobs.NewStore(pool, nil),
	}, nil
}

func (service *Service) Schedule(ctx context.Context) (jobs.JobID, error) {
	return service.queue.Enqueue(ctx, jobs.Command{
		Type: jobs.ApplyRetention, TargetType: "system", TargetID: TargetID,
		Payload: map[string]any{}, OperationKey: OperationKey, MaxAttempts: 3,
	})
}

func (service *Service) Apply(ctx context.Context, permit jobs.Permit) (map[string]any, error) {
	if err := service.cleanupExpiredAgentRunReservations(ctx, permit); err != nil {
		return nil, err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.assertPermit(ctx, tx, permit); err != nil {
		return nil, err
	}
	var databaseTime pgtype.Timestamptz
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseTime); err != nil || !databaseTime.Valid {
		if err == nil {
			err = errors.New("database clock did not return a timestamp")
		}
		return nil, err
	}
	now := databaseTime.Time
	counts := map[string]any{}
	if counts["agent_runs"], err = service.deleteAgentRuns(ctx, tx, permit, now.Add(-service.policy.AgentRuns)); err != nil {
		return nil, err
	}
	if counts["discord_context"], err = service.deleteDiscordContext(
		ctx, tx, permit, now, now.Add(-service.policy.DiscordContext),
	); err != nil {
		return nil, err
	}
	if _, err = service.stageOldWikis(ctx, tx, now.Add(-service.policy.OldWikis)); err != nil {
		return nil, err
	}
	if _, err = service.stageFailedDrafts(ctx, tx, now.Add(-service.policy.FailedDrafts)); err != nil {
		return nil, err
	}
	if counts["job_logs"], err = service.deleteJobLogs(ctx, tx, permit, now.Add(-service.policy.JobLogs)); err != nil {
		return nil, err
	}
	if counts["event_log"], err = service.deleteEventLog(ctx, tx, now.Add(-service.policy.EventLog)); err != nil {
		return nil, err
	}
	if err = service.releaseOldRunSources(ctx, tx, permit, now.Add(-service.policy.SourceSnapshots)); err != nil {
		return nil, err
	}
	if _, err = service.stageSourceSnapshots(ctx, tx, now.Add(-service.policy.SourceSnapshots)); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	deleted, err := service.applyDeletionIntents(ctx, permit)
	if err != nil {
		return nil, err
	}
	counts["old_wikis"] = deleted[wikiVersionIntent]
	counts["failed_drafts"] = deleted[failedDraftIntent]
	counts["source_snapshots"] = deleted[sourceSnapshotIntent]
	tx, err = service.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.assertPermit(ctx, tx, permit); err != nil {
		return nil, err
	}
	target := [16]byte(TargetID)
	if err = events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: "system", Action: "retention.completed", TargetType: "retention",
		TargetID: &target, RequestID: [16]byte(permit.JobID), Details: counts,
	}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return counts, nil
}

func (service *Service) cleanupExpiredAgentRunReservations(ctx context.Context, permit jobs.Permit) error {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.assertPermit(ctx, tx, permit); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if err = service.deleteExpiredAgentRunReservations(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service *Service) assertPermit(ctx context.Context, tx pgx.Tx, permit jobs.Permit) error {
	if err := service.queue.AssertPermit(ctx, tx, permit); err != nil {
		return err
	}
	var jobType, targetType, operationKey string
	var targetID pgtype.UUID
	if err := tx.QueryRow(ctx, `
		SELECT job_type,target_type,target_id,operation_key FROM jobs WHERE id=$1
	`, retentionUUID([16]byte(permit.JobID))).Scan(&jobType, &targetType, &targetID, &operationKey); err != nil {
		return err
	}
	if jobType != string(jobs.ApplyRetention) || targetType != "system" ||
		!targetID.Valid || jobs.UUID(targetID.Bytes) != TargetID || operationKey != OperationKey {
		return errors.New("retention permit target is invalid")
	}
	return nil
}

type retainedID [16]byte

func (id retainedID) artifactID() artifacts.ID   { return artifacts.ID(id) }
func (id retainedID) sourceID() sourcefiles.ID   { return sourcefiles.ID(id) }
func (id retainedID) pgUUID() pgtype.UUID        { return retentionUUID([16]byte(id)) }
func (id retainedID) auditTarget() [16]byte      { return [16]byte(id) }
func retentionUUID(id [16]byte) pgtype.UUID      { return pgtype.UUID{Bytes: id, Valid: true} }
func retentionDays(duration time.Duration) int64 { return int64(duration / (24 * time.Hour)) }
func retainedUUIDs(ids []retainedID) []pgtype.UUID {
	values := make([]pgtype.UUID, len(ids))
	for index, id := range ids {
		values[index] = id.pgUUID()
	}
	return values
}

func (service *Service) auditEach(
	ctx context.Context,
	tx pgx.Tx,
	permit jobs.Permit,
	action, targetType string,
	ids []retainedID,
	duration time.Duration,
) error {
	for _, id := range ids {
		target := id.auditTarget()
		if err := events.AppendAudit(ctx, tx, events.AuditEvent{
			ActorType: "system", Action: action, TargetType: targetType,
			TargetID: &target, RequestID: [16]byte(permit.JobID),
			Details: map[string]any{"retention_days": retentionDays(duration)},
		}); err != nil {
			return err
		}
	}
	return nil
}

func Handlers(service interface {
	Apply(context.Context, jobs.Permit) (map[string]any, error)
}) (worker.Registry, error) {
	if service == nil {
		return nil, errors.New("retention handler dependencies are incomplete")
	}
	return worker.Registry{jobs.ApplyRetention: func(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
		if command.TargetType != "system" || command.TargetID != TargetID || len(command.Payload) != 0 {
			return nil, errors.New("retention command is invalid")
		}
		return service.Apply(ctx, permit)
	}}, nil
}

func RunScheduling(
	ctx context.Context,
	service interface {
		Schedule(context.Context) (jobs.JobID, error)
	},
	scanEvery time.Duration,
	onError func(error),
) error {
	if service == nil || scanEvery <= 0 {
		return errors.New("retention scan interval must be positive")
	}
	ticker := time.NewTicker(scanEvery)
	defer ticker.Stop()
	for {
		if _, err := service.Schedule(ctx); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
