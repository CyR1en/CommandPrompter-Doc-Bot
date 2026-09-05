package discord

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSanitizeMarkdownOracleCases(t *testing.T) {
	input := strings.Join([]string{
		"#### Details", "", "| Name | Value |", "| --- | :---: |", "| one | two |",
		"", "---", "", "```text", "| --- | untouched |", "#### literal", "```", "", "done   ",
	}, "\n")
	want := strings.Join([]string{
		"### Details", "", "- **Name**: one; **Value**: two", "", "```text",
		"| --- | untouched |", "#### literal", "```", "", "done",
	}, "\n")
	if got := SanitizeMarkdown(input); got != want {
		t.Fatalf("sanitize:\n%s\nwant:\n%s", got, want)
	}
}

func TestSplitMessagePreservesNaturalBoundariesAndCodeFences(t *testing.T) {
	chunks, err := SplitMessage("alpha beta gamma", 10)
	if err != nil || len(chunks) != 3 || chunks[0] != "alpha" || chunks[1] != " beta" || chunks[2] != " gamma" {
		t.Fatalf("chunks=%q err=%v", chunks, err)
	}
	code := "```go\n" + strings.Repeat("x", 30) + "\n```"
	chunks, err = SplitMessage(code, 20)
	if err != nil || len(chunks) < 2 {
		t.Fatalf("code chunks=%q err=%v", chunks, err)
	}
	for index, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > 20 {
			t.Fatalf("chunk %d exceeds limit: %d %q", index, utf8.RuneCountInString(chunk), chunk)
		}
		if strings.Count(chunk, "```")%2 != 0 {
			t.Fatalf("chunk %d has unbalanced fence: %q", index, chunk)
		}
	}
	if chunks, err := SplitMessage("", MessageLimit); err != nil || len(chunks) != 1 || chunks[0] != "" {
		t.Fatalf("empty=%q err=%v", chunks, err)
	}
}

func TestAnswerEmbedsHaveExactLimitsFooterAndTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 34, 56, 0, time.FixedZone("test", -6*60*60))
	embeds, err := AnswerEmbeds(strings.Repeat("word ", 1600), now)
	if err != nil || len(embeds) < 2 {
		t.Fatalf("embeds=%d err=%v", len(embeds), err)
	}
	for index, embed := range embeds {
		if utf8.RuneCountInString(embed.Description) > EmbedDescriptionLimit || embed.Color != EmbedColor || embed.Timestamp != "2026-08-30T18:34:56Z" {
			t.Fatalf("embed %d=%+v", index, embed)
		}
		wantFooter := fmt.Sprintf("Page %d/%d", index+1, len(embeds))
		if embed.Footer == nil || embed.Footer.Text != wantFooter {
			t.Fatalf("footer %d=%+v want=%s", index, embed.Footer, wantFooter)
		}
	}
}

func TestSplitRejectsInvalidLimit(t *testing.T) {
	if _, err := SplitMessage("text", 0); err == nil {
		t.Fatal("zero limit accepted")
	}
}
