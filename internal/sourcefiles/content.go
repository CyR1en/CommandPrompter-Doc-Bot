package sourcefiles

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"iter"
	"slices"
	"strings"
	"unicode/utf8"
)

const maxSourcePathBytes = 4096

var (
	ErrInvalidID         = errors.New("source or revision ID is invalid")
	ErrInvalidSourcePath = errors.New("source path is invalid")
	ErrInvalidFileSet    = errors.New("source file set is invalid")
)

// ID is the raw RFC 4122 byte representation used by Python's UUID.bytes.
type ID [16]byte

// ParseID accepts the textual forms accepted by Python's UUID constructor and
// canonicalizes them when rendering artifact keys.
func ParseID(raw string) (ID, error) {
	var id ID
	value := raw
	if strings.HasPrefix(value, "urn:uuid:") {
		value = strings.TrimPrefix(value, "urn:uuid:")
	}
	if len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' {
		value = value[1 : len(value)-1]
	}
	value = strings.ReplaceAll(value, "-", "")
	if len(value) != hex.EncodedLen(len(id)) {
		return id, ErrInvalidID
	}
	if _, err := hex.Decode(id[:], []byte(value)); err != nil {
		return ID{}, ErrInvalidID
	}
	return id, nil
}

func (id ID) String() string {
	encoded := hex.EncodeToString(id[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32]
}

func SnapshotArtifactKey(sourceID, revisionID ID) string {
	return "sources/" + sourceID.String() + "/snapshots/" + revisionID.String()
}

type File struct {
	Path    string
	Content []byte
}

// Files adapts a finite file list to StoreSnapshot's streaming input.
func Files(values ...File) iter.Seq[File] {
	return func(yield func(File) bool) {
		for _, value := range values {
			if !yield(value) {
				return
			}
		}
	}
}

func ValidateSourcePath(raw string) error {
	if raw == "" ||
		!utf8.ValidString(raw) ||
		strings.HasPrefix(raw, "/") ||
		strings.HasSuffix(raw, "/") ||
		strings.Contains(raw, `\`) ||
		len([]byte(raw)) > maxSourcePathBytes {
		return ErrInvalidSourcePath
	}
	for _, character := range raw {
		if character < 32 || character == 127 {
			return ErrInvalidSourcePath
		}
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrInvalidSourcePath
		}
	}
	return nil
}

type Fingerprint struct {
	Digest    [sha256.Size]byte
	FileCount int
	ByteCount int64
}

// FingerprintFiles reproduces app.sources.content.fingerprint_files byte for byte.
func FingerprintFiles(files []File) (Fingerprint, error) {
	normalized := make(map[string][]byte, len(files))
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if err := ValidateSourcePath(file.Path); err != nil {
			return Fingerprint{}, err
		}
		if _, exists := normalized[file.Path]; exists {
			return Fingerprint{}, ErrInvalidFileSet
		}
		normalized[file.Path] = file.Content
		paths = append(paths, file.Path)
	}
	slices.Sort(paths)
	digest := sha256.New()
	var framedLength [8]byte
	var byteCount int64
	for _, path := range paths {
		content := normalized[path]
		binary.BigEndian.PutUint64(framedLength[:], uint64(len([]byte(path))))
		_, _ = digest.Write(framedLength[:])
		_, _ = digest.Write([]byte(path))
		binary.BigEndian.PutUint64(framedLength[:], uint64(len(content)))
		_, _ = digest.Write(framedLength[:])
		contentDigest := sha256.Sum256(content)
		_, _ = digest.Write(contentDigest[:])
		byteCount += int64(len(content))
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return Fingerprint{
		Digest:    result,
		FileCount: len(paths),
		ByteCount: byteCount,
	}, nil
}
