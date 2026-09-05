// Package agents owns stable Agent identity, immutable configuration versions,
// ordered knowledge-base membership, and catalog lifecycle readiness.
package agents

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	MaxKnowledgeBases       = 32
	MaxToolCalls            = 64
	MaxAnswerTokens         = 262_144
	MaxIdentityRunes        = 16_000
	MaxBehavioralRunes      = 16_000
	MaxDescriptionRunes     = 2_000
	MaxRefusalMarkdownRunes = 4_000
	MaxCatalogPageSize      = 100
	MaxVersionPageSize      = 100
	MaxRunPageSize          = 100
)

var (
	ErrInvalid              = errors.New("agent configuration is invalid")
	ErrNotFound             = errors.New("agent was not found")
	ErrConflict             = errors.New("agent mutation conflicts with current state")
	ErrNotReady             = errors.New("agent is not ready")
	ErrChatModelUnavailable = errors.New("chat model is unavailable")
)

type ID [16]byte
type AgentID ID
type VersionID ID
type KnowledgeBaseID ID
type ModelProfileID ID
type ModelProfileVersionID ID
type ProviderEndpointID ID

func (id ID) String() string {
	var encoded [36]byte
	hex.Encode(encoded[0:8], id[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], id[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], id[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], id[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], id[10:16])
	return string(encoded[:])
}

func (id AgentID) String() string               { return ID(id).String() }
func (id VersionID) String() string             { return ID(id).String() }
func (id KnowledgeBaseID) String() string       { return ID(id).String() }
func (id ModelProfileID) String() string        { return ID(id).String() }
func (id ModelProfileVersionID) String() string { return ID(id).String() }
func (id ProviderEndpointID) String() string    { return ID(id).String() }

func ParseID(value string) (ID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return ID{}, fmt.Errorf("%w: ID must use canonical UUID form", ErrInvalid)
	}
	var id ID
	if _, err := hex.Decode(id[:], []byte(strings.ReplaceAll(value, "-", ""))); err != nil || id.String() != value {
		return ID{}, fmt.Errorf("%w: ID must use canonical UUID form", ErrInvalid)
	}
	return id, nil
}

type Lifecycle string

const (
	Draft    Lifecycle = "DRAFT"
	Active   Lifecycle = "ACTIVE"
	Archived Lifecycle = "ARCHIVED"
)

type ReasoningEffort string

const (
	ReasoningNone    ReasoningEffort = "NONE"
	ReasoningMinimal ReasoningEffort = "MINIMAL"
	ReasoningLow     ReasoningEffort = "LOW"
	ReasoningMedium  ReasoningEffort = "MEDIUM"
	ReasoningHigh    ReasoningEffort = "HIGH"
	ReasoningMax     ReasoningEffort = "MAX"
)

type AnswerMode string

const (
	ToolCalling AnswerMode = "TOOL_CALLING"
	SinglePass  AnswerMode = "SINGLE_PASS"
)

type EvidenceAccess string

const (
	WikiOnly      EvidenceAccess = "WIKI_ONLY"
	WikiAndSource EvidenceAccess = "WIKI_AND_SOURCE"
)

type AccessPolicy string

const (
	Public     AccessPolicy = "PUBLIC"
	Restricted AccessPolicy = "RESTRICTED"
)

type Configuration struct {
	DisplayName            string
	Description            string
	ResponseLanguage       string
	IdentityInstructions   string
	ModelProfileID         ModelProfileID
	ReasoningEffort        ReasoningEffort
	AnswerMode             AnswerMode
	BehavioralInstructions string
	EvidenceAccess         EvidenceAccess
	RefusalMarkdown        string
	MaxToolCalls           int32
	MaxAnswerTokens        int32
	KnowledgeBaseIDs       []KnowledgeBaseID
}

type Membership struct {
	Position        int32
	KnowledgeBaseID KnowledgeBaseID
}

type Version struct {
	ID                VersionID
	AgentID           AgentID
	VersionNumber     int32
	Configuration     Configuration
	Memberships       []Membership
	CreatedByOperator [16]byte
	CreatedAt         time.Time
}

type Agent struct {
	ID               AgentID
	Key              string
	Lifecycle        Lifecycle
	CurrentVersionID VersionID
	CurrentVersion   Version
	Version          int32
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ActivatedAt      *time.Time
	ArchivedAt       *time.Time
}

func (agent Agent) Selector() string { return "agent:" + agent.Key }

type PageCursor struct {
	CreatedAt time.Time
	AgentID   AgentID
}

type Page struct {
	Agents     []Agent
	NextCursor *PageCursor
}

type VersionPageCursor struct {
	VersionNumber int32
	VersionID     VersionID
}

type VersionPage struct {
	Versions   []Version
	NextCursor *VersionPageCursor
}

type CreateCommand struct {
	Key           string
	Configuration Configuration
}

type ReplaceConfigurationCommand struct {
	AgentID         AgentID
	ExpectedVersion int32
	Configuration   Configuration
}

type SetLifecycleCommand struct {
	AgentID         AgentID
	ExpectedVersion int32
	Lifecycle       Lifecycle
}

type ReadinessIssueCode string

const (
	IssueModelUnavailable         ReadinessIssueCode = "MODEL_UNAVAILABLE"
	IssueEndpointUnavailable      ReadinessIssueCode = "ENDPOINT_UNAVAILABLE"
	IssueCredentialUnavailable    ReadinessIssueCode = "CREDENTIAL_UNAVAILABLE"
	IssueModelConfigurationStale  ReadinessIssueCode = "MODEL_CONFIGURATION_STALE"
	IssueModelLimitsUnknown       ReadinessIssueCode = "MODEL_LIMITS_UNKNOWN"
	IssueModelCapabilityMissing   ReadinessIssueCode = "MODEL_CAPABILITY_MISSING"
	IssueReasoningUnsupported     ReadinessIssueCode = "REASONING_UNSUPPORTED"
	IssueKnowledgeBaseMissing     ReadinessIssueCode = "KNOWLEDGE_BASE_MISSING"
	IssueKnowledgeBaseInactive    ReadinessIssueCode = "KNOWLEDGE_BASE_INACTIVE"
	IssueKnowledgeBaseUnpublished ReadinessIssueCode = "KNOWLEDGE_BASE_UNPUBLISHED"
)

type ReadinessIssue struct {
	Code            ReadinessIssueCode
	KnowledgeBaseID *KnowledgeBaseID
}

type Readiness struct {
	Ready                        bool
	EffectiveAccess              AccessPolicy
	ModelProfileVersionID        *ModelProfileVersionID
	ModelProfileVersionNumber    *int32
	ProviderEndpointID           *ProviderEndpointID
	EndpointConfigurationVersion *int32
	Issues                       []ReadinessIssue
}

type readinessError struct {
	readiness Readiness
}

func (err *readinessError) Error() string { return "agent is not ready" }
func (err *readinessError) Is(target error) bool {
	return target == ErrNotReady || target == ErrConflict
}
func (err *readinessError) Readiness() Readiness { return err.readiness }

func NotReadyDetails(err error) (Readiness, bool) {
	var value interface{ Readiness() Readiness }
	if !errors.As(err, &value) {
		return Readiness{}, false
	}
	return value.Readiness(), true
}
