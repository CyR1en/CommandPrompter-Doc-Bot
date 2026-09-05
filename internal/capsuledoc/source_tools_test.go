package capsuledoc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	docgen "github.com/cyr1en/ref0/internal/documentation"
)

func repositorySnapshot(t *testing.T) sourceSnapshot {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "service.py"), []byte("alpha\nbeta alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "nested", "other.py"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "app", "service.py"), filepath.Join(root, "linked.py")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := newSourceSnapshot(docgen.CapturedSource{
		SourceID:   testID(t, "11111111-1111-4111-8111-111111111111"),
		RevisionID: testID(t, "22222222-2222-4222-8222-222222222222"),
		Commit:     strings.Repeat("a", 40), Kind: "REPOSITORY",
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestSourceToolsListGlobGrepReadAreBoundedAndSymlinkSafe(t *testing.T) {
	snapshot := repositorySnapshot(t)
	tools, err := newSourceToolSession([]sourceSnapshot{snapshot}, DefaultSourceToolLimits())
	if err != nil {
		t.Fatal(err)
	}
	root := "/sources/" + snapshot.Captured.SourceID.String()
	listed, err := tools.list(root)
	if err != nil {
		t.Fatal(err)
	}
	entries := listed["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["path"] != root+"/app" {
		t.Fatalf("list leaked or omitted entries: %#v", entries)
	}
	globbed, err := tools.glob(root + "/*.py")
	if err != nil {
		t.Fatal(err)
	}
	paths := globbed["paths"].([]any)
	if len(paths) != 2 {
		t.Fatalf("Python fnmatch semantics were not preserved: %#v", paths)
	}
	grepped, err := tools.grep(root+"/app", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if matches := grepped["matches"].([]any); len(matches) != 3 {
		t.Fatalf("unexpected literal grep result: %#v", matches)
	}
	read, err := tools.read(root+"/app/service.py", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lines := read["lines"].([]any); len(lines) != 1 || lines[0] != "beta alpha" {
		t.Fatalf("unexpected read result: %#v", read)
	}
	if _, err = tools.read(root+"/linked.py", 1, nil); err == nil {
		t.Fatal("source symlink was readable")
	}
	if _, err = tools.read(root+"/../app/service.py", 1, nil); err == nil {
		t.Fatal("source traversal was readable")
	}
}

func TestSourceToolBudgetsArePerSession(t *testing.T) {
	snapshot := repositorySnapshot(t)
	limits := DefaultSourceToolLimits()
	limits.MaxCalls = 1
	tools, err := newSourceToolSession([]sourceSnapshot{snapshot}, limits)
	if err != nil {
		t.Fatal(err)
	}
	root := "/sources/" + snapshot.Captured.SourceID.String()
	if _, err = tools.list(root); err != nil {
		t.Fatal(err)
	}
	if _, err = tools.list(root); err == nil || !strings.Contains(err.Error(), "call limit") {
		t.Fatalf("call budget was not enforced: %v", err)
	}
	fresh, _ := newSourceToolSession([]sourceSnapshot{snapshot}, limits)
	if _, err = fresh.list(root); err != nil {
		t.Fatalf("budget leaked across sessions: %v", err)
	}
}

func TestWebsiteManifestIsTheOnlyEvidenceAllowlist(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pages"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pages", "home.md"), []byte("home\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceID := testID(t, "33333333-3333-4333-8333-333333333333")
	commit := strings.Repeat("b", 64)
	manifest := map[string]any{
		"native_version": commit,
		"pages": []any{map[string]any{
			"canonical_url": "https://example.test/docs?a=one two", "content_path": "pages/home.md",
		}},
	}
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "website-manifest.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := newSourceSnapshot(docgen.CapturedSource{
		SourceID: sourceID, RevisionID: testID(t, "44444444-4444-4444-8444-444444444444"),
		Commit: commit, Kind: "WEBSITE",
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := newSourceToolSession([]sourceSnapshot{snapshot}, DefaultSourceToolLimits())
	virtualRoot := "/sources/" + sourceID.String()
	if _, err = tools.read(virtualRoot+"/secret.txt", 1, nil); err == nil {
		t.Fatal("non-manifest website file was readable")
	}
	if _, err = tools.read(virtualRoot+"/website-manifest.json", 1, nil); err == nil {
		t.Fatal("website manifest was exposed as source evidence")
	}
	start, end := 1, 1
	resource, err := tools.evidenceResource(sourceID, "pages/home.md", &start, &end)
	if err != nil {
		t.Fatal(err)
	}
	expectedPrefix := "web://" + sourceID.String() + "@" + commit + "/https%3A%2F%2Fexample.test%2Fdocs%3Fa%3Done%20two"
	if resource != expectedPrefix+"#L1-L1" {
		t.Fatalf("website evidence URI was not canonical: %s", resource)
	}
}

func TestInvalidWebsiteEvidenceURIFailsSnapshotConstruction(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.md"), []byte("page"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"native_version":"` + strings.Repeat("c", 40) + `","pages":[{"canonical_url":"https://example.test/","content_path":"page.md","evidence_uri":"web://wrong"}]}`
	if err := os.WriteFile(filepath.Join(root, "website-manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newSourceSnapshot(docgen.CapturedSource{
		SourceID:   testID(t, "55555555-5555-4555-8555-555555555555"),
		RevisionID: testID(t, "66666666-6666-4666-8666-666666666666"),
		Commit:     strings.Repeat("c", 40), Kind: "WEBSITE",
	}, root)
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("invalid evidence binding was accepted: %v", err)
	}
}
