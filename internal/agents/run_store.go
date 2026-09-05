package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type RunPageCursor struct {
	CreatedAt time.Time
	RunID     RunID
}

type RunSummary struct {
	ID                   RunID
	AgentID              AgentID
	AgentVersionID       VersionID
	AgentResourceVersion int32
	AgentVersionNumber   int32
	Origin               Origin
	Subject              string
	Outcome              CompletionStatus
	Usage                map[string]int
	LatencyMS            int
	CreatedAt            time.Time
	CompletedAt          time.Time
}

type RunPage struct {
	Runs       []RunSummary
	NextCursor *RunPageCursor
}

type RunKnowledgeBase struct {
	Position             int32
	KnowledgeBaseID      KnowledgeBaseID
	KnowledgeBaseVersion int32
	AccessPolicy         AccessPolicy
	WikiVersionID        WikiVersionID
	DocumentationRunID   DocumentationRunID
	SourceRevisionIDs    []SourceRevisionID
	SourceScopeDigest    [32]byte
}

type RunDetail struct {
	RunSummary
	ModelProfileID                       ModelProfileID
	ModelProfileVersionID                ModelProfileVersionID
	ModelProfileVersionNumber            int32
	ProviderEndpointID                   ProviderEndpointID
	CapturedEndpointConfigurationVersion int32
	CapturedCredentialID                 *CredentialID
	CapturedCredentialVersion            *int32
	EffectiveAccess                      AccessPolicy
	ToolCalls                            []string
	Citations                            []Citation
	SanitizedError                       *string
	KnowledgeBases                       []RunKnowledgeBase
}

func (store *Store) ListRuns(ctx context.Context, agentID AgentID, after *RunPageCursor, limit int) (RunPage, error) {
	if zeroID(ID(agentID)) || limit < 1 || limit > MaxRunPageSize {
		return RunPage{}, invalid("agent run page is invalid")
	}
	query := `
		SELECT id,agent_id,agent_version_id,agent_resource_version,agent_version_number,
		       origin,subject,outcome,model_usage,latency_ms,created_at,completed_at
		FROM agent_runs WHERE agent_id=$1`
	args := []any{pgUUID(ID(agentID))}
	if after != nil {
		if after.CreatedAt.IsZero() || zeroID(ID(after.RunID)) {
			return RunPage{}, invalid("agent run cursor is invalid")
		}
		query += ` AND (created_at,id)<($2,$3)`
		args = append(args, after.CreatedAt, pgUUID(ID(after.RunID)))
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, err := store.pool.Query(ctx, query, args...)
	if err != nil {
		return RunPage{}, err
	}
	values := make([]RunSummary, 0, limit+1)
	for rows.Next() {
		value, scanErr := scanRunSummary(rows)
		if scanErr != nil {
			rows.Close()
			return RunPage{}, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return RunPage{}, err
	}
	rows.Close()
	if len(values) == 0 {
		var exists bool
		if err = store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1)`, pgUUID(ID(agentID))).Scan(&exists); err != nil {
			return RunPage{}, err
		}
		if !exists {
			return RunPage{}, notFound("agent does not exist")
		}
	}
	page := RunPage{Runs: values}
	if len(values) > limit {
		page.Runs = values[:limit]
		last := page.Runs[len(page.Runs)-1]
		page.NextCursor = &RunPageCursor{CreatedAt: last.CreatedAt, RunID: last.ID}
	}
	return page, nil
}

func (store *Store) GetRun(ctx context.Context, agentID AgentID, runID RunID) (RunDetail, error) {
	if zeroID(ID(agentID)) || zeroID(ID(runID)) {
		return RunDetail{}, invalid("agent and run IDs are required")
	}
	var (
		value                                   RunDetail
		storedRunID, storedAgentID, versionID   pgtype.UUID
		profileID, profileVersionID, endpointID pgtype.UUID
		credentialID                            pgtype.UUID
		credentialVersion                       pgtype.Int4
		usageJSON, toolJSON, citationJSON       []byte
		sanitized                               pgtype.Text
	)
	err := store.pool.QueryRow(ctx, `
		SELECT id,agent_id,agent_version_id,agent_resource_version,agent_version_number,
		       model_profile_id,model_profile_version_id,model_profile_version_number,
		       provider_endpoint_id,captured_endpoint_configuration_version,captured_credential_id,captured_credential_version,
		       origin,subject,effective_access_policy,outcome,model_usage,latency_ms,
		       tool_calls,citations,sanitized_error,created_at,completed_at
		FROM agent_runs WHERE id=$1 AND agent_id=$2
	`, pgUUID(ID(runID)), pgUUID(ID(agentID))).Scan(
		&storedRunID, &storedAgentID, &versionID, &value.AgentResourceVersion, &value.AgentVersionNumber,
		&profileID, &profileVersionID, &value.ModelProfileVersionNumber, &endpointID,
		&value.CapturedEndpointConfigurationVersion, &credentialID, &credentialVersion, &value.Origin, &value.Subject,
		&value.EffectiveAccess, &value.Outcome, &usageJSON, &value.LatencyMS, &toolJSON, &citationJSON,
		&sanitized, &value.CreatedAt, &value.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunDetail{}, notFound("agent run does not exist")
	}
	if err != nil {
		return RunDetail{}, err
	}
	value.ID = RunID(storedRunID.Bytes)
	value.AgentID = AgentID(storedAgentID.Bytes)
	value.AgentVersionID = VersionID(versionID.Bytes)
	value.ModelProfileID = ModelProfileID(profileID.Bytes)
	value.ModelProfileVersionID = ModelProfileVersionID(profileVersionID.Bytes)
	value.ProviderEndpointID = ProviderEndpointID(endpointID.Bytes)
	if credentialID.Valid {
		capturedCredentialID := CredentialID(credentialID.Bytes)
		value.CapturedCredentialID = &capturedCredentialID
	}
	value.CapturedCredentialVersion = executionOptionalInt(credentialVersion)
	value.SanitizedError = executionOptionalText(sanitized)
	if err = json.Unmarshal(usageJSON, &value.Usage); err != nil || value.Usage == nil ||
		json.Unmarshal(toolJSON, &value.ToolCalls) != nil || value.ToolCalls == nil ||
		json.Unmarshal(citationJSON, &value.Citations) != nil || value.Citations == nil {
		return RunDetail{}, errors.New("stored agent run metadata is invalid")
	}
	value.KnowledgeBases, err = loadRunKnowledgeBases(ctx, store.pool, runID)
	if err != nil {
		return RunDetail{}, err
	}
	return value, nil
}

func scanRunSummary(row rowScanner) (RunSummary, error) {
	var value RunSummary
	var runID, agentID, versionID pgtype.UUID
	var usageJSON []byte
	if err := row.Scan(
		&runID, &agentID, &versionID, &value.AgentResourceVersion, &value.AgentVersionNumber,
		&value.Origin, &value.Subject, &value.Outcome, &usageJSON, &value.LatencyMS, &value.CreatedAt, &value.CompletedAt,
	); err != nil {
		return RunSummary{}, err
	}
	value.ID = RunID(runID.Bytes)
	value.AgentID = AgentID(agentID.Bytes)
	value.AgentVersionID = VersionID(versionID.Bytes)
	if json.Unmarshal(usageJSON, &value.Usage) != nil || value.Usage == nil {
		return RunSummary{}, errors.New("stored agent run usage is invalid")
	}
	return value, nil
}

func loadRunKnowledgeBases(ctx context.Context, database queryer, runID RunID) ([]RunKnowledgeBase, error) {
	rows, err := database.Query(ctx, `
		SELECT position,knowledge_base_id,knowledge_base_version,access_policy,wiki_version_id,
		       documentation_run_id,source_revision_ids,source_scope_digest
		FROM agent_run_knowledge_bases WHERE run_id=$1 ORDER BY position
	`, pgUUID(ID(runID)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RunKnowledgeBase, 0)
	for rows.Next() {
		var value RunKnowledgeBase
		var knowledgeBaseID, wikiID, documentationRunID pgtype.UUID
		var revisions []pgtype.UUID
		var digest []byte
		if err = rows.Scan(
			&value.Position, &knowledgeBaseID, &value.KnowledgeBaseVersion, &value.AccessPolicy,
			&wikiID, &documentationRunID, &revisions, &digest,
		); err != nil {
			return nil, err
		}
		if len(digest) != len(value.SourceScopeDigest) {
			return nil, errors.New("stored agent run source digest is invalid")
		}
		value.KnowledgeBaseID = KnowledgeBaseID(knowledgeBaseID.Bytes)
		value.WikiVersionID = WikiVersionID(wikiID.Bytes)
		value.DocumentationRunID = DocumentationRunID(documentationRunID.Bytes)
		copy(value.SourceScopeDigest[:], digest)
		value.SourceRevisionIDs = make([]SourceRevisionID, len(revisions))
		for index, revision := range revisions {
			value.SourceRevisionIDs[index] = SourceRevisionID(revision.Bytes)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}
