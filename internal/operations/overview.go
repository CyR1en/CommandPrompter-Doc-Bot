package operations

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func (service *Service) Overview(ctx context.Context) (OperationalOverview, error) {
	value := OperationalOverview{
		UnhealthySources:    []UnhealthySource{},
		FailedJobs:          []FailedJob{},
		KnowledgeBaseIssues: []KnowledgeBaseIssue{},
		ProviderErrors:      []ProviderError{},
		AgentFailures:       []AgentFailure{},
	}
	if err := service.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&value.GeneratedAt); err != nil {
		return OperationalOverview{}, err
	}
	var err error
	if value.UnhealthySources, err = service.unhealthySources(ctx); err != nil {
		return OperationalOverview{}, err
	}
	if value.FailedJobs, err = service.failedJobs(ctx); err != nil {
		return OperationalOverview{}, err
	}
	if value.KnowledgeBaseIssues, err = service.knowledgeBaseIssues(ctx); err != nil {
		return OperationalOverview{}, err
	}
	if value.ProviderErrors, err = service.providerErrors(ctx); err != nil {
		return OperationalOverview{}, err
	}
	if value.AgentFailures, err = service.agentFailures(ctx); err != nil {
		return OperationalOverview{}, err
	}
	return value, nil
}

func (service *Service) unhealthySources(ctx context.Context) ([]UnhealthySource, error) {
	rows, err := service.pool.Query(ctx, `
		SELECT s.id, s.knowledge_base_id, kb.name, s.display_name,
		       lower(s.lifecycle), s.sanitized_error, s.checked_at
		FROM sources AS s
		JOIN knowledge_bases AS kb ON kb.id = s.knowledge_base_id
		WHERE s.health = 'UNHEALTHY' AND s.lifecycle <> 'REMOVED'
		ORDER BY s.checked_at DESC, s.id
		LIMIT $1
	`, overviewLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []UnhealthySource{}
	for rows.Next() {
		var id, knowledgeBaseID pgtype.UUID
		var value UnhealthySource
		if err = rows.Scan(&id, &knowledgeBaseID, &value.KnowledgeBaseName,
			&value.DisplayName, &value.Lifecycle, &value.SanitizedError,
			&value.CheckedAt); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.KnowledgeBaseID, err = requiredUUID(knowledgeBaseID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) failedJobs(ctx context.Context) ([]FailedJob, error) {
	rows, err := service.pool.Query(ctx, `
		SELECT id, lower(job_type), target_type, target_id, attempt_count,
		       max_attempts, sanitized_error, updated_at, finished_at
		FROM jobs WHERE status = 'FAILED'
		ORDER BY updated_at DESC, id
		LIMIT $1
	`, overviewLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []FailedJob{}
	for rows.Next() {
		var id, targetID pgtype.UUID
		var value FailedJob
		if err = rows.Scan(&id, &value.JobType, &value.TargetType, &targetID,
			&value.AttemptCount, &value.MaxAttempts, &value.SanitizedError,
			&value.UpdatedAt, &value.FinishedAt); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.TargetID, err = requiredUUID(targetID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) knowledgeBaseIssues(ctx context.Context) ([]KnowledgeBaseIssue, error) {
	rows, err := service.pool.Query(ctx, `
		SELECT kb.id, kb.name,
		       CASE WHEN kb.published_wiki_id IS NULL THEN 'unpublished' ELSE 'stale' END,
		       kb.published_wiki_id, kb.updated_at
		FROM knowledge_bases AS kb
		LEFT JOIN wiki_versions AS wv ON wv.id = kb.published_wiki_id
		LEFT JOIN documentation_runs AS dr ON dr.id = wv.documentation_run_id
		WHERE kb.lifecycle = 'ACTIVE'
		  AND (
		    kb.published_wiki_id IS NULL
		    OR kb.version IS DISTINCT FROM dr.knowledge_base_version + 1
		    OR COALESCE((
		      SELECT jsonb_object_agg(s.id::text, jsonb_build_array(s.current_revision_id,s.configuration_version))
		      FROM sources AS s
		      WHERE s.knowledge_base_id = kb.id AND s.lifecycle = 'ACTIVE'
		    ), '{}'::jsonb) IS DISTINCT FROM COALESCE((
		      SELECT jsonb_object_agg(drs.source_id::text, jsonb_build_array(drs.source_revision_id,drs.configuration_version))
		      FROM documentation_run_sources AS drs
		      WHERE drs.run_id = wv.documentation_run_id
		    ), '{}'::jsonb)
		    OR COALESCE((
		      SELECT jsonb_object_agg(ma.role, jsonb_build_array(
		        ma.model_profile_id, ma.reasoning_effort, mp.current_version_id,
		        pe.id, pe.configuration_version, c.secret_version,
		        c.deleted_at IS NULL
		      ))
		      FROM model_assignments AS ma
		      JOIN model_profiles AS mp ON mp.id = ma.model_profile_id
		      JOIN provider_endpoints AS pe ON pe.id = mp.endpoint_id
		      LEFT JOIN credentials AS c ON c.id = pe.credential_id
		      WHERE ma.knowledge_base_id = kb.id
		    ), '{}'::jsonb) IS DISTINCT FROM COALESCE((
		      SELECT jsonb_object_agg(drm.role, jsonb_build_array(
		        drm.model_profile_id, drm.reasoning_effort, drm.model_profile_version_id,
		        drm.provider_endpoint_id, drm.captured_endpoint_configuration_version,
		        drm.captured_credential_version, true
		      ))
		      FROM documentation_run_models AS drm
		      WHERE drm.run_id = wv.documentation_run_id
		    ), '{}'::jsonb)
		  )
		ORDER BY kb.updated_at DESC, kb.id
		LIMIT $1
	`, overviewLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []KnowledgeBaseIssue{}
	for rows.Next() {
		var id, published pgtype.UUID
		var value KnowledgeBaseIssue
		if err = rows.Scan(&id, &value.Name, &value.Kind, &published, &value.UpdatedAt); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		value.PublishedWikiID = optionalUUID(published)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) providerErrors(ctx context.Context) ([]ProviderError, error) {
	values := []ProviderError{}
	discoveryRows, err := service.pool.Query(ctx, `
		SELECT pe.id, pe.display_name, d.id, d.sanitized_error,
		       d.completed_at, d.created_at
		FROM discovery_runs AS d
		JOIN provider_endpoints AS pe ON pe.id = d.endpoint_id
		WHERE d.status = 'FAILED' AND d.sanitized_error IS NOT NULL
		ORDER BY d.completed_at DESC
		LIMIT $1
	`, overviewLimit)
	if err != nil {
		return nil, err
	}
	for discoveryRows.Next() {
		value, scanErr := scanProviderError(discoveryRows, "discovery")
		if scanErr != nil {
			discoveryRows.Close()
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err = discoveryRows.Err(); err != nil {
		discoveryRows.Close()
		return nil, err
	}
	discoveryRows.Close()

	probeRows, err := service.pool.Query(ctx, `
		SELECT pe.id, pe.display_name, p.id, p.sanitized_error,
		       p.completed_at, p.created_at
		FROM probe_runs AS p
		JOIN model_profiles AS mp ON mp.id = p.model_profile_id
		JOIN provider_endpoints AS pe ON pe.id = mp.endpoint_id
		WHERE p.status = 'FAILED' AND p.sanitized_error IS NOT NULL
		ORDER BY p.completed_at DESC
		LIMIT $1
	`, overviewLimit)
	if err != nil {
		return nil, err
	}
	defer probeRows.Close()
	for probeRows.Next() {
		value, scanErr := scanProviderError(probeRows, "probe")
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err = probeRows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(values, func(left, right int) bool {
		return values[left].OccurredAt.After(values[right].OccurredAt)
	})
	if len(values) > overviewLimit {
		values = values[:overviewLimit]
	}
	return values, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanProviderError(row rowScanner, operation string) (ProviderError, error) {
	var endpointID, runID pgtype.UUID
	var completedAt *time.Time
	var value ProviderError
	var createdAt time.Time
	if err := row.Scan(&endpointID, &value.EndpointName, &runID,
		&value.SanitizedError, &completedAt, &createdAt); err != nil {
		return ProviderError{}, err
	}
	var err error
	if value.EndpointID, err = requiredUUID(endpointID); err != nil {
		return ProviderError{}, err
	}
	if value.RunID, err = requiredUUID(runID); err != nil {
		return ProviderError{}, err
	}
	value.Operation = operation
	value.OccurredAt = createdAt
	if completedAt != nil {
		value.OccurredAt = *completedAt
	}
	return value, nil
}

func (service *Service) agentFailures(ctx context.Context) ([]AgentFailure, error) {
	rows, err := service.pool.Query(ctx, `
		SELECT run.id, run.agent_id, agent.agent_key, version.display_name,
		       run.agent_version_number, lower(run.origin), run.sanitized_error,
		       run.created_at, run.completed_at
		FROM agent_runs AS run
		JOIN agents AS agent ON agent.id = run.agent_id
		JOIN agent_versions AS version
		  ON version.agent_id = run.agent_id AND version.id = run.agent_version_id
		WHERE run.outcome = 'FAILED' AND run.sanitized_error IS NOT NULL
		ORDER BY run.created_at DESC, run.id
		LIMIT $1
	`, overviewLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []AgentFailure{}
	for rows.Next() {
		var id, agentID pgtype.UUID
		var value AgentFailure
		if err = rows.Scan(&id, &agentID, &value.AgentKey, &value.DisplayName,
			&value.AgentVersionNumber, &value.Origin, &value.SanitizedError,
			&value.CreatedAt, &value.CompletedAt); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.AgentID, err = requiredUUID(agentID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
