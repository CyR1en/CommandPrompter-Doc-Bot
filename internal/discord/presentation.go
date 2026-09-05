package discord

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

const (
	MessageLimit          = 2000
	EmbedDescriptionLimit = 3800
	EmbedColor            = 0x5865F2
	FallbackMessage       = "Sorry, I couldn't generate an answer for that. Please try again or rephrase your question."
	RateLimitMessage      = "You're asking questions a bit too quickly. Please try again shortly."
)

var (
	codeFenceHeader = regexp.MustCompile(`^\s*(` + "`" + `{3,}).*$`)
	headingFourPlus = regexp.MustCompile(`^(#{4,})\s+(.+?)\s*$`)
	tableSeparator  = regexp.MustCompile(`^:?-+:?$`)
)

func SanitizeMarkdown(text string) string {
	if text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inCode := false
	for index := 0; index < len(lines); {
		line := lines[index]
		if codeFenceHeader.MatchString(line) {
			inCode = !inCode
			out = append(out, line)
			index++
			continue
		}
		if inCode {
			out = append(out, line)
			index++
			continue
		}
		if isHorizontalRule(line) {
			out = append(out, "")
			index++
			continue
		}
		if isTableSeparator(line) && len(out) != 0 && !isTableSeparator(out[len(out)-1]) {
			rows := []string{}
			next := index + 1
			for next < len(lines) {
				candidate := lines[next]
				if !strings.HasPrefix(strings.TrimLeftFunc(candidate, unicode.IsSpace), "|") ||
					isHorizontalRule(candidate) || isTableSeparator(candidate) || codeFenceHeader.MatchString(candidate) {
					break
				}
				rows = append(rows, candidate)
				next++
			}
			if len(rows) != 0 {
				header := out[len(out)-1]
				out = out[:len(out)-1]
				out = append(out, tableBullets(header, rows)...)
				index = next
				continue
			}
		}
		if match := headingFourPlus.FindStringSubmatch(line); match != nil {
			out = append(out, "### "+match[2])
		} else {
			out = append(out, line)
		}
		index++
	}
	return normalizeSpacing(strings.Join(out, "\n"))
}

func SplitMessage(text string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, errors.New("limit must be a positive integer")
	}
	if text == "" {
		return []string{""}, nil
	}
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}, nil
	}
	chunks := []string{}
	remaining := []rune(text)
	for len(remaining) > limit {
		splitAt := splitPoint(remaining, limit)
		if startsWithPlainFence(remaining) && splitAt <= 3 {
			if limit <= 4 {
				return nil, errors.New("limit is too small to split a fenced code block")
			}
			splitAt = limit - 4
		}
		chunk := string(remaining[:splitAt])
		if chunk != "" && endsInsideCodeBlock(chunk) {
			if utf8.RuneCountInString(chunk)+4 > limit {
				if limit <= 4 {
					return nil, errors.New("limit is too small to split a fenced code block")
				}
				splitAt = splitPoint(remaining, limit-4)
				if startsWithPlainFence(remaining) && splitAt <= 3 {
					splitAt = limit - 4
				}
				chunk = string(remaining[:splitAt])
			}
			remaining = append([]rune(nil), remaining[splitAt:]...)
			chunk += "\n```"
			remaining = append([]rune("```\n"), remaining...)
		} else {
			remaining = append([]rune(nil), remaining[splitAt:]...)
		}
		chunks = append(chunks, chunk)
	}
	if len(remaining) != 0 {
		chunks = append(chunks, string(remaining))
	}
	return chunks, nil
}

func startsWithPlainFence(text []rune) bool {
	return len(text) >= 4 && text[0] == '`' && text[1] == '`' && text[2] == '`' && text[3] == '\n'
}

func AnswerEmbeds(markdown string, now time.Time) ([]*discordgo.MessageEmbed, error) {
	clean := SanitizeMarkdown(markdown)
	if clean == "" {
		return nil, nil
	}
	chunks, err := SplitMessage(clean, EmbedDescriptionLimit)
	if err != nil {
		return nil, err
	}
	stamp := now.UTC().Format(time.RFC3339)
	embeds := make([]*discordgo.MessageEmbed, 0, len(chunks))
	for index, chunk := range chunks {
		embed := &discordgo.MessageEmbed{
			Description: chunk,
			Color:       EmbedColor,
			Timestamp:   stamp,
		}
		if len(chunks) > 1 {
			embed.Footer = &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Page %d/%d", index+1, len(chunks))}
		}
		embeds = append(embeds, embed)
	}
	return embeds, nil
}

func isHorizontalRule(line string) bool {
	stripped := strings.TrimSpace(line)
	if utf8.RuneCountInString(stripped) < 3 {
		return false
	}
	runes := []rune(stripped)
	character := runes[0]
	if character != '-' && character != '*' && character != '_' {
		return false
	}
	count := 0
	for _, current := range runes {
		if current == ' ' {
			continue
		}
		if current != character {
			return false
		}
		count++
	}
	return count >= 3
}

func isTableSeparator(line string) bool {
	inner := strings.Trim(strings.TrimSpace(line), "|")
	if inner == "" {
		return false
	}
	for _, cell := range strings.Split(inner, "|") {
		if !tableSeparator.MatchString(strings.TrimSpace(cell)) {
			return false
		}
	}
	return true
}

func splitTableRow(line string) []string {
	stripped := strings.TrimSpace(line)
	stripped = strings.TrimPrefix(stripped, "|")
	stripped = strings.TrimSuffix(stripped, "|")
	values := strings.Split(stripped, "|")
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	return values
}

func tableBullets(header string, rows []string) []string {
	headers := splitTableRow(header)
	bullets := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row) == "" {
			continue
		}
		values := splitTableRow(row)
		if len(values) != len(headers) {
			bullets = append(bullets, "- "+strings.TrimSpace(row))
			continue
		}
		parts := make([]string, len(headers))
		for index := range headers {
			parts[index] = "**" + headers[index] + "**: " + values[index]
		}
		bullets = append(bullets, "- "+strings.Join(parts, "; "))
	}
	return bullets
}

func normalizeSpacing(text string) string {
	lines := strings.Split(text, "\n")
	result := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" {
			if blank || len(result) == 0 {
				continue
			}
			blank = true
			result = append(result, line)
			continue
		}
		blank = false
		result = append(result, line)
	}
	for len(result) != 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	return strings.Join(result, "\n")
}

func splitPoint(text []rune, limit int) int {
	window := string(text[:limit])
	for _, separator := range []string{"\n\n", "\n", " "} {
		byteIndex := strings.LastIndex(window, separator)
		if byteIndex > 0 {
			return utf8.RuneCountInString(window[:byteIndex])
		}
	}
	return limit
}

func endsInsideCodeBlock(text string) bool {
	count := 0
	for index := 0; index <= len(text)-3; {
		if text[index:index+3] == "```" {
			count++
			index += 3
			for index < len(text) && text[index] != '\n' {
				index++
			}
		} else {
			_, width := utf8.DecodeRuneInString(text[index:])
			index += width
		}
	}
	return count%2 == 1
}
