package sourcegit

import (
	"bytes"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/sourcefiles"
)

const (
	maximumPatternCount  = 1_000
	maximumPatternBytes  = 65_536
	maximumPatternLength = 4_096
)

var errInvalidPattern = errors.New("source pattern is invalid")

type filterPattern struct {
	segments []string
	ignored  bool
}

func newFilterPattern(raw string, allowNegation, defaultIgnored bool) (filterPattern, error) {
	value := strings.TrimSpace(raw)
	ignored := defaultIgnored
	if allowNegation && strings.HasPrefix(value, "!") {
		ignored = false
		value = strings.TrimPrefix(value, "!")
	}
	if value == "" || utf8.RuneCountInString(value) > maximumPatternLength || strings.Contains(value, `\`) || strings.ContainsAny(value, "[]") {
		return filterPattern{}, errInvalidPattern
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return filterPattern{}, errInvalidPattern
		}
	}
	value = strings.TrimPrefix(value, "/")
	directory := strings.HasSuffix(value, "/")
	value = strings.TrimSuffix(value, "/")
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, "**") && segment != "**" {
			return filterPattern{}, errInvalidPattern
		}
	}
	if len(segments) == 1 {
		segments = append([]string{"**"}, segments...)
	}
	if directory {
		segments = append(segments, "**")
	}
	return filterPattern{segments: segments, ignored: ignored}, nil
}

func (pattern filterPattern) matches(rawPath string) bool {
	values := strings.Split(rawPath, "/")
	closure := func(states map[int]struct{}) map[int]struct{} {
		result := make(map[int]struct{}, len(states)+1)
		pending := make([]int, 0, len(states))
		for state := range states {
			result[state] = struct{}{}
			pending = append(pending, state)
		}
		for len(pending) != 0 {
			index := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if index < len(pattern.segments) && pattern.segments[index] == "**" {
				following := index + 1
				if _, exists := result[following]; !exists {
					result[following] = struct{}{}
					pending = append(pending, following)
				}
			}
		}
		return result
	}
	states := closure(map[int]struct{}{0: {}})
	for _, value := range values {
		following := map[int]struct{}{}
		for index := range states {
			if index >= len(pattern.segments) {
				continue
			}
			segment := pattern.segments[index]
			if segment == "**" {
				following[index] = struct{}{}
				continue
			}
			matched, err := path.Match(segment, value)
			if err == nil && matched {
				following[index+1] = struct{}{}
			}
		}
		states = closure(following)
		if len(states) == 0 {
			return false
		}
	}
	_, matched := states[len(pattern.segments)]
	return matched
}

type pathFilter struct {
	include []filterPattern
	exclude []filterPattern
	ignore  []filterPattern
}

func newPathFilter(includeValues, excludeValues []string, ignoreFile []byte) (pathFilter, error) {
	if len(includeValues) > 100 || len(excludeValues) > 100 || stringBytes(includeValues) > maximumPatternBytes || stringBytes(excludeValues) > maximumPatternBytes {
		return pathFilter{}, errInvalidPattern
	}
	filter := pathFilter{}
	for _, value := range includeValues {
		pattern, err := newFilterPattern(value, false, false)
		if err != nil {
			return pathFilter{}, err
		}
		filter.include = append(filter.include, pattern)
	}
	for _, value := range excludeValues {
		pattern, err := newFilterPattern(value, false, true)
		if err != nil {
			return pathFilter{}, err
		}
		filter.exclude = append(filter.exclude, pattern)
	}
	if ignoreFile == nil {
		return filter, nil
	}
	if len(ignoreFile) > maximumPatternBytes || !utf8.Valid(ignoreFile) {
		return pathFilter{}, errInvalidPattern
	}
	lines := strings.FieldsFunc(string(ignoreFile), func(character rune) bool {
		switch character {
		case '\n', '\r', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', '\u2028', '\u2029':
			return true
		default:
			return false
		}
	})
	for _, line := range lines {
		value := strings.TrimSpace(line)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		pattern, err := newFilterPattern(value, true, true)
		if err != nil {
			return pathFilter{}, err
		}
		filter.ignore = append(filter.ignore, pattern)
		if len(filter.ignore) > maximumPatternCount {
			return pathFilter{}, errInvalidPattern
		}
	}
	return filter, nil
}

func (filter pathFilter) partition(paths []string) (selected, ignored []string, err error) {
	selected = []string{}
	ignored = []string{}
	values := append([]string(nil), paths...)
	slices.Sort(values)
	values = slices.Compact(values)
	for _, value := range values {
		if err := sourcefiles.ValidateSourcePath(value); err != nil {
			return nil, nil, err
		}
		permitted := true
		if len(filter.include) != 0 {
			permitted = false
			for _, pattern := range filter.include {
				if pattern.matches(value) {
					permitted = true
					break
				}
			}
		}
		if permitted {
			for _, pattern := range filter.exclude {
				if pattern.matches(value) {
					permitted = false
					break
				}
			}
		}
		if permitted {
			for _, pattern := range filter.ignore {
				if pattern.matches(value) {
					permitted = !pattern.ignored
				}
			}
		}
		if permitted {
			selected = append(selected, value)
		} else {
			ignored = append(ignored, value)
		}
	}
	return selected, ignored, nil
}

func stringBytes(values []string) int {
	total := 0
	for _, value := range values {
		total += len([]byte(value))
	}
	return total
}

func compactJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}
