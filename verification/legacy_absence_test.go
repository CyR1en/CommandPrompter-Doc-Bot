package verification

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyAnswerAndDashboardChatSurfacesAreAbsent(t *testing.T) {
	root := repositoryRoot(t)
	for _, relative := range []string{
		"internal/answers",
		"internal/api/chat.go",
		"internal/api/chat_test.go",
		"frontend/src/features/chat",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("legacy path %s remains: %v", relative, err)
		}
	}

	assertFileOmits(t, root, "db/migrations/00001_baseline.sql", []string{
		"CREATE TABLE public.conversation_messages (",
		"CREATE TABLE public.conversations (",
		"CREATE TABLE public.query_runs (",
		"'ANSWER'::character varying",
	})
	assertFileOmits(t, root, "frontend/src/app/router.tsx", []string{`path: "/chat"`})
	for _, relative := range []string{".env.example", "docker-compose.yml", "docker-compose.portainer.yml"} {
		assertFileOmits(t, root, relative, []string{"CONVERSATION_IDLE_MINUTES", "CONVERSATION_RETENTION_DAYS"})
	}

	raw, err := os.ReadFile(filepath.Join(root, "frontend", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/conversations/{conversation_id}",
		"/api/v1/knowledge-bases/{knowledge_base_id}/chat",
	} {
		if _, exists := document.Paths[path]; exists {
			t.Errorf("legacy OpenAPI path %s remains", path)
		}
	}
	for _, schema := range []string{"ChatRequest", "ChatResponse", "ConversationResponse"} {
		if _, exists := document.Components.Schemas[schema]; exists {
			t.Errorf("legacy OpenAPI schema %s remains", schema)
		}
	}
}

func assertFileOmits(t *testing.T, root, relative string, forbidden []string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if strings.Contains(string(content), value) {
			t.Errorf("%s retains %q", relative, value)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(directory, ".."))
}
