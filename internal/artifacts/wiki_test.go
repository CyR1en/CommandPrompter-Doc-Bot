package artifacts

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestWikiBundleMatchesRef0ManifestAndLayout(t *testing.T) {
	root := t.TempDir()
	store := mustWikiStore(t, root)
	knowledgeBaseID := mustArtifactID(t, "00112233-4455-6677-8899-aabbccddeeff")
	runID := mustArtifactID(t, "11111111-2222-3333-4444-555555555555")
	versionID := mustArtifactID(t, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	pages := []Page{
		oraclePage("overview", "Overview", "[Flow](architecture/flow.md)"),
		oraclePage("architecture/flow", "Flow", "[Overview](../overview.md)"),
	}
	published, err := store.Publish(
		knowledgeBaseID,
		runID,
		versionID,
		pages,
		[]SourceRevision{
			{"source_id": "b", "revision_id": "2"},
			{"source_id": "a", "revision_id": "3"},
		},
	)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	const ref0ManifestDigest = "9ad7198675107fa42316460635d56f0b183747124e7e78a95bf89d6c4988499e"
	if hex.EncodeToString(published.ManifestSHA256[:]) != ref0ManifestDigest ||
		published.ArtifactKey != WikiArtifactKey(knowledgeBaseID, versionID) ||
		published.PageCount != 2 {
		t.Fatalf("published bundle differs: %+v", published)
	}
	bundleRoot := filepath.Join(root, filepath.FromSlash(published.ArtifactKey))
	assertArtifactContent(t, filepath.Join(bundleRoot, "architecture", "index.md"), "# Architecture\n\n- [Flow](flow.md)\n")
	assertArtifactContent(t, filepath.Join(bundleRoot, "index.md"), "---\nokf_version: '0.2'\ntitle: Knowledge base\n---\n\n# Knowledge base\n\n- [Architecture](architecture/index.md)\n- [Overview](overview.md)\n")
	assertArtifactContent(t, filepath.Join(bundleRoot, ".last-update.json"), `{"format":"ref0-last-update/v1","run_id":"11111111-2222-3333-4444-555555555555","source_revisions":[{"revision_id":"3","source_id":"a"},{"revision_id":"2","source_id":"b"}]}`+"\n")
	manifest, err := os.ReadFile(filepath.Join(bundleRoot, ".page-manifest.json"))
	if err != nil || sha256.Sum256(manifest) != published.ManifestSHA256 {
		t.Fatalf("manifest hash mismatch: %v", err)
	}
	replay, err := store.Publish(
		knowledgeBaseID,
		runID,
		versionID,
		pages,
		[]SourceRevision{
			{"source_id": "b", "revision_id": "2"},
			{"source_id": "a", "revision_id": "3"},
		},
	)
	if err != nil || replay != published {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	validated, err := store.Validate(
		knowledgeBaseID,
		runID,
		versionID,
		pages,
		[]SourceRevision{
			{"source_id": "b", "revision_id": "2"},
			{"source_id": "a", "revision_id": "3"},
		},
		published.ManifestSHA256[:],
	)
	if err != nil || validated != published {
		t.Fatalf("validate = %+v, %v", validated, err)
	}
}

func TestWikiRejectsInvalidLinksLayoutAndClaimReferences(t *testing.T) {
	if got := humanTitle("123abc-foo2bar"); got != "123Abc Foo2Bar" {
		t.Fatalf("Python title rendering = %q", got)
	}
	store := mustWikiStore(t, t.TempDir())
	for index, link := range []string{"missing.md", "../../secret.md", "/etc/passwd"} {
		_, err := store.Publish(
			artifactID(byte(20+index)),
			artifactID(byte(30+index)),
			artifactID(byte(40+index)),
			[]Page{oraclePage("architecture/flow", "Flow", "[Bad]("+link+")")},
			nil,
		)
		if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "link") {
			t.Fatalf("link %q error = %v", link, err)
		}
	}
	_, err := store.Publish(
		artifactID(50), artifactID(51), artifactID(52),
		[]Page{
			oraclePage("operations", "Operations", "Top"),
			oraclePage("operations/deploy", "Deploy", "Nested"),
		},
		nil,
	)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "files and directories") {
		t.Fatalf("layout collision error = %v", err)
	}
	claims := []byte(`{"claims":[{"id":"duplicate"}]}` + "\n")
	first := oraclePage("one", "One", "Body")
	first.ClaimsJSON = claims
	first.ClaimsSHA256 = sha256.Sum256(claims)
	second := oraclePage("two", "Two", "Body")
	second.ClaimsJSON = claims
	second.ClaimsSHA256 = sha256.Sum256(claims)
	_, err = store.Publish(artifactID(53), artifactID(54), artifactID(55), []Page{first, second}, nil)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate Claim error = %v", err)
	}
}

func TestWikiExistingVersionAndTamperingAreNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	store := mustWikiStore(t, root)
	knowledgeBaseID, runID, versionID := artifactID(60), artifactID(61), artifactID(62)
	page := oraclePage("overview", "Overview", "Original")
	published, err := store.Publish(knowledgeBaseID, runID, versionID, []Page{page}, nil)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	bundleRoot := filepath.Join(root, filepath.FromSlash(published.ArtifactKey))
	for _, relative := range []string{
		"overview.md", ".claims/overview.json", ".page-manifest.json",
		".last-update.json", "index.md", "log.md",
	} {
		original, readErr := os.ReadFile(filepath.Join(bundleRoot, relative))
		if readErr != nil {
			t.Fatalf("read %s: %v", relative, readErr)
		}
		if err := os.WriteFile(filepath.Join(bundleRoot, relative), []byte("tampered\n"), 0o600); err != nil {
			t.Fatalf("tamper %s: %v", relative, err)
		}
		_, publishErr := store.Publish(knowledgeBaseID, runID, versionID, []Page{page}, nil)
		if !errors.Is(publishErr, ErrWikiDifferentContent) || !errors.Is(publishErr, os.ErrExist) {
			t.Fatalf("tampered %s replay error = %v", relative, publishErr)
		}
		if err := os.WriteFile(filepath.Join(bundleRoot, relative), original, 0o600); err != nil {
			t.Fatalf("restore %s: %v", relative, err)
		}
	}
	changed := oraclePage("overview", "Overview", "Changed")
	if _, err := store.Publish(knowledgeBaseID, runID, versionID, []Page{changed}, nil); !errors.Is(err, ErrWikiDifferentContent) {
		t.Fatalf("changed version error = %v", err)
	}
	content, err := store.ReadPage(knowledgeBaseID, versionID, "overview")
	if err != nil || !bytes.Contains(content, []byte("Original")) {
		t.Fatalf("retained page changed: %q, %v", content, err)
	}
}

func TestConcurrentWikiPublishersUsePermanentVersionLock(t *testing.T) {
	root := t.TempDir()
	store := mustWikiStore(t, root)
	secondStore := mustWikiStore(t, root)
	knowledgeBaseID, runID, versionID := artifactID(70), artifactID(71), artifactID(72)
	start := make(chan struct{})
	type result struct {
		page Page
		err  error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index, selected := range []Page{
		oraclePage("overview", "Overview", "First"),
		oraclePage("overview", "Overview", "Second"),
	} {
		selectedStore := []*WikiStore{store, secondStore}[index]
		selected := selected
		go func() {
			ready.Done()
			<-start
			_, err := selectedStore.Publish(knowledgeBaseID, runID, versionID, []Page{selected}, nil)
			results <- result{selected, err}
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
		} else if !errors.Is(selected.err, ErrWikiDifferentContent) {
			t.Fatalf("publisher error = %v", selected.err)
		}
	}
	if winner == nil {
		t.Fatal("no wiki publisher succeeded")
	}
	content, err := store.ReadPage(knowledgeBaseID, versionID, "overview")
	if err != nil || string(content) != winner.Markdown {
		t.Fatalf("published wiki was mixed: %q, %v", content, err)
	}
}

func TestWikiCleansCrashResidueAndRejectsSymlinkDestinations(t *testing.T) {
	root := t.TempDir()
	store := mustWikiStore(t, root)
	knowledgeBaseID, runID, versionID := artifactID(80), artifactID(81), artifactID(82)
	parent := filepath.Join(root, "knowledge-bases", knowledgeBaseID.String(), "wiki")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir wiki parent: %v", err)
	}
	residue := filepath.Join(parent, "."+versionID.String()+".simulated-crash")
	if err := os.Mkdir(residue, 0o700); err != nil {
		t.Fatalf("mkdir residue: %v", err)
	}
	if err := os.WriteFile(filepath.Join(residue, "partial"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write residue: %v", err)
	}
	if _, err := store.Publish(knowledgeBaseID, runID, versionID, []Page{oraclePage("overview", "Overview", "Body")}, nil); err != nil {
		t.Fatalf("publish after residue: %v", err)
	}
	if _, err := os.Lstat(residue); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging residue remains: %v", err)
	}
	otherVersion := artifactID(83)
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("safe"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, otherVersion.String())); err != nil {
		t.Fatalf("symlink destination: %v", err)
	}
	if _, err := store.Publish(knowledgeBaseID, runID, otherVersion, []Page{oraclePage("overview", "Overview", "Body")}, nil); !errors.Is(err, ErrWikiDifferentContent) {
		t.Fatalf("symlink destination error = %v", err)
	}
	assertArtifactContent(t, filepath.Join(outside, "sentinel"), "safe")
}

func TestWikiExportValidateAndDiscard(t *testing.T) {
	root := t.TempDir()
	store := mustWikiStore(t, root)
	knowledgeBaseID, runID, versionID := artifactID(90), artifactID(91), artifactID(92)
	page := oraclePage("overview", "Overview", "Body")
	published, err := store.Publish(knowledgeBaseID, runID, versionID, []Page{page}, nil)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	stale := make([]byte, sha256.Size)
	if _, err := store.Validate(knowledgeBaseID, runID, versionID, []Page{page}, nil, stale); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale validation error = %v", err)
	}
	archivePath := filepath.Join(root, "wiki.zip")
	if err := store.ExportZIP(knowledgeBaseID, versionID, archivePath); err != nil {
		t.Fatalf("export: %v", err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	names := make([]string, 0, len(archive.File))
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	_ = archive.Close()
	slices.Sort(names)
	if !slices.Contains(names, "index.md") || !slices.Contains(names, ".claims/overview.json") {
		t.Fatalf("archive entries = %v", names)
	}
	if published.PageCount != 1 {
		t.Fatalf("page count = %d", published.PageCount)
	}
	if err := store.Discard(knowledgeBaseID, versionID); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if err := store.Discard(knowledgeBaseID, versionID); err != nil {
		t.Fatalf("idempotent discard: %v", err)
	}
	if _, err := store.ReadPage(knowledgeBaseID, versionID, "overview"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read discarded wiki error = %v", err)
	}
}

func mustWikiStore(t *testing.T, root string) *WikiStore {
	t.Helper()
	store, err := NewWikiStore(root)
	if err != nil {
		t.Fatalf("new wiki store: %v", err)
	}
	return store
}

func mustArtifactID(t *testing.T, raw string) ID {
	t.Helper()
	id, err := ParseID(raw)
	if err != nil {
		t.Fatalf("parse artifact ID: %v", err)
	}
	return id
}

func assertArtifactContent(t *testing.T, selected, want string) {
	t.Helper()
	got, err := os.ReadFile(selected)
	if err != nil || string(got) != want {
		t.Fatalf("file %s = %q, %v; want %q", selected, got, err, want)
	}
}
