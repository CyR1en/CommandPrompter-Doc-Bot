// Package providers owns provider endpoint configuration, immutable model
// profile versions, bounded discovery/probe captures, and their worker seam.
package providers

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/safenet"
)

const (
	MinModelTimeoutSeconds  = int32(safenet.MinModelTimeout / time.Second)
	MaxModelTimeoutSeconds  = int32(safenet.MaxModelTimeout / time.Second)
	MinModelConcurrentTasks = int32(1)
	MaxModelConcurrentTasks = int32(32)
)

type ID [16]byte
type EndpointID ID
type DiscoveryRunID ID
type ProfileID ID
type ProfileVersionID ID
type ProbeRunID ID
type ActorID ID
type KnowledgeBaseID ID
type AssignmentID ID

func (id ID) String() string {
	var raw [32]byte
	hex.Encode(raw[:], id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:])
}

func (id EndpointID) String() string       { return ID(id).String() }
func (id DiscoveryRunID) String() string   { return ID(id).String() }
func (id ProfileID) String() string        { return ID(id).String() }
func (id ProfileVersionID) String() string { return ID(id).String() }
func (id ProbeRunID) String() string       { return ID(id).String() }
func (id ActorID) String() string          { return ID(id).String() }
func (id KnowledgeBaseID) String() string  { return ID(id).String() }
func (id AssignmentID) String() string     { return ID(id).String() }

func ParseID(value string) (ID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return ID{}, errors.New("provider ID must use canonical UUID form")
	}
	var id ID
	if _, err := hex.Decode(id[:], []byte(strings.ReplaceAll(value, "-", ""))); err != nil {
		return ID{}, errors.New("provider ID must use canonical UUID form")
	}
	return id, nil
}

type Lifecycle string

const (
	Active   Lifecycle = "ACTIVE"
	Archived Lifecycle = "ARCHIVED"
)

type Health string

const (
	Unknown   Health = "UNKNOWN"
	Healthy   Health = "HEALTHY"
	Unhealthy Health = "UNHEALTHY"
)

type CaptureStatus string

const (
	CapturePending    CaptureStatus = "PENDING"
	CaptureRunning    CaptureStatus = "RUNNING"
	CaptureSucceeded  CaptureStatus = "SUCCEEDED"
	CaptureFailed     CaptureStatus = "FAILED"
	CaptureSuperseded CaptureStatus = "SUPERSEDED"
)

func (status CaptureStatus) Terminal() bool {
	return status == CaptureSucceeded || status == CaptureFailed || status == CaptureSuperseded
}

type Availability string

const (
	Available   Availability = "AVAILABLE"
	Unavailable Availability = "UNAVAILABLE"
	Manual      Availability = "MANUAL"
)

type Transport string

const (
	ChatCompletions Transport = "CHAT_COMPLETIONS"
	Responses       Transport = "RESPONSES"
)

type ReasoningTransport string

const (
	NoReasoning     ReasoningTransport = "NONE"
	ReasoningEffort ReasoningTransport = "REASONING_EFFORT"
	CustomReasoning ReasoningTransport = "CUSTOM"
)

type MetadataOrigin string

const (
	OriginUnknown    MetadataOrigin = "UNKNOWN"
	OriginDiscovered MetadataOrigin = "DISCOVERED"
	OriginProbed     MetadataOrigin = "PROBED"
	OriginOperator   MetadataOrigin = "OPERATOR"
)

type VersionSource string

const (
	VersionDiscovery VersionSource = "DISCOVERY"
	VersionOperator  VersionSource = "OPERATOR"
	VersionProbe     VersionSource = "PROBE"
)

type ProbeCheck string

const (
	ProbeChat             ProbeCheck = "CHAT"
	ProbeStreaming        ProbeCheck = "STREAMING"
	ProbeTools            ProbeCheck = "TOOLS"
	ProbeStructuredOutput ProbeCheck = "STRUCTURED_OUTPUT"
)

type ModelRole string

const (
	DocumentationPlanner ModelRole = "DOCUMENTATION_PLANNER"
	DocumentationWriter  ModelRole = "DOCUMENTATION_WRITER"
)

type Effort string

const (
	EffortNone    Effort = "NONE"
	EffortMinimal Effort = "MINIMAL"
	EffortLow     Effort = "LOW"
	EffortMedium  Effort = "MEDIUM"
	EffortHigh    Effort = "HIGH"
	EffortMax     Effort = "MAX"
)

type AnswerMode string

const (
	ToolCalling AnswerMode = "TOOL_CALLING"
	SinglePass  AnswerMode = "SINGLE_PASS"
)

type NonSecretHeaders map[string]string

type Configuration struct {
	DisplayName         string           `json:"display_name"`
	DisplayKey          string           `json:"display_key"`
	BaseURL             string           `json:"base_url"`
	CredentialID        *credentials.ID  `json:"credential_id"`
	Headers             NonSecretHeaders `json:"headers"`
	ChatCompletionsPath string           `json:"chat_completions_path"`
	ResponsesPath       *string          `json:"responses_path"`
	ModelsPath          string           `json:"models_path"`
	AllowHTTP           bool             `json:"allow_http"`
	AllowPrivateNetwork bool             `json:"allow_private_network"`
}

type Endpoint struct {
	ID                   EndpointID
	Configuration        Configuration
	Lifecycle            Lifecycle
	Version              int32
	ConfigurationVersion int32
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ArchivedAt           *time.Time
	Health               Health
	HealthCheckedAt      *time.Time
}

type CustomReasoningMapping struct {
	Field  string         `json:"field"`
	Values map[string]any `json:"values"`
}

type Settings struct {
	Transport                Transport                 `json:"transport"`
	ContextWindowTokens      *int32                    `json:"context_window_tokens"`
	MaxOutputTokens          *int32                    `json:"max_output_tokens"`
	SupportsStreaming        *bool                     `json:"supports_streaming"`
	SupportsTools            *bool                     `json:"supports_tools"`
	SupportsStructuredOutput *bool                     `json:"supports_structured_output"`
	SupportsTemperature      *bool                     `json:"supports_temperature"`
	ReasoningTransport       ReasoningTransport        `json:"reasoning_transport"`
	ReasoningMapping         *CustomReasoningMapping   `json:"reasoning_mapping"`
	TimeoutSeconds           int32                     `json:"timeout_seconds"`
	MaxRetries               int32                     `json:"max_retries"`
	MaxConcurrentTasks       int32                     `json:"max_concurrent_tasks"`
	ExtraBody                map[string]any            `json:"extra_body"`
	MetadataOrigin           map[string]MetadataOrigin `json:"metadata_origin"`
}

type ProfileVersion struct {
	ID                   ProfileVersionID
	ProfileID            ProfileID
	VersionNumber        int32
	ConfigurationVersion int32
	Settings             Settings
	Source               VersionSource
	CreatedByActorID     *ActorID
	CreatedAt            time.Time
}

type Profile struct {
	ID             ProfileID
	EndpointID     EndpointID
	ModelID        string
	Availability   Availability
	CurrentVersion ProfileVersion
	Version        int32
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type DiscoveryRun struct {
	ID                           DiscoveryRunID
	EndpointID                   EndpointID
	JobID                        jobs.JobID
	CapturedConfigurationVersion int32
	CapturedCredentialVersion    *int32
	TLSRequired                  bool
	RequestedByActorID           ActorID
	Status                       CaptureStatus
	ModelIDs                     []string
	RawResponse                  map[string]any
	TLSVerified                  *bool
	AuthenticationSucceeded      *bool
	HTTPStatus                   *int32
	ResponseSHA256               []byte
	ModelCount                   *int32
	SanitizedError               *string
	CreatedAt                    time.Time
	StartedAt                    *time.Time
	CompletedAt                  *time.Time
}

type ProbeFindings struct {
	ChatSucceeded            *bool `json:"chat_succeeded"`
	SupportsStreaming        *bool `json:"supports_streaming"`
	SupportsTools            *bool `json:"supports_tools"`
	SupportsStructuredOutput *bool `json:"supports_structured_output"`
}

type ProbeRun struct {
	ID                           ProbeRunID
	ProfileID                    ProfileID
	JobID                        jobs.JobID
	CapturedConfigurationVersion int32
	CapturedCredentialVersion    *int32
	CapturedProfileVersionID     ProfileVersionID
	RequestedByActorID           ActorID
	SelectedChecks               []ProbeCheck
	AcknowledgeCost              bool
	Status                       CaptureStatus
	Findings                     *ProbeFindings
	RawResponse                  map[string]any
	SanitizedError               *string
	ResultingVersionID           *ProfileVersionID
	CreatedAt                    time.Time
	StartedAt                    *time.Time
	CompletedAt                  *time.Time
}

type Assignment struct {
	ID              AssignmentID
	KnowledgeBaseID KnowledgeBaseID
	Role            ModelRole
	ProfileID       ProfileID
	ReasoningEffort Effort
	AnswerMode      AnswerMode
	Version         int32
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CreateEndpoint struct{ Configuration Configuration }

type UpdateEndpoint struct {
	EndpointID      EndpointID
	ExpectedVersion int32
	Configuration   Configuration
	Lifecycle       Lifecycle
}

type CreateProfile struct {
	EndpointID EndpointID
	ModelID    string
	Settings   Settings
}

type EditProfile struct {
	ProfileID       ProfileID
	ExpectedVersion int32
	Settings        Settings
}

type ScheduleDiscovery struct {
	EndpointID      EndpointID
	ExpectedVersion int32
}

type CompleteDiscovery struct {
	RunID                   DiscoveryRunID
	ModelIDs                []string
	RawResponse             map[string]any
	SanitizedError          string
	TLSVerified             *bool
	AuthenticationSucceeded *bool
	HTTPStatus              *int32
	Retryable               bool
}

type ScheduleProbe struct {
	ProfileID       ProfileID
	ExpectedVersion int32
	SelectedChecks  []ProbeCheck
	AcknowledgeCost bool
}

type CompleteProbe struct {
	RunID          ProbeRunID
	Findings       *ProbeFindings
	RawResponse    map[string]any
	SanitizedError string
	Retryable      bool
}

type AssignModel struct {
	KnowledgeBaseID KnowledgeBaseID
	Role            ModelRole
	ProfileID       ProfileID
	ReasoningEffort Effort
	AnswerMode      AnswerMode
	ExpectedVersion *int32
}

var (
	ErrNotFound = errors.New("provider resource not found")
	ErrConflict = errors.New("provider resource conflicts with current state")
)
