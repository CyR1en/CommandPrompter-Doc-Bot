package sourcegit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/sourcefiles"
)

const (
	lfsHeader              = "version https://git-lfs.github.com/spec/v1"
	maximumLFSPointerBytes = 1024
)

var safeMirrorConfigKeys = map[string]struct{}{
	"core.bare": {}, "core.filemode": {}, "core.ignorecase": {},
	"core.logallrefupdates": {}, "core.precomposeunicode": {},
	"core.repositoryformatversion": {}, "extensions.compatobjectformat": {},
	"extensions.objectformat": {},
}

type ReferenceKind string

const (
	BranchReference ReferenceKind = "branch"
	CommitReference ReferenceKind = "commit"
)

type Reference struct {
	kind  ReferenceKind
	value string
}

func NewBranchReference(name string) (Reference, error) {
	if !validBranch(name) {
		return Reference{}, repositoryError(RepositoryInvalidRef)
	}
	return Reference{kind: BranchReference, value: name}, nil
}

func NewCommitReference(value string) (Reference, error) {
	normalized := strings.ToLower(value)
	if !commitPattern.MatchString(normalized) {
		return Reference{}, repositoryError(RepositoryInvalidRef)
	}
	return Reference{kind: CommitReference, value: normalized}, nil
}

func (reference Reference) Kind() ReferenceKind { return reference.kind }
func (reference Reference) Value() string       { return reference.value }

type Limits struct {
	MaxFiles            int
	MaxFileBytes        int64
	MaxTotalBytes       int64
	MaxTreeBytes        int
	MaxIgnoredPaths     int
	MaxIgnoredPathBytes int
	CommandTimeout      time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxFiles:            200_000,
		MaxFileBytes:        10 * 1024 * 1024,
		MaxTotalBytes:       1024 * 1024 * 1024,
		MaxTreeBytes:        32 * 1024 * 1024,
		MaxIgnoredPaths:     1_000,
		MaxIgnoredPathBytes: 1024 * 1024,
		CommandTimeout:      120 * time.Second,
	}
}

func (limits Limits) validate() error {
	if limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxTreeBytes <= 0 || limits.MaxIgnoredPaths <= 0 || limits.MaxIgnoredPathBytes <= 0 || limits.CommandTimeout <= 0 || limits.CommandTimeout > 10*time.Minute || limits.MaxFileBytes > limits.MaxTotalBytes {
		return errors.New("repository limits are invalid")
	}
	return nil
}

type SnapshotStore interface {
	MirrorPath(sourcefiles.ID) (string, error)
	LoadSnapshot(sourcefiles.ID, sourcefiles.ID) (*sourcefiles.StoredSnapshot, error)
	StoreSnapshot(sourcefiles.ID, sourcefiles.ID, iter.Seq[sourcefiles.File], *string) (sourcefiles.StoredSnapshot, error)
}

var _ SnapshotStore = (*sourcefiles.Store)(nil)

type MaterializeRequest struct {
	SourceID        sourcefiles.ID
	RevisionID      sourcefiles.ID
	RemoteURL       string
	SelectedRef     Reference
	Credential      *Credential
	IncludePatterns []string
	ExcludePatterns []string
}

type Snapshot struct {
	SourceID     sourcefiles.ID
	RevisionID   sourcefiles.ID
	Commit       string
	ArtifactKey  string
	Fingerprint  sourcefiles.Fingerprint
	IgnoredPaths []string
}

func (snapshot Snapshot) FileCount() int   { return snapshot.Fingerprint.FileCount }
func (snapshot Snapshot) ByteCount() int64 { return snapshot.Fingerprint.ByteCount }

type Acquirer struct {
	store      SnapshotStore
	transports transportProvider
	limits     Limits
}

func NewAcquirer(store SnapshotStore, validator *Validator, limits Limits) (*Acquirer, error) {
	if store == nil || validator == nil {
		return nil, errors.New("repository store and transport are required")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &Acquirer{store: store, transports: validator, limits: limits}, nil
}

func (acquirer *Acquirer) Materialize(ctx context.Context, request MaterializeRequest) (Snapshot, error) {
	replay, err := acquirer.store.LoadSnapshot(request.SourceID, request.RevisionID)
	if err != nil {
		return Snapshot{}, repositoryError(RepositoryInvalidMirror)
	}
	if replay != nil && replay.Metadata != nil {
		commit, ignored, err := acquirer.decodeMetadata(*replay.Metadata)
		if err != nil {
			return Snapshot{}, err
		}
		return Snapshot{SourceID: request.SourceID, RevisionID: request.RevisionID, Commit: commit, ArtifactKey: replay.ArtifactKey, Fingerprint: replay.Fingerprint, IgnoredPaths: ignored}, nil
	}
	if err := acquirer.validateReference(request.SelectedRef); err != nil {
		return Snapshot{}, err
	}
	mirror, err := acquirer.store.MirrorPath(request.SourceID)
	if err != nil {
		return Snapshot{}, repositoryError(RepositoryInvalidMirror)
	}
	commandCWD, err := os.MkdirTemp("", "ref0-repository-git-")
	if err != nil {
		return Snapshot{}, repositoryError(RepositoryGit)
	}
	defer os.RemoveAll(commandCWD)
	var stored sourcefiles.StoredSnapshot
	err = withSourceLock(ctx, mirror, acquirer.limits.CommandTimeout, func() error {
		if err := acquirer.initializeMirror(ctx, mirror, commandCWD); err != nil {
			return err
		}
		if err := acquirer.transports.withTransport(ctx, request.RemoteURL, request.Credential, func(transport gitTransport) error {
			return acquirer.fetch(ctx, mirror, transport)
		}); err != nil {
			return err
		}
		commit, err := acquirer.resolveCommit(ctx, mirror, request.SelectedRef, commandCWD)
		if err != nil {
			return err
		}
		entries, err := acquirer.tree(ctx, mirror, commit, commandCWD)
		if err != nil {
			return err
		}
		if err := acquirer.rejectLFSPointers(ctx, mirror, entries, commandCWD); err != nil {
			return err
		}
		files, ignored, err := acquirer.materializeFiles(ctx, mirror, entries, request.IncludePatterns, request.ExcludePatterns, commandCWD)
		if err != nil {
			return err
		}
		metadata, err := acquirer.encodeMetadata(commit, ignored)
		if err != nil {
			return repositoryError(RepositoryInvalidMirror)
		}
		stored, err = acquirer.store.StoreSnapshot(request.SourceID, request.RevisionID, sourcefiles.Files(files...), &metadata)
		if err != nil {
			return repositoryError(RepositoryInvalidMirror)
		}
		_, _, err = acquirer.decodeStoredMetadata(stored.Metadata)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	commit, ignored, err := acquirer.decodeStoredMetadata(stored.Metadata)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{SourceID: request.SourceID, RevisionID: request.RevisionID, Commit: commit, ArtifactKey: stored.ArtifactKey, Fingerprint: stored.Fingerprint, IgnoredPaths: ignored}, nil
}

func (acquirer *Acquirer) validateReference(reference Reference) error {
	switch reference.kind {
	case BranchReference:
		if !validBranch(reference.value) {
			return repositoryError(RepositoryInvalidRef)
		}
	case CommitReference:
		if !commitPattern.MatchString(reference.value) || reference.value != strings.ToLower(reference.value) {
			return repositoryError(RepositoryInvalidRef)
		}
	default:
		return repositoryError(RepositoryInvalidRef)
	}
	return nil
}

func withSourceLock(ctx context.Context, mirror string, timeout time.Duration, operation func() error) error {
	descriptor, err := os.OpenFile(filepath.Join(filepath.Dir(mirror), "source.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return repositoryError(RepositoryInvalidMirror)
	}
	defer descriptor.Close()
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(descriptor.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return repositoryError(RepositoryInvalidMirror)
		}
		if time.Now().After(deadline) {
			return repositoryError(RepositoryTimeout)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(descriptor.Fd()), syscall.LOCK_UN)
	return operation()
}

func (acquirer *Acquirer) initializeMirror(ctx context.Context, mirror, cwd string) error {
	info, err := os.Lstat(mirror)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return repositoryError(RepositoryInvalidMirror)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return repositoryError(RepositoryInvalidMirror)
	}
	if errors.Is(err, os.ErrNotExist) {
		if _, err := acquirer.run(ctx, []string{"-c", "core.hooksPath=/dev/null", "init", "--bare", mirror}, cwd, maximumGitDiagnosticBytes, nil); err != nil {
			return err
		}
	}
	raw, err := acquirer.run(ctx, []string{"--git-dir", mirror, "config", "--local", "--null", "--name-only", "--no-includes", "--list"}, cwd, maximumGitDiagnosticBytes, nil)
	if err != nil {
		return err
	}
	records := bytes.Split(raw, []byte{0})
	if len(records) < 2 || len(records[len(records)-1]) != 0 {
		return repositoryError(RepositoryInvalidMirror)
	}
	keys := map[string]struct{}{}
	for _, record := range records[:len(records)-1] {
		if len(record) == 0 || !ascii(record) {
			return repositoryError(RepositoryInvalidMirror)
		}
		keys[strings.ToLower(string(record))] = struct{}{}
	}
	if len(keys) == 0 {
		return repositoryError(RepositoryInvalidMirror)
	}
	for key := range keys {
		if _, safe := safeMirrorConfigKeys[key]; !safe {
			return repositoryError(RepositoryInvalidMirror)
		}
	}
	bare, err := acquirer.run(ctx, []string{"--git-dir", mirror, "rev-parse", "--is-bare-repository"}, cwd, 32, nil)
	if err != nil || string(bytes.TrimSpace(bare)) != "true" {
		return repositoryError(RepositoryInvalidMirror)
	}
	return nil
}

func (acquirer *Acquirer) fetch(ctx context.Context, mirror string, transport gitTransport) error {
	result, err := executeGit(ctx, transport.command("--git-dir", mirror, "fetch", "--quiet", "--force", "--prune", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", transport.remote.URL, "+refs/heads/*:refs/heads/*"), transport.environment, transport.cwd, maximumGitDiagnosticBytes, acquirer.limits.CommandTimeout, nil)
	if err != nil {
		return acquirer.mapExecutionError(err)
	}
	if result.exitCode != 0 {
		return repositoryError(RepositoryGit)
	}
	return nil
}

func (acquirer *Acquirer) resolveCommit(ctx context.Context, mirror string, reference Reference, cwd string) (string, error) {
	expression := reference.value + "^{commit}"
	if reference.kind == BranchReference {
		expression = "refs/heads/" + expression
	}
	result, err := acquirer.run(ctx, []string{"--git-dir", mirror, "rev-parse", "--verify", "--end-of-options", expression}, cwd, 128, nil)
	if err != nil {
		return "", err
	}
	commit := strings.ToLower(string(bytes.TrimSpace(result)))
	if !ascii([]byte(commit)) || !commitPattern.MatchString(commit) {
		return "", repositoryError(RepositoryGit)
	}
	return commit, nil
}

type treeEntry struct {
	mode     string
	kind     string
	objectID string
	size     int64
	path     string
}

func (acquirer *Acquirer) tree(ctx context.Context, mirror, commit, cwd string) ([]treeEntry, error) {
	raw, err := acquirer.run(ctx, []string{"--git-dir", mirror, "ls-tree", "-rz", "--full-tree", "--long", commit}, cwd, acquirer.limits.MaxTreeBytes, nil)
	if err != nil {
		return nil, err
	}
	records := bytes.Split(raw, []byte{0})
	if len(records) == 0 || len(records[len(records)-1]) != 0 {
		return nil, repositoryError(RepositoryUnsafeTree)
	}
	entries := make([]treeEntry, 0, len(records)-1)
	seen := map[string]struct{}{}
	for _, record := range records[:len(records)-1] {
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 || !utf8.Valid(parts[1]) {
			return nil, repositoryError(RepositoryUnsafeTree)
		}
		metadata := bytes.Fields(parts[0])
		if len(metadata) != 4 || !ascii(metadata[0]) || !ascii(metadata[1]) || !ascii(metadata[2]) || !ascii(metadata[3]) {
			return nil, repositoryError(RepositoryUnsafeTree)
		}
		entry := treeEntry{mode: string(metadata[0]), kind: string(metadata[1]), objectID: string(metadata[2]), path: string(parts[1])}
		if err := sourcefiles.ValidateSourcePath(entry.path); err != nil {
			return nil, repositoryError(RepositoryUnsafeTree)
		}
		for _, segment := range strings.Split(entry.path, "/") {
			if strings.EqualFold(segment, ".git") {
				return nil, repositoryError(RepositoryUnsafeTree)
			}
		}
		if _, duplicate := seen[entry.path]; duplicate {
			return nil, repositoryError(RepositoryUnsafeTree)
		}
		seen[entry.path] = struct{}{}
		if entry.mode == "160000" || entry.kind == "commit" {
			return nil, repositoryError(RepositorySubmodule)
		}
		if entry.mode == "120000" {
			return nil, repositoryError(RepositorySymlink)
		}
		if entry.mode != "100644" && entry.mode != "100755" || entry.kind != "blob" || !commitPattern.MatchString(strings.ToLower(entry.objectID)) {
			return nil, repositoryError(RepositoryUnsafeTree)
		}
		entry.objectID = strings.ToLower(entry.objectID)
		entry.size, err = strconv.ParseInt(string(metadata[3]), 10, 64)
		if err != nil || entry.size < 0 {
			return nil, repositoryError(RepositoryUnsafeTree)
		}
		entries = append(entries, entry)
		if len(entries) > acquirer.limits.MaxFiles {
			return nil, repositoryError(RepositorySnapshotLimit)
		}
	}
	return entries, nil
}

func (acquirer *Acquirer) rejectLFSPointers(ctx context.Context, mirror string, entries []treeEntry, cwd string) error {
	candidates := make([]treeEntry, 0)
	for _, entry := range entries {
		if entry.size >= int64(len(lfsHeader)) && entry.size <= maximumLFSPointerBytes {
			candidates = append(candidates, entry)
		}
	}
	for offset := 0; offset < len(candidates); offset += 1000 {
		end := min(offset+1000, len(candidates))
		chunk := candidates[offset:end]
		var request bytes.Buffer
		outputLimit := 0
		for _, entry := range chunk {
			request.WriteString(entry.objectID + "\n")
			outputLimit += int(entry.size) + len(entry.objectID) + 32
		}
		response, err := acquirer.run(ctx, []string{"--git-dir", mirror, "cat-file", "--batch"}, cwd, outputLimit, request.Bytes())
		if err != nil {
			return err
		}
		cursor := 0
		for _, entry := range chunk {
			relative := bytes.IndexByte(response[cursor:], '\n')
			if relative < 0 {
				return repositoryError(RepositoryGit)
			}
			newline := cursor + relative
			fields := bytes.Fields(response[cursor:newline])
			if len(fields) != 3 || string(fields[0]) != entry.objectID || string(fields[1]) != "blob" || string(fields[2]) != strconv.FormatInt(entry.size, 10) {
				return repositoryError(RepositoryGit)
			}
			start := newline + 1
			end := start + int(entry.size)
			if end >= len(response) || response[end] != '\n' {
				return repositoryError(RepositoryGit)
			}
			if bytes.HasPrefix(response[start:end], []byte(lfsHeader)) {
				return repositoryError(RepositoryGitLFS)
			}
			cursor = end + 1
		}
		if cursor != len(response) {
			return repositoryError(RepositoryGit)
		}
	}
	return nil
}

func (acquirer *Acquirer) materializeFiles(ctx context.Context, mirror string, entries []treeEntry, includes, excludes []string, cwd string) ([]sourcefiles.File, []string, error) {
	byPath := make(map[string]treeEntry, len(entries))
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		byPath[entry.path] = entry
		paths = append(paths, entry.path)
	}
	var ignore []byte
	if entry, exists := byPath[".openwikiignore"]; exists {
		value, err := acquirer.blob(ctx, mirror, entry, cwd)
		if err != nil {
			return nil, nil, err
		}
		ignore = value
	}
	filter, err := newPathFilter(includes, excludes, ignore)
	if err != nil {
		return nil, nil, repositoryError(RepositoryUnsafeTree)
	}
	selected, ignored, err := filter.partition(paths)
	if err != nil {
		return nil, nil, repositoryError(RepositoryUnsafeTree)
	}
	selected = slices.DeleteFunc(selected, func(value string) bool { return value == ".openwikiignore" })
	if _, exists := byPath[".openwikiignore"]; exists && !slices.Contains(ignored, ".openwikiignore") {
		ignored = append(ignored, ".openwikiignore")
		slices.Sort(ignored)
	}
	encodedIgnored, _ := compactJSON(ignored)
	if len(ignored) > acquirer.limits.MaxIgnoredPaths || len(encodedIgnored) > acquirer.limits.MaxIgnoredPathBytes {
		return nil, nil, repositoryError(RepositorySnapshotLimit)
	}
	var total int64
	files := make([]sourcefiles.File, 0, len(selected))
	for _, path := range selected {
		entry := byPath[path]
		if entry.size > acquirer.limits.MaxFileBytes || entry.size > math.MaxInt64-total {
			return nil, nil, repositoryError(RepositorySnapshotLimit)
		}
		total += entry.size
		if total > acquirer.limits.MaxTotalBytes {
			return nil, nil, repositoryError(RepositorySnapshotLimit)
		}
		content, err := acquirer.blob(ctx, mirror, entry, cwd)
		if err != nil {
			return nil, nil, err
		}
		if bytes.HasPrefix(content, []byte(lfsHeader)) {
			return nil, nil, repositoryError(RepositoryGitLFS)
		}
		files = append(files, sourcefiles.File{Path: path, Content: content})
	}
	return files, ignored, nil
}

func (acquirer *Acquirer) blob(ctx context.Context, mirror string, entry treeEntry, cwd string) ([]byte, error) {
	if entry.size > acquirer.limits.MaxFileBytes || entry.size > int64(math.MaxInt) {
		return nil, repositoryError(RepositorySnapshotLimit)
	}
	content, err := acquirer.run(ctx, []string{"--git-dir", mirror, "cat-file", "blob", entry.objectID}, cwd, int(acquirer.limits.MaxFileBytes), nil)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != entry.size {
		return nil, repositoryError(RepositoryGit)
	}
	return content, nil
}

func (acquirer *Acquirer) run(ctx context.Context, arguments []string, cwd string, outputLimit int, input []byte) ([]byte, error) {
	result, err := executeGit(ctx, arguments, baseGitEnvironment(), cwd, outputLimit, acquirer.limits.CommandTimeout, input)
	if err != nil {
		return nil, acquirer.mapExecutionError(err)
	}
	if result.exitCode != 0 {
		return nil, repositoryError(RepositoryGit)
	}
	return cloneBytes(result.stdout), nil
}

func (acquirer *Acquirer) mapExecutionError(err error) error {
	if errors.Is(err, errCommandOutputLimit) {
		return repositoryError(RepositoryOutputTooLarge)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return repositoryError(RepositoryTimeout)
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return repositoryError(RepositoryGit)
}

type replayMetadata struct {
	Commit       string   `json:"commit"`
	IgnoredPaths []string `json:"ignored_paths"`
}

func (acquirer *Acquirer) encodeMetadata(commit string, ignored []string) (string, error) {
	encoded, err := compactJSON(replayMetadata{Commit: commit, IgnoredPaths: ignored})
	return string(encoded), err
}

func (acquirer *Acquirer) decodeStoredMetadata(metadata *string) (string, []string, error) {
	if metadata == nil {
		return "", nil, repositoryError(RepositoryInvalidMirror)
	}
	return acquirer.decodeMetadata(*metadata)
}

func (acquirer *Acquirer) decodeMetadata(metadata string) (string, []string, error) {
	decoder := json.NewDecoder(strings.NewReader(metadata))
	decoder.DisallowUnknownFields()
	var value replayMetadata
	if err := decoder.Decode(&value); err != nil {
		return "", nil, repositoryError(RepositoryInvalidMirror)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || value.IgnoredPaths == nil {
		return "", nil, repositoryError(RepositoryInvalidMirror)
	}
	commit := strings.ToLower(value.Commit)
	encodedIgnored, err := compactJSON(value.IgnoredPaths)
	if err != nil || !commitPattern.MatchString(commit) || len(value.IgnoredPaths) > acquirer.limits.MaxIgnoredPaths || len(encodedIgnored) > acquirer.limits.MaxIgnoredPathBytes {
		return "", nil, repositoryError(RepositoryInvalidMirror)
	}
	return commit, append([]string(nil), value.IgnoredPaths...), nil
}

func ascii(value []byte) bool {
	for _, character := range value {
		if character > 127 {
			return false
		}
	}
	return true
}
