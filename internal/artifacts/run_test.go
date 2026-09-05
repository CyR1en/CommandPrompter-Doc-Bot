package artifacts

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunPageSnapshotMatchesPythonFormatAndVerifiesHashes(t *testing.T) {
	root := t.TempDir()
	store := mustRunStore(t, root)
	knowledgeBaseID := artifactID(1)
	runID := artifactID(2)
	page := oraclePage("overview", "Overview", "[Flow](architecture/flow.md)")
	directory := runSnapshotDirectory(root, knowledgeBaseID, runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("mkdir run snapshot: %v", err)
	}
	residue := filepath.Join(directory, ".overview.md.crash")
	if err := os.WriteFile(residue, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write crash residue: %v", err)
	}
	if err := store.SavePage(knowledgeBaseID, runID, page); err != nil {
		t.Fatalf("save page: %v", err)
	}
	if _, err := os.Lstat(residue); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run write residue remains: %v", err)
	}
	if err := store.SavePage(knowledgeBaseID, runID, page); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	loaded, err := store.LoadPage(knowledgeBaseID, runID, "overview")
	if err != nil || !samePage(loaded, page) {
		t.Fatalf("loaded page differs: %+v, %v", loaded, err)
	}
	metadata, err := os.ReadFile(filepath.Join(directory, ".metadata", "overview.json"))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	const goldenMetadata = `{"claims_sha256":"6da2c83add4bc0e24345b08fb7028d1160bcb7b99fc25ee2529767896f1d5720","content_sha256":"628cd364e9253b2991419325e9df72917d3666d1aa056e894d5b26762ecaf278","description":"Test","format":"ref0-accepted-page/v1","page_type":"Concept","slug":"overview","title":"Overview"}` + "\n"
	if string(metadata) != goldenMetadata {
		t.Fatalf("metadata mismatch\n got: %s\nwant: %s", metadata, goldenMetadata)
	}
	if err := os.WriteFile(filepath.Join(directory, "overview.md"), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper markdown: %v", err)
	}
	if _, err := store.LoadPage(knowledgeBaseID, runID, "overview"); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered page error = %v", err)
	}
}

func TestDeterministicJSONMatchesPythonEnsureASCIIFalse(t *testing.T) {
	value := struct {
		Actual  string `json:"actual"`
		Literal string `json:"literal"`
	}{Actual: "a\u2028b", Literal: `a\u2028b`}
	encoded, err := marshalDeterministicJSON(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := "{\"actual\":\"a\u2028b\",\"literal\":\"a\\\\u2028b\"}\n"
	if string(encoded) != want {
		t.Fatalf("Unicode JSON mismatch\n got: %q\nwant: %q", encoded, want)
	}
}

func TestRunPageChangedRetryIsImmutableAndConcurrentWritersDoNotMix(t *testing.T) {
	root := t.TempDir()
	store := mustRunStore(t, root)
	secondStore := mustRunStore(t, root)
	knowledgeBaseID := artifactID(3)
	runID := artifactID(4)
	first := oraclePage("overview", "Overview", "First")
	second := oraclePage("overview", "Overview", "Second")
	start := make(chan struct{})
	type result struct {
		page Page
		err  error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index, selected := range []Page{first, second} {
		selectedStore := []*RunStore{store, secondStore}[index]
		selected := selected
		go func() {
			ready.Done()
			<-start
			results <- result{selected, selectedStore.SavePage(knowledgeBaseID, runID, selected)}
		}()
	}
	ready.Wait()
	close(start)
	observed := []result{<-results, <-results}
	var winner *Page
	for _, selected := range observed {
		if selected.err == nil {
			copy := selected.page
			winner = &copy
			continue
		}
		if !errors.Is(selected.err, ErrImmutablePage) || !errors.Is(selected.err, ErrValidation) {
			t.Fatalf("changed writer error = %v", selected.err)
		}
	}
	if winner == nil {
		t.Fatal("no page writer succeeded")
	}
	loaded, err := store.LoadPage(knowledgeBaseID, runID, "overview")
	if err != nil || !samePage(loaded, *winner) {
		t.Fatalf("concurrent page was mixed: %+v, %v", loaded, err)
	}
	other := first
	if samePage(*winner, first) {
		other = second
	}
	if err := store.SavePage(knowledgeBaseID, runID, other); !errors.Is(err, ErrImmutablePage) {
		t.Fatalf("changed retry error = %v", err)
	}
}

func TestRunStoreRejectsTraversalAndSymlinkedParents(t *testing.T) {
	root := t.TempDir()
	store := mustRunStore(t, root)
	knowledgeBaseID := artifactID(5)
	runID := artifactID(6)
	page := oraclePage("overview", "Overview", "Body")
	page.Slug = "../outside"
	if err := store.SavePage(knowledgeBaseID, runID, page); !errors.Is(err, ErrValidation) {
		t.Fatalf("traversal slug error = %v", err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	directory := runSnapshotDirectory(root, knowledgeBaseID, runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("mkdir snapshot directory: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, ".claims")); err != nil {
		t.Fatalf("symlink claims: %v", err)
	}
	page = oraclePage("overview", "Overview", "Body")
	if err := store.SavePage(knowledgeBaseID, runID, page); !errors.Is(err, ErrValidation) {
		t.Fatalf("symlink parent error = %v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed: %v, %v", entries, err)
	}
}

func TestRunDiscardIsIdempotentAndSerialized(t *testing.T) {
	root := t.TempDir()
	store := mustRunStore(t, root)
	knowledgeBaseID := artifactID(7)
	runID := artifactID(8)
	if err := store.SavePage(knowledgeBaseID, runID, oraclePage("overview", "Overview", "Body")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.DiscardRun(knowledgeBaseID, runID); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if err := store.DiscardRun(knowledgeBaseID, runID); err != nil {
		t.Fatalf("idempotent discard: %v", err)
	}
	if _, err := store.LoadPage(knowledgeBaseID, runID, "overview"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load discarded page error = %v", err)
	}
}

func TestMissingArtifactDeletesDoNotCreateParents(t *testing.T) {
	root := t.TempDir()
	knowledgeBaseID, resourceID := artifactID(9), artifactID(10)
	if err := mustWikiStore(t, root).Discard(knowledgeBaseID, resourceID); err != nil {
		t.Fatalf("discard missing wiki: %v", err)
	}
	if err := mustRunStore(t, root).DiscardRun(knowledgeBaseID, resourceID); err != nil {
		t.Fatalf("discard missing run: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing deletes created artifacts: %v, %v", entries, err)
	}
}

func oraclePage(slug, title, body string) Page {
	markdown := "---\ntype: Concept\ntitle: " + title + "\ndescription: Test\n---\n\n# " + title + "\n\n" + body + "\n"
	claims := []byte("{\"claims\":[]}\n")
	return Page{
		Slug:          slug,
		Title:         title,
		Description:   "Test",
		PageType:      "Concept",
		Markdown:      markdown,
		ContentSHA256: sha256.Sum256([]byte(markdown)),
		ClaimsJSON:    claims,
		ClaimsSHA256:  sha256.Sum256(claims),
	}
}

func artifactID(last byte) ID {
	var id ID
	id[15] = last
	return id
}

func mustRunStore(t *testing.T, root string) *RunStore {
	t.Helper()
	store, err := NewRunStore(root)
	if err != nil {
		t.Fatalf("new run store: %v", err)
	}
	return store
}

func runSnapshotDirectory(root string, knowledgeBaseID, runID ID) string {
	return filepath.Join(
		root,
		"knowledge-bases",
		knowledgeBaseID.String(),
		"runs",
		runID.String(),
		"page-snapshots",
	)
}

func samePage(first, second Page) bool {
	return first.Slug == second.Slug &&
		first.Title == second.Title &&
		first.Description == second.Description &&
		first.PageType == second.PageType &&
		first.Markdown == second.Markdown &&
		first.ContentSHA256 == second.ContentSHA256 &&
		bytes.Equal(first.ClaimsJSON, second.ClaimsJSON) &&
		first.ClaimsSHA256 == second.ClaimsSHA256
}
