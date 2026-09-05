package docgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/artifacts"
)

const (
	maximumFrontmatterBytes = 65_536
	maximumSourceFileBytes  = 10 * 1024 * 1024
)

var (
	claimReference    = regexp.MustCompile(`\[\^([a-z][a-z0-9_-]{0,127})\]`)
	claimDefinition   = regexp.MustCompile(`(?m)^\[\^[^\]]+\]:`)
	mermaidBlock      = regexp.MustCompile("(?ms)^```mermaid[ \\t]*\\n(.*?)^```[ \\t]*$")
	yamlNumber        = regexp.MustCompile(`^[+-]?(?:[0-9][0-9_,]*)(?:\.[0-9_]*)?(?:[eE][+-]?[0-9]+)?$`)
	yamlDate          = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}(?:[Tt]|$)`)
	mermaidDirectives = map[string]struct{}{
		"architecture-beta": {}, "block-beta": {}, "c4context": {}, "c4container": {},
		"c4deployment": {}, "c4dynamic": {}, "c4component": {}, "classdiagram": {},
		"erdiagram": {}, "flowchart": {}, "gantt": {}, "gitgraph": {}, "graph": {},
		"journey": {}, "kanban": {}, "mindmap": {}, "packet-beta": {}, "pie": {},
		"quadrantchart": {}, "requirementdiagram": {}, "sequencediagram": {},
		"statediagram": {}, "statediagram-v2": {}, "timeline": {}, "xychart-beta": {},
	}
)

type SourceFileReader interface {
	ReadSourceFile(context.Context, ID, string) ([]byte, error)
}

// ValidateConceptPage converts an untrusted page submission into the exact
// immutable artifact value accepted by the run and wiki stores.
func ValidateConceptPage(
	ctx context.Context,
	target PlannedPage,
	submission PageSubmission,
	captured map[ID]CapturedSource,
	reader SourceFileReader,
	verifiedAt time.Time,
	applicationVersion string,
) (artifacts.Page, error) {
	if err := target.Validate(); err != nil {
		return artifacts.Page{}, err
	}
	if err := submission.Validate(); err != nil {
		return artifacts.Page{}, err
	}
	if submission.Slug != target.Slug {
		return artifacts.Page{}, validation("page submission does not match its target")
	}
	if verifiedAt.Location() == nil || verifiedAt.IsZero() {
		return artifacts.Page{}, errors.New("verification time must include a UTC offset")
	}
	if applicationVersion == "" || utf8.RuneCountInString(applicationVersion) > 64 || strings.IndexFunc(applicationVersion, func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\n' }) >= 0 {
		return artifacts.Page{}, errors.New("application version is invalid")
	}
	frontmatter, body, err := parseFrontmatter(submission.Markdown)
	if err != nil {
		return artifacts.Page{}, err
	}
	pageType, okType := requiredFrontmatter(frontmatter, "type", 255)
	title, okTitle := requiredFrontmatter(frontmatter, "title", 255)
	description, okDescription := requiredFrontmatter(frontmatter, "description", 2000)
	if !okType {
		return artifacts.Page{}, validation("page type is required")
	}
	if !okTitle {
		return artifacts.Page{}, validation("page title is required")
	}
	if !okDescription {
		return artifacts.Page{}, validation("page description is required")
	}
	if title != target.Title {
		return artifacts.Page{}, validation("page title does not match the accepted plan")
	}
	firstContent := ""
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			firstContent = strings.TrimSpace(line)
			break
		}
	}
	if firstContent != "# "+target.Title {
		return artifacts.Page{}, validation("page body must start with its planned heading")
	}
	if claimDefinition.MatchString(body) {
		return artifacts.Page{}, validation("page worker cannot write Claim definitions")
	}
	references := map[string]struct{}{}
	for _, match := range claimReference.FindAllStringSubmatchIndex(body, -1) {
		if match[0] > 0 && body[match[0]-1] == '!' {
			continue
		}
		references[body[match[2]:match[3]]] = struct{}{}
	}
	claims := make(map[string]Claim, len(submission.Claims))
	for _, claim := range submission.Claims {
		claims[claim.ID] = claim
	}
	if !sameKeys(references, claims) {
		return artifacts.Page{}, validation("page Claim references must match the submitted Claim set")
	}
	seenEvidence := map[string]struct{}{}
	projected := make([]projectedSource, 0)
	for _, claim := range submission.Claims {
		for _, evidence := range claim.Evidence {
			if _, exists := seenEvidence[evidence.ID]; exists {
				return artifacts.Page{}, validation("evidence IDs must be unique within a page")
			}
			seenEvidence[evidence.ID] = struct{}{}
			if err = verifyEvidence(ctx, evidence.Location, captured, reader); err != nil {
				return artifacts.Page{}, err
			}
			resource, _ := evidence.Location.Resource()
			projected = append(projected, projectedSource{ID: evidence.ID, Resource: resource})
		}
	}

	normalizedBody := strings.TrimRight(normalizeMermaid(body), "\n") + "\n"
	if len(target.RelatedPages) != 0 {
		normalizedBody += "\n## Related pages\n\n"
		for _, related := range target.RelatedPages {
			normalizedBody += "- [" + titleFromSlug(related) + "](" + relativeLink(target.Slug, related) + ")\n"
		}
	}
	if len(submission.Claims) != 0 {
		normalizedBody += "\n"
		for _, claim := range submission.Claims {
			normalizedBody += "[^" + claim.ID + "]: " + claim.Statement + "\n"
		}
	}
	timestamp := verifiedAt.UTC().Format("2006-01-02T15:04:05")
	if verifiedAt.UTC().Nanosecond() != 0 {
		timestamp = verifiedAt.UTC().Format("2006-01-02T15:04:05.000000")
	}
	timestamp += "Z"
	rendered := renderFrontmatter(frontmatter, pageType, target.Title, description, projected, timestamp, applicationVersion)
	markdown := "---\n" + rendered + "---\n\n" + normalizedBody
	claimsJSON, err := marshalClaims(submission.Claims)
	if err != nil {
		return artifacts.Page{}, validation("page Claim snapshot is invalid")
	}
	contentDigest := sha256.Sum256([]byte(markdown))
	claimsDigest := sha256.Sum256(claimsJSON)
	return artifacts.Page{
		Slug: target.Slug, Title: target.Title, Description: description, PageType: pageType,
		Markdown: markdown, ContentSHA256: contentDigest, ClaimsJSON: claimsJSON, ClaimsSHA256: claimsDigest,
	}, nil
}

type frontmatterField struct {
	Key   string
	Value string
	Raw   []string
}

func parseFrontmatter(markdown string) ([]frontmatterField, string, error) {
	if !strings.HasPrefix(markdown, "---\n") {
		return nil, "", validation("page frontmatter is required")
	}
	end := strings.Index(markdown[4:], "\n---\n")
	if end < 0 || end > maximumFrontmatterBytes {
		return nil, "", validation("page frontmatter is invalid")
	}
	raw := markdown[4 : 4+end]
	if strings.Count(raw, "*") > 20 || strings.Count(raw, "&") > 20 || strings.ContainsRune(raw, 0) {
		return nil, "", validation("page frontmatter is invalid")
	}
	lines := strings.Split(raw, "\n")
	fields := make([]frontmatterField, 0)
	for index := 0; index < len(lines); {
		line := lines[index]
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			return nil, "", validation("page frontmatter is invalid")
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" || key != strings.TrimSpace(key) {
			return nil, "", validation("page frontmatter is invalid")
		}
		field := frontmatterField{Key: key, Value: strings.TrimSpace(value), Raw: []string{line}}
		index++
		for index < len(lines) && (strings.HasPrefix(lines[index], " ") || strings.HasPrefix(lines[index], "-") || strings.TrimSpace(lines[index]) == "") {
			field.Raw = append(field.Raw, lines[index])
			index++
		}
		fields = append(fields, field)
	}
	body := strings.TrimLeft(markdown[4+end+5:], "\n")
	return fields, body, nil
}

func requiredFrontmatter(fields []frontmatterField, key string, maximum int) (string, bool) {
	for _, field := range fields {
		if field.Key != key || field.Value == "" || len(field.Raw) != 1 {
			continue
		}
		value := field.Value
		if len(value) >= 2 && (value[0] == '\'' && value[len(value)-1] == '\'' || value[0] == '"' && value[len(value)-1] == '"') {
			value = value[1 : len(value)-1]
		}
		if validTrimmedText(value, maximum) {
			return value, true
		}
	}
	return "", false
}

type projectedSource struct{ ID, Resource string }

func renderFrontmatter(fields []frontmatterField, pageType, title, description string, sources []projectedSource, timestamp, version string) string {
	var output strings.Builder
	seen := make(map[string]bool, len(fields))
	writeCanonical := func(key string) {
		switch key {
		case "type":
			output.WriteString("type: " + yamlScalar(pageType) + "\n")
		case "title":
			output.WriteString("title: " + yamlScalar(title) + "\n")
		case "description":
			output.WriteString("description: " + yamlScalar(description) + "\n")
		case "generated":
			output.WriteString("generated:\n  by: ")
			output.WriteString(yamlScalar("ref0-doc-platform/" + version))
			output.WriteString("\n  at: ")
			output.WriteString(yamlScalar(timestamp))
			output.WriteByte('\n')
		case "verified":
			output.WriteString("verified:\n  by: process:claim-validator\n  at: ")
			output.WriteString(yamlScalar(timestamp))
			output.WriteByte('\n')
		case "sources":
			output.WriteString("sources:")
			if len(sources) == 0 {
				output.WriteString(" []\n")
			} else {
				output.WriteByte('\n')
				for _, source := range sources {
					output.WriteString("- id: ")
					output.WriteString(yamlScalar(source.ID))
					output.WriteString("\n  resource: ")
					output.WriteString(yamlScalar(source.Resource))
					output.WriteByte('\n')
				}
			}
		}
	}
	for _, field := range fields {
		if seen[field.Key] {
			continue
		}
		seen[field.Key] = true
		if field.Key == "generated" || field.Key == "verified" || field.Key == "sources" || field.Key == "type" || field.Key == "title" || field.Key == "description" {
			writeCanonical(field.Key)
			continue
		}
		for _, line := range field.Raw {
			output.WriteString(line)
			output.WriteByte('\n')
		}
	}
	for _, key := range []string{"type", "title", "description", "generated", "verified", "sources"} {
		if !seen[key] {
			writeCanonical(key)
		}
	}
	return output.String()
}

func yamlScalar(value string) string {
	lower := strings.ToLower(value)
	ambiguous := lower == "null" || lower == "~" || lower == "true" || lower == "false" ||
		lower == "yes" || lower == "no" || lower == "on" || lower == "off" ||
		yamlNumber.MatchString(value) || yamlDate.MatchString(value)
	unsafeFirst := value == "" || strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", rune(value[0]))
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\n\r") || strings.Contains(value, ": ") ||
		strings.Contains(value, " #") || unsafeFirst || ambiguous {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return value
}

func verifyEvidence(ctx context.Context, location EvidenceLocation, captured map[ID]CapturedSource, reader SourceFileReader) error {
	selected, exists := captured[location.SourceID]
	if !exists || selected.RevisionID != location.SourceRevisionID || selected.Fingerprint != location.SourceVersion || selected.Commit != strings.ToLower(location.Commit) {
		return validation("evidence does not resolve to a captured source revision")
	}
	if reader == nil {
		return validation("evidence path does not exist")
	}
	content, err := reader.ReadSourceFile(ctx, location.SourceRevisionID, location.Path)
	if err != nil {
		return validation("evidence path does not exist")
	}
	if len(content) > maximumSourceFileBytes {
		return validation("evidence source file is invalid")
	}
	if !utf8.Valid(content) {
		return validation("evidence source file is not UTF-8 text")
	}
	if location.EndLine != nil && *location.EndLine > len(strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")) {
		return validation("evidence line range does not exist")
	}
	return nil
}

func marshalClaims(claims []Claim) ([]byte, error) {
	type encodedEvidence struct {
		ID               string `json:"id"`
		Resource         string `json:"resource"`
		SourceRevisionID string `json:"source_revision_id"`
		SourceVersion    string `json:"source_version"`
	}
	type encodedClaim struct {
		Evidence  []encodedEvidence `json:"evidence"`
		ID        string            `json:"id"`
		Statement string            `json:"statement"`
	}
	type payload struct {
		Claims []encodedClaim `json:"claims"`
	}
	value := payload{Claims: make([]encodedClaim, len(claims))}
	for claimIndex, claim := range claims {
		value.Claims[claimIndex] = encodedClaim{ID: claim.ID, Statement: claim.Statement, Evidence: make([]encodedEvidence, len(claim.Evidence))}
		for evidenceIndex, evidence := range claim.Evidence {
			resource, err := evidence.Location.Resource()
			if err != nil {
				return nil, err
			}
			value.Claims[claimIndex].Evidence[evidenceIndex] = encodedEvidence{
				ID: evidence.ID, Resource: resource, SourceRevisionID: evidence.Location.SourceRevisionID.String(), SourceVersion: hex.EncodeToString(evidence.Location.SourceVersion[:]),
			}
		}
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func normalizeMermaid(markdown string) string {
	return mermaidBlock.ReplaceAllStringFunc(markdown, func(block string) string {
		match := mermaidBlock.FindStringSubmatch(block)
		body := strings.Trim(match[1], "\n")
		if validMermaid(body) {
			return "```mermaid\n" + body + "\n```"
		}
		return "```text\nDiagram source (not rendered):\n" + body + "\n```"
	})
}

func validMermaid(body string) bool {
	if body == "" || len([]byte(body)) > 65_536 || strings.Contains(body, "```") {
		return false
	}
	lines := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	if len(lines) < 1 || len(lines) > 1000 {
		return false
	}
	directive := strings.ToLower(strings.Fields(lines[0])[0])
	if _, exists := mermaidDirectives[directive]; !exists {
		return false
	}
	stack := []rune{}
	var quote rune
	escaped := false
	for _, character := range body {
		if character < 9 || character > 13 && character < 32 {
			return false
		}
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if character == '\'' || character == '"' {
			if quote == character {
				quote = 0
			} else if quote == 0 {
				quote = character
			}
			continue
		}
		if quote != 0 {
			continue
		}
		switch character {
		case '(':
			stack = append(stack, ')')
		case '[':
			stack = append(stack, ']')
		case '{':
			stack = append(stack, '}')
		case ')', ']', '}':
			if len(stack) == 0 || stack[len(stack)-1] != character {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return quote == 0 && len(stack) == 0
}

func titleFromSlug(slug string) string {
	selected := path.Base(slug)
	parts := strings.Split(selected, "-")
	for index, part := range parts {
		if part != "" {
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func relativeLink(source, target string) string {
	sourceParts := strings.Split(source, "/")
	sourceParts = sourceParts[:len(sourceParts)-1]
	targetParts := strings.Split(target, "/")
	common := 0
	for common < len(sourceParts) && common < len(targetParts) && sourceParts[common] == targetParts[common] {
		common++
	}
	parts := make([]string, len(sourceParts)-common)
	for index := range parts {
		parts[index] = ".."
	}
	parts = append(parts, targetParts[common:]...)
	return strings.Join(parts, "/") + ".md"
}

func sameKeys(first map[string]struct{}, second map[string]Claim) bool {
	if len(first) != len(second) {
		return false
	}
	keys := make([]string, 0, len(first))
	for key := range first {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := second[key]; !exists {
			return false
		}
	}
	return true
}
