// Package operations exposes read-only, secret-safe operator projections.
package operations

import "time"

type CredentialConfiguration struct {
	ID            string     `json:"id" format:"uuid"`
	Kind          string     `json:"kind"`
	Label         string     `json:"label"`
	MaskedValue   string     `json:"masked_value"`
	SecretVersion int32      `json:"secret_version"`
	CreatedAt     time.Time  `json:"created_at"`
	RotatedAt     *time.Time `json:"rotated_at"`
	DeletedAt     *time.Time `json:"deleted_at"`
}

type KnowledgeBaseConfiguration struct {
	ID              string  `json:"id" format:"uuid"`
	Name            string  `json:"name"`
	Access          string  `json:"access"`
	Lifecycle       string  `json:"lifecycle"`
	Instructions    string  `json:"instructions"`
	Language        string  `json:"language"`
	PublishedWikiID *string `json:"published_wiki_id" format:"uuid"`
	WikiExportURL   *string `json:"wiki_export_url"`
	Version         int32   `json:"version"`
}

type RepositorySourceConfiguration struct {
	Kind                string   `json:"kind"`
	ID                  string   `json:"id" format:"uuid"`
	KnowledgeBaseID     string   `json:"knowledge_base_id" format:"uuid"`
	DisplayName         string   `json:"display_name"`
	Privacy             string   `json:"privacy"`
	Lifecycle           string   `json:"lifecycle"`
	RemoteURL           string   `json:"remote_url"`
	CredentialUsername  *string  `json:"credential_username"`
	CredentialID        *string  `json:"credential_id" format:"uuid"`
	RefKind             string   `json:"ref_kind"`
	RefValue            string   `json:"ref_value"`
	IncludePatterns     []string `json:"include_patterns"`
	ExcludePatterns     []string `json:"exclude_patterns"`
	PollIntervalSeconds *int32   `json:"poll_interval_seconds"`
	Version             int32    `json:"version"`
}

type WebsiteSourceConfiguration struct {
	Kind                string  `json:"kind"`
	ID                  string  `json:"id" format:"uuid"`
	KnowledgeBaseID     string  `json:"knowledge_base_id" format:"uuid"`
	DisplayName         string  `json:"display_name"`
	Privacy             string  `json:"privacy"`
	Lifecycle           string  `json:"lifecycle"`
	RootURL             string  `json:"root_url"`
	CredentialHeader    *string `json:"credential_header"`
	CredentialPrefix    *string `json:"credential_prefix"`
	CredentialID        *string `json:"credential_id" format:"uuid"`
	MaxConcurrency      int32   `json:"max_concurrency"`
	RequestsPerSecond   int32   `json:"requests_per_second"`
	MaxPages            int32   `json:"max_pages"`
	MaxPageBytes        int32   `json:"max_page_bytes"`
	MaxTotalBytes       int64   `json:"max_total_bytes"`
	MaxDepth            int32   `json:"max_depth"`
	PollIntervalSeconds *int32  `json:"poll_interval_seconds"`
	Version             int32   `json:"version"`
}

type ProviderConfiguration struct {
	ID                   string            `json:"id" format:"uuid"`
	DisplayName          string            `json:"display_name"`
	BaseURL              string            `json:"base_url"`
	CredentialID         *string           `json:"credential_id" format:"uuid"`
	HeaderNames          []string          `json:"header_names"`
	Headers              map[string]string `json:"headers"`
	ChatCompletionsPath  string            `json:"chat_completions_path"`
	ResponsesPath        *string           `json:"responses_path"`
	ModelsPath           string            `json:"models_path"`
	AllowHTTP            bool              `json:"allow_http"`
	AllowPrivateNetwork  bool              `json:"allow_private_network"`
	Lifecycle            string            `json:"lifecycle"`
	Version              int32             `json:"version"`
	ConfigurationVersion int32             `json:"configuration_version"`
}

type ModelConfiguration struct {
	ID                       string         `json:"id" format:"uuid"`
	EndpointID               string         `json:"endpoint_id" format:"uuid"`
	ModelID                  string         `json:"model_id"`
	Availability             string         `json:"availability"`
	CurrentVersionID         string         `json:"current_version_id" format:"uuid"`
	Transport                string         `json:"transport"`
	ContextWindowTokens      *int32         `json:"context_window_tokens"`
	MaxOutputTokens          *int32         `json:"max_output_tokens"`
	SupportsStreaming        *bool          `json:"supports_streaming"`
	SupportsTools            *bool          `json:"supports_tools"`
	SupportsStructuredOutput *bool          `json:"supports_structured_output"`
	SupportsTemperature      *bool          `json:"supports_temperature"`
	ReasoningTransport       string         `json:"reasoning_transport"`
	ReasoningMapping         map[string]any `json:"reasoning_mapping" nullable:"true"`
	TimeoutSeconds           int32          `json:"timeout_seconds"`
	MaxRetries               int32          `json:"max_retries"`
	ExtraBody                map[string]any `json:"extra_body"`
	Version                  int32          `json:"version"`
}

type ModelAssignmentConfiguration struct {
	ID              string `json:"id" format:"uuid"`
	KnowledgeBaseID string `json:"knowledge_base_id" format:"uuid"`
	Role            string `json:"role"`
	ModelProfileID  string `json:"model_profile_id" format:"uuid"`
	ReasoningEffort string `json:"reasoning_effort"`
	AnswerMode      string `json:"answer_mode"`
	Version         int32  `json:"version"`
}

type DiscordConnectionConfiguration struct {
	ID           string `json:"id" format:"uuid"`
	DisplayName  string `json:"display_name"`
	CredentialID string `json:"credential_id" format:"uuid"`
	Lifecycle    string `json:"lifecycle"`
	Version      int32  `json:"version"`
}

type DiscordBindingConfiguration struct {
	ID                string   `json:"id" format:"uuid"`
	ConnectionID      string   `json:"connection_id" format:"uuid"`
	ServerID          string   `json:"server_id"`
	ListenChannelID   string   `json:"listen_channel_id"`
	AgentID           string   `json:"agent_id" format:"uuid"`
	Triggers          []string `json:"triggers" enum:"mention,slash_command" nullable:"false"`
	ReplyPolicy       string   `json:"reply_policy"`
	ReplyChannelID    *string  `json:"reply_channel_id"`
	AllowedRoleIDs    []string `json:"allowed_role_ids"`
	AllowedUserIDs    []string `json:"allowed_user_ids"`
	RateRequests      int32    `json:"rate_requests"`
	RateWindowSeconds int32    `json:"rate_window_seconds"`
	Enabled           bool     `json:"enabled"`
	Version           int32    `json:"version"`
}

type AgentConfiguration struct {
	ID                     string   `json:"id" format:"uuid"`
	Key                    string   `json:"key"`
	Lifecycle              string   `json:"lifecycle" enum:"draft,active,archived"`
	CurrentVersionID       string   `json:"current_version_id" format:"uuid"`
	CurrentVersionNumber   int32    `json:"current_version_number"`
	DisplayName            string   `json:"display_name"`
	Description            string   `json:"description"`
	ResponseLanguage       string   `json:"response_language"`
	IdentityInstructions   string   `json:"identity_instructions"`
	ModelProfileID         string   `json:"model_profile_id" format:"uuid"`
	ReasoningEffort        string   `json:"reasoning_effort" enum:"none,minimal,low,medium,high,max"`
	AnswerMode             string   `json:"answer_mode" enum:"tool_calling,single_pass"`
	BehavioralInstructions string   `json:"behavioral_instructions"`
	EvidenceAccess         string   `json:"evidence_access" enum:"wiki_only,wiki_and_source"`
	RefusalMarkdown        string   `json:"refusal_markdown"`
	MaxToolCalls           int32    `json:"max_tool_calls"`
	MaxAnswerTokens        int32    `json:"max_answer_tokens"`
	KnowledgeBaseIDs       []string `json:"knowledge_base_ids" nullable:"false"`
	Version                int32    `json:"version"`
}

type ConfigurationExport struct {
	FormatVersion      int32                            `json:"format_version"`
	GeneratedAt        time.Time                        `json:"generated_at"`
	RedactedFields     []string                         `json:"redacted_fields"`
	Credentials        []CredentialConfiguration        `json:"credentials"`
	KnowledgeBases     []KnowledgeBaseConfiguration     `json:"knowledge_bases"`
	Sources            []any                            `json:"sources"`
	Providers          []ProviderConfiguration          `json:"providers"`
	Models             []ModelConfiguration             `json:"models"`
	ModelAssignments   []ModelAssignmentConfiguration   `json:"model_assignments"`
	Agents             []AgentConfiguration             `json:"agents" nullable:"false"`
	DiscordConnections []DiscordConnectionConfiguration `json:"discord_connections"`
	DiscordBindings    []DiscordBindingConfiguration    `json:"discord_bindings"`
}

type UnhealthySource struct {
	ID                string    `json:"id" format:"uuid"`
	KnowledgeBaseID   string    `json:"knowledge_base_id" format:"uuid"`
	KnowledgeBaseName string    `json:"knowledge_base_name"`
	DisplayName       string    `json:"display_name"`
	Lifecycle         string    `json:"lifecycle"`
	SanitizedError    string    `json:"sanitized_error"`
	CheckedAt         time.Time `json:"checked_at"`
}

type FailedJob struct {
	ID             string     `json:"id" format:"uuid"`
	JobType        string     `json:"job_type"`
	TargetType     string     `json:"target_type"`
	TargetID       string     `json:"target_id" format:"uuid"`
	AttemptCount   int32      `json:"attempt_count"`
	MaxAttempts    int32      `json:"max_attempts"`
	SanitizedError *string    `json:"sanitized_error"`
	UpdatedAt      time.Time  `json:"updated_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

type KnowledgeBaseIssue struct {
	ID              string    `json:"id" format:"uuid"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind" enum:"unpublished,stale"`
	PublishedWikiID *string   `json:"published_wiki_id" format:"uuid"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ProviderError struct {
	EndpointID     string    `json:"endpoint_id" format:"uuid"`
	EndpointName   string    `json:"endpoint_name"`
	Operation      string    `json:"operation" enum:"discovery,probe"`
	RunID          string    `json:"run_id" format:"uuid"`
	SanitizedError string    `json:"sanitized_error"`
	OccurredAt     time.Time `json:"occurred_at"`
}

type AgentFailure struct {
	ID                 string    `json:"id" format:"uuid"`
	AgentID            string    `json:"agent_id" format:"uuid"`
	AgentKey           string    `json:"agent_key"`
	DisplayName        string    `json:"display_name"`
	AgentVersionNumber int32     `json:"agent_version_number"`
	Origin             string    `json:"origin" enum:"http,discord"`
	SanitizedError     string    `json:"sanitized_error"`
	CreatedAt          time.Time `json:"created_at"`
	CompletedAt        time.Time `json:"completed_at"`
}

type OperationalOverview struct {
	GeneratedAt         time.Time            `json:"generated_at"`
	UnhealthySources    []UnhealthySource    `json:"unhealthy_sources"`
	FailedJobs          []FailedJob          `json:"failed_jobs"`
	KnowledgeBaseIssues []KnowledgeBaseIssue `json:"knowledge_base_issues"`
	ProviderErrors      []ProviderError      `json:"provider_errors"`
	AgentFailures       []AgentFailure       `json:"agent_failures"`
}
