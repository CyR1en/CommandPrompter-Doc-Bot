package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/sourcefiles"
)

const (
	maxSourceReadBytes   = 256_000
	maxSourceReadLines   = 400
	maxSourceSearchFiles = 2_000
	maxSourceSearchBytes = 5_000_000
)

type SourcePassage struct {
	Path      string
	StartLine int
	EndLine   int
	Text      string
	Citation  EvidenceCitation
}

type SourceReader struct {
	knowledgeBases []CapturedKnowledgeBase
}

func NewSourceReader(knowledgeBases []CapturedKnowledgeBase) (*SourceReader, error) {
	for _, knowledgeBase := range knowledgeBases {
		for _, source := range knowledgeBase.Sources {
			if err := validateCapturedSource(source); err != nil {
				return nil, err
			}
		}
	}
	return &SourceReader{knowledgeBases: append([]CapturedKnowledgeBase(nil), knowledgeBases...)}, nil
}

func validateCapturedSource(source CapturedSource) error {
	if source.ID == (SourceID{}) || source.RevisionID == (SourceRevisionID{}) || !filepath.IsAbs(source.ArtifactRoot) ||
		source.NativeVersion == "" || len(source.NativeVersion) > 128 || source.Kind != "REPOSITORY" && source.Kind != "WEBSITE" {
		return fmt.Errorf("%w: captured source is invalid", ErrEvidence)
	}
	info, err := os.Lstat(source.ArtifactRoot)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: captured source is unavailable", ErrEvidence)
	}
	return nil
}

func (reader *SourceReader) Search(ctx context.Context, position, sourceIndex int, query, pathGlob string, limit int) ([]SourcePassage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := reader.source(position, sourceIndex)
	if err != nil {
		return nil, err
	}
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 1024 ||
		pathGlob == "" || !utf8.ValidString(pathGlob) || utf8.RuneCountInString(pathGlob) > 512 || limit < 1 || limit > 20 {
		return nil, fmt.Errorf("%w: source search is invalid", ErrEvidence)
	}
	var files int
	var searchedBytes int64
	result := make([]SourcePassage, 0, limit)
	err = filepath.WalkDir(source.ArtifactRoot, func(selected string, entry os.DirEntry, walkErr error) error {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if walkErr != nil {
			return walkErr
		}
		if selected == source.ArtifactRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("source snapshot contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		relative, relativeErr := filepath.Rel(source.ArtifactRoot, selected)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		if !sourceAllows(source, relative) || !globMatches(pathGlob, relative) {
			return nil
		}
		files++
		if files > maxSourceSearchFiles {
			return errors.New("source search exceeded its file bound")
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		searchedBytes += info.Size()
		if searchedBytes > maxSourceSearchBytes {
			return errors.New("source search exceeded its byte bound")
		}
		if info.Size() > maxSourceReadBytes {
			return nil
		}
		content, readErr := sourcefiles.ReadFile(source.ArtifactRoot, relative, maxSourceReadBytes)
		if readErr != nil || !utf8.Valid(content) {
			return nil
		}
		lines := sourcefiles.SplitLines(string(content))
		for index, line := range lines {
			if !strings.Contains(line, query) {
				continue
			}
			passage, passageErr := makeSourcePassage(source, relative, index+1, index+1, lines)
			if passageErr != nil {
				return passageErr
			}
			result = append(result, passage)
			if len(result) == limit {
				return io.EOF
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %v", ErrEvidence, err)
	}
	return result, nil
}

func (reader *SourceReader) Read(ctx context.Context, position, sourceIndex int, selectedPath string, startLine int, endLine *int) (SourcePassage, error) {
	if err := ctx.Err(); err != nil {
		return SourcePassage{}, err
	}
	source, err := reader.source(position, sourceIndex)
	if err != nil {
		return SourcePassage{}, err
	}
	if validateEvidencePath(selectedPath) != nil || !sourceAllows(source, selectedPath) || startLine < 1 || endLine != nil && *endLine < startLine {
		return SourcePassage{}, fmt.Errorf("%w: source read is invalid", ErrEvidence)
	}
	content, err := sourcefiles.ReadFile(source.ArtifactRoot, selectedPath, maxSourceReadBytes)
	if err != nil || !utf8.Valid(content) {
		return SourcePassage{}, fmt.Errorf("%w: source path is unavailable", ErrEvidence)
	}
	if err = ctx.Err(); err != nil {
		return SourcePassage{}, err
	}
	lines := sourcefiles.SplitLines(string(content))
	selectedEnd := startLine + maxSourceReadLines - 1
	if endLine != nil {
		selectedEnd = *endLine
	}
	if selectedEnd > len(lines) {
		selectedEnd = len(lines)
	}
	if startLine > len(lines) || selectedEnd-startLine+1 > maxSourceReadLines {
		return SourcePassage{}, fmt.Errorf("%w: source line range exceeds its bound", ErrEvidence)
	}
	return makeSourcePassage(source, selectedPath, startLine, selectedEnd, lines)
}

func (reader *SourceReader) source(position, sourceIndex int) (CapturedSource, error) {
	if position < 0 || position >= len(reader.knowledgeBases) || sourceIndex < 0 || sourceIndex >= len(reader.knowledgeBases[position].Sources) {
		return CapturedSource{}, fmt.Errorf("%w: source handle is unavailable", ErrEvidence)
	}
	return reader.knowledgeBases[position].Sources[sourceIndex], nil
}

func makeSourcePassage(source CapturedSource, selectedPath string, startLine, endLine int, lines []string) (SourcePassage, error) {
	resource, err := sourceResource(source, selectedPath, startLine, endLine)
	if err != nil {
		return SourcePassage{}, err
	}
	label := source.Label
	if label == "" {
		label = source.ID.String()
	}
	revision := source.RevisionID
	pathValue, start, end := selectedPath, startLine, endLine
	return SourcePassage{
		Path: selectedPath, StartLine: startLine, EndLine: endLine,
		Text: strings.Join(lines[startLine-1:endLine], "\n"),
		Citation: EvidenceCitation{
			Label: label, Resource: resource, SourceRevisionID: &revision, Path: &pathValue,
			StartLine: &start, EndLine: &end,
		},
	}, nil
}

func sourceResource(source CapturedSource, selectedPath string, startLine, endLine int) (string, error) {
	fragment := fmt.Sprintf("#L%d-L%d", startLine, endLine)
	if source.Kind == "WEBSITE" {
		base, exists := source.WebsitePages[selectedPath]
		if !exists {
			return "", fmt.Errorf("%w: website path is unavailable", ErrEvidence)
		}
		return base + fragment, nil
	}
	segments := strings.Split(selectedPath, "/")
	for index, segment := range segments {
		segments[index] = quoteSourceComponent(segment)
	}
	return fmt.Sprintf("repo://%s@%s/%s%s", source.ID.String(), source.NativeVersion, strings.Join(segments, "/"), fragment), nil
}

func sourceAllows(source CapturedSource, selectedPath string) bool {
	if validateEvidencePath(selectedPath) != nil {
		return false
	}
	if source.Kind == "REPOSITORY" {
		return true
	}
	_, exists := source.WebsitePages[selectedPath]
	return exists
}

func globMatches(pattern, selectedPath string) bool {
	if pattern == "**/*" || pattern == "**" {
		return true
	}
	matched, err := path.Match(pattern, selectedPath)
	if err == nil && matched {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		matched, _ = path.Match(strings.TrimPrefix(pattern, "**/"), selectedPath)
		if matched {
			return true
		}
	}
	if !strings.Contains(pattern, "/") {
		matched, _ = path.Match(pattern, path.Base(selectedPath))
	}
	return matched
}

func validateEvidencePath(value string) error {
	if value != strings.TrimFunc(value, pythonWhitespace) {
		return errors.New("source path is invalid")
	}
	return sourcefiles.ValidateSourcePath(value)
}

func quoteSourceComponent(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func loadCapturedWebsiteManifest(source CapturedSource) (map[string]string, error) {
	content, err := sourcefiles.ReadFile(source.ArtifactRoot, "website-manifest.json", 5*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("%w: website manifest is unavailable", ErrEvidence)
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	var manifest struct {
		NativeVersion string `json:"native_version"`
		Pages         []struct {
			CanonicalURL string `json:"canonical_url"`
			ContentPath  string `json:"content_path"`
			EvidenceURI  string `json:"evidence_uri"`
		} `json:"pages"`
	}
	if err = decoder.Decode(&manifest); err != nil || manifest.NativeVersion != source.NativeVersion || len(manifest.Pages) < 1 || len(manifest.Pages) > 10_000 {
		return nil, fmt.Errorf("%w: website manifest is invalid", ErrEvidence)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: website manifest is invalid", ErrEvidence)
	}
	result := make(map[string]string, len(manifest.Pages))
	for _, item := range manifest.Pages {
		if validateEvidencePath(item.ContentPath) != nil {
			return nil, fmt.Errorf("%w: website manifest is invalid", ErrEvidence)
		}
		parsed, parseErr := url.Parse(item.CanonicalURL)
		if parseErr != nil || !utf8.ValidString(item.CanonicalURL) || len([]byte(item.CanonicalURL)) > 4096 ||
			parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
			return nil, fmt.Errorf("%w: website manifest is invalid", ErrEvidence)
		}
		expected := fmt.Sprintf("web://%s@%s/%s", source.ID.String(), source.NativeVersion, quoteSourceComponent(item.CanonicalURL))
		if item.EvidenceURI == "" {
			item.EvidenceURI = expected
		}
		if item.EvidenceURI != expected {
			return nil, fmt.Errorf("%w: website manifest is invalid", ErrEvidence)
		}
		if _, exists := result[item.ContentPath]; exists {
			return nil, fmt.Errorf("%w: website manifest is invalid", ErrEvidence)
		}
		result[item.ContentPath] = expected
	}
	return result, nil
}
