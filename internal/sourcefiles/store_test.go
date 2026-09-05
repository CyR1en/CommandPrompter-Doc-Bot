package sourcefiles

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSnapshotPublicationMatchesPythonMarkerAndIsReadOnly(t *testing.T) {
	store := newTestStore(t)
	sourceID := mustID(t, "00112233-4455-6677-8899-aabbccddeeff")
	revisionID := mustID(t, "11111111-2222-3333-4444-555555555555")
	stored, err := store.StoreSnapshot(
		sourceID,
		revisionID,
		Files(
			File{Path: "README.md", Content: []byte("# docs\n")},
			File{Path: "src/app.py", Content: []byte("pass\n")},
		),
		nil,
	)
	if err != nil {
		t.Fatalf("store snapshot: %v", err)
	}
	root, err := store.ResolveArtifactKey(stored.ArtifactKey)
	if err != nil {
		t.Fatalf("resolve snapshot: %v", err)
	}
	if stored.ArtifactKey != SnapshotArtifactKey(sourceID, revisionID) ||
		hex.EncodeToString(stored.Fingerprint.Digest[:]) != "cb7f5673cb4853efc3ff84a40d3adb1fd530f7c8063bf0e330f2af7f50e3fca2" ||
		stored.Fingerprint.FileCount != 2 ||
		stored.Fingerprint.ByteCount != 12 {
		t.Fatalf("stored snapshot differs: %+v", stored)
	}
	assertFileContent(t, filepath.Join(root, "README.md"), []byte("# docs\n"))
	assertFileContent(t, filepath.Join(root, "src", "app.py"), []byte("pass\n"))
	for _, selected := range []string{root, filepath.Join(root, "README.md")} {
		info, statErr := os.Stat(selected)
		if statErr != nil || info.Mode().Perm()&0o222 != 0 {
			t.Fatalf("artifact %s mode = %v, %v", selected, info.Mode(), statErr)
		}
	}
	marker := filepath.Join(filepath.Dir(root), "."+revisionID.String()+".complete")
	payload, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	const wantMarker = `{"byte_count":12,"file_count":2,"fingerprint":"cb7f5673cb4853efc3ff84a40d3adb1fd530f7c8063bf0e330f2af7f50e3fca2","metadata":null,"version":1}`
	if string(payload) != wantMarker {
		t.Fatalf("marker mismatch\n got: %s\nwant: %s", payload, wantMarker)
	}
	loaded, err := store.LoadSnapshot(sourceID, revisionID)
	if err != nil || loaded == nil || loaded.Fingerprint != stored.Fingerprint {
		t.Fatalf("load snapshot = %+v, %v", loaded, err)
	}
}

func TestMarkerJSONMatchesPythonUnicodeEncoding(t *testing.T) {
	metadata := "actual:\u2028 literal:\\u2028"
	payload, err := markerPayload(Fingerprint{}, &metadata)
	if err != nil {
		t.Fatalf("marker payload: %v", err)
	}
	want := "{\"byte_count\":0,\"file_count\":0,\"fingerprint\":\"" +
		strings.Repeat("0", 64) +
		"\",\"metadata\":\"actual:\u2028 literal:\\\\u2028\",\"version\":1}"
	if string(payload) != want {
		t.Fatalf("Unicode marker mismatch\n got: %q\nwant: %q", payload, want)
	}
}

func TestSnapshotReplayIsIdempotentAndRetainsFirstMetadata(t *testing.T) {
	store := newTestStore(t)
	sourceID := mustID(t, "10000000-0000-0000-0000-000000000001")
	revisionID := mustID(t, "20000000-0000-0000-0000-000000000002")
	firstMetadata := `{"commit":"first"}`
	secondMetadata := `{"commit":"second"}`
	files := Files(
		File{Path: "README.md", Content: []byte("stable")},
		File{Path: "docs/guide.md", Content: []byte("guide")},
	)
	first, err := store.StoreSnapshot(sourceID, revisionID, files, &firstMetadata)
	if err != nil {
		t.Fatalf("first store: %v", err)
	}
	replay, err := store.StoreSnapshot(
		sourceID,
		revisionID,
		Files(
			File{Path: "README.md", Content: []byte("stable")},
			File{Path: "docs/guide.md", Content: []byte("guide")},
		),
		&secondMetadata,
	)
	if err != nil || replay.Metadata == nil || *replay.Metadata != firstMetadata || replay.Fingerprint != first.Fingerprint {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	_, err = store.StoreSnapshot(
		sourceID,
		revisionID,
		Files(
			File{Path: "README.md", Content: []byte("changed")},
			File{Path: "docs/guide.md", Content: []byte("guide")},
		),
		nil,
	)
	if !errors.Is(err, ErrSnapshotExists) || !errors.Is(err, ErrSourceStorage) {
		t.Fatalf("changed replay error = %v", err)
	}
}

func TestSnapshotLimitsStopStreamingInputEarly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	store, err := NewStore(root, SnapshotLimits{MaxFiles: 1, MaxFileBytes: 4, MaxTotalBytes: 4})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	consumed := make([]string, 0)
	files := func(yield func(File) bool) {
		for _, selected := range []string{"a", "b", "c"} {
			consumed = append(consumed, selected)
			if !yield(File{Path: selected, Content: []byte("1")}) {
				return
			}
		}
	}
	_, err = store.StoreSnapshot(mustID(t, "30000000-0000-0000-0000-000000000003"), mustID(t, "40000000-0000-0000-0000-000000000004"), files, nil)
	if !errors.Is(err, ErrSourceStorage) || strings.Join(consumed, ",") != "a,b" {
		t.Fatalf("limit error = %v, consumed = %v", err, consumed)
	}
	_, err = store.StoreSnapshot(
		mustID(t, "30000000-0000-0000-0000-000000000003"),
		mustID(t, "50000000-0000-0000-0000-000000000005"),
		Files(File{Path: "large", Content: []byte("12345")}),
		nil,
	)
	if !errors.Is(err, ErrSourceStorage) {
		t.Fatalf("large file error = %v", err)
	}
}

func TestSourceStorageRejectsSymlinkedRootsAndArtifactPaths(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	linkedRoot := filepath.Join(base, "linked-root")
	if err := os.Symlink(outside, linkedRoot); err != nil {
		t.Fatalf("symlink root: %v", err)
	}
	if _, err := NewStore(linkedRoot); !errors.Is(err, ErrSourceStorage) {
		t.Fatalf("symlink root error = %v", err)
	}
	store := newTestStore(t)
	sourceID := mustID(t, "60000000-0000-0000-0000-000000000006")
	revisionID := mustID(t, "70000000-0000-0000-0000-000000000007")
	mirror, err := store.MirrorPath(sourceID)
	if err != nil {
		t.Fatalf("mirror path: %v", err)
	}
	snapshots := filepath.Join(filepath.Dir(mirror), "snapshots")
	outsideSnapshots := filepath.Join(base, "outside-snapshots")
	if err := os.Mkdir(outsideSnapshots, 0o700); err != nil {
		t.Fatalf("mkdir outside snapshots: %v", err)
	}
	if err := os.Symlink(outsideSnapshots, snapshots); err != nil {
		t.Fatalf("symlink snapshots: %v", err)
	}
	if _, err := store.ResolveArtifactKey(SnapshotArtifactKey(sourceID, revisionID)); !errors.Is(err, ErrSourceStorage) {
		t.Fatalf("symlink artifact error = %v", err)
	}
}

func TestPreexistingDestinationIsBlockedButInterruptedPublicationRecovers(t *testing.T) {
	store := newTestStore(t)
	sourceID := mustID(t, "80000000-0000-0000-0000-000000000008")
	blockedID := mustID(t, "90000000-0000-0000-0000-000000000009")
	blocked, err := store.SnapshotPath(sourceID, blockedID)
	if err != nil {
		t.Fatalf("blocked path: %v", err)
	}
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatalf("mkdir blocked destination: %v", err)
	}
	_, err = store.StoreSnapshot(sourceID, blockedID, Files(File{Path: "README.md", Content: []byte("new")}), nil)
	if !errors.Is(err, ErrSnapshotExists) {
		t.Fatalf("preexisting destination error = %v", err)
	}
	entries, err := os.ReadDir(blocked)
	if err != nil || len(entries) != 0 {
		t.Fatalf("preexisting destination changed: %v, %v", entries, err)
	}
	recoveredID := mustID(t, "a0000000-0000-0000-0000-00000000000a")
	recoveredPath, err := store.SnapshotPath(sourceID, recoveredID)
	if err != nil {
		t.Fatalf("recovered path: %v", err)
	}
	if err := os.MkdirAll(recoveredPath, 0o700); err != nil {
		t.Fatalf("mkdir partial destination: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recoveredPath, "partial.txt"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	intent := filepath.Join(filepath.Dir(recoveredPath), "."+recoveredID.String()+".complete.publishing")
	if err := os.WriteFile(intent, []byte("publishing\n"), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}
	stored, err := store.StoreSnapshot(sourceID, recoveredID, Files(File{Path: "README.md", Content: []byte("recovered")}), nil)
	if err != nil {
		t.Fatalf("recover publication: %v", err)
	}
	root, err := store.ResolveArtifactKey(stored.ArtifactKey)
	if err != nil {
		t.Fatalf("resolve recovered: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "README.md"), []byte("recovered"))
	if _, err := os.Stat(filepath.Join(root, "partial.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial residue remains: %v", err)
	}
	assertFileContent(t, intent, []byte("complete\n"))
}

func TestConcurrentPublishersUseOnePermanentRevisionLock(t *testing.T) {
	store := newTestStore(t)
	secondStore, err := NewStore(store.Root())
	if err != nil {
		t.Fatalf("second store: %v", err)
	}
	sourceID := mustID(t, "b0000000-0000-0000-0000-00000000000b")
	revisionID := mustID(t, "c0000000-0000-0000-0000-00000000000c")
	start := make(chan struct{})
	type result struct {
		content []byte
		err     error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index, content := range [][]byte{[]byte("first"), []byte("second")} {
		selectedStore := []*Store{store, secondStore}[index]
		content := bytes.Clone(content)
		go func() {
			ready.Done()
			<-start
			_, err := selectedStore.StoreSnapshot(
				sourceID,
				revisionID,
				Files(File{Path: "README.md", Content: content}),
				nil,
			)
			results <- result{content: content, err: err}
		}()
	}
	ready.Wait()
	close(start)
	first := <-results
	second := <-results
	winners := make([][]byte, 0, 1)
	for _, selected := range []result{first, second} {
		if selected.err == nil {
			winners = append(winners, selected.content)
		} else if !errors.Is(selected.err, ErrSnapshotExists) {
			t.Fatalf("publisher error = %v", selected.err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("winner count = %d; results: %v / %v", len(winners), first.err, second.err)
	}
	root, err := store.ResolveArtifactKey(SnapshotArtifactKey(sourceID, revisionID))
	if err != nil {
		t.Fatalf("resolve winner: %v", err)
	}
	assertFileContent(t, filepath.Join(root, "README.md"), winners[0])
	assertFileContent(t, filepath.Join(filepath.Dir(root), "."+revisionID.String()+".complete.publishing"), []byte("complete\n"))
}

func TestDiscardRemovesOnlySelectedSnapshotAndAllowsRepublish(t *testing.T) {
	store := newTestStore(t)
	sourceID := mustID(t, "d0000000-0000-0000-0000-00000000000d")
	retainedID := mustID(t, "e0000000-0000-0000-0000-00000000000e")
	candidateID := mustID(t, "f0000000-0000-0000-0000-00000000000f")
	retained, err := store.StoreSnapshot(sourceID, retainedID, Files(File{Path: "README.md", Content: []byte("retained")}), nil)
	if err != nil {
		t.Fatalf("store retained: %v", err)
	}
	candidate, err := store.StoreSnapshot(sourceID, candidateID, Files(File{Path: "README.md", Content: []byte("candidate")}), nil)
	if err != nil {
		t.Fatalf("store candidate: %v", err)
	}
	if err := store.DiscardSnapshot(sourceID, candidateID); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := store.ResolveArtifactKey(retained.ArtifactKey); err != nil {
		t.Fatalf("retained snapshot missing: %v", err)
	}
	if _, err := store.ResolveArtifactKey(candidate.ArtifactKey); !errors.Is(err, ErrSourceStorage) {
		t.Fatalf("candidate resolve error = %v", err)
	}
	if _, err := store.StoreSnapshot(sourceID, candidateID, Files(File{Path: "README.md", Content: []byte("candidate")}), nil); err != nil {
		t.Fatalf("republish: %v", err)
	}
}

func TestDiscardMissingSnapshotDoesNotCreateDirectoriesOrLocks(t *testing.T) {
	store := newTestStore(t)
	sourceID := mustID(t, "d1000000-0000-0000-0000-00000000000d")
	revisionID := mustID(t, "f1000000-0000-0000-0000-00000000000f")
	if err := store.DiscardSnapshot(sourceID, revisionID); err != nil {
		t.Fatalf("discard missing source: %v", err)
	}
	if entries, err := os.ReadDir(store.Root()); err != nil || len(entries) != 0 {
		t.Fatalf("missing source discard created artifacts: %v, %v", entries, err)
	}
	sourceRoot := filepath.Join(store.Root(), "sources", sourceID.String())
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.DiscardSnapshot(sourceID, revisionID); err != nil {
		t.Fatalf("discard missing snapshots: %v", err)
	}
	if entries, err := os.ReadDir(sourceRoot); err != nil || len(entries) != 0 {
		t.Fatalf("missing snapshots discard created artifacts: %v, %v", entries, err)
	}
}

func TestArtifactKeyParserAndInvalidMarkerFailClosed(t *testing.T) {
	store := newTestStore(t)
	for _, key := range []string{
		"../outside",
		"sources/not-a-uuid/snapshots/nope",
		"sources/x/mirrors/y",
		"sources//snapshots/",
	} {
		if _, err := store.ResolveArtifactKey(key); !errors.Is(err, ErrSourceStorage) {
			t.Fatalf("key %q error = %v", key, err)
		}
	}
	sourceID := mustID(t, "01000000-0000-0000-0000-000000000001")
	revisionID := mustID(t, "02000000-0000-0000-0000-000000000002")
	stored, err := store.StoreSnapshot(sourceID, revisionID, Files(File{Path: "README.md", Content: []byte("content")}), nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	root, err := store.ResolveArtifactKey(stored.ArtifactKey)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	marker := filepath.Join(filepath.Dir(root), "."+revisionID.String()+".complete")
	if err := os.Chmod(marker, 0o600); err != nil {
		t.Fatalf("chmod marker: %v", err)
	}
	if err := os.WriteFile(marker, []byte(`{"version":true}`), 0o600); err != nil {
		t.Fatalf("tamper marker: %v", err)
	}
	if _, err := store.LoadSnapshot(sourceID, revisionID); !errors.Is(err, ErrSourceStorage) {
		t.Fatalf("invalid marker error = %v", err)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	base := t.TempDir()
	store, err := NewStore(filepath.Join(base, "data"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return os.Chmod(path, 0o600)
		})
	})
	return store
}

func mustID(t *testing.T, raw string) ID {
	t.Helper()
	id, err := ParseID(raw)
	if err != nil {
		t.Fatalf("parse ID %q: %v", raw, err)
	}
	return id
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("file %s = %q, %v; want %q", path, got, err, want)
	}
}
