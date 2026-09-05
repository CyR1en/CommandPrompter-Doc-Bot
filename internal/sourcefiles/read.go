package sourcefiles

import (
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

var ErrSourceSymlink = errors.New("source path contains a symlink")

func ReadFile(rootPath, selectedPath string, maximum int64) ([]byte, error) {
	if ValidateSourcePath(selectedPath) != nil || maximum < 1 {
		return nil, ErrInvalidSourcePath
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, errors.New("source path does not exist")
	}
	defer root.Close()
	current := ""
	for _, segment := range strings.Split(selectedPath, "/") {
		if current == "" {
			current = segment
		} else {
			current += "/" + segment
		}
		info, statErr := root.Lstat(current)
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrSourceSymlink
		}
	}
	info, err := root.Lstat(selectedPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errors.New("source path does not exist")
	}
	file, err := root.Open(selectedPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(content)) > maximum {
		return nil, errors.New("source file exceeds its read bound")
	}
	return content, nil
}

func SplitLines(value string) []string {
	if value == "" {
		return []string{}
	}
	lines := []string{}
	start := 0
	for index := 0; index < len(value); {
		width := 1
		separator := false
		switch value[index] {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e:
			separator = true
		case '\r':
			separator = true
			if index+1 < len(value) && value[index+1] == '\n' {
				width = 2
			}
		default:
			runeValue, runeWidth := utf8.DecodeRuneInString(value[index:])
			width = runeWidth
			separator = runeValue == '\u0085' || runeValue == '\u2028' || runeValue == '\u2029'
		}
		if separator {
			lines = append(lines, value[start:index])
			start = index + width
		}
		index += width
	}
	if start < len(value) {
		lines = append(lines, value[start:])
	}
	return lines
}
