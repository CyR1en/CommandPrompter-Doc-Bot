package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (store *PostgresExecutionStore) SearchWiki(ctx context.Context, captured CapturedKnowledgeBase, query string, limit int) ([]WikiSearchHit, error) {
	query = strings.TrimFunc(query, pythonWhitespace)
	if captured.WikiVersionID == (WikiVersionID{}) || query == "" || len([]rune(query)) > 1000 || limit < 1 || limit > 20 {
		return nil, fmt.Errorf("%w: wiki search is invalid", ErrEvidence)
	}
	rows, err := store.pool.Query(ctx, `
		WITH queries AS (
			SELECT plainto_tsquery('simple',$2) AS exact,
			       COALESCE((SELECT string_agg(quote_literal(term),' | ')::tsquery
			         FROM unnest(tsvector_to_array(to_tsvector('simple',$2))) term
			         WHERE term <> ALL(ARRAY['a','an','the','how','do','does','did','i','we','you','is','are','can','could','what','where','when','why','to','of','for','in','on','it','my','please'])), ''::tsquery) AS broad
		), hits AS (
			SELECT page.slug,page.title,NULL::text AS statement,NULL::text AS claim_id,
			       (page.search_vector @@ q.exact)::int + ts_rank_cd(page.search_vector,q.broad,32) AS rank
			FROM wiki_pages page CROSS JOIN queries q
			WHERE page.wiki_version_id=$1 AND (page.search_vector @@ q.exact OR page.search_vector @@ q.broad)
			UNION ALL
			SELECT page.slug,page.title,claim.statement,claim.stable_id,
			       (claim.search_vector @@ q.exact)::int + ts_rank_cd(claim.search_vector,q.broad,32) AS rank
			FROM claims claim
			JOIN wiki_pages page ON page.id=claim.wiki_page_id AND page.wiki_version_id=claim.wiki_version_id
			CROSS JOIN queries q
			WHERE claim.wiki_version_id=$1 AND (claim.search_vector @@ q.exact OR claim.search_vector @@ q.broad)
		), ranked AS (
			SELECT * FROM hits ORDER BY rank DESC,slug,claim_id NULLS FIRST,title LIMIT $3
		)
		SELECT hit.slug,hit.title,hit.statement,hit.claim_id,hit.rank,
		       GREATEST(COALESCE(passage.line_number,1)-10,1)::int
		FROM ranked hit
		JOIN wiki_pages page ON page.wiki_version_id=$1 AND page.slug=hit.slug
		CROSS JOIN queries q
		LEFT JOIN LATERAL (
			SELECT line_number FROM regexp_split_to_table(page.body,E'\r\n|[\n\r\013\f\034\035\036\u0085\u2028\u2029]') WITH ORDINALITY AS lines(line,line_number)
			WHERE hit.claim_id IS NULL AND to_tsvector('simple',line) @@ q.broad
			ORDER BY ts_rank_cd(to_tsvector('simple',line),q.broad) DESC,line_number LIMIT 1
		) passage ON true
		ORDER BY hit.rank DESC,hit.slug,hit.claim_id NULLS FIRST,hit.title
	`, pgUUID(ID(captured.WikiVersionID)), query, limit)
	if err != nil {
		return nil, err
	}
	result := make([]WikiSearchHit, 0, limit)
	slugs := make([]string, 0, limit)
	seenSlugs := make(map[string]struct{})
	for rows.Next() {
		var slug, title string
		var statement, claimID pgtype.Text
		var rank float32
		var startLine int
		if err = rows.Scan(&slug, &title, &statement, &claimID, &rank, &startLine); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, WikiSearchHit{
			Slug: slug, Title: title, Statement: executionOptionalText(statement),
			ClaimStableID: executionOptionalText(claimID), Rank: float64(rank), StartLine: startLine,
		})
		if _, exists := seenSlugs[slug]; !exists {
			seenSlugs[slug] = struct{}{}
			slugs = append(slugs, slug)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(slugs) == 0 || len(result) >= limit {
		return result, nil
	}
	planRows, err := store.pool.Query(ctx, `
		SELECT related_pages FROM documentation_pages
		WHERE run_id=$1 AND slug=ANY($2::text[]) ORDER BY slug
	`, pgUUID(ID(captured.DocumentationRunID)), slugs)
	if err != nil {
		return nil, err
	}
	linked := make([]string, 0)
	linkedSeen := make(map[string]struct{})
	for planRows.Next() {
		var raw []byte
		if err = planRows.Scan(&raw); err != nil {
			planRows.Close()
			return nil, err
		}
		var values []string
		if json.Unmarshal(raw, &values) != nil {
			planRows.Close()
			return nil, fmt.Errorf("%w: related page set is invalid", ErrEvidence)
		}
		for _, slug := range values {
			if _, hit := seenSlugs[slug]; hit {
				continue
			}
			if _, seen := linkedSeen[slug]; !seen {
				linkedSeen[slug] = struct{}{}
				linked = append(linked, slug)
			}
		}
	}
	if err = planRows.Err(); err != nil {
		planRows.Close()
		return nil, err
	}
	planRows.Close()
	remaining := limit - len(result)
	if len(linked) > remaining {
		linked = linked[:remaining]
	}
	if len(linked) == 0 {
		return result, nil
	}
	linkedRows, err := store.pool.Query(ctx, `
		SELECT slug,title FROM wiki_pages
		WHERE wiki_version_id=$1 AND slug=ANY($2::text[]) ORDER BY slug,title
	`, pgUUID(ID(captured.WikiVersionID)), linked)
	if err != nil {
		return nil, err
	}
	defer linkedRows.Close()
	for linkedRows.Next() {
		var slug, title string
		if err = linkedRows.Scan(&slug, &title); err != nil {
			return nil, err
		}
		result = append(result, WikiSearchHit{Slug: slug, Title: title, Linked: true})
	}
	return result, linkedRows.Err()
}

func (store *PostgresExecutionStore) ReadWikiPage(ctx context.Context, captured CapturedKnowledgeBase, slug string, startLine int, endLine *int) (WikiPassage, error) {
	slug, err := normalizeAgentSlug(slug)
	if err != nil || startLine < 1 || endLine != nil && *endLine < startLine {
		return WikiPassage{}, fmt.Errorf("%w: wiki page request is invalid", ErrEvidence)
	}
	var title, body string
	err = store.pool.QueryRow(ctx, `
		SELECT title,body FROM wiki_pages WHERE wiki_version_id=$1 AND slug=$2
	`, pgUUID(ID(captured.WikiVersionID)), slug).Scan(&title, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return WikiPassage{}, fmt.Errorf("%w: wiki page is unavailable", ErrEvidence)
	}
	if err != nil {
		return WikiPassage{}, err
	}
	if len([]byte(body)) > 1024*1024 {
		return WikiPassage{}, fmt.Errorf("%w: wiki page exceeds its bound", ErrEvidence)
	}
	lines := sourcefiles.SplitLines(body)
	selectedEnd := startLine + 399
	if endLine != nil {
		selectedEnd = *endLine
	}
	if selectedEnd > len(lines) {
		selectedEnd = len(lines)
	}
	if startLine > len(lines) || selectedEnd-startLine+1 > 400 {
		return WikiPassage{}, fmt.Errorf("%w: wiki line range exceeds its bound", ErrEvidence)
	}
	pathValue, start, finish := slug, startLine, selectedEnd
	return WikiPassage{
		Slug: slug, Title: title, StartLine: startLine, EndLine: selectedEnd,
		Text: strings.Join(lines[startLine-1:selectedEnd], "\n"),
		Citation: EvidenceCitation{
			Label:    fmt.Sprintf("%s, lines %d-%d", title, startLine, selectedEnd),
			Resource: fmt.Sprintf("wiki://%s/%s#L%d-L%d", captured.WikiVersionID.String(), slug, startLine, selectedEnd),
			Path:     &pathValue, StartLine: &start, EndLine: &finish,
		},
	}, nil
}

func (store *PostgresExecutionStore) GetClaim(ctx context.Context, captured CapturedKnowledgeBase, stableID string) (Claim, error) {
	if stableID == "" || len(stableID) > 128 {
		return Claim{}, fmt.Errorf("%w: Claim handle target is invalid", ErrEvidence)
	}
	var claimID pgtype.UUID
	var statement, slug string
	err := store.pool.QueryRow(ctx, `
		SELECT claim.id,claim.statement,page.slug
		FROM claims claim
		JOIN wiki_pages page ON page.id=claim.wiki_page_id AND page.wiki_version_id=claim.wiki_version_id
		WHERE claim.wiki_version_id=$1 AND claim.stable_id=$2
	`, pgUUID(ID(captured.WikiVersionID)), stableID).Scan(&claimID, &statement, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, fmt.Errorf("%w: Claim is unavailable", ErrEvidence)
	}
	if err != nil {
		return Claim{}, err
	}
	authorized := make(map[[32]byte]CapturedSource, len(captured.Sources))
	for _, source := range captured.Sources {
		var key [32]byte
		copy(key[:16], source.ID[:])
		copy(key[16:], source.RevisionID[:])
		authorized[key] = source
	}
	rows, err := store.pool.Query(ctx, `
		SELECT evidence_id,source_id,source_revision_id,resource,path,start_line,end_line
		FROM evidence WHERE claim_id=$1 ORDER BY evidence_id
	`, claimID)
	if err != nil {
		return Claim{}, err
	}
	defer rows.Close()
	evidence := make([]EvidenceCitation, 0)
	for rows.Next() {
		var evidenceID, resource, selectedPath string
		var sourceID, revisionID pgtype.UUID
		var startLine, endLine pgtype.Int4
		if err = rows.Scan(&evidenceID, &sourceID, &revisionID, &resource, &selectedPath, &startLine, &endLine); err != nil {
			return Claim{}, err
		}
		var key [32]byte
		copy(key[:16], sourceID.Bytes[:])
		copy(key[16:], revisionID.Bytes[:])
		source, exists := authorized[key]
		if !exists {
			return Claim{}, fmt.Errorf("%w: Claim evidence is outside captured scope", ErrEvidence)
		}
		label := source.Label
		if label == "" {
			label = source.ID.String()
		}
		pathValue := selectedPath
		citation := EvidenceCitation{
			Label: label, Resource: resource, SourceRevisionID: &source.RevisionID, Path: &pathValue,
		}
		if startLine.Valid {
			start, end := int(startLine.Int32), int(endLine.Int32)
			citation.StartLine, citation.EndLine = &start, &end
		}
		evidence = append(evidence, citation)
	}
	if err = rows.Err(); err != nil {
		return Claim{}, err
	}
	claimPath := slug
	return Claim{
		StableID: stableID, Statement: statement, PageSlug: slug,
		ClaimCitation: EvidenceCitation{
			Label:    "Claim " + stableID + " on " + slug,
			Resource: fmt.Sprintf("wiki://%s/%s#claim-%s", captured.WikiVersionID.String(), slug, stableID),
			Path:     &claimPath,
		},
		Evidence: evidence,
	}, nil
}

var agentSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*(?:/[a-z0-9]+(?:-[a-z0-9]+)*)*$`)

func normalizeAgentSlug(value string) (string, error) {
	if value == "" || value != strings.TrimFunc(value, pythonWhitespace) || len([]byte(value)) > 255 || !agentSlugPattern.MatchString(value) {
		return "", ErrEvidence
	}
	last := value[strings.LastIndex(value, "/")+1:]
	if last == "index" || last == "log" {
		return "", ErrEvidence
	}
	return value, nil
}
