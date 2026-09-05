package sourcefiles

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

const maxSnapshotMetadataBytes = 2 * 1024 * 1024

type SnapshotLimits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

func DefaultSnapshotLimits() SnapshotLimits {
	return SnapshotLimits{
		MaxFiles:      200_000,
		MaxFileBytes:  10 * 1024 * 1024,
		MaxTotalBytes: 1024 * 1024 * 1024,
	}
}

func (limits SnapshotLimits) validate() error {
	if limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 {
		return errors.New("snapshot limits must be positive")
	}
	if limits.MaxFileBytes > limits.MaxTotalBytes {
		return errors.New("single-file limit cannot exceed total snapshot limit")
	}
	return nil
}

type StoredSnapshot struct {
	ArtifactKey string
	Fingerprint Fingerprint
	Metadata    *string
}

type Store struct {
	root   string
	limits SnapshotLimits
}

func NewStore(root string, configured ...SnapshotLimits) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("application data root must be absolute")
	}
	if len(configured) > 1 {
		return nil, errors.New("snapshot limits are invalid")
	}
	limits := DefaultSnapshotLimits()
	if len(configured) == 1 {
		limits = configured[0]
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, storageError("application data root is not a directory")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, storageError("application data root is not a directory")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	return &Store{root: filepath.Clean(resolved), limits: limits}, nil
}

func (store *Store) Root() string {
	return store.root
}

func (store *Store) MirrorPath(sourceID ID) (string, error) {
	root, err := store.sourceRoot(sourceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "mirror.git"), nil
}

func (store *Store) SnapshotPath(sourceID, revisionID ID) (string, error) {
	root, err := store.sourceRoot(sourceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "snapshots", revisionID.String()), nil
}

func (store *Store) ResolveArtifactKey(artifactKey string) (string, error) {
	parts := strings.Split(artifactKey, "/")
	if len(parts) != 4 || parts[0] != "sources" || parts[2] != "snapshots" {
		return "", storageError("source artifact key is invalid")
	}
	sourceID, sourceErr := ParseID(parts[1])
	revisionID, revisionErr := ParseID(parts[3])
	if sourceErr != nil || revisionErr != nil {
		return "", storageError("source artifact key is invalid")
	}
	candidate := filepath.Join(
		store.root,
		filepath.FromSlash(SnapshotArtifactKey(sourceID, revisionID)),
	)
	marker := filepath.Join(filepath.Dir(candidate), "."+revisionID.String()+".complete")
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || !store.contains(resolved) {
		return "", storageError("source artifact is missing or escapes the data root")
	}
	markerResolved, err := filepath.EvalSymlinks(marker)
	if err != nil || !store.contains(markerResolved) {
		return "", storageError("source artifact is missing or escapes the data root")
	}
	if err := store.rejectSymlinkSegments(candidate); err != nil {
		return "", storageError("source artifact path contains a symlink")
	}
	markerInfo, markerErr := os.Lstat(marker)
	rootInfo, rootErr := os.Stat(resolved)
	resolvedMarkerInfo, resolvedMarkerErr := os.Stat(markerResolved)
	if markerErr != nil ||
		markerInfo.Mode()&os.ModeSymlink != 0 ||
		rootErr != nil ||
		!rootInfo.IsDir() ||
		resolvedMarkerErr != nil ||
		!resolvedMarkerInfo.Mode().IsRegular() {
		return "", storageError("source artifact is incomplete or invalid")
	}
	return filepath.Clean(resolved), nil
}

func (store *Store) StoreSnapshot(
	sourceID ID,
	revisionID ID,
	files iter.Seq[File],
	metadata *string,
) (StoredSnapshot, error) {
	metadata = cloneString(metadata)
	if metadata != nil &&
		(!utf8.ValidString(*metadata) || len([]byte(*metadata)) > maxSnapshotMetadataBytes) {
		return StoredSnapshot{}, storageError("snapshot metadata is invalid")
	}
	materialized, err := store.materialize(files)
	if err != nil {
		return StoredSnapshot{}, err
	}
	fingerprint, err := FingerprintFiles(materialized)
	if err != nil {
		return StoredSnapshot{}, err
	}
	markerPayload, err := markerPayload(fingerprint, metadata)
	if err != nil {
		return StoredSnapshot{}, storageError("snapshot metadata is invalid")
	}
	sourceRoot, err := store.sourceRoot(sourceID)
	if err != nil {
		return StoredSnapshot{}, err
	}
	snapshots := filepath.Join(sourceRoot, "snapshots")
	if err := store.mkdirContained(snapshots); err != nil {
		return StoredSnapshot{}, err
	}
	destination := filepath.Join(snapshots, revisionID.String())
	marker := filepath.Join(snapshots, "."+revisionID.String()+".complete")
	temporary, err := os.MkdirTemp(snapshots, "."+revisionID.String()+"-")
	if err != nil {
		return StoredSnapshot{}, err
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = removeTree(temporary)
		return StoredSnapshot{}, err
	}
	defer func() {
		if _, statErr := os.Lstat(temporary); statErr == nil {
			_ = removeTree(temporary)
		}
	}()
	for _, file := range materialized {
		if err := writeSnapshotFile(temporary, file); err != nil {
			return StoredSnapshot{}, err
		}
	}
	if err := store.publishSnapshot(temporary, destination, marker, markerPayload); err != nil {
		if !errors.Is(err, ErrSnapshotExists) {
			return StoredSnapshot{}, err
		}
		artifactKey := SnapshotArtifactKey(sourceID, revisionID)
		if !store.snapshotMatches(artifactKey, materialized) {
			return StoredSnapshot{}, err
		}
		existing, loadErr := store.LoadSnapshot(sourceID, revisionID)
		if loadErr != nil {
			return StoredSnapshot{}, loadErr
		}
		if existing != nil {
			return *existing, nil
		}
	}
	return StoredSnapshot{
		ArtifactKey: SnapshotArtifactKey(sourceID, revisionID),
		Fingerprint: fingerprint,
		Metadata:    cloneString(metadata),
	}, nil
}

func (store *Store) LoadSnapshot(sourceID, revisionID ID) (*StoredSnapshot, error) {
	artifactKey := SnapshotArtifactKey(sourceID, revisionID)
	root, err := store.ResolveArtifactKey(artifactKey)
	if err != nil {
		if errors.Is(err, ErrSourceStorage) {
			return nil, nil
		}
		return nil, err
	}
	marker := filepath.Join(filepath.Dir(root), "."+revisionID.String()+".complete")
	payload, err := readFileNoFollow(marker)
	if err != nil {
		return nil, storageError("snapshot completion metadata is invalid")
	}
	if len(payload) == 0 {
		return nil, nil
	}
	fingerprint, metadata, err := parseMarker(payload)
	if err != nil {
		return nil, storageError("snapshot completion metadata is invalid")
	}
	return &StoredSnapshot{
		ArtifactKey: artifactKey,
		Fingerprint: fingerprint,
		Metadata:    metadata,
	}, nil
}

func (store *Store) DiscardSnapshot(sourceID, revisionID ID) error {
	sourceRoot := filepath.Join(store.root, "sources", sourceID.String())
	snapshots := filepath.Join(sourceRoot, "snapshots")
	for _, path := range []string{filepath.Join(store.root, "sources"), sourceRoot, snapshots} {
		if !store.contains(path) {
			return storageError("source storage path escapes the data root")
		}
		info, exists, err := lstat(path)
		if err != nil || !exists {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return storageError("snapshot cleanup path is invalid")
		}
	}
	destination := filepath.Join(snapshots, revisionID.String())
	marker := filepath.Join(snapshots, "."+revisionID.String()+".complete")
	intent := marker + ".publishing"
	descriptor, err := openNoFollow(intent, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return storageError("snapshot cleanup lock is invalid")
	}
	defer descriptor.Close()
	if !regularDescriptorPath(descriptor, intent) {
		return storageError("snapshot cleanup lock is invalid")
	}
	if err := lockFile(descriptor); err != nil {
		return storageError("snapshot cleanup lock is invalid")
	}
	defer unlockFile(descriptor)
	if isSymlink(marker) || isSymlink(destination) {
		return storageError("snapshot cleanup target is invalid")
	}
	if info, exists, statErr := lstat(destination); statErr != nil {
		return statErr
	} else if exists {
		if !info.IsDir() {
			return storageError("snapshot cleanup target is invalid")
		}
		if err := removeTree(destination); err != nil {
			return err
		}
	}
	if info, exists, statErr := lstat(marker); statErr != nil {
		return statErr
	} else if exists {
		if !info.Mode().IsRegular() {
			return storageError("snapshot cleanup marker is invalid")
		}
		if err := os.Chmod(marker, 0o600); err != nil {
			return err
		}
		if err := os.Remove(marker); err != nil {
			return err
		}
	}
	if err := descriptor.Truncate(0); err != nil {
		return err
	}
	if _, err := descriptor.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := descriptor.Sync(); err != nil {
		return err
	}
	return fsyncDirectory(snapshots)
}

func (store *Store) materialize(files iter.Seq[File]) ([]File, error) {
	if files == nil {
		return nil, ErrInvalidFileSet
	}
	materialized := make([]File, 0)
	var total int64
	var materializeErr error
	files(func(file File) bool {
		if materializeErr != nil {
			return false
		}
		if len(materialized) >= store.limits.MaxFiles {
			materializeErr = storageError("snapshot exceeds the file-count limit")
			return false
		}
		if err := ValidateSourcePath(file.Path); err != nil {
			materializeErr = err
			return false
		}
		fileBytes := int64(len(file.Content))
		if fileBytes > store.limits.MaxFileBytes {
			materializeErr = storageError("snapshot file exceeds the size limit")
			return false
		}
		if total > store.limits.MaxTotalBytes-fileBytes {
			materializeErr = storageError("snapshot exceeds the byte limit")
			return false
		}
		total += fileBytes
		materialized = append(materialized, File{
			Path:    file.Path,
			Content: bytes.Clone(file.Content),
		})
		return true
	})
	if materializeErr != nil {
		return nil, materializeErr
	}
	return materialized, nil
}

func (store *Store) sourceRoot(sourceID ID) (string, error) {
	sources := filepath.Join(store.root, "sources")
	if err := store.mkdirContained(sources); err != nil {
		return "", err
	}
	root := filepath.Join(sources, sourceID.String())
	if err := store.mkdirContained(root); err != nil {
		return "", err
	}
	return root, nil
}

func (store *Store) mkdirContained(path string) error {
	if !store.contains(path) {
		return storageError("source storage path escapes the data root")
	}
	relative, err := filepath.Rel(store.root, path)
	if err != nil {
		return storageError("source storage path escapes the data root")
	}
	current := store.root
	for _, segment := range splitRelative(relative) {
		current = filepath.Join(current, segment)
		info, exists, statErr := lstat(current)
		if statErr != nil {
			return statErr
		}
		if !exists {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return mkdirErr
			}
			info, exists, statErr = lstat(current)
			if statErr != nil || !exists {
				return storageError("source storage path is not a directory")
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return storageError("source storage path contains a symlink")
		}
		if !info.IsDir() {
			return storageError("source storage path is not a directory")
		}
	}
	return nil
}

func (store *Store) contains(path string) bool {
	if path == "" {
		return false
	}
	relative, err := filepath.Rel(store.root, filepath.Clean(path))
	return err == nil &&
		!filepath.IsAbs(relative) &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (store *Store) rejectSymlinkSegments(path string) error {
	if !store.contains(path) {
		return errors.New("outside root")
	}
	relative, err := filepath.Rel(store.root, path)
	if err != nil {
		return err
	}
	current := store.root
	for _, segment := range splitRelative(relative) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink")
		}
	}
	return nil
}

func (store *Store) publishSnapshot(
	temporary string,
	destination string,
	marker string,
	markerPayload []byte,
) (returnErr error) {
	intent := marker + ".publishing"
	descriptor, created, err := openPublicationIntent(intent)
	if err != nil {
		return snapshotExistsError()
	}
	defer descriptor.Close()
	if !regularDescriptorPath(descriptor, intent) {
		return snapshotExistsError()
	}
	if err := lockFile(descriptor); err != nil {
		return storageError("snapshot publication lock is invalid")
	}
	defer unlockFile(descriptor)
	if created {
		if err := descriptor.Sync(); err != nil {
			return err
		}
		if err := fsyncDirectory(filepath.Dir(destination)); err != nil {
			return err
		}
	}
	intentState, err := readIntentState(descriptor)
	if err != nil || !validIntentState(intentState) {
		return snapshotExistsError()
	}
	setIntentState := func(value string) error {
		if _, err := descriptor.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := descriptor.Truncate(0); err != nil {
			return err
		}
		if err := writeAll(descriptor, []byte(value)); err != nil {
			return err
		}
		return descriptor.Sync()
	}
	if _, markerExists, markerErr := lstat(marker); markerErr != nil {
		return markerErr
	} else if markerExists {
		if isSymlink(marker) {
			if err := setIntentState("blocked\n"); err != nil {
				return err
			}
		} else if err := setIntentState("complete\n"); err != nil {
			return err
		}
		return snapshotExistsError()
	}
	if intentState == "blocked\n" || intentState == "complete\n" {
		return snapshotExistsError()
	}
	destinationInfo, destinationExists, err := lstat(destination)
	if err != nil {
		return err
	}
	if intentState == "" {
		if destinationExists {
			if err := setIntentState("blocked\n"); err != nil {
				return err
			}
			return snapshotExistsError()
		}
		if err := setIntentState("publishing\n"); err != nil {
			return err
		}
	} else if destinationExists && destinationInfo.Mode()&os.ModeSymlink != 0 {
		if err := setIntentState("blocked\n"); err != nil {
			return err
		}
		return snapshotExistsError()
	} else if destinationExists {
		if !destinationInfo.IsDir() {
			return snapshotExistsError()
		}
		if err := removeTree(destination); err != nil {
			return err
		}
		if err := fsyncDirectory(filepath.Dir(destination)); err != nil {
			return err
		}
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return snapshotExistsError()
		}
		return err
	}
	if err := fsyncDirectory(filepath.Dir(destination)); err != nil {
		_ = removeTree(destination)
		return err
	}
	markerCreated := false
	defer func() {
		if returnErr == nil {
			return
		}
		if markerCreated {
			_ = os.Remove(marker)
		}
		if info, exists, _ := lstat(destination); exists && info.Mode()&os.ModeSymlink == 0 {
			_ = removeTree(destination)
			_ = fsyncDirectory(filepath.Dir(destination))
		}
	}()
	if err := os.Chmod(temporary, 0o700); err != nil {
		return err
	}
	children, err := os.ReadDir(temporary)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := os.Rename(
			filepath.Join(temporary, child.Name()),
			filepath.Join(destination, child.Name()),
		); err != nil {
			return err
		}
	}
	if err := os.Remove(temporary); err != nil {
		return err
	}
	if err := makeReadOnly(destination); err != nil {
		return err
	}
	if err := fsyncTree(destination); err != nil {
		return err
	}
	markerDescriptor, err := openNoFollow(
		marker,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o440,
	)
	if err != nil {
		return storageError("snapshot completion metadata could not be created")
	}
	markerCreated = true
	if err := writeAll(markerDescriptor, markerPayload); err != nil {
		_ = markerDescriptor.Close()
		return storageError("snapshot completion metadata could not be written")
	}
	if err := markerDescriptor.Sync(); err != nil {
		_ = markerDescriptor.Close()
		return err
	}
	if err := markerDescriptor.Close(); err != nil {
		return err
	}
	if err := fsyncDirectory(filepath.Dir(destination)); err != nil {
		return err
	}
	if err := setIntentState("complete\n"); err != nil {
		return err
	}
	return nil
}

func (store *Store) snapshotMatches(artifactKey string, expected []File) bool {
	root, err := store.ResolveArtifactKey(artifactKey)
	if err != nil {
		return false
	}
	expectedFiles := make(map[string][]byte, len(expected))
	expectedDirectories := make(map[string]struct{})
	for _, file := range expected {
		expectedFiles[file.Path] = file.Content
		parts := strings.Split(file.Path, "/")
		for index := 1; index < len(parts); index++ {
			expectedDirectories[strings.Join(parts[:index], "/")] = struct{}{}
		}
	}
	actualFiles := make(map[string]struct{})
	actualDirectories := make(map[string]struct{})
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			actualDirectories[relative] = struct{}{}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("not regular")
		}
		content, exists := expectedFiles[relative]
		if !exists || info.Size() != int64(len(content)) || !fileEqualsNoFollow(path, content) {
			return errors.New("content differs")
		}
		actualFiles[relative] = struct{}{}
		return nil
	}); err != nil {
		return false
	}
	return sameSet(actualFiles, expectedFiles) && sameDirectorySet(actualDirectories, expectedDirectories)
}

func markerPayload(fingerprint Fingerprint, metadata *string) ([]byte, error) {
	value := struct {
		ByteCount   int64   `json:"byte_count"`
		FileCount   int     `json:"file_count"`
		Fingerprint string  `json:"fingerprint"`
		Metadata    *string `json:"metadata"`
		Version     int     `json:"version"`
	}{
		ByteCount:   fingerprint.ByteCount,
		FileCount:   fingerprint.FileCount,
		Fingerprint: hex.EncodeToString(fingerprint.Digest[:]),
		Metadata:    metadata,
		Version:     1,
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(unescapeMarkerLineSeparators(encoded.Bytes()), []byte("\n")), nil
}

func unescapeMarkerLineSeparators(encoded []byte) []byte {
	result := make([]byte, 0, len(encoded))
	for index := 0; index < len(encoded); {
		if index+6 <= len(encoded) &&
			encoded[index] == '\\' &&
			(bytes.Equal(encoded[index:index+6], []byte(`\u2028`)) ||
				bytes.Equal(encoded[index:index+6], []byte(`\u2029`))) {
			preceding := 0
			for selected := index - 1; selected >= 0 && encoded[selected] == '\\'; selected-- {
				preceding++
			}
			if preceding%2 == 0 {
				if encoded[index+5] == '8' {
					result = append(result, []byte("\u2028")...)
				} else {
					result = append(result, []byte("\u2029")...)
				}
				index += 6
				continue
			}
		}
		result = append(result, encoded[index])
		index++
	}
	return result
}

func parseMarker(payload []byte) (Fingerprint, *string, error) {
	if !utf8.Valid(payload) {
		return Fingerprint{}, nil, errors.New("invalid marker")
	}
	var value map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&value); err != nil || value == nil {
		return Fingerprint{}, nil, errors.New("invalid marker")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Fingerprint{}, nil, err
	}
	version, err := jsonNonnegativeInt(value["version"])
	if err != nil || version != 1 {
		return Fingerprint{}, nil, errors.New("invalid marker")
	}
	fileCount64, err := jsonNonnegativeInt(value["file_count"])
	if err != nil || fileCount64 > math.MaxInt {
		return Fingerprint{}, nil, errors.New("invalid marker")
	}
	byteCount, err := jsonNonnegativeInt(value["byte_count"])
	if err != nil {
		return Fingerprint{}, nil, errors.New("invalid marker")
	}
	var digestText string
	if err := json.Unmarshal(value["fingerprint"], &digestText); err != nil {
		return Fingerprint{}, nil, errors.New("invalid marker")
	}
	digest, err := hex.DecodeString(digestText)
	if err != nil || len(digest) != 32 {
		return Fingerprint{}, nil, errors.New("invalid marker")
	}
	var fingerprintDigest [32]byte
	copy(fingerprintDigest[:], digest)
	metadataRaw, exists := value["metadata"]
	if !exists {
		return Fingerprint{}, nil, errors.New("invalid marker")
	}
	var metadata *string
	if !bytes.Equal(bytes.TrimSpace(metadataRaw), []byte("null")) {
		var decoded string
		if err := json.Unmarshal(metadataRaw, &decoded); err != nil {
			return Fingerprint{}, nil, errors.New("invalid marker")
		}
		metadata = &decoded
	}
	return Fingerprint{
		Digest:    fingerprintDigest,
		FileCount: int(fileCount64),
		ByteCount: byteCount,
	}, metadata, nil
}

func jsonNonnegativeInt(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, errors.New("missing integer")
	}
	value := string(bytes.TrimSpace(raw))
	if strings.ContainsAny(value, ".eE") {
		return 0, errors.New("not integer")
	}
	result, err := strconv.ParseInt(value, 10, 64)
	if err != nil || result < 0 {
		return 0, errors.New("not nonnegative integer")
	}
	return result, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func splitRelative(relative string) []string {
	if relative == "." || relative == "" {
		return nil
	}
	return strings.Split(relative, string(filepath.Separator))
}

func lstat(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return info, true, nil
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func regularDescriptorPath(descriptor *os.File, path string) bool {
	info, err := os.Lstat(path)
	descriptorInfo, descriptorErr := descriptor.Stat()
	return err == nil &&
		descriptorErr == nil &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().IsRegular() &&
		os.SameFile(info, descriptorInfo)
}

func openNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags|syscall.O_NOFOLLOW, mode)
}

func openPublicationIntent(path string) (*os.File, bool, error) {
	descriptor, err := openNoFollow(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return descriptor, true, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, false, err
	}
	descriptor, err = openNoFollow(path, os.O_RDWR, 0)
	return descriptor, false, err
}

func lockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func unlockFile(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func readIntentState(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	buffer := make([]byte, 32)
	count, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return string(buffer[:count]), nil
}

func validIntentState(state string) bool {
	return state == "" || state == "publishing\n" || state == "blocked\n" || state == "complete\n"
}

func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

func readFileNoFollow(path string) ([]byte, error) {
	descriptor, err := openNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer descriptor.Close()
	return io.ReadAll(descriptor)
}

func fsyncDirectory(path string) error {
	descriptor, err := openNoFollow(path, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer descriptor.Close()
	return descriptor.Sync()
}

func writeSnapshotFile(root string, file File) error {
	if err := ValidateSourcePath(file.Path); err != nil {
		return err
	}
	destination := filepath.Join(root, filepath.FromSlash(file.Path))
	parent := filepath.Dir(destination)
	if err := mkdirSnapshotParent(root, parent); err != nil {
		return err
	}
	descriptor, err := openNoFollow(
		destination,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return storageError("snapshot file could not be created safely")
	}
	if err := writeAll(descriptor, file.Content); err != nil {
		_ = descriptor.Close()
		return err
	}
	if err := descriptor.Sync(); err != nil {
		_ = descriptor.Close()
		return err
	}
	return descriptor.Close()
}

func mkdirSnapshotParent(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return storageError("snapshot path escapes its root")
	}
	current := root
	for _, segment := range splitRelative(relative) {
		current = filepath.Join(current, segment)
		info, exists, statErr := lstat(current)
		if statErr != nil {
			return statErr
		}
		if !exists {
			if mkdirErr := os.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return mkdirErr
			}
			info, exists, statErr = lstat(current)
			if statErr != nil || !exists {
				return storageError("snapshot parent is not a directory")
			}
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return storageError("snapshot path contains a symlink")
		}
		if !info.IsDir() {
			return storageError("snapshot parent is not a directory")
		}
	}
	return nil
}

func makeReadOnly(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return storageError("snapshot contains a symlink")
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return storageError("snapshot contains a non-regular file")
		}
		return os.Chmod(path, 0o440)
	}); err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index], 0o550); err != nil {
			return err
		}
	}
	return nil
}

func fsyncTree(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return storageError("snapshot contains a symlink")
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		descriptor, err := openNoFollow(path, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		syncErr := descriptor.Sync()
		closeErr := descriptor.Close()
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}); err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := fsyncDirectory(directories[index]); err != nil {
			return err
		}
	}
	return nil
}

func removeTree(root string) error {
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return os.Chmod(path, 0o600)
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(root)
}

func fileEqualsNoFollow(path string, expected []byte) bool {
	descriptor, err := openNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer descriptor.Close()
	buffer := make([]byte, 64*1024)
	offset := 0
	for offset < len(expected) {
		want := len(expected) - offset
		if want > len(buffer) {
			want = len(buffer)
		}
		count, err := io.ReadFull(descriptor, buffer[:want])
		if err != nil || !bytes.Equal(buffer[:count], expected[offset:offset+count]) {
			return false
		}
		offset += count
	}
	count, err := descriptor.Read(buffer[:1])
	return count == 0 && errors.Is(err, io.EOF)
}

func sameSet(actual map[string]struct{}, expected map[string][]byte) bool {
	if len(actual) != len(expected) {
		return false
	}
	for value := range expected {
		if _, exists := actual[value]; !exists {
			return false
		}
	}
	return true
}

func sameDirectorySet(actual, expected map[string]struct{}) bool {
	if len(actual) != len(expected) {
		return false
	}
	for value := range expected {
		if _, exists := actual[value]; !exists {
			return false
		}
	}
	return true
}
