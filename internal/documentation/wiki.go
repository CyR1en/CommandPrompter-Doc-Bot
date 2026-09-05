package docgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (store *Store) Publish(ctx context.Context, runID RunID, wikiVersionID WikiVersionID, bundle artifacts.PublishedWikiBundle, pages []artifacts.Page, permit jobs.Permit) (RunDetail, error) {
	var result RunDetail
	err := store.withTx(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		if err := assertJob(ctx, tx, permit, jobs.FinalizeRun, "documentation_run", ID(runID), map[string]any{"run_id": runID.String()}); err != nil {
			return err
		}
		detail, err := detailTx(ctx, tx, runID, true)
		if err != nil {
			return err
		}
		if detail.Run.Status == RunPublished {
			if detail.Run.PublishedWikiVersionID == nil || *detail.Run.PublishedWikiVersionID != wikiVersionID {
				return conflict("published wiki version is immutable")
			}
			result = detail
			return nil
		}
		if detail.Run.Status != RunFinalizing {
			return conflict("run cannot publish")
		}
		current, err := store.configurationCurrentTx(ctx, tx, detail.Run)
		if err != nil {
			return err
		}
		if !current {
			result, err = store.interruptTx(ctx, tx, detail, "documentation:source_drift")
			return err
		}
		expected := map[string]Page{}
		for _, page := range detail.Pages {
			if page.Status == PageComplete {
				expected[page.Target.Slug] = page
			}
		}
		supplied := map[string]artifacts.Page{}
		for _, page := range pages {
			if _, exists := supplied[page.Slug]; exists {
				return validation("published page set is incomplete")
			}
			supplied[page.Slug] = page
		}
		if len(expected) != len(supplied) || bundle.PageCount != len(supplied) {
			return validation("published page set is incomplete")
		}
		if bundle.ArtifactKey != artifacts.WikiArtifactKey(artifacts.ID(detail.Run.KnowledgeBaseID), artifacts.ID(wikiVersionID)) {
			return validation("wiki artifact key is invalid")
		}
		seenClaims := map[string]struct{}{}
		for _, page := range pages {
			claims, parseErr := parseClaimsSnapshot(page.ClaimsJSON)
			if parseErr != nil {
				return parseErr
			}
			for _, claim := range claims {
				if _, exists := seenClaims[claim.ID]; exists {
					return validation("wiki Claim IDs must be unique across pages")
				}
				seenClaims[claim.ID] = struct{}{}
			}
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO wiki_versions (id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count,created_at,published_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, pgUUID(ID(wikiVersionID)), pgUUID(detail.Run.KnowledgeBaseID), pgUUID(ID(runID)), bundle.ArtifactKey, bundle.ManifestSHA256[:], bundle.PageCount, now); err != nil {
			return err
		}
		captured := map[ID]CapturedSource{}
		for _, source := range detail.Run.Sources {
			captured[source.SourceID] = source
		}
		slugs := make([]string, 0, len(supplied))
		for slug := range supplied {
			slugs = append(slugs, slug)
		}
		sort.Strings(slugs)
		for _, slug := range slugs {
			accepted := supplied[slug]
			currentPage := expected[slug]
			digest := sha256.Sum256(append(append([]byte(nil), accepted.ContentSHA256[:]...), accepted.ClaimsSHA256[:]...))
			if !bytes.Equal(currentPage.SubmissionDigest, digest[:]) {
				return validation("page snapshot does not match database")
			}
			pageID, createErr := newID()
			if createErr != nil {
				return createErr
			}
			if _, err = tx.Exec(ctx, `INSERT INTO wiki_pages (id,wiki_version_id,slug,title,description,page_type,content_sha256,claims_sha256,body) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, pgUUID(pageID), pgUUID(ID(wikiVersionID)), slug, accepted.Title, accepted.Description, accepted.PageType, accepted.ContentSHA256[:], accepted.ClaimsSHA256[:], accepted.Markdown); err != nil {
				return err
			}
			if err = store.insertClaimsTx(ctx, tx, wikiVersionID, pageID, accepted.ClaimsJSON, captured); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE knowledge_bases SET published_wiki_id=$2,version=version+1,updated_at=$3 WHERE id=$1`, pgUUID(detail.Run.KnowledgeBaseID), pgUUID(ID(wikiVersionID)), now); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET status='PUBLISHED',published_wiki_version_id=$2,sanitized_error=NULL,updated_at=$3,completed_at=$3 WHERE id=$1`, pgUUID(ID(runID)), pgUUID(ID(wikiVersionID)), now); err != nil {
			return err
		}
		result, err = detailTx(ctx, tx, runID, false)
		if err != nil {
			return err
		}
		if err = recordRun(ctx, tx, result, nil, "documentation_run.published"); err != nil {
			return err
		}
		return store.followUpContentDriftTx(ctx, tx, detail.Run)
	})
	return result, err
}

func (store *Store) followUpContentDriftTx(ctx context.Context, tx pgx.Tx, run Run) error {
	var behind bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
	 SELECT 1 FROM documentation_run_sources captured JOIN sources current ON current.id=captured.source_id
	 WHERE captured.run_id=$1 AND captured.source_revision_id IS DISTINCT FROM current.current_revision_id
	)`, pgUUID(ID(run.ID))).Scan(&behind); err != nil {
		return err
	}
	if behind {
		_, followup, createErr := store.newRunTx(ctx, tx, run.KnowledgeBaseID, 0, nil, "follow-up:"+run.ID.String())
		if createErr != nil {
			return createErr
		}
		return recordRun(ctx, tx, followup, nil, "documentation_run.requested")
	}
	return nil
}

type snapshotEvidence struct{ ID, Resource, SourceRevisionID, SourceVersion string }
type snapshotClaim struct {
	ID, Statement string
	Evidence      []snapshotEvidence
}

func parseClaimsSnapshot(raw []byte) ([]snapshotClaim, error) {
	var root struct {
		Claims []struct {
			ID        string `json:"id"`
			Statement string `json:"statement"`
			Evidence  []struct {
				ID               string `json:"id"`
				Resource         string `json:"resource"`
				SourceRevisionID string `json:"source_revision_id"`
				SourceVersion    string `json:"source_version"`
			} `json:"evidence"`
		} `json:"claims"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&root); err != nil {
		return nil, validation("page Claim snapshot is invalid")
	}
	if decoder.Decode(&struct{}{}) == nil {
		return nil, validation("page Claim snapshot is invalid")
	}
	values := make([]snapshotClaim, len(root.Claims))
	seen := map[string]struct{}{}
	for i, claim := range root.Claims {
		if !claimID.MatchString(claim.ID) || claim.Statement == "" {
			return nil, validation("page Claim snapshot is invalid")
		}
		if _, exists := seen[claim.ID]; exists {
			return nil, validation("page Claim snapshot is invalid")
		}
		seen[claim.ID] = struct{}{}
		values[i] = snapshotClaim{ID: claim.ID, Statement: claim.Statement, Evidence: make([]snapshotEvidence, len(claim.Evidence))}
		evidenceIDs := map[string]struct{}{}
		for j, item := range claim.Evidence {
			if item.ID == "" || len(item.ID) > 255 {
				return nil, validation("page evidence snapshot is invalid")
			}
			if _, exists := evidenceIDs[item.ID]; exists {
				return nil, validation("page evidence snapshot is invalid")
			}
			evidenceIDs[item.ID] = struct{}{}
			values[i].Evidence[j] = snapshotEvidence{item.ID, item.Resource, item.SourceRevisionID, item.SourceVersion}
		}
	}
	return values, nil
}

func (store *Store) insertClaimsTx(ctx context.Context, tx pgx.Tx, versionID WikiVersionID, pageID ID, raw []byte, captured map[ID]CapturedSource) error {
	claims, err := parseClaimsSnapshot(raw)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		claimIDValue, createErr := newID()
		if createErr != nil {
			return createErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO claims (id,wiki_version_id,wiki_page_id,stable_id,statement) VALUES ($1,$2,$3,$4,$5)`, pgUUID(claimIDValue), pgUUID(ID(versionID)), pgUUID(pageID), claim.ID, claim.Statement); err != nil {
			return err
		}
		for _, item := range claim.Evidence {
			resource, parseErr := ParseEvidenceResource(item.Resource)
			if parseErr != nil {
				return validation("page evidence snapshot is invalid")
			}
			revisionID, parseErr := ParseID(item.SourceRevisionID)
			if parseErr != nil {
				return validation("page evidence snapshot is invalid")
			}
			fingerprintBytes, parseErr := hex.DecodeString(item.SourceVersion)
			if parseErr != nil || len(fingerprintBytes) != sha256.Size {
				return validation("page evidence snapshot is invalid")
			}
			var fingerprint [sha256.Size]byte
			copy(fingerprint[:], fingerprintBytes)
			source, exists := captured[resource.SourceID]
			if !exists || source.RevisionID != revisionID || source.Fingerprint != fingerprint || source.Commit != resource.Commit {
				return validation("page evidence is outside the run capture")
			}
			pathValue, resolveErr := store.evidencePathTx(ctx, tx, source, resource, revisionID)
			if resolveErr != nil {
				return resolveErr
			}
			evidenceID, createErr := newID()
			if createErr != nil {
				return createErr
			}
			if _, err = tx.Exec(ctx, `INSERT INTO evidence (id,claim_id,evidence_id,source_id,source_revision_id,source_fingerprint,native_version,path,start_line,end_line,resource) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, pgUUID(evidenceID), pgUUID(claimIDValue), item.ID, pgUUID(resource.SourceID), pgUUID(revisionID), fingerprint[:], resource.Commit, pathValue, resource.StartLine, resource.EndLine, resource.Value()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (store *Store) evidencePathTx(ctx context.Context, tx pgx.Tx, source CapturedSource, resource EvidenceResource, revisionID ID) (string, error) {
	if source.Kind == "REPOSITORY" {
		if resource.Scheme != "repo" {
			return "", validation("page evidence scheme does not match its source")
		}
		return resource.Path, nil
	}
	if source.Kind != "WEBSITE" || resource.Scheme != "web" {
		return "", validation("page evidence scheme does not match its source")
	}
	base, err := NewEvidenceResource(resource.SourceID, resource.Commit, resource.Path, nil, nil, "web")
	if err != nil {
		return "", err
	}
	var contentPath string
	err = tx.QueryRow(ctx, `SELECT content_path FROM website_revision_pages WHERE source_id=$1 AND revision_id=$2 AND canonical_url=$3 AND evidence_uri=$4`, pgUUID(resource.SourceID), pgUUID(revisionID), resource.Path, base.Value()).Scan(&contentPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", validation("website evidence does not match a captured page")
	}
	if err != nil {
		return "", err
	}
	return NormalizeSourcePath(contentPath)
}

func (store *Store) ListWikiVersions(ctx context.Context, knowledgeBaseID ID) ([]WikiVersion, error) {
	if err := store.assertKnowledgeBase(ctx, knowledgeBaseID); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `SELECT id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count,created_at,published_at FROM wiki_versions WHERE knowledge_base_id=$1 AND NOT EXISTS(SELECT 1 FROM artifact_deletion_intents i WHERE i.kind='WIKI_VERSION' AND i.resource_id=wiki_versions.id) ORDER BY published_at DESC,id`, pgUUID(knowledgeBaseID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []WikiVersion{}
	for rows.Next() {
		value, scanErr := scanWikiVersion(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanWikiVersion(row scanner) (WikiVersion, error) {
	var id, kbID, runID pgtype.UUID
	var value WikiVersion
	var digest []byte
	err := row.Scan(&id, &kbID, &runID, &value.ArtifactKey, &digest, &value.PageCount, &value.CreatedAt, &value.PublishedAt)
	if err != nil {
		return WikiVersion{}, err
	}
	if len(digest) != sha256.Size || value.PageCount <= 0 {
		return WikiVersion{}, errors.New("stored wiki version metadata is invalid")
	}
	value.ID, value.KnowledgeBaseID, value.DocumentationRunID = WikiVersionID(id.Bytes), ID(kbID.Bytes), RunID(runID.Bytes)
	copy(value.ManifestSHA256[:], digest)
	if err = value.Validate(); err != nil {
		return WikiVersion{}, fmt.Errorf("stored wiki version is invalid: %w", err)
	}
	return value, nil
}

func (store *Store) GetWiki(ctx context.Context, knowledgeBaseID ID, versionID *WikiVersionID, slug *string) (WikiView, error) {
	var normalized *string
	if slug != nil {
		value, err := NormalizePageSlug(*slug)
		if err != nil {
			return WikiView{}, err
		}
		normalized = &value
	}
	version, err := store.selectedWikiVersion(ctx, knowledgeBaseID, versionID)
	if err != nil {
		return WikiView{}, err
	}
	rows, err := store.pool.Query(ctx, `SELECT id,slug,title,description,page_type,content_sha256,claims_sha256,body FROM wiki_pages WHERE wiki_version_id=$1 ORDER BY slug`, pgUUID(ID(version.ID)))
	if err != nil {
		return WikiView{}, err
	}
	defer rows.Close()
	type wikiRow struct {
		id              ID
		summary         WikiPageSummary
		content, claims [32]byte
		body            string
	}
	selectedRows := []wikiRow{}
	view := WikiView{Version: version, Pages: []WikiPageSummary{}}
	for rows.Next() {
		var id pgtype.UUID
		var row wikiRow
		var content, claims []byte
		if err = rows.Scan(&id, &row.summary.Slug, &row.summary.Title, &row.summary.Description, &row.summary.PageType, &content, &claims, &row.body); err != nil {
			return WikiView{}, err
		}
		if len(content) != 32 || len(claims) != 32 {
			return WikiView{}, errors.New("stored published page hashes are invalid")
		}
		row.id = ID(id.Bytes)
		copy(row.content[:], content)
		copy(row.claims[:], claims)
		if err = row.summary.Validate(); err != nil {
			return WikiView{}, fmt.Errorf("stored wiki page summary is invalid: %w", err)
		}
		view.Pages = append(view.Pages, row.summary)
		selectedRows = append(selectedRows, row)
	}
	if err = rows.Err(); err != nil {
		return WikiView{}, err
	}
	if normalized == nil {
		return view, nil
	}
	var chosen *wikiRow
	for index := range selectedRows {
		if selectedRows[index].summary.Slug == *normalized {
			chosen = &selectedRows[index]
			break
		}
	}
	if chosen == nil {
		return WikiView{}, notFound("wiki page does not exist")
	}
	page := &PublishedWikiPage{Summary: chosen.summary, Markdown: chosen.body, ContentSHA256: chosen.content, ClaimsSHA256: chosen.claims, Claims: []PublishedClaim{}}
	claimRows, err := store.pool.Query(ctx, `SELECT id,stable_id,statement FROM claims WHERE wiki_page_id=$1 ORDER BY stable_id`, pgUUID(chosen.id))
	if err != nil {
		return WikiView{}, err
	}
	defer claimRows.Close()
	for claimRows.Next() {
		var claimID pgtype.UUID
		var claim PublishedClaim
		if err = claimRows.Scan(&claimID, &claim.ID, &claim.Statement); err != nil {
			return WikiView{}, err
		}
		evidenceRows, queryErr := store.pool.Query(ctx, `SELECT evidence_id,source_id,source_revision_id,source_fingerprint,native_version,path,start_line,end_line,resource FROM evidence WHERE claim_id=$1 ORDER BY evidence_id`, claimID)
		if queryErr != nil {
			return WikiView{}, queryErr
		}
		for evidenceRows.Next() {
			var item PublishedEvidence
			var sourceID, revisionID pgtype.UUID
			var fingerprint []byte
			var start, end pgtype.Int4
			var resource string
			if queryErr = evidenceRows.Scan(&item.ID, &sourceID, &revisionID, &fingerprint, &item.Location.Commit, &item.Location.Path, &start, &end, &resource); queryErr != nil {
				evidenceRows.Close()
				return WikiView{}, queryErr
			}
			item.Location.SourceID, item.Location.SourceRevisionID = ID(sourceID.Bytes), ID(revisionID.Bytes)
			if len(fingerprint) != 32 {
				evidenceRows.Close()
				return WikiView{}, errors.New("stored evidence fingerprint is invalid")
			}
			copy(item.Location.SourceVersion[:], fingerprint)
			if start.Valid {
				s, e := int(start.Int32), int(end.Int32)
				item.Location.StartLine, item.Location.EndLine = &s, &e
			}
			if strings.HasPrefix(resource, "web://") {
				item.Location.ResourceURI = &resource
			}
			claim.Evidence = append(claim.Evidence, item)
		}
		if queryErr = evidenceRows.Err(); queryErr != nil {
			evidenceRows.Close()
			return WikiView{}, queryErr
		}
		evidenceRows.Close()
		page.Claims = append(page.Claims, claim)
	}
	if err = claimRows.Err(); err != nil {
		return WikiView{}, err
	}
	if err = page.Validate(); err != nil {
		return WikiView{}, fmt.Errorf("stored published wiki page is invalid: %w", err)
	}
	view.Page = page
	return view, nil
}

func (store *Store) ExportWiki(ctx context.Context, knowledgeBaseID ID, versionID *WikiVersionID) ([]byte, error) {
	version, err := store.selectedWikiVersion(ctx, knowledgeBaseID, versionID)
	if err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp("", "ref0-wiki-export-*.zip")
	if err != nil {
		return nil, err
	}
	name := temporary.Name()
	if err = temporary.Close(); err != nil {
		_ = os.Remove(name)
		return nil, err
	}
	defer os.Remove(name)
	if err = store.wikiArtifacts.ExportZIP(artifacts.ID(knowledgeBaseID), artifacts.ID(version.ID), name); err != nil {
		return nil, err
	}
	return os.ReadFile(name)
}

func (store *Store) SearchWiki(ctx context.Context, knowledgeBaseID ID, query string, limit int) ([]WikiSearchHit, error) {
	normalized := strings.TrimSpace(query)
	if normalized == "" || len([]rune(normalized)) > 1000 || limit < 1 || limit > 50 {
		return nil, errors.New("wiki search request is invalid")
	}
	version, err := store.selectedWikiVersion(ctx, knowledgeBaseID, nil)
	if err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `WITH combined AS (SELECT p.slug,p.title,NULL::text AS statement,ts_rank_cd(p.search_vector,plainto_tsquery('simple',$2)) AS rank FROM wiki_pages p WHERE p.wiki_version_id=$1 AND p.search_vector @@ plainto_tsquery('simple',$2) UNION ALL SELECT p.slug,p.title,c.statement,ts_rank_cd(c.search_vector,plainto_tsquery('simple',$2)) AS rank FROM claims c JOIN wiki_pages p ON p.id=c.wiki_page_id WHERE c.wiki_version_id=$1 AND c.search_vector @@ plainto_tsquery('simple',$2)) SELECT slug,title,statement,rank FROM combined ORDER BY rank DESC,slug LIMIT $3`, pgUUID(ID(version.ID)), normalized, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []WikiSearchHit{}
	for rows.Next() {
		var value WikiSearchHit
		var statement pgtype.Text
		if err = rows.Scan(&value.Slug, &value.Title, &statement, &value.Rank); err != nil {
			return nil, err
		}
		if statement.Valid {
			s := statement.String
			value.Statement = &s
		}
		if err = value.Validate(); err != nil {
			return nil, fmt.Errorf("stored wiki search hit is invalid: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *Store) selectedWikiVersion(ctx context.Context, knowledgeBaseID ID, selected *WikiVersionID) (WikiVersion, error) {
	var versionID pgtype.UUID
	if selected == nil {
		if err := store.pool.QueryRow(ctx, `SELECT published_wiki_id FROM knowledge_bases WHERE id=$1 AND lifecycle <> 'DELETED'`, pgUUID(knowledgeBaseID)).Scan(&versionID); errors.Is(err, pgx.ErrNoRows) {
			return WikiVersion{}, notFound("knowledge base does not exist")
		} else if err != nil {
			return WikiVersion{}, err
		}
		if !versionID.Valid {
			return WikiVersion{}, notFound("knowledge base has no published wiki")
		}
	} else {
		versionID = pgUUID(ID(*selected))
	}
	value, err := scanWikiVersion(store.pool.QueryRow(ctx, `SELECT id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count,created_at,published_at FROM wiki_versions WHERE id=$1 AND knowledge_base_id=$2 AND NOT EXISTS(SELECT 1 FROM artifact_deletion_intents i WHERE i.kind='WIKI_VERSION' AND i.resource_id=wiki_versions.id)`, versionID, pgUUID(knowledgeBaseID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return WikiVersion{}, notFound("wiki version does not exist")
	}
	return value, err
}

func (store *Store) assertKnowledgeBase(ctx context.Context, id ID) error {
	var exists bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_bases WHERE id=$1 AND lifecycle <> 'DELETED')`, pgUUID(id)).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFound("knowledge base does not exist")
	}
	return nil
}

func (store *Store) retainedWikiHealthyTx(ctx context.Context, tx pgx.Tx, run Run, versionID WikiVersionID) (bool, error) {
	var artifactKey string
	var manifest []byte
	var pageCount int
	if err := tx.QueryRow(ctx, `SELECT artifact_key,manifest_sha256,page_count FROM wiki_versions WHERE id=$1 AND knowledge_base_id=$2 AND documentation_run_id=$3 AND NOT EXISTS(SELECT 1 FROM artifact_deletion_intents i WHERE i.kind='WIKI_VERSION' AND i.resource_id=wiki_versions.id)`, pgUUID(ID(versionID)), pgUUID(run.KnowledgeBaseID), pgUUID(ID(run.ID))).Scan(&artifactKey, &manifest, &pageCount); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if artifactKey != artifacts.WikiArtifactKey(artifacts.ID(run.KnowledgeBaseID), artifacts.ID(versionID)) || len(manifest) != 32 {
		return false, nil
	}
	rows, err := tx.Query(ctx, `SELECT id,slug,title,description,page_type,content_sha256,claims_sha256,body FROM wiki_pages WHERE wiki_version_id=$1 ORDER BY slug`, pgUUID(ID(versionID)))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	pages := []artifacts.Page{}
	pageIDs := []ID{}
	for rows.Next() {
		var pageID pgtype.UUID
		var page artifacts.Page
		var content, claims []byte
		if err = rows.Scan(&pageID, &page.Slug, &page.Title, &page.Description, &page.PageType, &content, &claims, &page.Markdown); err != nil {
			return false, err
		}
		if len(content) != 32 || len(claims) != 32 || sha256.Sum256([]byte(page.Markdown)) != toDigest(content) {
			return false, nil
		}
		raw, readErr := store.wikiArtifacts.ReadClaims(artifacts.ID(run.KnowledgeBaseID), artifacts.ID(versionID), page.Slug)
		if readErr != nil || sha256.Sum256(raw) != toDigest(claims) {
			return false, nil
		}
		copy(page.ContentSHA256[:], content)
		copy(page.ClaimsSHA256[:], claims)
		page.ClaimsJSON = raw
		pages = append(pages, page)
		pageIDs = append(pageIDs, ID(pageID.Bytes))
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	rows.Close()
	for index, page := range pages {
		matches, err := store.claimsMatchTx(ctx, tx, pageIDs[index], page.ClaimsJSON)
		if err != nil || !matches {
			return false, err
		}
	}
	if len(pages) == 0 || len(pages) != pageCount {
		return false, nil
	}
	revisions := make([]artifacts.SourceRevision, len(run.Sources))
	for index, source := range run.Sources {
		revisions[index] = artifacts.SourceRevision{"source_id": source.SourceID.String(), "revision_id": source.RevisionID.String(), "fingerprint": hex.EncodeToString(source.Fingerprint[:]), "commit": source.Commit}
	}
	bundle, validateErr := store.wikiArtifacts.Validate(artifacts.ID(run.KnowledgeBaseID), artifacts.ID(run.ID), artifacts.ID(versionID), pages, revisions, manifest)
	if validateErr != nil {
		return false, nil
	}
	return bundle.PageCount == pageCount, nil
}

func (store *Store) claimsMatchTx(ctx context.Context, tx pgx.Tx, pageID ID, raw []byte) (bool, error) {
	claims, err := parseClaimsSnapshot(raw)
	if err != nil {
		return false, nil
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM claims WHERE wiki_page_id=$1`, pgUUID(pageID)).Scan(&count); err != nil {
		return false, err
	}
	if count != len(claims) {
		return false, nil
	}
	for _, claim := range claims {
		var dbID pgtype.UUID
		var statement string
		if err = tx.QueryRow(ctx, `SELECT id,statement FROM claims WHERE wiki_page_id=$1 AND stable_id=$2`, pgUUID(pageID), claim.ID).Scan(&dbID, &statement); errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		if statement != claim.Statement {
			return false, nil
		}
		var evidenceCount int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM evidence WHERE claim_id=$1`, dbID).Scan(&evidenceCount); err != nil {
			return false, err
		}
		if evidenceCount != len(claim.Evidence) {
			return false, nil
		}
		for _, item := range claim.Evidence {
			var resource, revision, version string
			if err = tx.QueryRow(ctx, `SELECT resource,source_revision_id::text,encode(source_fingerprint,'hex') FROM evidence WHERE claim_id=$1 AND evidence_id=$2`, dbID, item.ID).Scan(&resource, &revision, &version); errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			} else if err != nil {
				return false, err
			}
			if resource != item.Resource || revision != item.SourceRevisionID || version != item.SourceVersion {
				return false, nil
			}
		}
	}
	return true, nil
}

func toDigest(value []byte) [32]byte { var digest [32]byte; copy(digest[:], value); return digest }

// TerminalCallback closes documentation state when a durable job can no longer
// be retried. It runs inside the job's terminal transaction.
func TerminalCallback(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	switch job.Type {
	case jobs.PrepareRun, jobs.PlanRun, jobs.FinalizeRun:
		return terminateRunJob(ctx, tx, job)
	case jobs.GeneratePage:
		return terminatePageJob(ctx, tx, job)
	default:
		return nil
	}
}

func terminateRunJob(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	var status RunStatus
	var sanitized string
	switch job.Status {
	case jobs.Failed:
		status = RunFailed
		switch job.Type {
		case jobs.PrepareRun:
			sanitized = "documentation:preparation_failed"
		case jobs.PlanRun:
			sanitized = "documentation:planning_failed"
		case jobs.FinalizeRun:
			sanitized = "documentation:publication_failed"
		}
	case jobs.Cancelled:
		status, sanitized = RunInterrupted, "documentation:cancelled"
	default:
		return nil
	}
	var runID pgtype.UUID
	var err error
	if job.Type == jobs.PrepareRun {
		if job.TargetType != "knowledge_base" {
			return errors.New("documentation preparation job target is invalid")
		}
		err = tx.QueryRow(ctx, `
			SELECT id FROM documentation_runs
			WHERE prepare_job_id=$1 AND knowledge_base_id=$2 AND status='PREPARING'
			FOR UPDATE
		`, pgUUID(ID(job.ID)), pgUUID(ID(job.TargetID))).Scan(&runID)
	} else {
		if job.TargetType != "documentation_run" {
			return errors.New("documentation run job target is invalid")
		}
		expected := string(RunPlanning)
		if job.Type == jobs.FinalizeRun {
			expected = string(RunFinalizing)
		}
		err = tx.QueryRow(ctx, `
			SELECT id FROM documentation_runs WHERE id=$1 AND status=$2 FOR UPDATE
		`, pgUUID(ID(job.TargetID)), expected).Scan(&runID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	now := job.UpdatedAt
	if job.FinishedAt != nil {
		now = *job.FinishedAt
	}
	pageError := "documentation_page:run_failed"
	if job.Status == jobs.Cancelled {
		pageError = "documentation_page:cancelled"
	}
	if _, err = tx.Exec(ctx, `
		UPDATE documentation_pages
		SET status='SKIPPED', sanitized_error=$2, updated_at=$3, completed_at=$3
		WHERE run_id=$1 AND status IN ('PENDING','RUNNING')
	`, runID, pageError, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE documentation_runs SET status=$2,sanitized_error=$3,updated_at=$4,completed_at=$4 WHERE id=$1`, runID, string(status), sanitized, now); err != nil {
		return err
	}
	detail, err := detailTx(ctx, tx, RunID(runID.Bytes), false)
	if err != nil {
		return err
	}
	return recordRun(ctx, tx, detail, nil, "documentation_run."+strings.ToLower(string(status)))
}

func terminatePageJob(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	if job.TargetType != "documentation_page" {
		return errors.New("documentation page job target is invalid")
	}
	var pageID, runID pgtype.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id,run_id FROM documentation_pages WHERE id=$1 AND job_id=$2
	`, pgUUID(ID(job.TargetID)), pgUUID(ID(job.ID))).Scan(&pageID, &runID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var runStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM documentation_runs WHERE id=$1 FOR UPDATE`, runID).Scan(&runStatus); err != nil {
		return err
	}
	var pageStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM documentation_pages WHERE id=$1 FOR UPDATE`, pageID).Scan(&pageStatus); err != nil {
		return err
	}
	if pageStatus == string(PageComplete) || pageStatus == string(PageSkipped) {
		return nil
	}
	now := job.UpdatedAt
	if job.FinishedAt != nil {
		now = *job.FinishedAt
	}
	pageError := "documentation_page:job_failed"
	runError := "documentation:page_skipped"
	if job.Status == jobs.Cancelled {
		pageError = "documentation_page:cancelled"
		runError = "documentation:cancelled"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE documentation_pages
		SET status='SKIPPED',
		    sanitized_error=CASE WHEN id=$2 THEN $3 ELSE 'documentation_page:run_interrupted' END,
		    updated_at=$4, completed_at=$4
		WHERE run_id=$1 AND status IN ('PENDING','RUNNING')
	`, runID, pageID, pageError, now); err != nil {
		return err
	}
	status := RunStatus(runStatus)
	if !status.Terminal() {
		if _, err := tx.Exec(ctx, `
			UPDATE documentation_runs
			SET status='INTERRUPTED', sanitized_error=$2, updated_at=$3, completed_at=$3
			WHERE id=$1
		`, runID, runError, now); err != nil {
			return err
		}
	}
	detail, err := detailTx(ctx, tx, RunID(runID.Bytes), false)
	if err != nil {
		return err
	}
	return recordRun(ctx, tx, detail, nil, "documentation_run.interrupted")
}
