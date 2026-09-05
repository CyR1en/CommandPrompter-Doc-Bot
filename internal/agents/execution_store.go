package agents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SourceArtifactResolver interface {
	ResolveArtifactKey(string) (string, error)
}

type PostgresExecutionStore struct {
	pool      *pgxpool.Pool
	artifacts SourceArtifactResolver
	vault     *security.CredentialVault
}

const executionReservationTTL = 24 * time.Hour

func NewPostgresExecutionStore(pool *pgxpool.Pool, artifacts SourceArtifactResolver, vault *security.CredentialVault) (*PostgresExecutionStore, error) {
	if pool == nil || artifacts == nil || vault == nil {
		return nil, errors.New("agent execution store dependencies are incomplete")
	}
	return &PostgresExecutionStore{pool: pool, artifacts: artifacts, vault: vault}, nil
}

func (store *PostgresExecutionStore) Capture(ctx context.Context, selector string) (ExecutionCapture, error) {
	key, ok := strings.CutPrefix(selector, "agent:")
	if !ok {
		return ExecutionCapture{}, ErrExecutionUnavailable
	}
	if _, err := ParseKey(key); err != nil {
		return ExecutionCapture{}, ErrExecutionUnavailable
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ExecutionCapture{}, err
	}
	defer tx.Rollback(ctx)
	var agentID pgtype.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM agents WHERE agent_key=$1`, key).Scan(&agentID); errors.Is(err, pgx.ErrNoRows) {
		return ExecutionCapture{}, ErrExecutionUnavailable
	} else if err != nil {
		return ExecutionCapture{}, err
	}
	agent, err := loadAgent(ctx, tx, AgentID(agentID.Bytes), false)
	if err != nil || agent.Lifecycle != Active {
		return ExecutionCapture{}, ErrExecutionUnavailable
	}
	readiness, err := evaluateReadiness(ctx, tx, agent.CurrentVersion)
	if err != nil || !readiness.Ready {
		return ExecutionCapture{}, ErrExecutionUnavailable
	}
	model, err := loadCapturedModel(ctx, tx, agent.CurrentVersion)
	if err != nil {
		return ExecutionCapture{}, err
	}
	knowledgeBases, effectiveAccess, err := store.loadCapturedKnowledgeBases(ctx, tx, agent.CurrentVersion)
	if err != nil {
		return ExecutionCapture{}, err
	}
	var capturedAt time.Time
	if err = tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&capturedAt); err != nil {
		return ExecutionCapture{}, err
	}
	rawRunID, err := newUUID()
	if err != nil {
		return ExecutionCapture{}, err
	}
	for _, knowledgeBase := range knowledgeBases {
		if _, err = tx.Exec(ctx, `
			INSERT INTO agent_run_scope_reservations(
				run_id,position,knowledge_base_id,wiki_version_id,expires_at,created_at
			) VALUES($1,$2,$3,$4,$5,$6)
		`, pgUUID(rawRunID), knowledgeBase.Position, pgUUID(ID(knowledgeBase.ID)),
			pgUUID(ID(knowledgeBase.WikiVersionID)), capturedAt.Add(executionReservationTTL), capturedAt); err != nil {
			return ExecutionCapture{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return ExecutionCapture{}, err
	}
	return ExecutionCapture{
		RunID: RunID(rawRunID), Agent: agent, Model: model, KnowledgeBases: knowledgeBases,
		EffectiveAccess: effectiveAccess, CapturedAt: capturedAt,
	}, nil
}

func (store *PostgresExecutionStore) ReleaseCapture(ctx context.Context, capture ExecutionCapture) error {
	if capture.RunID == (RunID{}) {
		return nil
	}
	_, err := store.pool.Exec(ctx, `DELETE FROM agent_run_scope_reservations WHERE run_id=$1`, pgUUID(ID(capture.RunID)))
	return err
}

func (store *PostgresExecutionStore) loadCapturedKnowledgeBases(ctx context.Context, tx pgx.Tx, version Version) ([]CapturedKnowledgeBase, AccessPolicy, error) {
	rows, err := tx.Query(ctx, `
		SELECT membership.position,kb.id,kb.version,kb.lifecycle,kb.access_policy,
		       wiki.id,wiki.documentation_run_id
		FROM agent_version_knowledge_bases membership
		JOIN knowledge_bases kb ON kb.id=membership.knowledge_base_id
		JOIN wiki_versions wiki ON wiki.id=kb.published_wiki_id AND wiki.knowledge_base_id=kb.id
		WHERE membership.agent_version_id=$1
		ORDER BY membership.position
		FOR SHARE OF kb,wiki
	`, pgUUID(ID(version.ID)))
	if err != nil {
		return nil, Public, err
	}
	defer rows.Close()
	result := make([]CapturedKnowledgeBase, 0, len(version.Memberships))
	effective := Public
	for rows.Next() {
		var position, resourceVersion int32
		var knowledgeBaseID, wikiID, documentationRunID pgtype.UUID
		var lifecycle, access string
		if err = rows.Scan(&position, &knowledgeBaseID, &resourceVersion, &lifecycle, &access, &wikiID, &documentationRunID); err != nil {
			return nil, Public, err
		}
		if lifecycle != "ACTIVE" || position != int32(len(result)) || resourceVersion <= 0 {
			return nil, Public, ErrExecutionUnavailable
		}
		policy := AccessPolicy(access)
		if policy != Public && policy != Restricted {
			return nil, Public, ErrExecutionUnavailable
		}
		if policy == Restricted {
			effective = Restricted
		}
		captured := CapturedKnowledgeBase{
			Position: position, ID: KnowledgeBaseID(knowledgeBaseID.Bytes), ResourceVersion: resourceVersion,
			AccessPolicy: policy, WikiVersionID: WikiVersionID(wikiID.Bytes), DocumentationRunID: DocumentationRunID(documentationRunID.Bytes),
		}
		result = append(result, captured)
	}
	if err = rows.Err(); err != nil {
		return nil, Public, err
	}
	rows.Close()
	if len(result) != len(version.Memberships) || len(result) == 0 {
		return nil, Public, ErrExecutionUnavailable
	}
	for index := range result {
		if result[index].ID != version.Memberships[index].KnowledgeBaseID {
			return nil, Public, ErrExecutionUnavailable
		}
		result[index].Sources, result[index].SourceScopeDigest, err = store.loadCapturedSources(ctx, tx, result[index])
		if err != nil {
			return nil, Public, err
		}
	}
	return result, effective, nil
}

func (store *PostgresExecutionStore) loadCapturedSources(ctx context.Context, tx pgx.Tx, captured CapturedKnowledgeBase) ([]CapturedSource, [32]byte, error) {
	rows, err := tx.Query(ctx, `
		SELECT drs.source_id,drs.source_revision_id,drs.native_version,
		       revision.artifact_key,source.kind,source.display_name
		FROM documentation_run_sources drs
		JOIN source_revisions revision ON revision.source_id=drs.source_id AND revision.id=drs.source_revision_id
		JOIN sources source ON source.id=drs.source_id
		WHERE drs.run_id=$1
		ORDER BY drs.source_id,drs.source_revision_id
	`, pgUUID(ID(captured.DocumentationRunID)))
	if err != nil {
		return nil, [32]byte{}, err
	}
	defer rows.Close()
	result := make([]CapturedSource, 0)
	digest := sha256.New()
	_, _ = digest.Write([]byte("ref0.agent.source-scope.v1\x00"))
	_, _ = digest.Write(captured.ID[:])
	_, _ = digest.Write(captured.WikiVersionID[:])
	_, _ = digest.Write(captured.DocumentationRunID[:])
	for rows.Next() {
		var sourceID, revisionID pgtype.UUID
		var nativeVersion, artifactKey, kind, label string
		if err = rows.Scan(&sourceID, &revisionID, &nativeVersion, &artifactKey, &kind, &label); err != nil {
			return nil, [32]byte{}, err
		}
		root, resolveErr := store.artifacts.ResolveArtifactKey(artifactKey)
		if resolveErr != nil {
			return nil, [32]byte{}, ErrExecutionUnavailable
		}
		source := CapturedSource{
			ID: SourceID(sourceID.Bytes), RevisionID: SourceRevisionID(revisionID.Bytes), NativeVersion: nativeVersion,
			ArtifactRoot: root, Kind: kind, Label: label,
		}
		if kind == "WEBSITE" {
			source.WebsitePages, err = loadCapturedWebsiteManifest(source)
			if err != nil {
				return nil, [32]byte{}, err
			}
		}
		if err = validateCapturedSource(source); err != nil {
			return nil, [32]byte{}, err
		}
		_, _ = digest.Write(sourceID.Bytes[:])
		_, _ = digest.Write(revisionID.Bytes[:])
		result = append(result, source)
	}
	if err = rows.Err(); err != nil {
		return nil, [32]byte{}, err
	}
	var scopeDigest [32]byte
	copy(scopeDigest[:], digest.Sum(nil))
	return result, scopeDigest, nil
}

func loadCapturedModel(ctx context.Context, tx pgx.Tx, version Version) (CapturedModel, error) {
	var (
		profileID, endpointID, currentVersionID, profileVersionID pgtype.UUID
		modelID, availability, transport, reasoningTransport      string
		profileResourceVersion, profileVersionNumber              int32
		profileConfiguration                                      int32
		contextTokens, outputTokens                               pgtype.Int4
		streaming, tools, structured, temperature                 pgtype.Bool
		reasoningJSON, extraJSON, metadataJSON                    []byte
		timeoutSeconds, maxRetries, maxConcurrentTasks            int32
		versionSource                                             string
		createdBy                                                 pgtype.UUID
		profileCreated, profileUpdated, profileVersionCreated     time.Time
		displayName, displayKey, baseURL, chatPath, modelsPath    string
		credentialID                                              pgtype.UUID
		headersJSON                                               []byte
		responsesPath                                             pgtype.Text
		allowHTTP, allowPrivate                                   bool
		endpointLifecycle, endpointHealth                         string
		endpointVersion, endpointConfiguration                    int32
		endpointCreated, endpointUpdated                          time.Time
		endpointArchived, healthChecked, credentialDeleted        pgtype.Timestamptz
		credentialVersion                                         pgtype.Int4
	)
	err := tx.QueryRow(ctx, `
		SELECT profile.id,profile.endpoint_id,profile.model_id,profile.availability,
		       profile.current_version_id,profile.version,profile.created_at,profile.updated_at,
		       profile_version.id,profile_version.version_number,profile_version.configuration_version,
		       profile_version.transport,profile_version.context_window_tokens,profile_version.max_output_tokens,
		       profile_version.supports_streaming,profile_version.supports_tools,
		       profile_version.supports_structured_output,profile_version.supports_temperature,
		       profile_version.reasoning_transport,profile_version.reasoning_mapping,
		       profile_version.timeout_seconds,profile_version.max_retries,profile_version.max_concurrent_tasks,
		       profile_version.extra_body,profile_version.metadata_origin,profile_version.source,
		       profile_version.created_by_operator_id,profile_version.created_at,
		       endpoint.id,endpoint.display_name,endpoint.display_key,endpoint.base_url,endpoint.credential_id,
		       endpoint.headers,endpoint.chat_completions_path,endpoint.responses_path,endpoint.models_path,
		       endpoint.allow_http,endpoint.allow_private_network,endpoint.lifecycle,endpoint.version,
		       endpoint.configuration_version,endpoint.created_at,endpoint.updated_at,endpoint.archived_at,
		       endpoint.health,endpoint.health_checked_at,credential.secret_version,credential.deleted_at
		FROM model_profiles profile
		JOIN model_profile_versions profile_version
		  ON profile_version.profile_id=profile.id AND profile_version.id=profile.current_version_id
		JOIN provider_endpoints endpoint ON endpoint.id=profile.endpoint_id
		LEFT JOIN credentials credential ON credential.id=endpoint.credential_id
		WHERE profile.id=$1
	`, pgUUID(ID(version.Configuration.ModelProfileID))).Scan(
		&profileID, &endpointID, &modelID, &availability, &currentVersionID, &profileResourceVersion,
		&profileCreated, &profileUpdated, &profileVersionID, &profileVersionNumber, &profileConfiguration,
		&transport, &contextTokens, &outputTokens, &streaming, &tools, &structured, &temperature,
		&reasoningTransport, &reasoningJSON, &timeoutSeconds, &maxRetries, &maxConcurrentTasks,
		&extraJSON, &metadataJSON, &versionSource, &createdBy, &profileVersionCreated,
		&endpointID, &displayName, &displayKey, &baseURL, &credentialID, &headersJSON, &chatPath,
		&responsesPath, &modelsPath, &allowHTTP, &allowPrivate, &endpointLifecycle, &endpointVersion,
		&endpointConfiguration, &endpointCreated, &endpointUpdated, &endpointArchived, &endpointHealth,
		&healthChecked, &credentialVersion, &credentialDeleted,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CapturedModel{}, ErrExecutionUnavailable
	}
	if err != nil {
		return CapturedModel{}, err
	}
	if currentVersionID.Bytes != profileVersionID.Bytes || profileConfiguration != endpointConfiguration ||
		providers.Availability(availability) == providers.Unavailable || providers.Lifecycle(endpointLifecycle) != providers.Active ||
		providers.Health(endpointHealth) != providers.Healthy || providers.Transport(transport) != providers.ChatCompletions ||
		!contextTokens.Valid || contextTokens.Int32 <= 0 || !outputTokens.Valid || outputTokens.Int32 <= 0 ||
		credentialDeleted.Valid || credentialID.Valid != credentialVersion.Valid {
		return CapturedModel{}, ErrExecutionUnavailable
	}
	if version.Configuration.AnswerMode == ToolCalling && (!tools.Valid || !tools.Bool) {
		return CapturedModel{}, ErrExecutionUnavailable
	}
	headers := providers.NonSecretHeaders{}
	extra := map[string]any{}
	metadataRaw := map[string]string{}
	if json.Unmarshal(headersJSON, &headers) != nil || json.Unmarshal(extraJSON, &extra) != nil || json.Unmarshal(metadataJSON, &metadataRaw) != nil {
		return CapturedModel{}, ErrExecutionUnavailable
	}
	metadata := make(map[string]providers.MetadataOrigin, len(metadataRaw))
	for key, value := range metadataRaw {
		metadata[key] = providers.MetadataOrigin(value)
	}
	var reasoning *providers.CustomReasoningMapping
	if len(reasoningJSON) != 0 && string(reasoningJSON) != "null" {
		var value providers.CustomReasoningMapping
		if json.Unmarshal(reasoningJSON, &value) != nil {
			return CapturedModel{}, ErrExecutionUnavailable
		}
		reasoning = &value
	}
	settings := providers.Settings{
		Transport: providers.Transport(transport), ContextWindowTokens: executionOptionalInt(contextTokens),
		MaxOutputTokens: executionOptionalInt(outputTokens), SupportsStreaming: executionOptionalBool(streaming),
		SupportsTools: executionOptionalBool(tools), SupportsStructuredOutput: executionOptionalBool(structured),
		SupportsTemperature: executionOptionalBool(temperature), ReasoningTransport: providers.ReasoningTransport(reasoningTransport),
		ReasoningMapping: reasoning, TimeoutSeconds: timeoutSeconds, MaxRetries: maxRetries,
		MaxConcurrentTasks: maxConcurrentTasks, ExtraBody: extra, MetadataOrigin: metadata,
	}
	var actor *providers.ActorID
	if createdBy.Valid {
		value := providers.ActorID(createdBy.Bytes)
		actor = &value
	}
	profileVersion := providers.ProfileVersion{
		ID: providers.ProfileVersionID(profileVersionID.Bytes), ProfileID: providers.ProfileID(profileID.Bytes),
		VersionNumber: profileVersionNumber, ConfigurationVersion: profileConfiguration, Settings: settings,
		Source: providers.VersionSource(versionSource), CreatedByActorID: actor, CreatedAt: profileVersionCreated,
	}
	profile := providers.Profile{
		ID: providers.ProfileID(profileID.Bytes), EndpointID: providers.EndpointID(endpointID.Bytes), ModelID: modelID,
		Availability: providers.Availability(availability), CurrentVersion: profileVersion,
		Version: profileResourceVersion, CreatedAt: profileCreated, UpdatedAt: profileUpdated,
	}
	endpoint := providers.Endpoint{
		ID: providers.EndpointID(endpointID.Bytes), Configuration: providers.Configuration{
			DisplayName: displayName, DisplayKey: displayKey, BaseURL: baseURL,
			CredentialID: executionCredentialID(credentialID), Headers: headers, ChatCompletionsPath: chatPath,
			ResponsesPath: executionOptionalText(responsesPath), ModelsPath: modelsPath,
			AllowHTTP: allowHTTP, AllowPrivateNetwork: allowPrivate,
		}, Lifecycle: providers.Lifecycle(endpointLifecycle), Version: endpointVersion,
		ConfigurationVersion: endpointConfiguration, CreatedAt: endpointCreated, UpdatedAt: endpointUpdated,
		ArchivedAt: executionOptionalTime(endpointArchived), Health: providers.Health(endpointHealth),
		HealthCheckedAt: executionOptionalTime(healthChecked),
	}
	captured := CapturedModel{
		Endpoint: endpoint, Profile: profile, ProfileVersionID: ModelProfileVersionID(profileVersionID.Bytes),
		ProfileVersionNumber: profileVersionNumber, ReasoningEffort: version.Configuration.ReasoningEffort,
		AnswerMode: version.Configuration.AnswerMode, CapturedCredentialVersion: executionOptionalInt(credentialVersion),
	}
	if credentialID.Valid {
		value := CredentialID(credentialID.Bytes)
		captured.CapturedCredentialID = &value
	}
	return captured, nil
}

func (store *PostgresExecutionStore) DigestRequest(capture ExecutionCapture, request ExecuteRequest) ([32]byte, error) {
	payload := struct {
		AgentID              string    `json:"agent_id"`
		AgentResourceVersion int32     `json:"agent_resource_version"`
		AgentVersionID       string    `json:"agent_version_id"`
		Origin               Origin    `json:"origin"`
		Subject              string    `json:"subject"`
		Messages             []Message `json:"messages"`
		MaxTokens            int32     `json:"max_tokens"`
	}{
		AgentID: capture.Agent.ID.String(), AgentResourceVersion: capture.Agent.Version,
		AgentVersionID: capture.Agent.CurrentVersionID.String(), Origin: request.Origin,
		Subject: request.Subject, Messages: request.Messages, MaxTokens: request.MaxTokens,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return [32]byte{}, err
	}
	digests, err := store.vault.KeyedDigests([]byte("agent.execution.request.v1"), encoded)
	if err != nil || len(digests) == 0 || len(digests[0]) != 32 {
		return [32]byte{}, errors.New("agent execution digest is unavailable")
	}
	var result [32]byte
	copy(result[:], digests[0])
	return result, nil
}

func (store *PostgresExecutionStore) AssertFresh(ctx context.Context, capture ExecutionCapture) error {
	return store.assertFresh(ctx, capture, true)
}

func (store *PostgresExecutionStore) AssertSecurityFresh(ctx context.Context, capture ExecutionCapture) error {
	return store.assertFresh(ctx, capture, false)
}

func (store *PostgresExecutionStore) assertFresh(ctx context.Context, capture ExecutionCapture, includeModel bool) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var currentVersionID pgtype.UUID
	var lifecycle string
	var resourceVersion int32
	if err = tx.QueryRow(ctx, `SELECT lifecycle,current_version_id,version FROM agents WHERE id=$1`, pgUUID(ID(capture.Agent.ID))).Scan(&lifecycle, &currentVersionID, &resourceVersion); err != nil ||
		lifecycle != "ACTIVE" || currentVersionID.Bytes != [16]byte(capture.Agent.CurrentVersionID) || resourceVersion != capture.Agent.Version {
		return ErrExecutionUnavailable
	}
	rows, err := tx.Query(ctx, `
		SELECT membership.position,membership.knowledge_base_id,kb.lifecycle,kb.access_policy,
		       kb.published_wiki_id IS NOT NULL
		FROM agent_version_knowledge_bases membership
		JOIN knowledge_bases kb ON kb.id=membership.knowledge_base_id
		WHERE membership.agent_version_id=$1 ORDER BY membership.position
	`, pgUUID(ID(capture.Agent.CurrentVersionID)))
	if err != nil {
		return err
	}
	position := 0
	for rows.Next() {
		var storedPosition int32
		var knowledgeBaseID pgtype.UUID
		var kbLifecycle, access string
		var published bool
		if err = rows.Scan(&storedPosition, &knowledgeBaseID, &kbLifecycle, &access, &published); err != nil {
			rows.Close()
			return err
		}
		if position >= len(capture.KnowledgeBases) || storedPosition != int32(position) ||
			knowledgeBaseID.Bytes != [16]byte(capture.KnowledgeBases[position].ID) || kbLifecycle != "ACTIVE" ||
			AccessPolicy(access) != capture.KnowledgeBases[position].AccessPolicy || !published {
			rows.Close()
			return ErrExecutionUnavailable
		}
		position++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if position != len(capture.KnowledgeBases) {
		return ErrExecutionUnavailable
	}
	for _, knowledgeBase := range capture.KnowledgeBases {
		var immutableScopeExists bool
		if err = tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM wiki_versions wiki
				JOIN documentation_runs run
				  ON run.id=wiki.documentation_run_id AND run.knowledge_base_id=wiki.knowledge_base_id
				WHERE wiki.id=$1 AND wiki.knowledge_base_id=$2 AND wiki.documentation_run_id=$3
			)
		`, pgUUID(ID(knowledgeBase.WikiVersionID)), pgUUID(ID(knowledgeBase.ID)), pgUUID(ID(knowledgeBase.DocumentationRunID))).Scan(&immutableScopeExists); err != nil {
			return err
		}
		if !immutableScopeExists {
			return ErrExecutionUnavailable
		}
		sourceRows, sourceErr := tx.Query(ctx, `
			SELECT run_source.source_id,run_source.source_revision_id
			FROM documentation_run_sources run_source
			JOIN source_revisions revision
			  ON revision.source_id=run_source.source_id AND revision.id=run_source.source_revision_id
			WHERE run_source.run_id=$1 ORDER BY run_source.source_id,run_source.source_revision_id
		`, pgUUID(ID(knowledgeBase.DocumentationRunID)))
		if sourceErr != nil {
			return sourceErr
		}
		sourceIndex := 0
		for sourceRows.Next() {
			var sourceID, revisionID pgtype.UUID
			if sourceErr = sourceRows.Scan(&sourceID, &revisionID); sourceErr != nil {
				sourceRows.Close()
				return sourceErr
			}
			if sourceIndex >= len(knowledgeBase.Sources) || sourceID.Bytes != [16]byte(knowledgeBase.Sources[sourceIndex].ID) ||
				revisionID.Bytes != [16]byte(knowledgeBase.Sources[sourceIndex].RevisionID) {
				sourceRows.Close()
				return ErrExecutionUnavailable
			}
			sourceIndex++
		}
		if sourceErr = sourceRows.Err(); sourceErr != nil {
			sourceRows.Close()
			return sourceErr
		}
		sourceRows.Close()
		if sourceIndex != len(knowledgeBase.Sources) {
			return ErrExecutionUnavailable
		}
	}
	if includeModel {
		var profileVersionID, endpointID, credentialID pgtype.UUID
		var availability, endpointLifecycle, endpointHealth string
		var endpointConfiguration int32
		var credentialVersion pgtype.Int4
		var credentialDeleted pgtype.Timestamptz
		err = tx.QueryRow(ctx, `
			SELECT profile.current_version_id,profile.availability,endpoint.id,
			       endpoint.configuration_version,endpoint.lifecycle,endpoint.health,
			       endpoint.credential_id,credential.secret_version,credential.deleted_at
			FROM model_profiles profile
			JOIN provider_endpoints endpoint ON endpoint.id=profile.endpoint_id
			LEFT JOIN credentials credential ON credential.id=endpoint.credential_id
			WHERE profile.id=$1
		`, pgUUID(ID(capture.Agent.CurrentVersion.Configuration.ModelProfileID))).Scan(
			&profileVersionID, &availability, &endpointID, &endpointConfiguration,
			&endpointLifecycle, &endpointHealth, &credentialID, &credentialVersion, &credentialDeleted,
		)
		if err != nil || profileVersionID.Bytes != [16]byte(capture.Model.ProfileVersionID) ||
			providers.Availability(availability) != capture.Model.Profile.Availability ||
			endpointID.Bytes != [16]byte(capture.Model.Endpoint.ID) || endpointConfiguration != capture.Model.Endpoint.ConfigurationVersion ||
			endpointLifecycle != "ACTIVE" || endpointHealth != "HEALTHY" || credentialDeleted.Valid ||
			credentialID.Valid != (capture.Model.CapturedCredentialID != nil) || credentialVersion.Valid != (capture.Model.CapturedCredentialVersion != nil) {
			return ErrExecutionUnavailable
		}
		if credentialID.Valid && (credentialID.Bytes != [16]byte(*capture.Model.CapturedCredentialID) || credentialVersion.Int32 != *capture.Model.CapturedCredentialVersion) {
			return ErrExecutionUnavailable
		}
	}
	return tx.Commit(ctx)
}

func (store *PostgresExecutionStore) RecordRun(ctx context.Context, record RunRecord) (RunID, error) {
	record.Capture.CapturedAt = postgresTimestamp(record.Capture.CapturedAt)
	record.CompletedAt = postgresTimestamp(record.CompletedAt)
	if record.CompletedAt.Before(record.Capture.CapturedAt) {
		record.CompletedAt = record.Capture.CapturedAt
	}
	if record.Usage == nil {
		record.Usage = map[string]int{}
	}
	if record.ToolCalls == nil {
		record.ToolCalls = []string{}
	}
	if record.Citations == nil {
		record.Citations = []Citation{}
	}
	usageJSON, usageErr := json.Marshal(record.Usage)
	toolJSON, toolErr := json.Marshal(record.ToolCalls)
	citationJSON, citationErr := json.Marshal(record.Citations)
	if usageErr != nil || toolErr != nil || citationErr != nil {
		return RunID{}, fmt.Errorf("%w: run audit is invalid", ErrExecutionInvalid)
	}
	if err := validateRunRecord(record, usageJSON, toolJSON, citationJSON); err != nil {
		return RunID{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return RunID{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "agent-run:"+record.Capture.RunID.String()); err != nil {
		return RunID{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1)`, pgUUID(ID(record.Capture.RunID))).Scan(&exists); err != nil {
		return RunID{}, err
	}
	if exists {
		matches, matchErr := storedRunMatches(ctx, tx, record, usageJSON, toolJSON, citationJSON)
		if matchErr != nil {
			return RunID{}, matchErr
		}
		if !matches {
			return RunID{}, ErrExecutionConflict
		}
		return record.Capture.RunID, tx.Commit(ctx)
	}
	if err = lockRunScopeReservation(ctx, tx, record.Capture); err != nil {
		return RunID{}, err
	}
	credentialVersion := any(nil)
	credentialID := any(nil)
	if record.Capture.Model.CapturedCredentialID != nil {
		credentialID = pgUUID(ID(*record.Capture.Model.CapturedCredentialID))
	}
	if record.Capture.Model.CapturedCredentialVersion != nil {
		credentialVersion = *record.Capture.Model.CapturedCredentialVersion
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO agent_runs (
			id,agent_id,agent_version_id,agent_resource_version,agent_version_number,
			model_profile_id,model_profile_version_id,model_profile_version_number,
			provider_endpoint_id,captured_endpoint_configuration_version,captured_credential_id,captured_credential_version,
			origin,subject,request_digest,effective_access_policy,outcome,model_usage,latency_ms,
			tool_calls,citations,sanitized_error,created_at,completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
	`, pgUUID(ID(record.Capture.RunID)), pgUUID(ID(record.Capture.Agent.ID)), pgUUID(ID(record.Capture.Agent.CurrentVersionID)),
		record.Capture.Agent.Version, record.Capture.Agent.CurrentVersion.VersionNumber,
		pgUUID(ID(record.Capture.Agent.CurrentVersion.Configuration.ModelProfileID)), pgUUID(ID(record.Capture.Model.ProfileVersionID)),
		record.Capture.Model.ProfileVersionNumber, pgUUID(ID(ProviderEndpointID(record.Capture.Model.Endpoint.ID))),
		record.Capture.Model.Endpoint.ConfigurationVersion, credentialID, credentialVersion, record.Origin, record.Subject, record.RequestDigest[:],
		record.Capture.EffectiveAccess, record.Outcome, usageJSON, record.LatencyMS, toolJSON, citationJSON,
		record.SanitizedError, record.Capture.CapturedAt, record.CompletedAt,
	); err != nil {
		return RunID{}, err
	}
	for _, knowledgeBase := range record.Capture.KnowledgeBases {
		revisions := make([]pgtype.UUID, len(knowledgeBase.Sources))
		for index, source := range knowledgeBase.Sources {
			revisions[index] = pgUUID(ID(source.RevisionID))
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO agent_run_knowledge_bases (
				run_id,position,knowledge_base_id,knowledge_base_version,access_policy,
				wiki_version_id,documentation_run_id,source_revision_ids,source_scope_digest
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, pgUUID(ID(record.Capture.RunID)), knowledgeBase.Position, pgUUID(ID(knowledgeBase.ID)), knowledgeBase.ResourceVersion,
			knowledgeBase.AccessPolicy, pgUUID(ID(knowledgeBase.WikiVersionID)), pgUUID(ID(knowledgeBase.DocumentationRunID)),
			revisions, knowledgeBase.SourceScopeDigest[:]); err != nil {
			return RunID{}, err
		}
	}
	result, err := tx.Exec(ctx, `DELETE FROM agent_run_scope_reservations WHERE run_id=$1`, pgUUID(ID(record.Capture.RunID)))
	if err != nil {
		return RunID{}, err
	}
	if result.RowsAffected() != int64(len(record.Capture.KnowledgeBases)) {
		return RunID{}, ErrExecutionUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return RunID{}, err
	}
	return record.Capture.RunID, nil
}

func lockRunScopeReservation(ctx context.Context, tx pgx.Tx, capture ExecutionCapture) error {
	for _, knowledgeBase := range capture.KnowledgeBases {
		var wikiVersionID pgtype.UUID
		err := tx.QueryRow(ctx, `
			SELECT id FROM wiki_versions
			WHERE knowledge_base_id=$1 AND id=$2
			FOR KEY SHARE
		`, pgUUID(ID(knowledgeBase.ID)), pgUUID(ID(knowledgeBase.WikiVersionID))).Scan(&wikiVersionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrExecutionUnavailable
		}
		if err != nil {
			return err
		}
	}
	var databaseTime time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseTime); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT position,knowledge_base_id,wiki_version_id,expires_at
		FROM agent_run_scope_reservations
		WHERE run_id=$1
		ORDER BY position
		FOR UPDATE
	`, pgUUID(ID(capture.RunID)))
	if err != nil {
		return err
	}
	defer rows.Close()
	position := 0
	for rows.Next() {
		var storedPosition int32
		var knowledgeBaseID, wikiVersionID pgtype.UUID
		var expiresAt time.Time
		if err = rows.Scan(&storedPosition, &knowledgeBaseID, &wikiVersionID, &expiresAt); err != nil {
			return err
		}
		if position >= len(capture.KnowledgeBases) {
			return ErrExecutionUnavailable
		}
		expected := capture.KnowledgeBases[position]
		if storedPosition != expected.Position || knowledgeBaseID.Bytes != [16]byte(expected.ID) ||
			wikiVersionID.Bytes != [16]byte(expected.WikiVersionID) || !expiresAt.After(databaseTime) {
			return ErrExecutionUnavailable
		}
		position++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if position != len(capture.KnowledgeBases) {
		return ErrExecutionUnavailable
	}
	return nil
}

func storedRunMatches(ctx context.Context, tx pgx.Tx, record RunRecord, usageJSON, toolJSON, citationJSON []byte) (bool, error) {
	var (
		agentID, agentVersionID, profileID, profileVersionID, endpointID, credentialID        pgtype.UUID
		agentResourceVersion, agentVersionNumber, profileVersionNumber, endpointConfiguration int32
		credentialVersion                                                                     pgtype.Int4
		origin, subject, access, outcome                                                      string
		requestDigest, storedUsage, storedTools, storedCitations                              []byte
		latency                                                                               int32
		sanitized                                                                             pgtype.Text
		createdAt, completedAt                                                                time.Time
	)
	err := tx.QueryRow(ctx, `
		SELECT agent_id,agent_version_id,agent_resource_version,agent_version_number,
		       model_profile_id,model_profile_version_id,model_profile_version_number,
		       provider_endpoint_id,captured_endpoint_configuration_version,captured_credential_id,captured_credential_version,
		       origin,subject,request_digest,effective_access_policy,outcome,model_usage,latency_ms,
		       tool_calls,citations,sanitized_error,created_at,completed_at
		FROM agent_runs WHERE id=$1
	`, pgUUID(ID(record.Capture.RunID))).Scan(
		&agentID, &agentVersionID, &agentResourceVersion, &agentVersionNumber, &profileID, &profileVersionID,
		&profileVersionNumber, &endpointID, &endpointConfiguration, &credentialID, &credentialVersion, &origin, &subject,
		&requestDigest, &access, &outcome, &storedUsage, &latency, &storedTools, &storedCitations,
		&sanitized, &createdAt, &completedAt,
	)
	if err != nil {
		return false, err
	}
	expectedCredential := record.Capture.Model.CapturedCredentialVersion
	expectedCredentialID := record.Capture.Model.CapturedCredentialID
	expectedError := ""
	if record.SanitizedError != nil {
		expectedError = *record.SanitizedError
	}
	if agentID.Bytes != [16]byte(record.Capture.Agent.ID) || agentVersionID.Bytes != [16]byte(record.Capture.Agent.CurrentVersionID) ||
		agentResourceVersion != record.Capture.Agent.Version || agentVersionNumber != record.Capture.Agent.CurrentVersion.VersionNumber ||
		profileID.Bytes != [16]byte(record.Capture.Agent.CurrentVersion.Configuration.ModelProfileID) ||
		profileVersionID.Bytes != [16]byte(record.Capture.Model.ProfileVersionID) || profileVersionNumber != record.Capture.Model.ProfileVersionNumber ||
		endpointID.Bytes != [16]byte(record.Capture.Model.Endpoint.ID) || endpointConfiguration != record.Capture.Model.Endpoint.ConfigurationVersion ||
		credentialID.Valid != (expectedCredentialID != nil) || credentialID.Valid && credentialID.Bytes != [16]byte(*expectedCredentialID) ||
		credentialVersion.Valid != (expectedCredential != nil) || credentialVersion.Valid && credentialVersion.Int32 != *expectedCredential ||
		origin != string(record.Origin) || subject != record.Subject || !bytes.Equal(requestDigest, record.RequestDigest[:]) ||
		access != string(record.Capture.EffectiveAccess) || outcome != string(record.Outcome) || latency != int32(record.LatencyMS) ||
		sanitized.Valid != (record.SanitizedError != nil) || sanitized.Valid && sanitized.String != expectedError ||
		!createdAt.Equal(postgresTimestamp(record.Capture.CapturedAt)) || !completedAt.Equal(postgresTimestamp(record.CompletedAt)) ||
		!jsonEqual(storedUsage, usageJSON) || !jsonEqual(storedTools, toolJSON) || !jsonEqual(storedCitations, citationJSON) {
		return false, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT position,knowledge_base_id,knowledge_base_version,access_policy,wiki_version_id,
		       documentation_run_id,source_revision_ids,source_scope_digest
		FROM agent_run_knowledge_bases WHERE run_id=$1 ORDER BY position
	`, pgUUID(ID(record.Capture.RunID)))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	position := 0
	for rows.Next() {
		var storedPosition, resourceVersion int32
		var knowledgeBaseID, wikiID, documentationRunID pgtype.UUID
		var storedAccess string
		var revisions []pgtype.UUID
		var scopeDigest []byte
		if err = rows.Scan(&storedPosition, &knowledgeBaseID, &resourceVersion, &storedAccess, &wikiID, &documentationRunID, &revisions, &scopeDigest); err != nil {
			return false, err
		}
		if position >= len(record.Capture.KnowledgeBases) {
			return false, nil
		}
		expected := record.Capture.KnowledgeBases[position]
		if storedPosition != expected.Position || knowledgeBaseID.Bytes != [16]byte(expected.ID) || resourceVersion != expected.ResourceVersion ||
			storedAccess != string(expected.AccessPolicy) || wikiID.Bytes != [16]byte(expected.WikiVersionID) ||
			documentationRunID.Bytes != [16]byte(expected.DocumentationRunID) || !bytes.Equal(scopeDigest, expected.SourceScopeDigest[:]) ||
			len(revisions) != len(expected.Sources) {
			return false, nil
		}
		for index, revision := range revisions {
			if revision.Bytes != [16]byte(expected.Sources[index].RevisionID) {
				return false, nil
			}
		}
		position++
	}
	return position == len(record.Capture.KnowledgeBases), rows.Err()
}

func validateRunRecord(record RunRecord, usageJSON, toolJSON, citationJSON []byte) error {
	if record.Capture.RunID == (RunID{}) || record.Capture.CapturedAt.IsZero() || record.CompletedAt.Before(record.Capture.CapturedAt) ||
		record.RequestDigest == ([32]byte{}) ||
		record.Origin != OriginHTTP && record.Origin != OriginDiscord || record.Subject == "" || record.LatencyMS < 0 ||
		record.Outcome != CompletionAnswered && record.Outcome != CompletionRefused && record.Outcome != CompletionInsufficientEvidence && record.Outcome != CompletionFailed ||
		(record.Outcome == CompletionFailed) != (record.SanitizedError != nil) || record.SanitizedError != nil && (len(*record.SanitizedError) == 0 || len(*record.SanitizedError) > 1000) ||
		len(record.ToolCalls) > 256 || len(record.Citations) > 256 {
		return fmt.Errorf("%w: run receipt is invalid", ErrExecutionInvalid)
	}
	if len(usageJSON) > 65536 {
		return fmt.Errorf("%w: run usage is invalid", ErrExecutionInvalid)
	}
	for key, value := range record.Usage {
		if key == "" || value < 0 {
			return fmt.Errorf("%w: run usage is invalid", ErrExecutionInvalid)
		}
	}
	if len(toolJSON) > 262144 || len(citationJSON) > 262144 {
		return fmt.Errorf("%w: run audit exceeds its bound", ErrExecutionInvalid)
	}
	return nil
}

func postgresTimestamp(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.Round(time.Microsecond)
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	return leftDecoder.Decode(&leftValue) == nil && rightDecoder.Decode(&rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func executionOptionalInt(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	result := value.Int32
	return &result
}

func executionOptionalBool(value pgtype.Bool) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Bool
	return &result
}

func executionOptionalText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func executionOptionalTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func executionCredentialID(value pgtype.UUID) *credentials.ID {
	if !value.Valid {
		return nil
	}
	result := credentials.ID(value.Bytes)
	return &result
}
