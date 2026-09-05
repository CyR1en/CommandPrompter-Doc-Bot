package docgen

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/sourcefiles"
)

type EvidenceExcerpt struct {
	Text      string `json:"text"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Truncated bool   `json:"truncated"`
}

func (store *Store) GetWikiEvidence(ctx context.Context, knowledgeBaseID ID, versionID *WikiVersionID, slug, claimID, evidenceID string) (EvidenceExcerpt, error) {
	view, err := store.GetWiki(ctx, knowledgeBaseID, versionID, &slug)
	if err != nil {
		return EvidenceExcerpt{}, err
	}
	if view.Page != nil {
		for _, claim := range view.Page.Claims {
			if claim.ID != claimID {
				continue
			}
			for _, evidence := range claim.Evidence {
				if evidence.ID != evidenceID {
					continue
				}
				var key string
				location := evidence.Location
				err = store.pool.QueryRow(ctx, `SELECT artifact_key FROM source_revisions WHERE id=$1 AND source_id=$2 AND artifact_purged_at IS NULL`, pgUUID(location.SourceRevisionID), pgUUID(location.SourceID)).Scan(&key)
				if err != nil {
					return EvidenceExcerpt{}, notFound("evidence snapshot is unavailable")
				}
				root, err := store.sourceArtifacts.ResolveArtifactKey(key)
				if err != nil {
					return EvidenceExcerpt{}, notFound("evidence snapshot is unavailable")
				}
				content, err := sourcefiles.ReadFile(root, location.Path, 256_000)
				if err != nil || !utf8.Valid(content) {
					return EvidenceExcerpt{}, notFound("evidence file is unavailable or exceeds its read limit")
				}
				lines := sourcefiles.SplitLines(string(content))
				start, end := 1, len(lines)
				if location.StartLine != nil {
					start = *location.StartLine
					end = *location.EndLine
				}
				if start < 1 || end < start || end > len(lines) {
					return EvidenceExcerpt{}, notFound("evidence line range is unavailable")
				}
				selectedEnd := min(end, start+399)
				return EvidenceExcerpt{Text: strings.Join(lines[start-1:selectedEnd], "\n"), StartLine: start, EndLine: selectedEnd, Truncated: selectedEnd < end}, nil
			}
		}
	}
	return EvidenceExcerpt{}, notFound("evidence does not exist in this published page")
}
