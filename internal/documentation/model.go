// Package documentation owns durable documentation-run state and validation.
package docgen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/providers"
)

type ID = jobs.UUID
type RunID ID
type PageID ID
type WikiVersionID ID
type WikiPageID ID

func ParseID(raw string) (ID, error)    { return jobs.ParseUUID(raw) }
func (id RunID) String() string         { return ID(id).String() }
func (id PageID) String() string        { return ID(id).String() }
func (id WikiVersionID) String() string { return ID(id).String() }
func (id WikiPageID) String() string    { return ID(id).String() }

type RunStatus string

const (
	RunPreparing   RunStatus = "PREPARING"
	RunPlanning    RunStatus = "PLANNING"
	RunGenerating  RunStatus = "GENERATING"
	RunFinalizing  RunStatus = "FINALIZING"
	RunNoOp        RunStatus = "NO_OP"
	RunPublished   RunStatus = "PUBLISHED"
	RunInterrupted RunStatus = "INTERRUPTED"
	RunFailed      RunStatus = "FAILED"
)

func (status RunStatus) Terminal() bool {
	switch status {
	case RunNoOp, RunPublished, RunInterrupted, RunFailed:
		return true
	default:
		return false
	}
}

type PageStatus string

const (
	PagePending  PageStatus = "PENDING"
	PageRunning  PageStatus = "RUNNING"
	PageComplete PageStatus = "COMPLETE"
	PageSkipped  PageStatus = "SKIPPED"
)

var (
	ErrConflict   = errors.New("documentation state conflicts with the operation")
	ErrNotFound   = errors.New("documentation resource does not exist")
	ErrValidation = errors.New("documentation value is invalid")

	slugSegment = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	claimID     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,127}$`)
	commitID    = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type classifiedError struct {
	message string
	kind    error
}

func (err *classifiedError) Error() string        { return err.message }
func (err *classifiedError) Is(target error) bool { return target == err.kind }

func conflict(message string) error   { return &classifiedError{message, ErrConflict} }
func notFound(message string) error   { return &classifiedError{message, ErrNotFound} }
func validation(message string) error { return &classifiedError{message, ErrValidation} }

type ModelUsage struct {
	ModelCalls           int `json:"model_calls"`
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	TotalTokens          int `json:"total_tokens"`
	TruncatedToolResults int `json:"truncated_tool_results"`
}

func (usage ModelUsage) Validate() error {
	if usage.ModelCalls < 0 || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 || usage.TruncatedToolResults < 0 {
		return errors.New("model usage counters must be non-negative integers")
	}
	return nil
}

func (usage ModelUsage) Add(other ModelUsage) ModelUsage {
	return ModelUsage{
		ModelCalls:           usage.ModelCalls + other.ModelCalls,
		InputTokens:          usage.InputTokens + other.InputTokens,
		OutputTokens:         usage.OutputTokens + other.OutputTokens,
		TotalTokens:          usage.TotalTokens + other.TotalTokens,
		TruncatedToolResults: usage.TruncatedToolResults + other.TruncatedToolResults,
	}
}

func NormalizePageSlug(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || value != pythonStrip(value) || len([]byte(value)) > 255 ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") || strings.Contains(value, `\`) {
		return "", validation("page slug is invalid")
	}
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if !slugSegment.MatchString(segment) || strings.HasPrefix(segment, ".") ||
			(index == len(segments)-1 && (segment == "index" || segment == "log")) {
			return "", validation("page slug is invalid")
		}
	}
	return value, nil
}

func NormalizeSourcePath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || value != pythonStrip(value) ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") ||
		strings.Contains(value, `\`) || len([]byte(value)) > 4096 {
		return "", validation("evidence path is invalid")
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return "", validation("evidence path is invalid")
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", validation("evidence path is invalid")
		}
	}
	return value, nil
}

type SourceSeedPath struct {
	SourceID ID     `json:"source_id"`
	Path     string `json:"path"`
}

func NewSourceSeedPath(sourceID ID, path string) (SourceSeedPath, error) {
	normalized, err := NormalizeSourcePath(path)
	if err != nil {
		return SourceSeedPath{}, err
	}
	return SourceSeedPath{SourceID: sourceID, Path: normalized}, nil
}

func (seed SourceSeedPath) VirtualPath() string {
	return "/sources/" + seed.SourceID.String() + "/" + seed.Path
}

type PlannedPage struct {
	Slug            string
	Title           string
	Purpose         string
	RelatedPages    []string
	SourceSeedPaths []SourceSeedPath
}

func (page PlannedPage) Validate() error {
	slug, err := NormalizePageSlug(page.Slug)
	if err != nil || slug != page.Slug {
		return validation("page slug is invalid")
	}
	if !validTrimmedText(page.Title, 255) || !validTrimmedText(page.Purpose, 2000) {
		return validation("page title or purpose is invalid")
	}
	if len(page.RelatedPages) > 50 {
		return validation("related page set is invalid")
	}
	related := make(map[string]struct{}, len(page.RelatedPages))
	for _, raw := range page.RelatedPages {
		value, normalizeErr := NormalizePageSlug(raw)
		if normalizeErr != nil || value == page.Slug {
			return validation("related page set is invalid")
		}
		if _, exists := related[value]; exists {
			return validation("related page set is invalid")
		}
		related[value] = struct{}{}
	}
	if len(page.SourceSeedPaths) > 200 {
		return validation("source seed path set is invalid")
	}
	seeds := make(map[string]struct{}, len(page.SourceSeedPaths))
	for _, seed := range page.SourceSeedPaths {
		if _, err = NormalizeSourcePath(seed.Path); err != nil {
			return validation("source seed path set is invalid")
		}
		key := seed.SourceID.String() + "\x00" + seed.Path
		if _, exists := seeds[key]; exists {
			return validation("source seed path set is invalid")
		}
		seeds[key] = struct{}{}
	}
	return nil
}

type PagePlan struct {
	Pages []PlannedPage
}

func (plan PagePlan) Validate() error {
	if len(plan.Pages) < 1 || len(plan.Pages) > 200 {
		return validation("page plan must contain 1 to 200 pages")
	}
	known := make(map[string]struct{}, len(plan.Pages))
	for _, page := range plan.Pages {
		if err := page.Validate(); err != nil {
			return err
		}
		if _, exists := known[page.Slug]; exists {
			return validation("page plan slugs must be unique")
		}
		known[page.Slug] = struct{}{}
	}
	for _, page := range plan.Pages {
		parts := strings.Split(page.Slug, "/")
		for depth := 1; depth < len(parts); depth++ {
			if _, exists := known[strings.Join(parts[:depth], "/")]; exists {
				return validation("page plan slugs cannot be both files and directories")
			}
		}
		for _, related := range page.RelatedPages {
			if _, exists := known[related]; !exists {
				return validation("related page is outside the accepted plan")
			}
		}
	}
	return nil
}

func (plan PagePlan) SemanticDigest() ([sha256.Size]byte, error) {
	if err := plan.Validate(); err != nil {
		return [sha256.Size]byte{}, err
	}
	type canonicalSeed struct {
		Path     string `json:"path"`
		SourceID string `json:"source_id"`
	}
	type canonicalPage struct {
		Purpose         string          `json:"purpose"`
		RelatedPages    []string        `json:"related_pages"`
		Slug            string          `json:"slug"`
		SourceSeedPaths []canonicalSeed `json:"source_seed_paths"`
		Title           string          `json:"title"`
	}
	value := make([]canonicalPage, len(plan.Pages))
	for index, page := range plan.Pages {
		value[index] = canonicalPage{Purpose: page.Purpose, RelatedPages: nonNilStrings(page.RelatedPages), Slug: page.Slug, Title: page.Title}
		value[index].SourceSeedPaths = make([]canonicalSeed, len(page.SourceSeedPaths))
		for seedIndex, seed := range page.SourceSeedPaths {
			value[index].SourceSeedPaths[seedIndex] = canonicalSeed{Path: seed.Path, SourceID: seed.SourceID.String()}
		}
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return [sha256.Size]byte{}, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	encoded = unescapeSemanticLineSeparators(encoded)
	return sha256.Sum256(encoded), nil
}

func unescapeSemanticLineSeparators(encoded []byte) []byte {
	result := make([]byte, 0, len(encoded))
	for index := 0; index < len(encoded); {
		if encoded[index] == '\\' && index+6 <= len(encoded) &&
			(string(encoded[index:index+6]) == `\u2028` || string(encoded[index:index+6]) == `\u2029`) {
			preceding := 0
			for cursor := index - 1; cursor >= 0 && encoded[cursor] == '\\'; cursor-- {
				preceding++
			}
			if preceding%2 == 0 {
				if encoded[index+5] == '8' {
					result = append(result, []byte("\u2028")...)
				} else {
					result = append(result, []byte("\u2029")...)
				}
				index += 6
				continue
			}
		}
		result = append(result, encoded[index])
		index++
	}
	return result
}

type EvidenceResource struct {
	SourceID  ID
	Commit    string
	Path      string
	StartLine *int
	EndLine   *int
	Scheme    string
}

func NewEvidenceResource(sourceID ID, commit, path string, startLine, endLine *int, scheme string) (EvidenceResource, error) {
	if scheme == "" {
		scheme = "repo"
	}
	value := EvidenceResource{SourceID: sourceID, Commit: strings.ToLower(commit), Path: path, StartLine: cloneInt(startLine), EndLine: cloneInt(endLine), Scheme: scheme}
	if !commitID.MatchString(value.Commit) {
		return EvidenceResource{}, validation("evidence commit is invalid")
	}
	if scheme == "repo" {
		var err error
		value.Path, err = NormalizeSourcePath(path)
		if err != nil {
			return EvidenceResource{}, err
		}
	} else if scheme == "web" {
		parsed, err := url.Parse(path)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || len([]byte(path)) > 4096 {
			return EvidenceResource{}, validation("website evidence URL is invalid")
		}
	} else {
		return EvidenceResource{}, validation("evidence resource scheme is invalid")
	}
	if err := validateLineRange(startLine, endLine); err != nil {
		return EvidenceResource{}, err
	}
	return value, nil
}

func (resource EvidenceResource) Value() string {
	fragment := ""
	if resource.StartLine != nil {
		fragment = fmt.Sprintf("#L%d-L%d", *resource.StartLine, *resource.EndLine)
	}
	safe := "/-._~"
	if resource.Scheme == "web" {
		safe = ""
	}
	return resource.Scheme + "://" + resource.SourceID.String() + "@" + resource.Commit + "/" + percentEncode(resource.Path, safe) + fragment
}

func ParseEvidenceResource(value string) (EvidenceResource, error) {
	scheme := ""
	if strings.HasPrefix(value, "repo://") {
		scheme = "repo"
	} else if strings.HasPrefix(value, "web://") {
		scheme = "web"
	} else {
		return EvidenceResource{}, validation("evidence resource is invalid")
	}
	remainder := strings.TrimPrefix(value, scheme+"://")
	authorityPath, fragment, hasFragment := strings.Cut(remainder, "#")
	authority, encodedPath, hasSlash := strings.Cut(authorityPath, "/")
	sourceText, commit, hasAt := strings.Cut(authority, "@")
	if !hasSlash || !hasAt || encodedPath == "" || hasFragment && fragment == "" || strings.Contains(fragment, "#") {
		return EvidenceResource{}, validation("evidence resource is invalid")
	}
	sourceID, err := ParseID(sourceText)
	if err != nil || sourceID.String() != sourceText || commit != strings.ToLower(commit) {
		return EvidenceResource{}, validation("evidence resource is invalid")
	}
	path, err := percentDecode(encodedPath)
	if err != nil {
		return EvidenceResource{}, validation("evidence resource is invalid")
	}
	safe := "/-._~"
	if scheme == "web" {
		safe = ""
	}
	if percentEncode(path, safe) != encodedPath {
		return EvidenceResource{}, validation("evidence path encoding is not canonical")
	}
	var startLine, endLine *int
	if hasFragment {
		var start, end int
		if _, scanErr := fmt.Sscanf(fragment, "L%d-L%d", &start, &end); scanErr != nil || fragment != fmt.Sprintf("L%d-L%d", start, end) {
			return EvidenceResource{}, validation("evidence line range is invalid")
		}
		startLine, endLine = &start, &end
	}
	return NewEvidenceResource(sourceID, commit, path, startLine, endLine, scheme)
}

type EvidenceLocation struct {
	SourceID         ID
	SourceRevisionID ID
	SourceVersion    [sha256.Size]byte
	Commit           string
	Path             string
	StartLine        *int
	EndLine          *int
	ResourceURI      *string
}

func (location EvidenceLocation) Validate() error {
	resource, err := NewEvidenceResource(location.SourceID, location.Commit, location.Path, location.StartLine, location.EndLine, "repo")
	if err != nil {
		return err
	}
	if location.ResourceURI != nil {
		parsed, parseErr := ParseEvidenceResource(*location.ResourceURI)
		if parseErr != nil || parsed.Scheme != "web" || parsed.SourceID != location.SourceID || parsed.Commit != resource.Commit || !equalInt(parsed.StartLine, location.StartLine) || !equalInt(parsed.EndLine, location.EndLine) {
			return validation("evidence resource does not match its source")
		}
	}
	return nil
}

func (location EvidenceLocation) Resource() (string, error) {
	if err := location.Validate(); err != nil {
		return "", err
	}
	if location.ResourceURI != nil {
		return *location.ResourceURI, nil
	}
	resource, _ := NewEvidenceResource(location.SourceID, location.Commit, location.Path, location.StartLine, location.EndLine, "repo")
	return resource.Value(), nil
}

type ClaimEvidence struct {
	ID       string
	Location EvidenceLocation
}

type Claim struct {
	ID        string
	Statement string
	Evidence  []ClaimEvidence
}

func (claim Claim) Validate() error {
	if !claimID.MatchString(claim.ID) || !validTrimmedText(claim.Statement, 10000) || len(claim.Evidence) < 1 || len(claim.Evidence) > 50 {
		return validation("claim is invalid")
	}
	ids := make(map[string]struct{}, len(claim.Evidence))
	for _, evidence := range claim.Evidence {
		if !claimID.MatchString(evidence.ID) {
			return validation("evidence ID is invalid")
		}
		if _, exists := ids[evidence.ID]; exists {
			return validation("claim evidence IDs must be unique")
		}
		ids[evidence.ID] = struct{}{}
		if err := evidence.Location.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type PageSubmission struct {
	Slug     string
	Markdown string
	Claims   []Claim
}

func (submission PageSubmission) Validate() error {
	if _, err := NormalizePageSlug(submission.Slug); err != nil {
		return err
	}
	if !utf8.ValidString(submission.Markdown) || strings.TrimSpace(submission.Markdown) == "" || len([]byte(submission.Markdown)) > 1_048_576 || strings.ContainsRune(submission.Markdown, 0) {
		return validation("page draft is invalid")
	}
	if len(submission.Claims) > 500 {
		return validation("page Claim set is invalid")
	}
	ids := make(map[string]struct{}, len(submission.Claims))
	for _, claim := range submission.Claims {
		if err := claim.Validate(); err != nil {
			return err
		}
		if _, exists := ids[claim.ID]; exists {
			return validation("page Claim set is invalid")
		}
		ids[claim.ID] = struct{}{}
	}
	return nil
}

type CapturedSource struct {
	SourceID    ID
	RevisionID  ID
	Fingerprint [sha256.Size]byte
	Commit      string
	Kind        string
}

func (source CapturedSource) Validate() error {
	commit := strings.ToLower(source.Commit)
	if !commitID.MatchString(commit) || source.Commit != commit || source.Kind != "REPOSITORY" && source.Kind != "WEBSITE" {
		return errors.New("captured source commit is invalid")
	}
	return nil
}

type CapturedModel struct {
	Role                         providers.ModelRole
	ProfileID                    providers.ProfileID
	ProfileVersionID             providers.ProfileVersionID
	ProfileVersion               int
	EndpointID                   providers.EndpointID
	EndpointConfigurationVersion int
	CredentialVersion            *int
	ReasoningEffort              providers.Effort
	MaxConcurrentTasks           int
}

func (model CapturedModel) Validate() error {
	if model.ProfileVersion <= 0 || model.EndpointConfigurationVersion <= 0 ||
		model.CredentialVersion != nil && *model.CredentialVersion <= 0 ||
		model.MaxConcurrentTasks < int(providers.MinModelConcurrentTasks) || model.MaxConcurrentTasks > int(providers.MaxModelConcurrentTasks) {
		return errors.New("captured model settings are invalid")
	}
	switch model.Role {
	case providers.DocumentationPlanner, providers.DocumentationWriter:
	default:
		return errors.New("captured model role is invalid")
	}
	switch model.ReasoningEffort {
	case providers.EffortNone, providers.EffortMinimal, providers.EffortLow, providers.EffortMedium, providers.EffortHigh, providers.EffortMax:
	default:
		return errors.New("captured model reasoning effort is invalid")
	}
	return nil
}

type Run struct {
	ID                     RunID
	KnowledgeBaseID        ID
	Status                 RunStatus
	PrepareJobID           jobs.JobID
	KnowledgeBaseVersion   int
	Instructions           string
	Language               string
	Sources                []CapturedSource
	Models                 []CapturedModel
	PriorWikiVersionID     *WikiVersionID
	PlanDigest             []byte
	PublishedWikiVersionID *WikiVersionID
	SanitizedError         *string
	CreatedAt              time.Time
	UpdatedAt              time.Time
	CompletedAt            *time.Time
	PlannerUsage           ModelUsage
}

func (run Run) Validate() error {
	if run.KnowledgeBaseVersion <= 0 {
		return errors.New("captured knowledge-base version must be positive")
	}
	switch run.Status {
	case RunPreparing, RunPlanning, RunGenerating, RunFinalizing, RunNoOp, RunPublished, RunInterrupted, RunFailed:
	default:
		return errors.New("documentation run status is invalid")
	}
	if len(run.PlanDigest) != 0 && len(run.PlanDigest) != sha256.Size {
		return errors.New("plan digest must contain 32 bytes")
	}
	sources := make(map[ID]struct{}, len(run.Sources))
	for _, source := range run.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if _, exists := sources[source.SourceID]; exists {
			return errors.New("captured run sources must be unique")
		}
		sources[source.SourceID] = struct{}{}
	}
	roles := make(map[providers.ModelRole]struct{}, len(run.Models))
	for _, model := range run.Models {
		if err := model.Validate(); err != nil {
			return err
		}
		if _, exists := roles[model.Role]; exists {
			return errors.New("captured run models must be unique by role")
		}
		roles[model.Role] = struct{}{}
	}
	return run.PlannerUsage.Validate()
}

type Page struct {
	ID               PageID
	RunID            RunID
	JobID            jobs.JobID
	Position         int
	Target           PlannedPage
	Status           PageStatus
	SubmissionDigest []byte
	ContentSHA256    []byte
	ClaimsSHA256     []byte
	SanitizedError   *string
	AttemptCount     int
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      *time.Time
	Usage            ModelUsage
}

func (page Page) Validate() error {
	if page.Position < 0 || page.AttemptCount < 0 {
		return errors.New("documentation page counters are invalid")
	}
	if err := page.Target.Validate(); err != nil {
		return err
	}
	switch page.Status {
	case PagePending, PageRunning, PageComplete, PageSkipped:
	default:
		return errors.New("documentation page status is invalid")
	}
	if len(page.SubmissionDigest) != 0 && len(page.SubmissionDigest) != sha256.Size {
		return errors.New("page submission digest must contain 32 bytes")
	}
	if len(page.ContentSHA256) != len(page.ClaimsSHA256) || len(page.ContentSHA256) != 0 && len(page.ContentSHA256) != sha256.Size {
		return errors.New("page artifact hashes are invalid")
	}
	return page.Usage.Validate()
}

type RunDetail struct {
	Run   Run
	Pages []Page
}

func (detail RunDetail) Usage() ModelUsage {
	result := detail.Run.PlannerUsage
	for _, page := range detail.Pages {
		result = result.Add(page.Usage)
	}
	return result
}

type WikiVersion struct {
	ID                 WikiVersionID
	KnowledgeBaseID    ID
	DocumentationRunID RunID
	ArtifactKey        string
	ManifestSHA256     [sha256.Size]byte
	PageCount          int
	CreatedAt          time.Time
	PublishedAt        time.Time
}

func (version WikiVersion) Validate() error {
	if version.PageCount <= 0 {
		return errors.New("wiki version metadata is invalid")
	}
	return nil
}

type WikiPageSummary struct {
	Slug, Title, Description, PageType string
}

func (summary WikiPageSummary) Validate() error {
	_, err := NormalizePageSlug(summary.Slug)
	return err
}

type PublishedEvidence struct {
	ID       string
	Location EvidenceLocation
}

type PublishedClaim struct {
	ID        string
	Statement string
	Evidence  []PublishedEvidence
}

type PublishedWikiPage struct {
	Summary       WikiPageSummary
	Markdown      string
	ContentSHA256 [sha256.Size]byte
	ClaimsSHA256  [sha256.Size]byte
	Claims        []PublishedClaim
}

func (page PublishedWikiPage) Validate() error {
	if err := page.Summary.Validate(); err != nil {
		return err
	}
	for _, claim := range page.Claims {
		if !claimID.MatchString(claim.ID) {
			return errors.New("published claim ID is invalid")
		}
		for _, evidence := range claim.Evidence {
			if err := evidence.Location.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

type WikiView struct {
	Version WikiVersion
	Pages   []WikiPageSummary
	Page    *PublishedWikiPage
}

type WikiSearchHit struct {
	Slug, Title string
	Statement   *string
	Rank        float64
}

func (hit WikiSearchHit) Validate() error {
	if _, err := NormalizePageSlug(hit.Slug); err != nil || hit.Title == "" || hit.Rank < 0 {
		return errors.New("wiki search hit is invalid")
	}
	return nil
}

func validTrimmedText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && value == pythonStrip(value) && utf8.RuneCountInString(value) <= maximum
}

func pythonStrip(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || character >= '\x1c' && character <= '\x1f'
	})
}

func validateLineRange(start, end *int) error {
	if (start == nil) != (end == nil) {
		return validation("evidence line range is incomplete")
	}
	if start != nil && (*start < 1 || *start > *end || *end > 1_000_000) {
		return validation("evidence line range is invalid")
	}
	return nil
}

func percentEncode(value, safe string) string {
	const digits = "0123456789ABCDEF"
	var output strings.Builder
	for _, character := range []byte(value) {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune("-._~"+safe, rune(character)) {
			output.WriteByte(character)
		} else {
			output.WriteByte('%')
			output.WriteByte(digits[character>>4])
			output.WriteByte(digits[character&15])
		}
	}
	return output.String()
}

func percentDecode(value string) (string, error) {
	decoded := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			decoded = append(decoded, value[index])
			continue
		}
		if index+2 >= len(value) {
			return "", errors.New("invalid percent encoding")
		}
		var pair [1]byte
		if _, err := hex.Decode(pair[:], []byte(value[index+1:index+3])); err != nil {
			return "", err
		}
		decoded = append(decoded, pair[0])
		index += 2
	}
	if !utf8.Valid(decoded) {
		return "", errors.New("invalid utf-8")
	}
	return string(decoded), nil
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func equalInt(first, second *int) bool {
	return first == nil && second == nil || first != nil && second != nil && *first == *second
}

func nonNilStrings(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
}
