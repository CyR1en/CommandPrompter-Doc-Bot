package operations

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/cases"
)

const overviewLimit = 20

type configurationQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

var redactedFields = []string{
	"credentials.key_id",
	"credentials.nonce",
	"credentials.ciphertext",
	"provider_endpoints.headers.secret_fields",
	"model_profile_versions.reasoning_mapping.secret_fields",
	"model_profile_versions.extra_body.secret_fields",
}

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) (*Service, error) {
	if pool == nil {
		return nil, errors.New("operations database pool is required")
	}
	return &Service{pool: pool}, nil
}

func (service *Service) ExportConfiguration(ctx context.Context) (ConfigurationExport, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ConfigurationExport{}, err
	}
	defer tx.Rollback(ctx)
	value := ConfigurationExport{
		FormatVersion:      1,
		RedactedFields:     append([]string(nil), redactedFields...),
		Credentials:        []CredentialConfiguration{},
		KnowledgeBases:     []KnowledgeBaseConfiguration{},
		Sources:            []any{},
		Providers:          []ProviderConfiguration{},
		Models:             []ModelConfiguration{},
		ModelAssignments:   []ModelAssignmentConfiguration{},
		Agents:             []AgentConfiguration{},
		DiscordConnections: []DiscordConnectionConfiguration{},
		DiscordBindings:    []DiscordBindingConfiguration{},
	}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&value.GeneratedAt); err != nil {
		return ConfigurationExport{}, err
	}
	if value.Credentials, err = service.credentials(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if value.KnowledgeBases, err = service.knowledgeBases(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if value.Sources, err = service.sources(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if value.Providers, err = service.providers(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if value.Models, err = service.models(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if value.ModelAssignments, err = service.assignments(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if value.Agents, err = service.agents(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if value.DiscordConnections, err = service.discordConnections(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if value.DiscordBindings, err = service.discordBindings(ctx, tx); err != nil {
		return ConfigurationExport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ConfigurationExport{}, err
	}
	return value, nil
}

func (service *Service) credentials(ctx context.Context, database configurationQueryer) ([]CredentialConfiguration, error) {
	rows, err := database.Query(ctx, `
		SELECT id, lower(kind), label, masked_value, secret_version,
		       created_at, rotated_at, deleted_at
		FROM credentials ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []CredentialConfiguration{}
	for rows.Next() {
		var id pgtype.UUID
		var value CredentialConfiguration
		if err = rows.Scan(&id, &value.Kind, &value.Label, &value.MaskedValue,
			&value.SecretVersion, &value.CreatedAt, &value.RotatedAt, &value.DeletedAt); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) knowledgeBases(ctx context.Context, database configurationQueryer) ([]KnowledgeBaseConfiguration, error) {
	rows, err := database.Query(ctx, `
		SELECT id, name, lower(access_policy), lower(lifecycle), instructions,
		       language, published_wiki_id, version
		FROM knowledge_bases ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []KnowledgeBaseConfiguration{}
	for rows.Next() {
		var id, published pgtype.UUID
		var value KnowledgeBaseConfiguration
		if err = rows.Scan(&id, &value.Name, &value.Access, &value.Lifecycle,
			&value.Instructions, &value.Language, &published, &value.Version); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		value.PublishedWikiID = optionalUUID(published)
		if value.PublishedWikiID != nil {
			url := "/api/v1/knowledge-bases/" + value.ID + "/wiki/export?version_id=" + *value.PublishedWikiID
			value.WikiExportURL = &url
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) sources(ctx context.Context, database configurationQueryer) ([]any, error) {
	values := []any{}
	repositories, err := database.Query(ctx, `
		SELECT s.id, s.knowledge_base_id, s.display_name, lower(s.privacy),
		       lower(s.lifecycle), r.remote_url, r.credential_username,
		       r.credential_id, lower(r.ref_kind), r.ref_value,
		       r.include_patterns, r.exclude_patterns, r.poll_interval_seconds,
		       s.version
		FROM sources AS s
		JOIN repository_sources AS r ON r.source_id = s.id
		ORDER BY s.id
	`)
	if err != nil {
		return nil, err
	}
	for repositories.Next() {
		var id, knowledgeBaseID, credentialID pgtype.UUID
		var includeJSON, excludeJSON []byte
		value := RepositorySourceConfiguration{Kind: "repository"}
		if err = repositories.Scan(&id, &knowledgeBaseID, &value.DisplayName,
			&value.Privacy, &value.Lifecycle, &value.RemoteURL,
			&value.CredentialUsername, &credentialID, &value.RefKind, &value.RefValue,
			&includeJSON, &excludeJSON, &value.PollIntervalSeconds, &value.Version); err != nil {
			repositories.Close()
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			repositories.Close()
			return nil, err
		}
		if value.KnowledgeBaseID, err = requiredUUID(knowledgeBaseID); err != nil {
			repositories.Close()
			return nil, err
		}
		value.CredentialID = optionalUUID(credentialID)
		if value.IncludePatterns, err = stringArray(includeJSON); err != nil {
			repositories.Close()
			return nil, err
		}
		if value.ExcludePatterns, err = stringArray(excludeJSON); err != nil {
			repositories.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err = repositories.Err(); err != nil {
		repositories.Close()
		return nil, err
	}
	repositories.Close()

	websites, err := database.Query(ctx, `
		SELECT s.id, s.knowledge_base_id, s.display_name, lower(s.privacy),
		       lower(s.lifecycle), w.root_url, w.credential_header,
		       w.credential_prefix, w.credential_id, w.max_concurrency,
		       w.requests_per_second, w.max_pages, w.max_page_bytes,
		       w.max_total_bytes, w.max_depth, w.poll_interval_seconds, s.version
		FROM sources AS s
		JOIN website_sources AS w ON w.source_id = s.id
		ORDER BY s.id
	`)
	if err != nil {
		return nil, err
	}
	defer websites.Close()
	for websites.Next() {
		var id, knowledgeBaseID, credentialID pgtype.UUID
		value := WebsiteSourceConfiguration{Kind: "website"}
		if err = websites.Scan(&id, &knowledgeBaseID, &value.DisplayName,
			&value.Privacy, &value.Lifecycle, &value.RootURL, &value.CredentialHeader,
			&value.CredentialPrefix, &credentialID, &value.MaxConcurrency,
			&value.RequestsPerSecond, &value.MaxPages, &value.MaxPageBytes,
			&value.MaxTotalBytes, &value.MaxDepth, &value.PollIntervalSeconds,
			&value.Version); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.KnowledgeBaseID, err = requiredUUID(knowledgeBaseID); err != nil {
			return nil, err
		}
		value.CredentialID = optionalUUID(credentialID)
		values = append(values, value)
	}
	if err = websites.Err(); err != nil {
		return nil, err
	}
	sort.Slice(values, func(left, right int) bool { return sourceID(values[left]) < sourceID(values[right]) })
	return values, nil
}

func (service *Service) providers(ctx context.Context, database configurationQueryer) ([]ProviderConfiguration, error) {
	rows, err := database.Query(ctx, `
		SELECT id, display_name, base_url, credential_id, headers,
		       chat_completions_path, responses_path, models_path, allow_http,
		       allow_private_network, lower(lifecycle), version, configuration_version
		FROM provider_endpoints ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ProviderConfiguration{}
	for rows.Next() {
		var id, credentialID pgtype.UUID
		var raw []byte
		var value ProviderConfiguration
		if err = rows.Scan(&id, &value.DisplayName, &value.BaseURL, &credentialID,
			&raw, &value.ChatCompletionsPath, &value.ResponsesPath, &value.ModelsPath,
			&value.AllowHTTP, &value.AllowPrivateNetwork, &value.Lifecycle,
			&value.Version, &value.ConfigurationVersion); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		value.CredentialID = optionalUUID(credentialID)
		var headers map[string]any
		if err = json.Unmarshal(raw, &headers); err != nil {
			return nil, err
		}
		value.HeaderNames = make([]string, 0, len(headers))
		for name := range headers {
			value.HeaderNames = append(value.HeaderNames, name)
		}
		sort.Strings(value.HeaderNames)
		value.Headers = map[string]string{}
		for name, item := range nonSecretMap(headers) {
			if text, ok := item.(string); ok {
				value.Headers[name] = text
			}
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) models(ctx context.Context, database configurationQueryer) ([]ModelConfiguration, error) {
	rows, err := database.Query(ctx, `
		SELECT p.id, p.endpoint_id, p.model_id, lower(p.availability),
		       p.current_version_id, lower(v.transport), v.context_window_tokens,
		       v.max_output_tokens, v.supports_streaming, v.supports_tools,
		       v.supports_structured_output, v.supports_temperature,
		       lower(v.reasoning_transport), v.reasoning_mapping,
		       v.timeout_seconds, v.max_retries, v.extra_body, p.version
		FROM model_profiles AS p
		JOIN model_profile_versions AS v ON v.id = p.current_version_id
		ORDER BY p.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ModelConfiguration{}
	for rows.Next() {
		var id, endpointID, versionID pgtype.UUID
		var reasoningJSON, extraJSON []byte
		var value ModelConfiguration
		if err = rows.Scan(&id, &endpointID, &value.ModelID, &value.Availability,
			&versionID, &value.Transport, &value.ContextWindowTokens,
			&value.MaxOutputTokens, &value.SupportsStreaming, &value.SupportsTools,
			&value.SupportsStructuredOutput, &value.SupportsTemperature,
			&value.ReasoningTransport, &reasoningJSON, &value.TimeoutSeconds,
			&value.MaxRetries, &extraJSON, &value.Version); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.EndpointID, err = requiredUUID(endpointID); err != nil {
			return nil, err
		}
		if value.CurrentVersionID, err = requiredUUID(versionID); err != nil {
			return nil, err
		}
		if reasoningJSON != nil {
			var mapping map[string]any
			if err = json.Unmarshal(reasoningJSON, &mapping); err != nil {
				return nil, err
			}
			value.ReasoningMapping = nonSecretMap(mapping)
		}
		var extra map[string]any
		if err = json.Unmarshal(extraJSON, &extra); err != nil {
			return nil, err
		}
		value.ExtraBody = nonSecretMap(extra)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) assignments(ctx context.Context, database configurationQueryer) ([]ModelAssignmentConfiguration, error) {
	rows, err := database.Query(ctx, `
		SELECT id, knowledge_base_id, lower(role), model_profile_id,
		       lower(reasoning_effort), lower(answer_mode), version
		FROM model_assignments ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ModelAssignmentConfiguration{}
	for rows.Next() {
		var id, knowledgeBaseID, profileID pgtype.UUID
		var value ModelAssignmentConfiguration
		if err = rows.Scan(&id, &knowledgeBaseID, &value.Role, &profileID,
			&value.ReasoningEffort, &value.AnswerMode, &value.Version); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.KnowledgeBaseID, err = requiredUUID(knowledgeBaseID); err != nil {
			return nil, err
		}
		if value.ModelProfileID, err = requiredUUID(profileID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) discordConnections(ctx context.Context, database configurationQueryer) ([]DiscordConnectionConfiguration, error) {
	rows, err := database.Query(ctx, `
		SELECT id, display_name, credential_id, lower(lifecycle), version
		FROM discord_connections ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []DiscordConnectionConfiguration{}
	for rows.Next() {
		var id, credentialID pgtype.UUID
		var value DiscordConnectionConfiguration
		if err = rows.Scan(&id, &value.DisplayName, &credentialID, &value.Lifecycle, &value.Version); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.CredentialID, err = requiredUUID(credentialID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) agents(ctx context.Context, database configurationQueryer) ([]AgentConfiguration, error) {
	rows, err := database.Query(ctx, `
		SELECT agent.id,agent.agent_key,lower(agent.lifecycle),agent.current_version_id,
		       version.version_number,version.display_name,version.description,
		       version.response_language,version.identity_instructions,version.model_profile_id,
		       lower(version.reasoning_effort),lower(version.answer_mode),
		       version.behavioral_instructions,lower(version.evidence_access),
		       version.refusal_markdown,version.max_tool_calls,version.max_answer_tokens,
		       ARRAY(
		         SELECT membership.knowledge_base_id
		         FROM agent_version_knowledge_bases AS membership
		         WHERE membership.agent_version_id=version.id
		         ORDER BY membership.position
		       ),agent.version
		FROM agents AS agent
		JOIN agent_versions AS version ON version.id=agent.current_version_id
		ORDER BY agent.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []AgentConfiguration{}
	for rows.Next() {
		var id, versionID, modelProfileID pgtype.UUID
		var knowledgeBaseIDs []pgtype.UUID
		var value AgentConfiguration
		if err = rows.Scan(
			&id, &value.Key, &value.Lifecycle, &versionID, &value.CurrentVersionNumber,
			&value.DisplayName, &value.Description, &value.ResponseLanguage,
			&value.IdentityInstructions, &modelProfileID, &value.ReasoningEffort,
			&value.AnswerMode, &value.BehavioralInstructions, &value.EvidenceAccess,
			&value.RefusalMarkdown, &value.MaxToolCalls, &value.MaxAnswerTokens,
			&knowledgeBaseIDs, &value.Version,
		); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.CurrentVersionID, err = requiredUUID(versionID); err != nil {
			return nil, err
		}
		if value.ModelProfileID, err = requiredUUID(modelProfileID); err != nil {
			return nil, err
		}
		value.KnowledgeBaseIDs = make([]string, len(knowledgeBaseIDs))
		for index, knowledgeBaseID := range knowledgeBaseIDs {
			if value.KnowledgeBaseIDs[index], err = requiredUUID(knowledgeBaseID); err != nil {
				return nil, err
			}
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *Service) discordBindings(ctx context.Context, database configurationQueryer) ([]DiscordBindingConfiguration, error) {
	rows, err := database.Query(ctx, `
		SELECT binding.id, binding.connection_id, binding.server_id, binding.listen_channel_id, binding.agent_id,
		       ARRAY(SELECT lower(trigger.trigger_type)
		             FROM channel_binding_triggers AS trigger
		             WHERE trigger.binding_id=binding.id ORDER BY trigger.trigger_type),
		       lower(binding.reply_policy), binding.reply_channel_id,
		       allowed_role_ids, allowed_user_ids, rate_requests,
		       rate_window_seconds, enabled, version
		FROM channel_bindings AS binding WHERE deleted_at IS NULL ORDER BY binding.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []DiscordBindingConfiguration{}
	for rows.Next() {
		var id, connectionID, agentID pgtype.UUID
		var roleJSON, userJSON []byte
		var value DiscordBindingConfiguration
		if err = rows.Scan(&id, &connectionID, &value.ServerID, &value.ListenChannelID,
			&agentID, &value.Triggers, &value.ReplyPolicy,
			&value.ReplyChannelID, &roleJSON, &userJSON, &value.RateRequests,
			&value.RateWindowSeconds, &value.Enabled, &value.Version); err != nil {
			return nil, err
		}
		if value.ID, err = requiredUUID(id); err != nil {
			return nil, err
		}
		if value.ConnectionID, err = requiredUUID(connectionID); err != nil {
			return nil, err
		}
		if value.AgentID, err = requiredUUID(agentID); err != nil {
			return nil, err
		}
		if value.AllowedRoleIDs, err = stringArray(roleJSON); err != nil {
			return nil, err
		}
		if value.AllowedUserIDs, err = stringArray(userJSON); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func requiredUUID(value pgtype.UUID) (string, error) {
	if !value.Valid {
		return "", errors.New("required UUID is null")
	}
	return formatUUID(value.Bytes), nil
}

func optionalUUID(value pgtype.UUID) *string {
	if !value.Valid {
		return nil
	}
	formatted := formatUUID(value.Bytes)
	return &formatted
}

func formatUUID(value [16]byte) string {
	var encoded [36]byte
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded[:])
}

func stringArray(raw []byte) ([]string, error) {
	values := []string{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func sourceID(value any) string {
	switch typed := value.(type) {
	case RepositorySourceConfiguration:
		return typed.ID
	case WebsiteSourceConfiguration:
		return typed.ID
	default:
		return ""
	}
}

var secretParts = []string{
	"apikey", "authorization", "ciphertext", "cookie", "nonce", "password", "secret", "token",
}

func nonSecretMap(value map[string]any) map[string]any {
	clean := make(map[string]any, len(value))
	for name, item := range value {
		if secretField(name) {
			continue
		}
		clean[name] = nonSecretJSON(item)
	}
	return clean
}

func nonSecretJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return nonSecretMap(typed)
	case []any:
		clean := make([]any, len(typed))
		for index, item := range typed {
			clean[index] = nonSecretJSON(item)
		}
		return clean
	default:
		return value
	}
}

func secretField(name string) bool {
	folded := cases.Fold().String(name)
	var normalized strings.Builder
	for _, character := range folded {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			normalized.WriteRune(character)
		}
	}
	value := normalized.String()
	for _, part := range secretParts {
		if strings.Contains(value, part) {
			return true
		}
	}
	return false
}
