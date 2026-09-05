package artifacts

import (
	"crypto/sha256"
	"regexp"
	"strings"

	"github.com/cyr1en/ref0/internal/sourcefiles"
)

type ID = sourcefiles.ID

func ParseID(raw string) (ID, error) {
	return sourcefiles.ParseID(raw)
}

type Page struct {
	Slug          string
	Title         string
	Description   string
	PageType      string
	Markdown      string
	ContentSHA256 [sha256.Size]byte
	ClaimsJSON    []byte
	ClaimsSHA256  [sha256.Size]byte
}

type SourceRevision map[string]string

type PublishedWikiBundle struct {
	ArtifactKey    string
	ManifestSHA256 [sha256.Size]byte
	PageCount      int
}

var slugSegmentPattern = regexp.MustCompile(`\A[a-z0-9]+(?:-[a-z0-9]+)*\z`)

func NormalizePageSlug(value string) (string, error) {
	if value == "" ||
		value != strings.TrimSpace(value) ||
		len([]byte(value)) > 255 ||
		strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") ||
		strings.Contains(value, "//") ||
		strings.Contains(value, `\`) {
		return "", validationError("page slug is invalid")
	}
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if !slugSegmentPattern.MatchString(segment) ||
			strings.HasPrefix(segment, ".") ||
			(index == len(segments)-1 && (segment == "index" || segment == "log")) {
			return "", validationError("page slug is invalid")
		}
	}
	return value, nil
}
