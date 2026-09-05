package sourcegit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cyr1en/ref0/internal/sourcefiles"
)

func TestRepositoryMaterializesFilteredBranchAndCommitSnapshots(t *testing.T) {
	fixture := newLocalGitFixture(t)
	writeFixtureFile(t, fixture.working, "docs/keep.md", "kept\n")
	writeFixtureFile(t, fixture.working, "ignored.txt", "ignored\n")
	writeFixtureFile(t, fixture.working, ".openwikiignore", "ignored.txt\n")
	commit := fixture.publish(t, "add filtered content", true)
	store := newSourceStore(t)
	provider := newLocalTransport(t, fixture.remote)
	acquirer := newTestAcquirer(t, store, provider, DefaultLimits())
	branch, _ := NewBranchReference("main")
	commitRef, _ := NewCommitReference(strings.ToUpper(commit))
	sourceID := sourcefiles.ID{0: 1}
	firstRevision := sourcefiles.ID{0: 2}
	first, err := acquirer.Materialize(context.Background(), MaterializeRequest{
		SourceID: sourceID, RevisionID: firstRevision, RemoteURL: "ignored by local transport",
		SelectedRef: branch, IncludePatterns: []string{"README.md", "docs/**", "ignored.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquirer.Materialize(context.Background(), MaterializeRequest{
		SourceID: sourceID, RevisionID: sourcefiles.ID{0: 3}, RemoteURL: "ignored",
		SelectedRef: commitRef, IncludePatterns: []string{"README.md", "docs/**", "ignored.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Commit != commit || second.Commit != commit || first.Fingerprint != second.Fingerprint || first.FileCount() != 2 || first.ByteCount() != int64(len("# Local fixture\nkept\n")) {
		t.Fatalf("snapshots = %#v / %#v", first, second)
	}
	if !slices.Contains(first.IgnoredPaths, "ignored.txt") || !slices.Contains(first.IgnoredPaths, ".openwikiignore") {
		t.Fatalf("ignored paths = %#v", first.IgnoredPaths)
	}
	root, err := store.ResolveArtifactKey(first.ArtifactKey)
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, root, "README.md", "# Local fixture\n")
	assertFileContent(t, root, "docs/keep.md", "kept\n")
	if _, err := os.Stat(filepath.Join(root, "ignored.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignored file exists: %v", err)
	}
	mirror, err := store.MirrorPath(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "FETCH_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("FETCH_HEAD exists: %v", err)
	}
	config := testGit(t, "--git-dir", mirror, "config", "--local", "--list")
	if strings.Contains(config, fixture.remote) || strings.Contains(config, "remote.") {
		t.Fatalf("mirror persisted remote configuration: %s", config)
	}

	writeFixtureFile(t, fixture.working, "README.md", "# Branch advanced\n")
	advanced := fixture.publish(t, "advance branch", true)
	replay, err := acquirer.Materialize(context.Background(), MaterializeRequest{SourceID: sourceID, RevisionID: firstRevision, RemoteURL: "ignored", SelectedRef: branch})
	if err != nil || replay.Commit != first.Commit || replay.Fingerprint != first.Fingerprint {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	next, err := acquirer.Materialize(context.Background(), MaterializeRequest{SourceID: sourceID, RevisionID: sourcefiles.ID{0: 4}, RemoteURL: "ignored", SelectedRef: branch})
	if err != nil || next.Commit != advanced {
		t.Fatalf("advanced snapshot = %#v, %v", next, err)
	}
	if provider.calls.Load() != 3 {
		t.Fatalf("transport calls = %d; replay should not fetch", provider.calls.Load())
	}
}

func TestRepositoryRejectsUnsupportedEntriesBeforePublication(t *testing.T) {
	tests := []struct {
		name     string
		wantCode RepositoryFailureCode
		mutate   func(*testing.T, *localGitFixture)
		excludes []string
	}{
		{
			name: "LFS pointer", wantCode: RepositoryGitLFS, excludes: []string{"large.bin"},
			mutate: func(t *testing.T, fixture *localGitFixture) {
				writeFixtureFile(t, fixture.working, "large.bin", "version https://git-lfs.github.com/spec/v1\noid sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nsize 1234\n")
				fixture.publish(t, "add LFS pointer", true)
			},
		},
		{
			name: "symlink", wantCode: RepositorySymlink,
			mutate: func(t *testing.T, fixture *localGitFixture) {
				if err := os.Symlink("README.md", filepath.Join(fixture.working, "linked")); err != nil {
					t.Fatal(err)
				}
				fixture.publish(t, "add symlink", true)
			},
		},
		{
			name: "submodule gitlink", wantCode: RepositorySubmodule,
			mutate: func(t *testing.T, fixture *localGitFixture) {
				testGit(t, "-C", fixture.working, "update-index", "--add", "--cacheinfo", "160000,"+fixture.commit+",vendor/dependency")
				fixture.publish(t, "add gitlink", false)
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLocalGitFixture(t)
			test.mutate(t, fixture)
			store := newSourceStore(t)
			provider := newLocalTransport(t, fixture.remote)
			acquirer := newTestAcquirer(t, store, provider, DefaultLimits())
			branch, _ := NewBranchReference("main")
			sourceID := sourcefiles.ID{0: byte(index + 10)}
			revisionID := sourcefiles.ID{0: 50}
			_, err := acquirer.Materialize(context.Background(), MaterializeRequest{SourceID: sourceID, RevisionID: revisionID, SelectedRef: branch, ExcludePatterns: test.excludes})
			assertRepositoryCode(t, err, test.wantCode)
			snapshotPath, pathErr := store.SnapshotPath(sourceID, revisionID)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if _, statErr := os.Stat(snapshotPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("snapshot was published: %v", statErr)
			}
		})
	}
}

func TestRepositoryEnforcesFileTreeAndIgnoredPathLimits(t *testing.T) {
	fixture := newLocalGitFixture(t)
	writeFixtureFile(t, fixture.working, "first.txt", "first\n")
	writeFixtureFile(t, fixture.working, "second.txt", "second\n")
	fixture.publish(t, "add files", true)
	branch, _ := NewBranchReference("main")
	tests := []struct {
		name     string
		limits   func(Limits) Limits
		includes []string
		wantCode RepositoryFailureCode
	}{
		{name: "file count", limits: func(value Limits) Limits { value.MaxFiles = 1; return value }, wantCode: RepositorySnapshotLimit},
		{name: "file bytes", limits: func(value Limits) Limits { value.MaxFileBytes = 5; return value }, wantCode: RepositorySnapshotLimit},
		{name: "tree output", limits: func(value Limits) Limits { value.MaxTreeBytes = 8; return value }, wantCode: RepositoryOutputTooLarge},
		{name: "ignored paths", includes: []string{"README.md"}, limits: func(value Limits) Limits { value.MaxIgnoredPaths = 1; return value }, wantCode: RepositorySnapshotLimit},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newSourceStore(t)
			limits := test.limits(DefaultLimits())
			if limits.MaxFileBytes > limits.MaxTotalBytes {
				limits.MaxTotalBytes = limits.MaxFileBytes
			}
			acquirer := newTestAcquirer(t, store, newLocalTransport(t, fixture.remote), limits)
			_, err := acquirer.Materialize(context.Background(), MaterializeRequest{SourceID: sourcefiles.ID{1: byte(index + 1)}, RevisionID: sourcefiles.ID{2: 1}, SelectedRef: branch, IncludePatterns: test.includes})
			assertRepositoryCode(t, err, test.wantCode)
		})
	}
}

func TestRepositoryRejectsPersistedTransportConfigurationBeforeFetch(t *testing.T) {
	for _, key := range []string{"remote.origin.url", "credential.helper", "include.path", "url.https://rewrite.invalid/.insteadof"} {
		t.Run(key, func(t *testing.T) {
			store := newSourceStore(t)
			sourceID := sourcefiles.ID{0: 99}
			mirror, err := store.MirrorPath(sourceID)
			if err != nil {
				t.Fatal(err)
			}
			testGit(t, "init", "--bare", mirror)
			testGit(t, "--git-dir", mirror, "config", key, "https://stored.invalid/repository.git")
			provider := newLocalTransport(t, t.TempDir())
			acquirer := newTestAcquirer(t, store, provider, DefaultLimits())
			branch, _ := NewBranchReference("main")
			_, err = acquirer.Materialize(context.Background(), MaterializeRequest{SourceID: sourceID, RevisionID: sourcefiles.ID{0: 100}, SelectedRef: branch})
			assertRepositoryCode(t, err, RepositoryInvalidMirror)
			if provider.calls.Load() != 0 {
				t.Fatal("transport ran before mirror validation")
			}
		})
	}
}

func TestReferenceAndFilterGrammar(t *testing.T) {
	for _, value := range []string{"main", "release/2026", "feature.docs"} {
		if _, err := NewBranchReference(value); err != nil {
			t.Fatalf("valid branch %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "../main", "main..old", "refs//main", "topic.lock", "bad branch"} {
		_, err := NewBranchReference(value)
		assertRepositoryCode(t, err, RepositoryInvalidRef)
	}
	commit, err := NewCommitReference(strings.Repeat("A", 40))
	if err != nil || commit.Value() != strings.Repeat("a", 40) {
		t.Fatalf("commit ref = %#v, %v", commit, err)
	}
	filter, err := newPathFilter([]string{"docs/**", "important.tmp"}, []string{"docs/drafts/**"}, []byte("*.tmp\n!important.tmp\n"))
	if err != nil {
		t.Fatal(err)
	}
	selected, ignored, err := filter.partition([]string{"docs/readme.md", "docs/drafts/old.md", "cache.tmp", "important.tmp"})
	if err != nil || !slices.Equal(selected, []string{"docs/readme.md", "important.tmp"}) || !slices.Equal(ignored, []string{"cache.tmp", "docs/drafts/old.md"}) {
		t.Fatalf("partition = %#v / %#v, %v", selected, ignored, err)
	}
}

type localTransport struct {
	remote string
	cwd    string
	calls  atomic.Int32
}

func newLocalTransport(t *testing.T, remote string) *localTransport {
	t.Helper()
	return &localTransport{remote: remote, cwd: t.TempDir()}
}

func (provider *localTransport) withTransport(_ context.Context, _ string, _ *Credential, operation func(gitTransport) error) error {
	provider.calls.Add(1)
	return operation(gitTransport{
		remote: Remote{URL: provider.remote}, cwd: provider.cwd, environment: baseGitEnvironment(),
		configuration: []string{
			"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
			"-c", "protocol.ext.allow=never", "-c", "credential.helper=",
			"-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false",
		},
	})
}

type localGitFixture struct {
	working string
	remote  string
	commit  string
}

func newLocalGitFixture(t *testing.T) *localGitFixture {
	t.Helper()
	root := t.TempDir()
	working := filepath.Join(root, "working")
	remote := filepath.Join(root, "remote.git")
	testGit(t, "init", "--quiet", "--initial-branch=main", working)
	testGit(t, "-C", working, "config", "user.email", "fixture@example.test")
	testGit(t, "-C", working, "config", "user.name", "Fixture")
	writeFixtureFile(t, working, "README.md", "# Local fixture\n")
	testGit(t, "-C", working, "add", "README.md")
	testGit(t, "-C", working, "commit", "--quiet", "-m", "initial")
	commit := strings.TrimSpace(testGit(t, "-C", working, "rev-parse", "HEAD"))
	testGit(t, "clone", "--quiet", "--bare", working, remote)
	return &localGitFixture{working: working, remote: remote, commit: commit}
}

func (fixture *localGitFixture) publish(t *testing.T, message string, stage bool) string {
	t.Helper()
	if stage {
		testGit(t, "-C", fixture.working, "add", "-A")
	}
	testGit(t, "-C", fixture.working, "commit", "--quiet", "-m", message)
	fixture.commit = strings.TrimSpace(testGit(t, "-C", fixture.working, "rev-parse", "HEAD"))
	testGit(t, "-C", fixture.working, "push", "--quiet", fixture.remote, "main")
	return fixture.commit
}

func newSourceStore(t *testing.T) *sourcefiles.Store {
	t.Helper()
	base := t.TempDir()
	store, err := sourcefiles.NewStore(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
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

func newTestAcquirer(t *testing.T, store SnapshotStore, provider transportProvider, limits Limits) *Acquirer {
	t.Helper()
	if err := limits.validate(); err != nil {
		t.Fatal(err)
	}
	return &Acquirer{store: store, transports: provider, limits: limits}
}

func writeFixtureFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testGit(t *testing.T, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Env = baseGitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func assertFileContent(t *testing.T, root, relative, want string) {
	t.Helper()
	value, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || string(value) != want {
		t.Fatalf("%s = %q, %v", relative, value, err)
	}
}

func assertRepositoryCode(t *testing.T, err error, code RepositoryFailureCode) {
	t.Helper()
	var repositoryErr *RepositoryError
	if !errors.As(err, &repositoryErr) || repositoryErr.Code != code {
		t.Fatalf("error = %v, want repository code %s", err, code)
	}
}
