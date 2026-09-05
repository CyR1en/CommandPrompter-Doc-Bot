package sourcefiles

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestFingerprintMatchesGoldenValue(t *testing.T) {
	first, err := FingerprintFiles([]File{
		{Path: "b.txt", Content: []byte("two")},
		{Path: "a.txt", Content: []byte("one")},
	})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	reordered, err := FingerprintFiles([]File{
		{Path: "a.txt", Content: []byte("one")},
		{Path: "b.txt", Content: []byte("two")},
	})
	if err != nil {
		t.Fatalf("reordered fingerprint: %v", err)
	}
	const pythonDigest = "9d54622517bf0386d9d2b71b87b07f1090207d21c95b81cd3395e59b7deab84c"
	if first != reordered || hex.EncodeToString(first.Digest[:]) != pythonDigest {
		t.Fatalf("fingerprint mismatch: %x / %x", first.Digest, reordered.Digest)
	}
	if first.FileCount != 2 || first.ByteCount != 6 {
		t.Fatalf("fingerprint counts = %d/%d", first.FileCount, first.ByteCount)
	}
	empty, err := FingerprintFiles(nil)
	if err != nil || hex.EncodeToString(empty.Digest[:]) != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty fingerprint = %x, %v", empty.Digest, err)
	}
}

func TestSourcePathsAndDuplicateFileSetsAreClosed(t *testing.T) {
	invalid := []string{
		"", "/etc/passwd", "../secret", "src//file.py", "src\\file.py",
		"src/./file.py", "src/file.py/", "control\x00path", strings.Repeat("a", 4097),
	}
	for _, value := range invalid {
		if err := ValidateSourcePath(value); !errors.Is(err, ErrInvalidSourcePath) {
			t.Fatalf("path %q error = %v", value, err)
		}
	}
	if err := ValidateSourcePath("unicodé/文档.txt"); err != nil {
		t.Fatalf("valid Unicode path: %v", err)
	}
	if _, err := FingerprintFiles([]File{
		{Path: "README.md", Content: []byte("one")},
		{Path: "README.md", Content: []byte("two")},
	}); !errors.Is(err, ErrInvalidFileSet) {
		t.Fatalf("duplicate file error = %v", err)
	}
}

func TestIDParsingAndArtifactKeyMatchPythonUUIDRendering(t *testing.T) {
	canonical := "00112233-4455-6677-8899-aabbccddeeff"
	for _, raw := range []string{
		canonical,
		"00112233445566778899AABBCCDDEEFF",
		"{00112233-4455-6677-8899-aabbccddeeff}",
		"urn:uuid:00112233-4455-6677-8899-aabbccddeeff",
	} {
		id, err := ParseID(raw)
		if err != nil || id.String() != canonical {
			t.Fatalf("parse ID %q = %q, %v", raw, id.String(), err)
		}
	}
	if _, err := ParseID("not-a-uuid"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid ID error = %v", err)
	}
}
