package modelbudget

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFitsIncludesOutputAndSafetyReserves(t *testing.T) {
	encoded := []byte(strings.Repeat("x", 400))
	if !Fits(encoded, 1_125, 1, 1_024) {
		t.Fatal("exact context boundary was rejected")
	}
	if Fits(encoded, 1_124, 1, 1_024) {
		t.Fatal("payload exceeding context by one token was accepted")
	}
}

func TestTruncateResultUsesExplicitLongestUTF8Prefix(t *testing.T) {
	original := []byte(`{"text":"` + strings.Repeat("é<&", 30) + `"}`)
	const maximum = 100
	fit := func(value map[string]any) bool {
		encoded, err := json.Marshal(value)
		return err == nil && len(encoded) <= maximum
	}
	bounded, err := TruncateResult(original, fit)
	if err != nil {
		t.Fatal(err)
	}
	prefix, ok := bounded["content_prefix"].(string)
	if !ok || prefix == "" || len(prefix) >= len(original) || !utf8.ValidString(prefix) ||
		bounded["truncated"] != true || bounded["original_bytes"] != len(original) || !fit(bounded) {
		t.Fatalf("invalid truncation envelope: %#v", bounded)
	}
	_, width := utf8.DecodeRune(original[len(prefix):])
	if width > 0 {
		larger := map[string]any{
			"content_prefix": string(original[:len(prefix)+width]),
			"original_bytes": len(original),
			"truncated":      true,
		}
		if fit(larger) {
			t.Fatal("truncation did not retain the longest fitting UTF-8 prefix")
		}
	}
	if _, err := TruncateResult(original, func(map[string]any) bool { return false }); err == nil {
		t.Fatal("an envelope that cannot fit was accepted")
	}
}
