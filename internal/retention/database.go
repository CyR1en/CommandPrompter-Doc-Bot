package retention

import (
	"context"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (service *Service) deleteAgentRuns(ctx context.Context, tx pgx.Tx, permit jobs.Permit, cutoff time.Time) (int, error) {
	ids, err := selectRetainedIDs(ctx, tx, `
		SELECT id FROM agent_runs
		WHERE completed_at <= $1
		ORDER BY completed_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
	`, cutoff, service.policy.BatchSize)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if err = service.auditEach(ctx, tx, permit, "retention.agent_run_deleted", "agent_run", ids, service.policy.AgentRuns); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM agent_runs WHERE id = ANY($1::uuid[])`, retainedUUIDs(ids)); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (service *Service) deleteExpiredAgentRunReservations(ctx context.Context, tx pgx.Tx, now time.Time) error {
	_, err := tx.Exec(ctx, `
		WITH expired AS (
			SELECT run_id,position
			FROM agent_run_scope_reservations
			WHERE expires_at <= $1
			ORDER BY expires_at,run_id,position
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM agent_run_scope_reservations AS reservation
		USING expired
		WHERE reservation.run_id=expired.run_id AND reservation.position=expired.position
	`, now, service.policy.BatchSize)
	return err
}

func (service *Service) deleteDiscordContext(
	ctx context.Context,
	tx pgx.Tx,
	permit jobs.Permit,
	now time.Time,
	cutoff time.Time,
) (int, error) {
	ids, err := selectRetainedIDs(ctx, tx, `
		SELECT id FROM discord_conversations
		WHERE expires_at <= $1 OR created_at <= $2
		ORDER BY LEAST(expires_at,created_at),id LIMIT $3 FOR UPDATE SKIP LOCKED
	`, now, cutoff, service.policy.BatchSize)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if err = service.auditEach(ctx, tx, permit, "retention.discord_context_deleted",
		"discord_context", ids, service.policy.DiscordContext); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM discord_conversations WHERE id = ANY($1::uuid[])`, retainedUUIDs(ids)); err != nil {
		return 0, err
	}
	return len(ids), nil
}

type retainedWiki struct{ id, knowledgeBaseID retainedID }

func (service *Service) stageOldWikis(ctx context.Context, tx pgx.Tx, cutoff time.Time) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT w.id,w.knowledge_base_id FROM wiki_versions w
		JOIN knowledge_bases kb ON kb.id=w.knowledge_base_id
		WHERE w.published_at <= $1
		  AND kb.lifecycle IN ('ACTIVE','ARCHIVED')
		  AND NOT EXISTS(SELECT 1 FROM knowledge_bases kb WHERE kb.published_wiki_id=w.id)
		  AND NOT EXISTS(SELECT 1 FROM agent_run_knowledge_bases arkb WHERE arkb.wiki_version_id=w.id)
		  AND NOT EXISTS(
			SELECT 1 FROM agent_run_scope_reservations reservation
			WHERE reservation.knowledge_base_id=w.knowledge_base_id
			  AND reservation.wiki_version_id=w.id
			  AND reservation.expires_at > clock_timestamp()
		  )
		  AND NOT EXISTS(
			SELECT 1 FROM artifact_deletion_intents i
			WHERE i.kind='WIKI_VERSION' AND i.resource_id=w.id
		  )
		ORDER BY w.published_at,w.id LIMIT $2 FOR UPDATE OF w,kb SKIP LOCKED
	`, cutoff, service.policy.BatchSize)
	if err != nil {
		return 0, err
	}
	values := []retainedWiki{}
	for rows.Next() {
		var id, knowledgeBaseID pgtype.UUID
		if err = rows.Scan(&id, &knowledgeBaseID); err != nil || !id.Valid || !knowledgeBaseID.Valid {
			rows.Close()
			return 0, invalidRetainedRow(err)
		}
		values = append(values, retainedWiki{retainedID(id.Bytes), retainedID(knowledgeBaseID.Bytes)})
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(values) == 0 {
		return 0, err
	}
	staged := 0
	for _, value := range values {
		result, insertErr := tx.Exec(ctx, `
			INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
			VALUES('WIKI_VERSION',$1,$2,$2) ON CONFLICT DO NOTHING
		`, value.id.pgUUID(), value.knowledgeBaseID.pgUUID())
		if insertErr != nil {
			return 0, insertErr
		}
		staged += int(result.RowsAffected())
	}
	return staged, nil
}

type retainedRun struct{ id, knowledgeBaseID retainedID }

func (service *Service) stageFailedDrafts(ctx context.Context, tx pgx.Tx, cutoff time.Time) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id,r.knowledge_base_id FROM documentation_runs r
		JOIN knowledge_bases kb ON kb.id=r.knowledge_base_id
		WHERE r.status IN ('FAILED','INTERRUPTED') AND r.completed_at <= $1
		  AND kb.lifecycle IN ('ACTIVE','ARCHIVED')
		  AND NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.action='retention.failed_draft_deleted' AND a.target_id=r.id)
		  AND NOT EXISTS(SELECT 1 FROM artifact_deletion_intents i WHERE i.kind='FAILED_DRAFT' AND i.resource_id=r.id)
		ORDER BY r.completed_at,r.id LIMIT $2 FOR UPDATE OF r,kb SKIP LOCKED
	`, cutoff, service.policy.BatchSize)
	if err != nil {
		return 0, err
	}
	values := []retainedRun{}
	for rows.Next() {
		var id, knowledgeBaseID pgtype.UUID
		if err = rows.Scan(&id, &knowledgeBaseID); err != nil || !id.Valid || !knowledgeBaseID.Valid {
			rows.Close()
			return 0, invalidRetainedRow(err)
		}
		values = append(values, retainedRun{retainedID(id.Bytes), retainedID(knowledgeBaseID.Bytes)})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	staged := 0
	for _, value := range values {
		result, insertErr := tx.Exec(ctx, `
			INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
			VALUES('FAILED_DRAFT',$1,$2,$2) ON CONFLICT DO NOTHING
		`, value.id.pgUUID(), value.knowledgeBaseID.pgUUID())
		if insertErr != nil {
			return 0, insertErr
		}
		staged += int(result.RowsAffected())
	}
	return staged, nil
}

func (service *Service) deleteJobLogs(ctx context.Context, tx pgx.Tx, permit jobs.Permit, cutoff time.Time) (int, error) {
	ids, err := selectRetainedIDs(ctx, tx, `
		SELECT j.id FROM jobs j
		WHERE j.status IN ('SUCCEEDED','FAILED','CANCELLED') AND j.finished_at <= $1
		  AND (EXISTS(SELECT 1 FROM job_events e WHERE e.job_id=j.id)
		       OR EXISTS(SELECT 1 FROM job_attempts a WHERE a.job_id=j.id))
		ORDER BY j.finished_at,j.id LIMIT $2 FOR UPDATE OF j SKIP LOCKED
	`, cutoff, service.policy.BatchSize)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	if err = service.auditEach(ctx, tx, permit, "retention.job_logs_deleted", "job", ids, service.policy.JobLogs); err != nil {
		return 0, err
	}
	values := retainedUUIDs(ids)
	if _, err = tx.Exec(ctx, `DELETE FROM job_events WHERE job_id = ANY($1::uuid[])`, values); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM job_attempts WHERE job_id = ANY($1::uuid[])`, values); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (service *Service) deleteEventLog(ctx context.Context, tx pgx.Tx, cutoff time.Time) (int, error) {
	var deletedCount int
	err := tx.QueryRow(ctx, `
		WITH first_retained AS (
			SELECT sequence FROM event_log
			WHERE created_at > $1
			ORDER BY sequence LIMIT 1
		), candidates AS (
			SELECT sequence FROM event_log
			WHERE created_at <= $1
			  AND sequence < COALESCE((SELECT sequence FROM first_retained), 9223372036854775807)
			ORDER BY sequence LIMIT $2
		), deleted AS (
			DELETE FROM event_log
			WHERE sequence IN (SELECT sequence FROM candidates)
			RETURNING sequence
		), watermark AS (
			SELECT MAX(sequence) AS sequence FROM deleted
		), updated AS (
			UPDATE event_stream_state
			SET pruned_through=GREATEST(pruned_through,COALESCE((SELECT sequence FROM watermark),pruned_through)),
			    updated_at=clock_timestamp()
			WHERE id=1 AND EXISTS(SELECT 1 FROM deleted)
			RETURNING id
		)
		SELECT count(*) FROM deleted
	`, cutoff, service.policy.BatchSize).Scan(&deletedCount)
	if err != nil {
		return 0, err
	}
	return deletedCount, nil
}

func (service *Service) releaseOldRunSources(ctx context.Context, tx pgx.Tx, permit jobs.Permit, cutoff time.Time) error {
	ids, err := selectRetainedIDs(ctx, tx, `
		SELECT r.id FROM documentation_runs r
		WHERE r.status IN ('NO_OP','PUBLISHED','INTERRUPTED','FAILED')
		  AND r.completed_at <= $1
		  AND EXISTS(SELECT 1 FROM documentation_run_sources s WHERE s.run_id=r.id)
		  AND NOT EXISTS(SELECT 1 FROM wiki_versions w WHERE w.documentation_run_id=r.id)
		ORDER BY r.completed_at,r.id LIMIT $2 FOR UPDATE OF r SKIP LOCKED
	`, cutoff, service.policy.BatchSize)
	if err != nil || len(ids) == 0 {
		return err
	}
	if err = service.auditEach(ctx, tx, permit, "retention.run_source_details_deleted", "documentation_run", ids, service.policy.SourceSnapshots); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM documentation_run_sources WHERE run_id = ANY($1::uuid[])`, retainedUUIDs(ids))
	return err
}

type retainedRevision struct{ id, sourceID, knowledgeBaseID retainedID }

func (service *Service) stageSourceSnapshots(ctx context.Context, tx pgx.Tx, cutoff time.Time) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id,r.source_id,kb.id FROM source_revisions r
		JOIN sources s0 ON s0.id=r.source_id
		JOIN knowledge_bases kb ON kb.id=s0.knowledge_base_id
		WHERE r.created_at <= $1 AND r.artifact_purged_at IS NULL
		  AND kb.lifecycle IN ('ACTIVE','ARCHIVED')
		  AND NOT EXISTS(SELECT 1 FROM sources s WHERE s.current_revision_id=r.id)
		  AND NOT EXISTS(SELECT 1 FROM documentation_run_sources d WHERE d.source_revision_id=r.id)
		  AND NOT EXISTS(SELECT 1 FROM evidence e WHERE e.source_revision_id=r.id)
		  AND NOT EXISTS(SELECT 1 FROM website_revision_pages p WHERE p.reused_from_revision_id=r.id)
		  AND NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.action='retention.source_snapshot_deleted' AND a.target_id=r.id)
		  AND NOT EXISTS(SELECT 1 FROM artifact_deletion_intents i WHERE i.kind='SOURCE_SNAPSHOT' AND i.resource_id=r.id)
		ORDER BY r.created_at,r.id LIMIT $2 FOR UPDATE OF r,kb SKIP LOCKED
	`, cutoff, service.policy.BatchSize)
	if err != nil {
		return 0, err
	}
	values := []retainedRevision{}
	for rows.Next() {
		var id, sourceID, knowledgeBaseID pgtype.UUID
		if err = rows.Scan(&id, &sourceID, &knowledgeBaseID); err != nil || !id.Valid || !sourceID.Valid || !knowledgeBaseID.Valid {
			rows.Close()
			return 0, invalidRetainedRow(err)
		}
		values = append(values, retainedRevision{retainedID(id.Bytes), retainedID(sourceID.Bytes), retainedID(knowledgeBaseID.Bytes)})
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(values) == 0 {
		return 0, err
	}
	staged := 0
	for _, value := range values {
		result, insertErr := tx.Exec(ctx, `
			INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
			VALUES('SOURCE_SNAPSHOT',$1,$2,$3) ON CONFLICT DO NOTHING
		`, value.id.pgUUID(), value.sourceID.pgUUID(), value.knowledgeBaseID.pgUUID())
		if insertErr != nil {
			return 0, insertErr
		}
		staged += int(result.RowsAffected())
	}
	return staged, nil
}

func selectRetainedIDs(ctx context.Context, tx pgx.Tx, query string, arguments ...any) ([]retainedID, error) {
	rows, err := tx.Query(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []retainedID{}
	for rows.Next() {
		var id pgtype.UUID
		if err = rows.Scan(&id); err != nil || !id.Valid {
			return nil, invalidRetainedRow(err)
		}
		values = append(values, retainedID(id.Bytes))
	}
	return values, rows.Err()
}

func invalidRetainedRow(err error) error {
	if err != nil {
		return err
	}
	return pgx.ErrNoRows
}
