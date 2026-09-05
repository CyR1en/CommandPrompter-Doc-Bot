package agents

import (
	"context"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/providers"
)

const (
	MaxTranscriptMessages = 100
	MaxTranscriptBytes    = 256 * 1024
	MaxMessageBytes       = 32 * 1024
	MaxSearchPerCorpus    = 8
	MaxSearchResults      = 32
	MaxToolResultBytes    = 256 * 1024
)

var (
	ErrExecutionUnavailable = errors.New("agent execution is unavailable")
	ErrExecutionForbidden   = errors.New("agent execution is forbidden")
	ErrExecutionInvalid     = errors.New("agent execution request is invalid")
	ErrExecutionConflict    = errors.New("agent execution receipt conflicts with stored state")
	ErrEvidence             = errors.New("agent evidence request failed")
)

type Origin string

const (
	OriginHTTP    Origin = "HTTP"
	OriginDiscord Origin = "DISCORD"
)

type MessageRole string

const (
	RoleSystem    MessageRole = "SYSTEM"
	RoleUser      MessageRole = "USER"
	RoleAssistant MessageRole = "ASSISTANT"
)

type Message struct {
	Role    MessageRole
	Content string
}

type ExecuteRequest struct {
	Selector  string
	Origin    Origin
	Subject   string
	Messages  []Message
	MaxTokens int32
}

type RunID ID
type WikiVersionID ID
type DocumentationRunID ID
type SourceID ID
type SourceRevisionID ID
type CredentialID ID

func (id RunID) String() string              { return ID(id).String() }
func (id WikiVersionID) String() string      { return ID(id).String() }
func (id DocumentationRunID) String() string { return ID(id).String() }
func (id SourceID) String() string           { return ID(id).String() }
func (id SourceRevisionID) String() string   { return ID(id).String() }
func (id CredentialID) String() string       { return ID(id).String() }

type CapturedSource struct {
	ID            SourceID
	RevisionID    SourceRevisionID
	NativeVersion string
	ArtifactRoot  string
	Kind          string
	Label         string
	WebsitePages  map[string]string
}

type CapturedKnowledgeBase struct {
	Position           int32
	ID                 KnowledgeBaseID
	ResourceVersion    int32
	AccessPolicy       AccessPolicy
	WikiVersionID      WikiVersionID
	DocumentationRunID DocumentationRunID
	Sources            []CapturedSource
	SourceScopeDigest  [32]byte
}

type CapturedModel struct {
	Endpoint                  providers.Endpoint
	Profile                   providers.Profile
	ProfileVersionID          ModelProfileVersionID
	ProfileVersionNumber      int32
	CapturedCredentialID      *CredentialID
	CapturedCredentialVersion *int32
	ReasoningEffort           ReasoningEffort
	AnswerMode                AnswerMode
}

func (model CapturedModel) ContextWindowTokens() (int, error) {
	value := model.Profile.CurrentVersion.Settings.ContextWindowTokens
	if value == nil || *value <= 0 {
		return 0, ErrExecutionUnavailable
	}
	return int(*value), nil
}

func (model CapturedModel) MaxOutputTokens() (int, error) {
	value := model.Profile.CurrentVersion.Settings.MaxOutputTokens
	if value == nil || *value <= 0 {
		return 0, ErrExecutionUnavailable
	}
	return int(*value), nil
}

type ExecutionCapture struct {
	RunID           RunID
	Agent           Agent
	Model           CapturedModel
	KnowledgeBases  []CapturedKnowledgeBase
	EffectiveAccess AccessPolicy
	CapturedAt      time.Time
}

type AuthorizationScope struct {
	AgentID              AgentID
	AgentVersionID       VersionID
	AgentResourceVersion int32
	AgentKey             string
	Origin               Origin
	Subject              string
	EffectiveAccess      AccessPolicy
	Corpus               []AuthorizedCorpusMember
}

type AuthorizedCorpusMember struct {
	Position             int32
	KnowledgeBaseID      KnowledgeBaseID
	KnowledgeBaseVersion int32
	AccessPolicy         AccessPolicy
	WikiVersionID        WikiVersionID
	SourceScopeDigest    [32]byte
}

type Authorizer interface {
	Authorize(context.Context, AuthorizationScope) error
}

type CompletionStatus string

const (
	CompletionAnswered             CompletionStatus = "ANSWERED"
	CompletionRefused              CompletionStatus = "REFUSED"
	CompletionInsufficientEvidence CompletionStatus = "INSUFFICIENT_EVIDENCE"
	CompletionFailed               CompletionStatus = "FAILED"
)

type Citation struct {
	ID               string  `json:"id"`
	KnowledgeBaseID  string  `json:"knowledge_base_id"`
	WikiVersionID    string  `json:"wiki_version_id"`
	Label            string  `json:"label"`
	Resource         string  `json:"resource"`
	SourceRevisionID *string `json:"source_revision_id,omitempty"`
	Path             *string `json:"path,omitempty"`
	StartLine        *int    `json:"start_line,omitempty"`
	EndLine          *int    `json:"end_line,omitempty"`
}

type ExecuteResult struct {
	RunID     RunID
	Status    CompletionStatus
	Markdown  string
	Citations []Citation
	Usage     map[string]int
	LatencyMS int
}

type RunRecord struct {
	Capture        ExecutionCapture
	Origin         Origin
	Subject        string
	RequestDigest  [32]byte
	Outcome        CompletionStatus
	Usage          map[string]int
	LatencyMS      int
	ToolCalls      []string
	Citations      []Citation
	SanitizedError *string
	CompletedAt    time.Time
}

type ExecutionRepository interface {
	Capture(context.Context, string) (ExecutionCapture, error)
	ReleaseCapture(context.Context, ExecutionCapture) error
	AssertFresh(context.Context, ExecutionCapture) error
	AssertSecurityFresh(context.Context, ExecutionCapture) error
	RecordRun(context.Context, RunRecord) (RunID, error)
	SearchWiki(context.Context, CapturedKnowledgeBase, string, int) ([]WikiSearchHit, error)
	ReadWikiPage(context.Context, CapturedKnowledgeBase, string, int, *int) (WikiPassage, error)
	GetClaim(context.Context, CapturedKnowledgeBase, string) (Claim, error)
}

type RequestDigester interface {
	DigestRequest(ExecutionCapture, ExecuteRequest) ([32]byte, error)
}

type ModelMessage struct {
	Role       string
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

type ModelRequest struct {
	Capture         ExecutionCapture
	Messages        []ModelMessage
	Tools           []ToolDefinition
	MaxOutputTokens int
	BeforeRequest   func(context.Context) error
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type DraftStatus string

const (
	DraftAnswered             DraftStatus = "answered"
	DraftRefused              DraftStatus = "refused"
	DraftInsufficientEvidence DraftStatus = "insufficient_evidence"
)

type DraftSpan struct {
	Markdown    string   `json:"markdown"`
	CitationIDs []string `json:"citation_ids"`
}

type AnswerDraft struct {
	Status DraftStatus `json:"status"`
	Spans  []DraftSpan `json:"spans"`
}

type ModelTurn struct {
	Draft     *AnswerDraft
	ToolCalls []ToolCall
	Usage     map[string]int
}

type Model interface {
	Complete(context.Context, ModelRequest) (ModelTurn, error)
}

type ToolDefinition struct {
	Name        string
	Description string
	Schema      map[string]any
}
