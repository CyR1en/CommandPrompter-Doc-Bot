package agents

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ScopeDescription is the complete non-secret Agent scope metadata shown to
// operators when chat access tokens are issued or listed.
type ScopeDescription struct {
	AgentID          AgentID
	AgentKey         string
	KnowledgeBaseIDs []KnowledgeBaseID
	EffectiveAccess  AccessPolicy
	Ready            bool
}

// DescribeScopes resolves a unique Agent set in one repeatable-read snapshot.
// The fixed pair of set queries avoids per-Agent catalog/readiness reads and
// preserves the caller's input order.
func (store *Store) DescribeScopes(ctx context.Context, agentIDs []AgentID) ([]ScopeDescription, error) {
	if len(agentIDs) == 0 {
		return []ScopeDescription{}, nil
	}
	if !uniqueAgentIDs(agentIDs) {
		return nil, invalid("Agent scope IDs must be unique")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	type scopeState struct {
		description ScopeDescription
		memberships int
	}
	states := make(map[AgentID]*scopeState, len(agentIDs))
	rows, err := tx.Query(ctx, `
		SELECT agent.id,agent.agent_key,agent.lifecycle,agent.current_version_id,
		       version.reasoning_effort,version.answer_mode,version.max_answer_tokens,
		       profile.availability,profile.current_version_id,profile_version.version_number,
		       profile_version.configuration_version,profile_version.transport,
		       profile_version.context_window_tokens,profile_version.max_output_tokens,
		       profile_version.supports_tools,profile_version.reasoning_transport,
		       profile_version.reasoning_mapping,endpoint.id,endpoint.lifecycle,endpoint.health,
		       endpoint.configuration_version,endpoint.credential_id,credential.secret_version,
		       credential.deleted_at
		FROM unnest($1::uuid[]) WITH ORDINALITY requested(id,position)
		JOIN agents agent ON agent.id=requested.id
		JOIN agent_versions version
		  ON version.id=agent.current_version_id AND version.agent_id=agent.id
		LEFT JOIN model_profiles profile ON profile.id=version.model_profile_id
		LEFT JOIN model_profile_versions profile_version
		  ON profile_version.profile_id=profile.id AND profile_version.id=profile.current_version_id
		LEFT JOIN provider_endpoints endpoint ON endpoint.id=profile.endpoint_id
		LEFT JOIN credentials credential ON credential.id=endpoint.credential_id
		ORDER BY requested.position
	`, agentUUIDs(agentIDs))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			storedID, versionID                                                            pgtype.UUID
			key, lifecycle, reasoningEffort, answerMode                                    string
			maxAnswerTokens                                                                int32
			availability, transport, reasoningTransport, endpointLifecycle, endpointHealth pgtype.Text
			profileVersionID, endpointID, credentialID                                     pgtype.UUID
			profileVersionNumber, profileConfiguration, contextTokens, outputTokens        pgtype.Int4
			endpointConfiguration, credentialVersion                                       pgtype.Int4
			supportsTools                                                                  pgtype.Bool
			reasoningMapping                                                               []byte
			credentialDeleted                                                              pgtype.Timestamptz
		)
		if err = rows.Scan(
			&storedID, &key, &lifecycle, &versionID, &reasoningEffort, &answerMode, &maxAnswerTokens,
			&availability, &profileVersionID, &profileVersionNumber, &profileConfiguration,
			&transport, &contextTokens, &outputTokens, &supportsTools, &reasoningTransport,
			&reasoningMapping, &endpointID, &endpointLifecycle, &endpointHealth,
			&endpointConfiguration, &credentialID, &credentialVersion, &credentialDeleted,
		); err != nil {
			rows.Close()
			return nil, err
		}
		id := AgentID(storedID.Bytes)
		modelReady := availability.Valid && availability.String != "UNAVAILABLE" &&
			profileVersionID.Valid && profileVersionNumber.Valid && profileVersionNumber.Int32 > 0 &&
			profileConfiguration.Valid && endpointConfiguration.Valid &&
			profileConfiguration.Int32 == endpointConfiguration.Int32 &&
			transport.Valid && transport.String == "CHAT_COMPLETIONS" &&
			contextTokens.Valid && contextTokens.Int32 > 0 && outputTokens.Valid && outputTokens.Int32 > 0 &&
			maxAnswerTokens <= outputTokens.Int32 && endpointID.Valid &&
			endpointLifecycle.Valid && endpointLifecycle.String == "ACTIVE" &&
			endpointHealth.Valid && endpointHealth.String == "HEALTHY"
		if credentialID.Valid && (!credentialVersion.Valid || credentialVersion.Int32 <= 0 || credentialDeleted.Valid) {
			modelReady = false
		}
		if AnswerMode(answerMode) == ToolCalling && (!supportsTools.Valid || !supportsTools.Bool) {
			modelReady = false
		}
		if !reasoningTransport.Valid || !reasoningSupported(ReasoningEffort(reasoningEffort), reasoningTransport.String, reasoningMapping) {
			modelReady = false
		}
		states[id] = &scopeState{
			description: ScopeDescription{
				AgentID: id, AgentKey: key, EffectiveAccess: Public,
				KnowledgeBaseIDs: []KnowledgeBaseID{}, Ready: Lifecycle(lifecycle) == Active && modelReady,
			},
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(states) != len(agentIDs) {
		return nil, notFound("Agent scope contains an unknown Agent")
	}

	rows, err = tx.Query(ctx, `
		SELECT agent.id,membership.position,membership.knowledge_base_id,
		       knowledge_base.lifecycle,knowledge_base.access_policy,
		       knowledge_base.published_wiki_id,wiki.id
		FROM agents agent
		JOIN agent_version_knowledge_bases membership
		  ON membership.agent_version_id=agent.current_version_id AND membership.agent_id=agent.id
		LEFT JOIN knowledge_bases knowledge_base ON knowledge_base.id=membership.knowledge_base_id
		LEFT JOIN wiki_versions wiki
		  ON wiki.id=knowledge_base.published_wiki_id AND wiki.knowledge_base_id=knowledge_base.id
		WHERE agent.id=ANY($1::uuid[])
		ORDER BY agent.id,membership.position
	`, agentUUIDs(agentIDs))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var agentID, knowledgeBaseID, publishedWikiID, joinedWikiID pgtype.UUID
		var position int32
		var lifecycle, access pgtype.Text
		if err = rows.Scan(
			&agentID, &position, &knowledgeBaseID, &lifecycle, &access, &publishedWikiID, &joinedWikiID,
		); err != nil {
			rows.Close()
			return nil, err
		}
		state := states[AgentID(agentID.Bytes)]
		if state == nil {
			rows.Close()
			return nil, errors.New("Agent scope snapshot contains an unexpected membership")
		}
		if position != int32(state.memberships) || !lifecycle.Valid || lifecycle.String != "ACTIVE" ||
			!access.Valid || !publishedWikiID.Valid || !joinedWikiID.Valid {
			state.description.Ready = false
		}
		id := KnowledgeBaseID(knowledgeBaseID.Bytes)
		state.description.KnowledgeBaseIDs = append(state.description.KnowledgeBaseIDs, id)
		if access.Valid && access.String == "RESTRICTED" {
			state.description.EffectiveAccess = Restricted
		}
		state.memberships++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	result := make([]ScopeDescription, len(agentIDs))
	for index, id := range agentIDs {
		state := states[id]
		if state.memberships == 0 || len(state.description.KnowledgeBaseIDs) > MaxKnowledgeBases {
			state.description.Ready = false
		}
		result[index] = state.description
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// ListReadyScoped evaluates a caller-provided fixed Agent scope in one
// repeatable-read snapshot. It never widens the scope or returns unready roots.
func (store *Store) ListReadyScoped(ctx context.Context, scopedIDs []AgentID) ([]Agent, error) {
	if len(scopedIDs) == 0 || !uniqueAgentIDs(scopedIDs) {
		return nil, ErrChatModelUnavailable
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id FROM agents WHERE id=ANY($1::uuid[]) ORDER BY agent_key
	`, agentUUIDs(scopedIDs))
	if err != nil {
		return nil, err
	}
	ids := make([]AgentID, 0, len(scopedIDs))
	for rows.Next() {
		var id pgtype.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, AgentID(id.Bytes))
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	result := make([]Agent, 0, len(ids))
	for _, id := range ids {
		agent, loadErr := loadAgent(ctx, tx, id, false)
		if loadErr != nil {
			return nil, loadErr
		}
		if agent.Lifecycle != Active {
			continue
		}
		readiness, readinessErr := evaluateReadiness(ctx, tx, agent.CurrentVersion)
		if readinessErr != nil {
			return nil, readinessErr
		}
		if readiness.Ready {
			result = append(result, agent)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// ResolveReadyScoped performs the non-enumerating scoped selector preflight
// before an execution capture can perform any corpus work.
func (store *Store) ResolveReadyScoped(ctx context.Context, scopedIDs []AgentID, key string) (Agent, error) {
	if _, err := ParseKey(key); err != nil || len(scopedIDs) == 0 || !uniqueAgentIDs(scopedIDs) {
		return Agent{}, ErrChatModelUnavailable
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback(ctx)
	var id pgtype.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM agents WHERE agent_key=$1 AND id=ANY($2::uuid[])
	`, key, agentUUIDs(scopedIDs)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrChatModelUnavailable
	}
	if err != nil {
		return Agent{}, err
	}
	agent, err := loadAgent(ctx, tx, AgentID(id.Bytes), false)
	if err != nil || agent.Lifecycle != Active {
		return Agent{}, ErrChatModelUnavailable
	}
	readiness, err := evaluateReadiness(ctx, tx, agent.CurrentVersion)
	if err != nil {
		return Agent{}, err
	}
	if !readiness.Ready {
		return Agent{}, ErrChatModelUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return Agent{}, err
	}
	return agent, nil
}

func uniqueAgentIDs(ids []AgentID) bool {
	seen := make(map[AgentID]struct{}, len(ids))
	for _, id := range ids {
		if zeroID(ID(id)) {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func agentUUIDs(ids []AgentID) []pgtype.UUID {
	result := make([]pgtype.UUID, len(ids))
	for index, id := range ids {
		result[index] = pgUUID(ID(id))
	}
	return result
}
