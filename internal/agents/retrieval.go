package agents

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type WikiSearchHit struct {
	Slug          string
	Title         string
	Statement     *string
	ClaimStableID *string
	Rank          float64
	Linked        bool
	StartLine     int
}

type EvidenceCitation struct {
	Label            string
	Resource         string
	SourceRevisionID *SourceRevisionID
	Path             *string
	StartLine        *int
	EndLine          *int
}

type WikiPassage struct {
	Slug      string
	Title     string
	StartLine int
	EndLine   int
	Text      string
	Citation  EvidenceCitation
}

type Claim struct {
	StableID      string
	Statement     string
	PageSlug      string
	ClaimCitation EvidenceCitation
	Evidence      []EvidenceCitation
}

type ManifestEntry struct {
	Handle   string
	Position int32
	Label    string
}

type handleKind uint8

const (
	handlePage handleKind = iota + 1
	handleClaim
	handleSource
	handleSourcePath
)

type scopeHandle struct {
	kind        handleKind
	position    int
	slug        string
	claimID     string
	sourceIndex int
	path        string
}

type ScopeLedger struct {
	capture   ExecutionCapture
	handles   map[string]scopeHandle
	citations map[string]Citation
	manifest  []ManifestEntry
}

func NewScopeLedger(capture ExecutionCapture) (*ScopeLedger, error) {
	if len(capture.KnowledgeBases) == 0 || len(capture.KnowledgeBases) > MaxKnowledgeBases {
		return nil, fmt.Errorf("%w: captured corpus is invalid", ErrEvidence)
	}
	ledger := &ScopeLedger{
		capture: capture, handles: make(map[string]scopeHandle), citations: make(map[string]Citation),
	}
	for position, knowledgeBase := range capture.KnowledgeBases {
		if int(knowledgeBase.Position) != position || knowledgeBase.ID == (KnowledgeBaseID{}) || knowledgeBase.WikiVersionID == (WikiVersionID{}) {
			return nil, fmt.Errorf("%w: captured corpus is invalid", ErrEvidence)
		}
		if capture.Agent.CurrentVersion.Configuration.EvidenceAccess != WikiAndSource {
			continue
		}
		for sourceIndex, source := range knowledgeBase.Sources {
			handle, err := ledger.addHandle("src", scopeHandle{kind: handleSource, position: position, sourceIndex: sourceIndex})
			if err != nil {
				return nil, err
			}
			label := source.Label
			if label == "" {
				label = "Captured source"
			}
			ledger.manifest = append(ledger.manifest, ManifestEntry{Handle: handle, Position: knowledgeBase.Position, Label: label})
		}
	}
	return ledger, nil
}

func (ledger *ScopeLedger) Manifest() []ManifestEntry {
	return append([]ManifestEntry(nil), ledger.manifest...)
}

func (ledger *ScopeLedger) Citations() map[string]Citation {
	result := make(map[string]Citation, len(ledger.citations))
	for key, value := range ledger.citations {
		result[key] = value
	}
	return result
}

func (ledger *ScopeLedger) addHandle(prefix string, entry scopeHandle) (string, error) {
	for attempts := 0; attempts < 4; attempts++ {
		var entropy [18]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return "", fmt.Errorf("%w: handle generation failed", ErrEvidence)
		}
		handle := prefix + "_" + base64.RawURLEncoding.EncodeToString(entropy[:])
		if _, exists := ledger.handles[handle]; exists {
			continue
		}
		ledger.handles[handle] = entry
		return handle, nil
	}
	return "", fmt.Errorf("%w: handle generation failed", ErrEvidence)
}

func (ledger *ScopeLedger) resolve(handle string, kind handleKind) (scopeHandle, error) {
	entry, exists := ledger.handles[handle]
	if !exists || entry.kind != kind || entry.position < 0 || entry.position >= len(ledger.capture.KnowledgeBases) {
		return scopeHandle{}, fmt.Errorf("%w: evidence handle is unavailable", ErrEvidence)
	}
	return entry, nil
}

func (ledger *ScopeLedger) allow(position int, value EvidenceCitation) (Citation, error) {
	if position < 0 || position >= len(ledger.capture.KnowledgeBases) || value.Label == "" || value.Resource == "" {
		return Citation{}, fmt.Errorf("%w: citation is invalid", ErrEvidence)
	}
	if value.Path == nil || *value.Path == "" || !utf8.ValidString(*value.Path) ||
		(value.StartLine == nil) != (value.EndLine == nil) ||
		value.StartLine != nil && (*value.StartLine < 1 || *value.EndLine < *value.StartLine) {
		return Citation{}, fmt.Errorf("%w: citation is invalid", ErrEvidence)
	}
	knowledgeBase := ledger.capture.KnowledgeBases[position]
	if value.SourceRevisionID == nil {
		if _, err := normalizeAgentSlug(*value.Path); err != nil ||
			!strings.HasPrefix(value.Resource, "wiki://"+knowledgeBase.WikiVersionID.String()+"/") {
			return Citation{}, fmt.Errorf("%w: citation is outside captured wiki scope", ErrEvidence)
		}
	} else {
		if validateEvidencePath(*value.Path) != nil {
			return Citation{}, fmt.Errorf("%w: citation path is invalid", ErrEvidence)
		}
		var source *CapturedSource
		for index := range knowledgeBase.Sources {
			candidate := &knowledgeBase.Sources[index]
			if candidate.RevisionID != *value.SourceRevisionID {
				continue
			}
			if source != nil {
				return Citation{}, fmt.Errorf("%w: citation source is ambiguous", ErrEvidence)
			}
			source = candidate
		}
		if source == nil {
			return Citation{}, fmt.Errorf("%w: citation source is outside captured scope", ErrEvidence)
		}
		scheme := "repo"
		if source.Kind == "WEBSITE" {
			scheme = "web"
		} else if source.Kind != "REPOSITORY" {
			return Citation{}, fmt.Errorf("%w: citation source is invalid", ErrEvidence)
		}
		prefix := fmt.Sprintf("%s://%s@%s/", scheme, source.ID.String(), source.NativeVersion)
		if !strings.HasPrefix(value.Resource, prefix) {
			return Citation{}, fmt.Errorf("%w: citation resource is outside captured source scope", ErrEvidence)
		}
	}
	id, err := ledger.mintCitationID(position)
	if err != nil {
		return Citation{}, err
	}
	citation := Citation{
		ID: id, KnowledgeBaseID: knowledgeBase.ID.String(), WikiVersionID: knowledgeBase.WikiVersionID.String(),
		Label: value.Label, Resource: value.Resource, Path: cloneString(value.Path),
		StartLine: cloneInt(value.StartLine), EndLine: cloneInt(value.EndLine),
	}
	if value.SourceRevisionID != nil {
		revision := value.SourceRevisionID.String()
		citation.SourceRevisionID = &revision
	}
	ledger.citations[id] = citation
	return citation, nil
}

func (ledger *ScopeLedger) mintCitationID(position int) (string, error) {
	for attempts := 0; attempts < 4; attempts++ {
		var entropy [15]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return "", fmt.Errorf("%w: citation generation failed", ErrEvidence)
		}
		id := fmt.Sprintf("c%d_cite_%s", position+1, base64.RawURLEncoding.EncodeToString(entropy[:]))
		if _, exists := ledger.citations[id]; !exists {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: citation generation failed", ErrEvidence)
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func FairMerge[T any](groups [][]T, limit int) []T {
	if limit <= 0 {
		return []T{}
	}
	result := make([]T, 0, limit)
	for offset := 0; len(result) < limit; offset++ {
		added := false
		for _, group := range groups {
			if offset >= len(group) {
				continue
			}
			result = append(result, group[offset])
			added = true
			if len(result) == limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return result
}

type rankedHit struct {
	position int
	hit      WikiSearchHit
	page     string
	claim    string
}

type ToolRuntime struct {
	repository     ExecutionRepository
	ledger         *ScopeLedger
	sources        *SourceReader
	remainingCalls int
	callAudit      []string
}

func NewToolRuntime(repository ExecutionRepository, capture ExecutionCapture) (*ToolRuntime, error) {
	if repository == nil {
		return nil, errors.New("agent tool repository is required")
	}
	ledger, err := NewScopeLedger(capture)
	if err != nil {
		return nil, err
	}
	sources, err := NewSourceReader(capture.KnowledgeBases)
	if err != nil {
		return nil, err
	}
	return &ToolRuntime{
		repository: repository, ledger: ledger, sources: sources,
		remainingCalls: int(capture.Agent.CurrentVersion.Configuration.MaxToolCalls),
	}, nil
}

func (runtime *ToolRuntime) Manifest() []ManifestEntry      { return runtime.ledger.Manifest() }
func (runtime *ToolRuntime) Citations() map[string]Citation { return runtime.ledger.Citations() }
func (runtime *ToolRuntime) CallAudit() []string            { return append([]string(nil), runtime.callAudit...) }

func (runtime *ToolRuntime) InitialEvidence(ctx context.Context, question string, mode AnswerMode) ([]string, error) {
	hits, err := runtime.search(ctx, question, MaxSearchPerCorpus)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(hits))
	for _, hit := range hits {
		if mode == ToolCalling {
			encoded, marshalErr := marshalAgentJSON(searchHitPayload(hit))
			if marshalErr != nil {
				return nil, marshalErr
			}
			result = append(result, string(encoded))
			continue
		}
		var payload map[string]any
		if hit.claim != "" {
			payload, err = runtime.getClaim(ctx, hit.claim)
		} else {
			start := max(1, hit.hit.StartLine)
			end := start + 99
			payload, err = runtime.readWikiPage(ctx, hit.page, start, &end)
		}
		if err != nil {
			return nil, err
		}
		encoded, marshalErr := marshalAgentJSON(payload)
		if marshalErr != nil {
			return nil, marshalErr
		}
		result = append(result, string(encoded))
	}
	runtime.callAudit = append(runtime.callAudit, "initial_search_wiki")
	return result, nil
}

func (runtime *ToolRuntime) Dispatch(ctx context.Context, call ToolCall) (map[string]any, error) {
	if runtime.remainingCalls <= 0 {
		return nil, fmt.Errorf("%w: tool-call limit is exhausted", ErrEvidence)
	}
	runtime.remainingCalls--
	runtime.callAudit = append(runtime.callAudit, call.Name)
	switch call.Name {
	case "search_wiki":
		var arguments struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := decodeToolArguments(call.Arguments, &arguments); err != nil {
			return nil, fmt.Errorf("%w: search arguments are invalid", ErrEvidence)
		}
		if arguments.Limit == 0 {
			arguments.Limit = MaxSearchPerCorpus
		}
		hits, err := runtime.search(ctx, arguments.Query, arguments.Limit)
		if err != nil {
			return nil, err
		}
		values := make([]any, len(hits))
		for index, hit := range hits {
			values[index] = searchHitPayload(hit)
		}
		return map[string]any{"untrusted_evidence": true, "results": values}, nil
	case "read_wiki_page":
		var arguments struct {
			Handle    string `json:"handle"`
			StartLine int    `json:"start_line"`
			EndLine   *int   `json:"end_line"`
		}
		if err := decodeToolArguments(call.Arguments, &arguments); err != nil {
			return nil, fmt.Errorf("%w: page arguments are invalid", ErrEvidence)
		}
		if arguments.StartLine == 0 {
			arguments.StartLine = 1
		}
		return runtime.readWikiPage(ctx, arguments.Handle, arguments.StartLine, arguments.EndLine)
	case "get_claim":
		var arguments struct {
			Handle string `json:"handle"`
		}
		if err := decodeToolArguments(call.Arguments, &arguments); err != nil {
			return nil, fmt.Errorf("%w: Claim arguments are invalid", ErrEvidence)
		}
		return runtime.getClaim(ctx, arguments.Handle)
	case "search_source":
		if runtime.ledger.capture.Agent.CurrentVersion.Configuration.EvidenceAccess != WikiAndSource {
			return nil, fmt.Errorf("%w: source access is disabled", ErrEvidence)
		}
		var arguments struct {
			SourceHandle string `json:"source_handle"`
			Query        string `json:"query"`
			PathGlob     string `json:"path_glob"`
			Limit        int    `json:"limit"`
		}
		if err := decodeToolArguments(call.Arguments, &arguments); err != nil {
			return nil, fmt.Errorf("%w: source search arguments are invalid", ErrEvidence)
		}
		return runtime.searchSource(ctx, arguments.SourceHandle, arguments.Query, arguments.PathGlob, arguments.Limit)
	case "read_source":
		if runtime.ledger.capture.Agent.CurrentVersion.Configuration.EvidenceAccess != WikiAndSource {
			return nil, fmt.Errorf("%w: source access is disabled", ErrEvidence)
		}
		var arguments struct {
			PathHandle string `json:"path_handle"`
			StartLine  int    `json:"start_line"`
			EndLine    *int   `json:"end_line"`
		}
		if err := decodeToolArguments(call.Arguments, &arguments); err != nil {
			return nil, fmt.Errorf("%w: source read arguments are invalid", ErrEvidence)
		}
		if arguments.StartLine == 0 {
			arguments.StartLine = 1
		}
		return runtime.readSource(ctx, arguments.PathHandle, arguments.StartLine, arguments.EndLine)
	default:
		return nil, fmt.Errorf("%w: tool is unavailable", ErrEvidence)
	}
}

func (runtime *ToolRuntime) search(ctx context.Context, query string, perCorpus int) ([]rankedHit, error) {
	query = strings.TrimFunc(query, pythonWhitespace)
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 1000 || perCorpus < 1 || perCorpus > 20 {
		return nil, fmt.Errorf("%w: wiki search is invalid", ErrEvidence)
	}
	groups := make([][]rankedHit, len(runtime.ledger.capture.KnowledgeBases))
	for position, knowledgeBase := range runtime.ledger.capture.KnowledgeBases {
		hits, err := runtime.repository.SearchWiki(ctx, knowledgeBase, query, perCorpus)
		if err != nil {
			return nil, err
		}
		groups[position] = make([]rankedHit, 0, len(hits))
		for _, hit := range hits {
			page, err := runtime.ledger.addHandle("page", scopeHandle{kind: handlePage, position: position, slug: hit.Slug})
			if err != nil {
				return nil, err
			}
			claim := ""
			if hit.ClaimStableID != nil {
				claim, err = runtime.ledger.addHandle("claim", scopeHandle{kind: handleClaim, position: position, claimID: *hit.ClaimStableID})
				if err != nil {
					return nil, err
				}
			}
			groups[position] = append(groups[position], rankedHit{position: position, hit: hit, page: page, claim: claim})
		}
		sort.SliceStable(groups[position], func(left, right int) bool {
			first, second := groups[position][left].hit, groups[position][right].hit
			if first.Rank != second.Rank {
				return first.Rank > second.Rank
			}
			firstClaim, secondClaim := "", ""
			if first.ClaimStableID != nil {
				firstClaim = *first.ClaimStableID
			}
			if second.ClaimStableID != nil {
				secondClaim = *second.ClaimStableID
			}
			if first.Slug != second.Slug {
				return first.Slug < second.Slug
			}
			if firstClaim != secondClaim {
				return firstClaim < secondClaim
			}
			return first.Title < second.Title
		})
		if len(groups[position]) > perCorpus {
			groups[position] = groups[position][:perCorpus]
		}
	}
	return FairMerge(groups, MaxSearchResults), nil
}

func searchHitPayload(value rankedHit) map[string]any {
	return map[string]any{
		"untrusted_evidence": true, "corpus": value.position + 1, "page_handle": value.page,
		"claim_handle": value.claim, "title": value.hit.Title, "statement": value.hit.Statement,
		"rank": value.hit.Rank, "linked": value.hit.Linked, "start_line": max(1, value.hit.StartLine),
	}
}

func (runtime *ToolRuntime) readWikiPage(ctx context.Context, handle string, startLine int, endLine *int) (map[string]any, error) {
	entry, err := runtime.ledger.resolve(handle, handlePage)
	if err != nil {
		return nil, err
	}
	passage, err := runtime.repository.ReadWikiPage(ctx, runtime.ledger.capture.KnowledgeBases[entry.position], entry.slug, startLine, endLine)
	if err != nil {
		return nil, err
	}
	citation, err := runtime.ledger.allow(entry.position, passage.Citation)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"untrusted_evidence": true, "corpus": entry.position + 1, "title": passage.Title,
		"start_line": passage.StartLine, "end_line": passage.EndLine, "text": passage.Text,
		"citation_id": citation.ID,
	}, nil
}

func (runtime *ToolRuntime) getClaim(ctx context.Context, handle string) (map[string]any, error) {
	entry, err := runtime.ledger.resolve(handle, handleClaim)
	if err != nil {
		return nil, err
	}
	claim, err := runtime.repository.GetClaim(ctx, runtime.ledger.capture.KnowledgeBases[entry.position], entry.claimID)
	if err != nil {
		return nil, err
	}
	claimCitation, err := runtime.ledger.allow(entry.position, claim.ClaimCitation)
	if err != nil {
		return nil, err
	}
	evidence := make([]any, 0, len(claim.Evidence))
	for _, raw := range claim.Evidence {
		citation, allowErr := runtime.ledger.allow(entry.position, raw)
		if allowErr != nil {
			return nil, allowErr
		}
		evidence = append(evidence, map[string]any{"citation_id": citation.ID, "label": citation.Label, "resource": citation.Resource})
	}
	return map[string]any{
		"untrusted_evidence": true, "corpus": entry.position + 1, "statement": claim.Statement,
		"citation_id": claimCitation.ID, "evidence": evidence,
	}, nil
}

func (runtime *ToolRuntime) searchSource(ctx context.Context, handle, query, pathGlob string, limit int) (map[string]any, error) {
	entry, err := runtime.ledger.resolve(handle, handleSource)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 20 {
		return nil, fmt.Errorf("%w: source result limit is invalid", ErrEvidence)
	}
	if pathGlob == "" {
		pathGlob = "**/*"
	}
	passages, err := runtime.sources.Search(ctx, entry.position, entry.sourceIndex, query, pathGlob, limit)
	if err != nil {
		return nil, err
	}
	results := make([]any, 0, len(passages))
	for _, passage := range passages {
		pathHandle, addErr := runtime.ledger.addHandle("path", scopeHandle{
			kind: handleSourcePath, position: entry.position, sourceIndex: entry.sourceIndex, path: passage.Path,
		})
		if addErr != nil {
			return nil, addErr
		}
		citation, allowErr := runtime.ledger.allow(entry.position, passage.Citation)
		if allowErr != nil {
			return nil, allowErr
		}
		results = append(results, map[string]any{
			"untrusted_evidence": true, "path_handle": pathHandle, "path": passage.Path,
			"start_line": passage.StartLine, "end_line": passage.EndLine, "text": passage.Text,
			"citation_id": citation.ID,
		})
	}
	return map[string]any{"untrusted_evidence": true, "results": results}, nil
}

func (runtime *ToolRuntime) readSource(ctx context.Context, handle string, startLine int, endLine *int) (map[string]any, error) {
	entry, err := runtime.ledger.resolve(handle, handleSourcePath)
	if err != nil {
		return nil, err
	}
	passage, err := runtime.sources.Read(ctx, entry.position, entry.sourceIndex, entry.path, startLine, endLine)
	if err != nil {
		return nil, err
	}
	citation, err := runtime.ledger.allow(entry.position, passage.Citation)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"untrusted_evidence": true, "path": passage.Path, "start_line": passage.StartLine,
		"end_line": passage.EndLine, "text": passage.Text, "citation_id": citation.ID,
	}, nil
}

func toolDefinitions(access EvidenceAccess) []ToolDefinition {
	stringValue := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func(defaultValue int) map[string]any {
		return map[string]any{"type": "integer", "default": defaultValue}
	}
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	tools := []ToolDefinition{
		{Name: "search_wiki", Description: "Search every captured wiki fairly.", Schema: object(map[string]any{"query": stringValue(), "limit": integer(MaxSearchPerCorpus)}, "query")},
		{Name: "read_wiki_page", Description: "Read bounded lines using a page handle from this run.", Schema: object(map[string]any{"handle": stringValue(), "start_line": integer(1), "end_line": map[string]any{"type": []string{"integer", "null"}}}, "handle")},
		{Name: "get_claim", Description: "Read one Claim using a Claim handle from this run.", Schema: object(map[string]any{"handle": stringValue()}, "handle")},
	}
	if access == WikiAndSource {
		tools = append(tools,
			ToolDefinition{Name: "search_source", Description: "Search one captured source using its run handle.", Schema: object(map[string]any{"source_handle": stringValue(), "query": stringValue(), "path_glob": stringValue(), "limit": integer(10)}, "source_handle", "query")},
			ToolDefinition{Name: "read_source", Description: "Read bounded lines using a path handle minted by source search.", Schema: object(map[string]any{"path_handle": stringValue(), "start_line": integer(1), "end_line": map[string]any{"type": []string{"integer", "null"}}}, "path_handle")},
		)
	}
	return tools
}

func boundedToolResult(value map[string]any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxToolResultBytes {
		return "", fmt.Errorf("%w: tool result exceeds its bound", ErrEvidence)
	}
	return string(encoded), nil
}
