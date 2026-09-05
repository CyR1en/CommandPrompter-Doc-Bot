package retention

import (
	"context"
	"errors"
	"fmt"

	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type deletionKind string

const (
	wikiVersionIntent    deletionKind = "WIKI_VERSION"
	failedDraftIntent    deletionKind = "FAILED_DRAFT"
	sourceSnapshotIntent deletionKind = "SOURCE_SNAPSHOT"
)

type deletionIntent struct {
	kind       deletionKind
	resourceID retainedID
	ownerID    retainedID
	scopeID    retainedID
}

func (service *Service) applyDeletionIntents(ctx context.Context, permit jobs.Permit) (map[deletionKind]int, error) {
	counts := map[deletionKind]int{
		wikiVersionIntent: 0, failedDraftIntent: 0, sourceSnapshotIntent: 0,
	}
	rows, err := service.pool.Query(ctx, `
		SELECT kind,resource_id,owner_id,scope_id
		FROM artifact_deletion_intents
		WHERE kind IN ('WIKI_VERSION','FAILED_DRAFT','SOURCE_SNAPSHOT')
		ORDER BY created_at,kind,resource_id
		LIMIT $1
	`, service.policy.BatchSize*3)
	if err != nil {
		return counts, err
	}
	intents := make([]deletionIntent, 0, service.policy.BatchSize*3)
	for rows.Next() {
		var kind string
		var resourceID, ownerID, scopeID pgtype.UUID
		if err = rows.Scan(&kind, &resourceID, &ownerID, &scopeID); err != nil ||
			!resourceID.Valid || !ownerID.Valid || !scopeID.Valid {
			rows.Close()
			return counts, invalidRetainedRow(err)
		}
		intent := deletionIntent{
			kind: deletionKind(kind), resourceID: retainedID(resourceID.Bytes),
			ownerID: retainedID(ownerID.Bytes), scopeID: retainedID(scopeID.Bytes),
		}
		if !intent.kind.valid() {
			rows.Close()
			return counts, errors.New("stored artifact deletion intent kind is invalid")
		}
		intents = append(intents, intent)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return counts, err
	}

	var failures error
	for _, intent := range intents {
		if err = service.checkPermit(ctx, permit); err != nil {
			return counts, errors.Join(failures, err)
		}
		if intent.kind != wikiVersionIntent {
			err = service.discardIntent(intent)
		}
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("discard %s artifact: %w", intent.kind, err))
			continue
		}
		finalized, finalizeErr := service.finalizeIntent(ctx, permit, intent)
		if finalizeErr != nil {
			failures = errors.Join(failures, finalizeErr)
			continue
		}
		if finalized {
			counts[intent.kind]++
		}
	}
	return counts, failures
}

func (kind deletionKind) valid() bool {
	return kind == wikiVersionIntent || kind == failedDraftIntent || kind == sourceSnapshotIntent
}

func (service *Service) checkPermit(ctx context.Context, permit jobs.Permit) error {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.assertPermit(ctx, tx, permit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service *Service) discardIntent(intent deletionIntent) error {
	switch intent.kind {
	case wikiVersionIntent:
		return service.wikiArtifacts.Discard(intent.ownerID.artifactID(), intent.resourceID.artifactID())
	case failedDraftIntent:
		return service.runArtifacts.DiscardRun(intent.ownerID.artifactID(), intent.resourceID.artifactID())
	case sourceSnapshotIntent:
		return service.sourceArtifacts.DiscardSnapshot(intent.ownerID.sourceID(), intent.resourceID.sourceID())
	default:
		return errors.New("stored artifact deletion intent kind is invalid")
	}
}

func (service *Service) finalizeIntent(
	ctx context.Context,
	permit jobs.Permit,
	intent deletionIntent,
) (bool, error) {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.assertPermit(ctx, tx, permit); err != nil {
		return false, err
	}
	var storedOwner, storedScope pgtype.UUID
	err = tx.QueryRow(ctx, `
		SELECT owner_id,scope_id FROM artifact_deletion_intents
		WHERE kind=$1 AND resource_id=$2 FOR UPDATE
	`, string(intent.kind), intent.resourceID.pgUUID()).Scan(&storedOwner, &storedScope)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if !storedOwner.Valid || !storedScope.Valid || storedOwner.Bytes != [16]byte(intent.ownerID) || storedScope.Bytes != [16]byte(intent.scopeID) {
		return false, errors.New("stored artifact deletion intent changed during execution")
	}
	if intent.kind == wikiVersionIntent {
		eligible, eligibilityErr := service.lockWikiDeletionEligibility(ctx, tx, intent)
		if eligibilityErr != nil {
			return false, eligibilityErr
		}
		if !eligible {
			if err = service.deleteIntent(ctx, tx, intent); err != nil {
				return false, err
			}
			return false, tx.Commit(ctx)
		}
		if err = service.discardIntent(intent); err != nil {
			return false, fmt.Errorf("discard %s artifact: %w", intent.kind, err)
		}
	}
	finalized, err := service.finalizeResource(ctx, tx, permit, intent)
	if err != nil {
		return false, err
	}
	if err = service.deleteIntent(ctx, tx, intent); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return finalized, nil
}

func (service *Service) lockWikiDeletionEligibility(
	ctx context.Context,
	tx pgx.Tx,
	intent deletionIntent,
) (bool, error) {
	var currentlyPublished bool
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(kb.published_wiki_id=w.id,false)
		FROM wiki_versions AS w
		JOIN knowledge_bases AS kb ON kb.id=w.knowledge_base_id
		WHERE w.id=$1 AND w.knowledge_base_id=$2
		FOR UPDATE OF w,kb
	`, intent.resourceID.pgUUID(), intent.ownerID.pgUUID()).Scan(&currentlyPublished)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if currentlyPublished {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `
		WITH expired AS (
			SELECT run_id,position
			FROM agent_run_scope_reservations
			WHERE knowledge_base_id=$1 AND wiki_version_id=$2
			  AND expires_at <= clock_timestamp()
			ORDER BY expires_at,run_id,position
			LIMIT $3
			FOR UPDATE
		)
		DELETE FROM agent_run_scope_reservations AS reservation
		USING expired
		WHERE reservation.run_id=expired.run_id AND reservation.position=expired.position
	`, intent.ownerID.pgUUID(), intent.resourceID.pgUUID(), service.policy.BatchSize); err != nil {
		return false, err
	}
	var reservedByAgentRun bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM agent_run_scope_reservations
			WHERE knowledge_base_id=$1 AND wiki_version_id=$2
		)
	`, intent.ownerID.pgUUID(), intent.resourceID.pgUUID()).Scan(&reservedByAgentRun); err != nil {
		return false, err
	}
	if reservedByAgentRun {
		return false, nil
	}
	var retainedByAgentRun bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM agent_run_knowledge_bases
			WHERE knowledge_base_id=$1 AND wiki_version_id=$2
		)
	`, intent.ownerID.pgUUID(), intent.resourceID.pgUUID()).Scan(&retainedByAgentRun); err != nil {
		return false, err
	}
	return !retainedByAgentRun, nil
}

func (service *Service) finalizeResource(
	ctx context.Context,
	tx pgx.Tx,
	permit jobs.Permit,
	intent deletionIntent,
) (bool, error) {
	var (
		action     string
		targetType string
		duration   = service.policy.SourceSnapshots
		finalized  bool
	)
	switch intent.kind {
	case wikiVersionIntent:
		action, targetType, duration = "retention.wiki_version_deleted", "wiki_version", service.policy.OldWikis
		for _, query := range []string{
			`UPDATE documentation_runs SET prior_wiki_version_id=NULL WHERE prior_wiki_version_id=$1`,
			`UPDATE documentation_runs SET published_wiki_version_id=NULL WHERE published_wiki_version_id=$1`,
		} {
			if _, err := tx.Exec(ctx, query, intent.resourceID.pgUUID()); err != nil {
				return false, err
			}
		}
		result, err := tx.Exec(ctx, `
			DELETE FROM wiki_versions WHERE id=$1 AND knowledge_base_id=$2
		`, intent.resourceID.pgUUID(), intent.ownerID.pgUUID())
		if err != nil {
			return false, err
		}
		finalized = result.RowsAffected() == 1
	case failedDraftIntent:
		action, targetType, duration = "retention.failed_draft_deleted", "documentation_run", service.policy.FailedDrafts
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM documentation_runs
				WHERE id=$1 AND knowledge_base_id=$2 AND status IN ('FAILED','INTERRUPTED')
			)
		`, intent.resourceID.pgUUID(), intent.ownerID.pgUUID()).Scan(&finalized); err != nil {
			return false, err
		}
	case sourceSnapshotIntent:
		action, targetType = "retention.source_snapshot_deleted", "source_revision"
		result, err := tx.Exec(ctx, `
			UPDATE source_revisions SET artifact_purged_at=clock_timestamp()
			WHERE id=$1 AND source_id=$2 AND artifact_purged_at IS NULL
		`, intent.resourceID.pgUUID(), intent.ownerID.pgUUID())
		if err != nil {
			return false, err
		}
		finalized = result.RowsAffected() == 1
	default:
		return false, errors.New("stored artifact deletion intent kind is invalid")
	}
	if !finalized {
		return false, nil
	}
	target := intent.resourceID.auditTarget()
	if err := events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: "system", Action: action, TargetType: targetType,
		TargetID: &target, RequestID: [16]byte(permit.JobID),
		Details: map[string]any{"retention_days": retentionDays(duration)},
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (service *Service) deleteIntent(ctx context.Context, tx pgx.Tx, intent deletionIntent) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM artifact_deletion_intents
		WHERE kind=$1 AND resource_id=$2 AND owner_id=$3 AND scope_id=$4
	`, string(intent.kind), intent.resourceID.pgUUID(), intent.ownerID.pgUUID(), intent.scopeID.pgUUID())
	return err
}
