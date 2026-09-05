package operations

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNonSecretMapRecursivelyRemovesCredentialLikeFields(t *testing.T) {
	input := map[string]any{
		"Authorization": "header-secret-sentinel",
		"X-Tenant":      "docs",
		"nested": map[string]any{
			"api_key": "nested-secret-sentinel",
			"values": []any{
				map[string]any{"cookie-value": "cookie-secret-sentinel", "safe": "kept"},
			},
		},
		"myPaßword": "unicode-secret-sentinel",
	}
	want := map[string]any{
		"X-Tenant": "docs",
		"nested": map[string]any{
			"values": []any{map[string]any{"safe": "kept"}},
		},
	}
	got := nonSecretMap(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized = %#v, want %#v", got, want)
	}
	serialized, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "secret-sentinel") {
		t.Fatalf("secret survived recursive scrub: %s", serialized)
	}
	if _, exists := input["Authorization"]; !exists {
		t.Fatal("scrub mutated its input")
	}
}

func TestSecretFieldMatchesPythonNormalizationShape(t *testing.T) {
	for _, name := range []string{
		"api_key", "Authorization", "cipher-text", "COOKIE", "n.o.n.c.e",
		"pass word", "client-secret", "access_token", "MyPaßWord",
	} {
		if !secretField(name) {
			t.Errorf("secretField(%q) = false", name)
		}
	}
	for _, name := range []string{"field", "values", "seed", "X-Tenant"} {
		if secretField(name) {
			t.Errorf("secretField(%q) = true", name)
		}
	}
}
