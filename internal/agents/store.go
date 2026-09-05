package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type rowScanner interface{ Scan(...any) error }

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, errors.New("agent store database is required")
	}
	return &Store{pool: pool}, nil
}

func (store *Store) Get(ctx context.Context, id AgentID) (Agent, error) {
	if zeroID(ID(id)) {
		return Agent{}, invalid("agent ID is required")
	}
	return loadAgent(ctx, store.pool, id, false)
}

func (store *Store) GetByKey(ctx context.Context, key string) (Agent, error) {
	if _, err := ParseKey(key); err != nil {
		return Agent{}, err
	}
	var id pgtype.UUID
	err := store.pool.QueryRow(ctx, `SELECT id FROM agents WHERE agent_key=$1`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, notFound("agent key does not exist")
	}
	if err != nil {
		return Agent{}, err
	}
	return loadAgent(ctx, store.pool, AgentID(id.Bytes), false)
}

func (store *Store) ListPage(ctx context.Context, after *PageCursor, limit int) (Page, error) {
	if limit < 1 || limit > MaxCatalogPageSize {
		return Page{}, invalid("agent page limit is out of bounds")
	}
	query := `SELECT id,created_at FROM agents`
	args := make([]any, 0, 3)
	if after != nil {
		if after.CreatedAt.IsZero() || zeroID(ID(after.AgentID)) {
			return Page{}, invalid("agent page cursor is invalid")
		}
		query += ` WHERE (created_at,id)>($1,$2)`
		args = append(args, after.CreatedAt, pgUUID(ID(after.AgentID)))
	}
	query += ` ORDER BY created_at,id LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit+1)
	rows, err := store.pool.Query(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	type rowValue struct {
		id        AgentID
		createdAt time.Time
	}
	ids := make([]rowValue, 0, limit+1)
	for rows.Next() {
		var id pgtype.UUID
		var createdAt time.Time
		if err = rows.Scan(&id, &createdAt); err != nil {
			rows.Close()
			return Page{}, err
		}
		ids = append(ids, rowValue{id: AgentID(id.Bytes), createdAt: createdAt})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return Page{}, err
	}
	rows.Close()
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	page := Page{Agents: make([]Agent, 0, len(ids))}
	for _, row := range ids {
		value, loadErr := loadAgent(ctx, store.pool, row.id, false)
		if loadErr != nil {
			return Page{}, loadErr
		}
		page.Agents = append(page.Agents, value)
	}
	if more {
		last := ids[len(ids)-1]
		page.NextCursor = &PageCursor{CreatedAt: last.createdAt, AgentID: last.id}
	}
	return page, nil
}

func (store *Store) ListVersions(
	ctx context.Context,
	agentID AgentID,
	after *VersionPageCursor,
	limit int,
) (VersionPage, error) {
	if zeroID(ID(agentID)) || limit < 1 || limit > MaxVersionPageSize {
		return VersionPage{}, invalid("Agent version page parameters are invalid")
	}
	if after != nil && (after.VersionNumber <= 0 || zeroID(ID(after.VersionID))) {
		return VersionPage{}, invalid("Agent version page cursor is invalid")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return VersionPage{}, err
	}
	defer tx.Rollback(ctx)
	query := `
		SELECT id,agent_id,version_number,display_name,description,response_language,
		       identity_instructions,model_profile_id,reasoning_effort,answer_mode,
		       behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
		       max_answer_tokens,created_by_operator_id,created_at
		FROM agent_versions WHERE agent_id=$1`
	args := []any{pgUUID(ID(agentID))}
	if after != nil {
		query += ` AND (version_number,id)<($2,$3)`
		args = append(args, after.VersionNumber, pgUUID(ID(after.VersionID)))
	}
	query += fmt.Sprintf(` ORDER BY version_number DESC,id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return VersionPage{}, err
	}
	values := make([]Version, 0, limit+1)
	for rows.Next() {
		value, scanErr := scanVersionRoot(rows)
		if scanErr != nil {
			rows.Close()
			return VersionPage{}, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return VersionPage{}, err
	}
	rows.Close()
	if len(values) == 0 {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1)`, pgUUID(ID(agentID))).Scan(&exists); err != nil {
			return VersionPage{}, err
		}
		if !exists {
			return VersionPage{}, notFound("agent does not exist")
		}
	}
	more := len(values) > limit
	if more {
		values = values[:limit]
	}
	if len(values) > 0 {
		ids := make([]pgtype.UUID, len(values))
		byID := make(map[VersionID]*Version, len(values))
		for index := range values {
			ids[index] = pgUUID(ID(values[index].ID))
			byID[values[index].ID] = &values[index]
		}
		rows, err = tx.Query(ctx, `
			SELECT agent_version_id,position,knowledge_base_id
			FROM agent_version_knowledge_bases
			WHERE agent_version_id=ANY($1::uuid[])
			ORDER BY agent_version_id,position
		`, ids)
		if err != nil {
			return VersionPage{}, err
		}
		for rows.Next() {
			var storedVersionID, knowledgeBaseID pgtype.UUID
			var position int32
			if err = rows.Scan(&storedVersionID, &position, &knowledgeBaseID); err != nil {
				rows.Close()
				return VersionPage{}, err
			}
			value := byID[VersionID(storedVersionID.Bytes)]
			if value == nil {
				rows.Close()
				return VersionPage{}, errors.New("stored Agent membership references an unexpected version")
			}
			membership := Membership{Position: position, KnowledgeBaseID: KnowledgeBaseID(knowledgeBaseID.Bytes)}
			value.Memberships = append(value.Memberships, membership)
			value.Configuration.KnowledgeBaseIDs = append(value.Configuration.KnowledgeBaseIDs, membership.KnowledgeBaseID)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return VersionPage{}, err
		}
		rows.Close()
		for index := range values {
			if err = validateStoredVersion(values[index]); err != nil {
				return VersionPage{}, err
			}
		}
	}
	page := VersionPage{Versions: values}
	if more {
		last := values[len(values)-1]
		page.NextCursor = &VersionPageCursor{VersionNumber: last.VersionNumber, VersionID: last.ID}
	}
	if err = tx.Commit(ctx); err != nil {
		return VersionPage{}, err
	}
	return page, nil
}

func (store *Store) GetVersion(ctx context.Context, agentID AgentID, versionID VersionID) (Version, error) {
	if zeroID(ID(agentID)) || zeroID(ID(versionID)) {
		return Version{}, invalid("agent and version IDs are required")
	}
	value, err := loadVersion(ctx, store.pool, versionID)
	if err != nil {
		return Version{}, err
	}
	if value.AgentID != agentID {
		return Version{}, notFound("agent version does not exist")
	}
	return value, nil
}

func (store *Store) EvaluateReadiness(ctx context.Context, agentID AgentID) (Readiness, error) {
	value, err := store.Get(ctx, agentID)
	if err != nil {
		return Readiness{}, err
	}
	return evaluateReadiness(ctx, store.pool, value.CurrentVersion)
}

func loadAgent(ctx context.Context, database queryer, id AgentID, lock bool) (Agent, error) {
	query := `
		SELECT id,agent_key,lifecycle,current_version_id,version,created_at,
		       updated_at,activated_at,archived_at
		FROM agents WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var (
		value                      Agent
		storedID, currentVersionID pgtype.UUID
		activatedAt, archivedAt    pgtype.Timestamptz
	)
	err := database.QueryRow(ctx, query, pgUUID(ID(id))).Scan(
		&storedID, &value.Key, &value.Lifecycle, &currentVersionID, &value.Version,
		&value.CreatedAt, &value.UpdatedAt, &activatedAt, &archivedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, notFound("agent does not exist")
	}
	if err != nil {
		return Agent{}, err
	}
	value.ID = AgentID(storedID.Bytes)
	value.CurrentVersionID = VersionID(currentVersionID.Bytes)
	value.ActivatedAt = optionalTime(activatedAt)
	value.ArchivedAt = optionalTime(archivedAt)
	if _, err = ParseKey(value.Key); err != nil || !validLifecycle(value.Lifecycle) || value.Version <= 0 ||
		zeroID(ID(value.CurrentVersionID)) || !validAgentTimes(value) {
		return Agent{}, errors.New("stored agent root is invalid")
	}
	value.CurrentVersion, err = loadVersion(ctx, database, value.CurrentVersionID)
	if err != nil {
		return Agent{}, err
	}
	if value.CurrentVersion.AgentID != value.ID {
		return Agent{}, errors.New("stored agent current version belongs to another agent")
	}
	return value, nil
}

func loadVersion(ctx context.Context, database queryer, id VersionID) (Version, error) {
	value, err := scanVersionRoot(database.QueryRow(ctx, `
		SELECT id,agent_id,version_number,display_name,description,response_language,
		       identity_instructions,model_profile_id,reasoning_effort,answer_mode,
		       behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
		       max_answer_tokens,created_by_operator_id,created_at
		FROM agent_versions WHERE id=$1
	`, pgUUID(ID(id))))
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, notFound("agent version does not exist")
	}
	if err != nil {
		return Version{}, err
	}
	rows, err := database.Query(ctx, `
		SELECT position,knowledge_base_id
		FROM agent_version_knowledge_bases
		WHERE agent_version_id=$1 ORDER BY position
	`, pgUUID(ID(id)))
	if err != nil {
		return Version{}, err
	}
	for rows.Next() {
		var membership Membership
		var knowledgeBaseID pgtype.UUID
		if err = rows.Scan(&membership.Position, &knowledgeBaseID); err != nil {
			rows.Close()
			return Version{}, err
		}
		membership.KnowledgeBaseID = KnowledgeBaseID(knowledgeBaseID.Bytes)
		value.Memberships = append(value.Memberships, membership)
		value.Configuration.KnowledgeBaseIDs = append(value.Configuration.KnowledgeBaseIDs, membership.KnowledgeBaseID)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return Version{}, err
	}
	rows.Close()
	if err = validateStoredVersion(value); err != nil {
		return Version{}, err
	}
	return value, nil
}

func scanVersionRoot(row rowScanner) (Version, error) {
	var (
		value                        Version
		storedID, agentID, profileID pgtype.UUID
		actorID                      pgtype.UUID
	)
	err := row.Scan(
		&storedID, &agentID, &value.VersionNumber, &value.Configuration.DisplayName,
		&value.Configuration.Description, &value.Configuration.ResponseLanguage,
		&value.Configuration.IdentityInstructions, &profileID,
		&value.Configuration.ReasoningEffort, &value.Configuration.AnswerMode,
		&value.Configuration.BehavioralInstructions, &value.Configuration.EvidenceAccess,
		&value.Configuration.RefusalMarkdown, &value.Configuration.MaxToolCalls,
		&value.Configuration.MaxAnswerTokens, &actorID, &value.CreatedAt,
	)
	if err != nil {
		return Version{}, err
	}
	value.ID = VersionID(storedID.Bytes)
	value.AgentID = AgentID(agentID.Bytes)
	value.Configuration.ModelProfileID = ModelProfileID(profileID.Bytes)
	value.CreatedByOperator = actorID.Bytes
	return value, nil
}

func validateStoredVersion(value Version) error {
	normalized, err := NormalizeConfiguration(value.Configuration)
	if err != nil || !equalConfiguration(value.Configuration, normalized) || value.VersionNumber <= 0 ||
		zeroID(ID(value.ID)) || zeroID(ID(value.AgentID)) || zeroID(ID(value.Configuration.ModelProfileID)) ||
		zeroID(ID(AgentID(value.CreatedByOperator))) || !contiguousMemberships(value.Memberships) {
		return errors.New("stored agent version is invalid")
	}
	return nil
}

func evaluateReadiness(ctx context.Context, database queryer, version Version) (Readiness, error) {
	readiness := Readiness{EffectiveAccess: Public}
	if err := evaluateModelReadiness(ctx, database, version, &readiness); err != nil {
		return Readiness{}, err
	}
	if err := evaluateKnowledgeBaseReadiness(ctx, database, version, &readiness); err != nil {
		return Readiness{}, err
	}
	readiness.Ready = len(readiness.Issues) == 0
	return readiness, nil
}

func evaluateModelReadiness(ctx context.Context, database queryer, version Version, readiness *Readiness) error {
	var (
		availability, endpointLifecycle, endpointHealth, transport, reasoningTransport string
		profileVersionID, endpointID, credentialID                                     pgtype.UUID
		profileVersionNumber, profileConfiguration, endpointConfiguration              int32
		contextTokens, outputTokens, credentialVersion                                 pgtype.Int4
		supportsTools                                                                  pgtype.Bool
		reasoningMapping                                                               []byte
		credentialDeleted                                                              pgtype.Timestamptz
	)
	err := database.QueryRow(ctx, `
		SELECT mp.availability,mp.current_version_id,mpv.version_number,
		       mpv.configuration_version,mpv.transport,mpv.context_window_tokens,
		       mpv.max_output_tokens,mpv.supports_tools,mpv.reasoning_transport,
		       mpv.reasoning_mapping,pe.id,pe.lifecycle,pe.health,pe.configuration_version,
		       pe.credential_id,c.secret_version,c.deleted_at
		FROM model_profiles mp
		JOIN model_profile_versions mpv
		  ON mpv.profile_id=mp.id AND mpv.id=mp.current_version_id
		JOIN provider_endpoints pe ON pe.id=mp.endpoint_id
		LEFT JOIN credentials c ON c.id=pe.credential_id
		WHERE mp.id=$1
	`, pgUUID(ID(version.Configuration.ModelProfileID))).Scan(
		&availability, &profileVersionID, &profileVersionNumber, &profileConfiguration,
		&transport, &contextTokens, &outputTokens, &supportsTools, &reasoningTransport,
		&reasoningMapping, &endpointID, &endpointLifecycle, &endpointHealth, &endpointConfiguration,
		&credentialID, &credentialVersion, &credentialDeleted,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueModelUnavailable})
		return nil
	}
	if err != nil {
		return err
	}
	modelVersionID := ModelProfileVersionID(profileVersionID.Bytes)
	providerID := ProviderEndpointID(endpointID.Bytes)
	readiness.ModelProfileVersionID = &modelVersionID
	readiness.ModelProfileVersionNumber = int32Pointer(profileVersionNumber)
	readiness.ProviderEndpointID = &providerID
	readiness.EndpointConfigurationVersion = int32Pointer(endpointConfiguration)
	if availability == "UNAVAILABLE" {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueModelUnavailable})
	}
	if endpointLifecycle != "ACTIVE" || endpointHealth != "HEALTHY" {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueEndpointUnavailable})
	}
	if profileConfiguration != endpointConfiguration {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueModelConfigurationStale})
	}
	if credentialID.Valid && (!credentialVersion.Valid || credentialVersion.Int32 <= 0 || credentialDeleted.Valid) {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueCredentialUnavailable})
	}
	if transport != "CHAT_COMPLETIONS" || !contextTokens.Valid || contextTokens.Int32 <= 0 ||
		!outputTokens.Valid || outputTokens.Int32 <= 0 || version.Configuration.MaxAnswerTokens > outputTokens.Int32 {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueModelLimitsUnknown})
	}
	if version.Configuration.AnswerMode == ToolCalling && (!supportsTools.Valid || !supportsTools.Bool) {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueModelCapabilityMissing})
	}
	if !reasoningSupported(version.Configuration.ReasoningEffort, reasoningTransport, reasoningMapping) {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueReasoningUnsupported})
	}
	return nil
}

func evaluateKnowledgeBaseReadiness(ctx context.Context, database queryer, version Version, readiness *Readiness) error {
	rows, err := database.Query(ctx, `
		SELECT membership.position,membership.knowledge_base_id,kb.lifecycle,kb.access_policy,
		       kb.published_wiki_id,wiki.id
		FROM agent_version_knowledge_bases membership
		LEFT JOIN knowledge_bases kb ON kb.id=membership.knowledge_base_id
		LEFT JOIN wiki_versions wiki
		  ON wiki.id=kb.published_wiki_id AND wiki.knowledge_base_id=kb.id
		WHERE membership.agent_version_id=$1
		ORDER BY membership.position
	`, pgUUID(ID(version.ID)))
	if err != nil {
		return err
	}
	seen := 0
	for rows.Next() {
		var position int32
		var storedID, publishedWikiID, joinedWikiID pgtype.UUID
		var lifecycle, access pgtype.Text
		if err = rows.Scan(&position, &storedID, &lifecycle, &access, &publishedWikiID, &joinedWikiID); err != nil {
			rows.Close()
			return err
		}
		seen++
		id := KnowledgeBaseID(storedID.Bytes)
		if !lifecycle.Valid || !access.Valid {
			readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueKnowledgeBaseMissing, KnowledgeBaseID: &id})
			continue
		}
		if lifecycle.String != "ACTIVE" {
			readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueKnowledgeBaseInactive, KnowledgeBaseID: &id})
		}
		if access.String == "RESTRICTED" {
			readiness.EffectiveAccess = Restricted
		}
		if !publishedWikiID.Valid || !joinedWikiID.Valid {
			readiness.Issues = append(readiness.Issues, ReadinessIssue{Code: IssueKnowledgeBaseUnpublished, KnowledgeBaseID: &id})
		}
		if int(position) != seen-1 {
			rows.Close()
			return errors.New("stored agent membership positions are not contiguous")
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if seen != len(version.Memberships) {
		return errors.New("stored agent membership set changed during readiness evaluation")
	}
	return nil
}

func reasoningSupported(effort ReasoningEffort, transport string, raw []byte) bool {
	if effort == ReasoningNone {
		return true
	}
	switch transport {
	case "REASONING_EFFORT":
		return true
	case "CUSTOM":
		var mapping struct {
			Values map[string]any `json:"values"`
		}
		if json.Unmarshal(raw, &mapping) != nil {
			return false
		}
		_, exists := mapping.Values[strings.ToLower(string(effort))]
		return exists
	default:
		return false
	}
}

func contiguousMemberships(values []Membership) bool {
	if len(values) == 0 || len(values) > MaxKnowledgeBases {
		return false
	}
	for index, value := range values {
		if value.Position != int32(index) || zeroID(ID(value.KnowledgeBaseID)) {
			return false
		}
	}
	return true
}

func validAgentTimes(value Agent) bool {
	if value.UpdatedAt.Before(value.CreatedAt) {
		return false
	}
	switch value.Lifecycle {
	case Draft:
		return value.ActivatedAt == nil && value.ArchivedAt == nil
	case Active:
		return value.ActivatedAt != nil && value.ArchivedAt == nil && !value.ActivatedAt.Before(value.CreatedAt)
	case Archived:
		return value.ArchivedAt != nil && !value.ArchivedAt.Before(value.CreatedAt)
	default:
		return false
	}
}

func optionalTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func int32Pointer(value int32) *int32 {
	result := value
	return &result
}

func pgUUID(id ID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(id), Valid: true} }
