package sources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (store *Store) ListSyncs(ctx context.Context, sourceID ID) ([]Sync, error) {
	if _, err := store.Get(ctx, sourceID); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, syncSelect+" WHERE source_id=$1 ORDER BY created_at,id", pgUUID(sourceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Sync{}
	for rows.Next() {
		value, err := scanSync(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *Store) ListRevisions(ctx context.Context, sourceID ID) ([]Revision, error) {
	source, err := store.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		SELECT id,source_id,observed_ref_kind,observed_ref,native_version,
		       fingerprint,artifact_key,file_count,byte_count,ignored_paths,created_at
		FROM source_revisions
		WHERE source_id=$1 AND artifact_purged_at IS NULL
		  AND NOT EXISTS(
			SELECT 1 FROM artifact_deletion_intents i
			WHERE i.kind='SOURCE_SNAPSHOT' AND i.resource_id=source_revisions.id
		  )
		ORDER BY created_at,id
	`, pgUUID(sourceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Revision{}
	byID := map[ID]int{}
	for rows.Next() {
		value, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		byID[value.ID] = len(result)
		result = append(result, value)
	}
	if err := rows.Err(); err != nil || source.Kind != Website || len(result) == 0 {
		return result, err
	}
	pageRows, err := store.pool.Query(ctx, `
		SELECT revision_id,canonical_url,content_path,content_sha256,evidence_uri,
		       freshness,etag,last_modified,reused_from_revision_id
		FROM website_revision_pages
		WHERE source_id=$1
		ORDER BY revision_id,canonical_url
	`, pgUUID(sourceID))
	if err != nil {
		return nil, err
	}
	defer pageRows.Close()
	for pageRows.Next() {
		var revisionID, reused pgtype.UUID
		var canonicalURL, contentPath, evidenceURI, freshness string
		var digest []byte
		var etag, lastModified pgtype.Text
		if err := pageRows.Scan(&revisionID, &canonicalURL, &contentPath, &digest, &evidenceURI, &freshness, &etag, &lastModified, &reused); err != nil {
			return nil, err
		}
		index, exists := byID[ID(revisionID.Bytes)]
		if !exists {
			continue
		}
		if len(digest) != sha256.Size {
			return nil, errors.New("stored website page digest is invalid")
		}
		var contentDigest [sha256.Size]byte
		copy(contentDigest[:], digest)
		result[index].WebsitePages = append(result[index].WebsitePages, PageCapture{
			CanonicalURL: canonicalURL, ContentPath: contentPath, ContentSHA256: contentDigest,
			EvidenceURI: evidenceURI, Freshness: freshness, ETag: stringPointer(etag),
			LastModified: stringPointer(lastModified), ReusedFromRevisionID: idPointer(reused),
		})
	}
	return result, pageRows.Err()
}

func (store *Store) ScheduleDue(ctx context.Context, limit int) ([]Sync, error) {
	if limit < 1 || limit > 50 {
		return nil, errors.New("source poll batch limit must be between 1 and 50")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT s.id
		FROM sources s
		LEFT JOIN repository_sources r ON r.source_id=s.id
		LEFT JOIN website_sources w ON w.source_id=s.id
		WHERE s.lifecycle='ACTIVE'
		  AND coalesce(r.poll_interval_seconds,w.poll_interval_seconds) IS NOT NULL
		ORDER BY s.updated_at,s.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	ids := []ID{}
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, ID(id.Bytes))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	result := []Sync{}
	for _, id := range ids {
		var scheduled *Sync
		err := store.transaction(ctx, func(tx pgx.Tx) error {
			value, err := store.scheduleDueOne(ctx, tx, id)
			if err != nil {
				return err
			}
			scheduled = value
			return nil
		})
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if scheduled != nil {
			result = append(result, *scheduled)
		}
	}
	return result, nil
}

func (store *Store) scheduleDueOne(ctx context.Context, tx pgx.Tx, id ID) (*Sync, error) {
	value, err := lockedSourceWithKnowledgeBase(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	var interval *int
	if value.Repository != nil {
		interval = value.Repository.PollIntervalSeconds
	} else {
		interval = value.Website.PollIntervalSeconds
	}
	if value.Lifecycle != Active || interval == nil {
		return nil, nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM source_syncs ss JOIN jobs j ON j.id=ss.job_id
			WHERE ss.source_id=$1 AND ss.sync_kind='SYNC'
			  AND (ss.status IN ('PENDING','RUNNING')
			       OR j.status IN ('PENDING','LEASED','RETRY_WAIT','CANCEL_REQUESTED'))
		)
	`, pgUUID(id)).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, nil
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return nil, err
	}
	var last pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `SELECT max(created_at) FROM source_syncs WHERE source_id=$1 AND sync_kind='SYNC'`, pgUUID(id)).Scan(&last); err != nil {
		return nil, err
	}
	if last.Valid && last.Time.After(now.Add(-time.Duration(*interval)*time.Second)) {
		return nil, nil
	}
	if err := knowledgeBasePolicy(ctx, tx, value.KnowledgeBaseID, value.Privacy, false); err != nil {
		return nil, err
	}
	credentialID, credentialKind := sourceCredential(value)
	sourceCredentialVersion, err := credentialVersion(ctx, tx, credentialID, credentialKind, true)
	if err != nil {
		return nil, err
	}
	var tinyVersion *int
	if value.Website != nil && value.Website.AcquisitionMode == TinyFishCrawl {
		tinyVersion, err = credentialVersion(ctx, tx, value.Website.TinyFishCredentialID, security.CredentialTinyFishAPIKey, true)
		if err != nil {
			return nil, err
		}
	}
	sync, err := store.scheduleOnce(ctx, tx, value, sourceCredentialVersion, tinyVersion, nil, Synchronization, now)
	if err != nil {
		return nil, err
	}
	return &sync, nil
}

func (store *Store) Begin(ctx context.Context, syncID ID, permit jobs.Permit) (Sync, error) {
	var result Sync
	err := store.transaction(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		value, err := getSync(ctx, tx, syncID, true)
		if err != nil {
			return err
		}
		if err := assertCapturePermit(ctx, tx, value, permit); err != nil {
			return err
		}
		if value.Status != SyncPending && value.Status != SyncFailed {
			result = value
			return nil
		}
		now, err := databaseTime(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE source_syncs SET status='RUNNING',result_revision_id=NULL,
			resolved_native_version=NULL,sanitized_error=NULL,started_at=$2,completed_at=NULL
			WHERE id=$1
		`, pgUUID(syncID), now); err != nil {
			return err
		}
		result, err = getSync(ctx, tx, syncID, false)
		if err != nil {
			return err
		}
		return recordSync(ctx, tx, result, nil, "source_sync.running")
	})
	return result, err
}

func (store *Store) CompleteValidation(ctx context.Context, completion ValidationCompletion, permit jobs.Permit) (Sync, error) {
	if err := validateOutcome(completion.ResolvedNativeVersion != nil, completion.SanitizedError, completion.Retryable); err != nil {
		return Sync{}, err
	}
	if completion.ResolvedNativeVersion != nil {
		value := strings.ToLower(*completion.ResolvedNativeVersion)
		completion.ResolvedNativeVersion = &value
	}
	return store.complete(ctx, completion.SyncID, permit, Validation, completion.ResolvedNativeVersion, nil, completion.SanitizedError)
}

func (store *Store) CompleteSync(ctx context.Context, completion SyncCompletion, permit jobs.Permit) (Sync, error) {
	if err := validateOutcome(completion.Revision != nil, completion.SanitizedError, completion.Retryable); err != nil {
		return Sync{}, err
	}
	return store.complete(ctx, completion.SyncID, permit, Synchronization, nil, completion.Revision, completion.SanitizedError)
}

func (store *Store) complete(ctx context.Context, syncID ID, permit jobs.Permit, expectedKind SyncKind, resolved *string, revision *RevisionCandidate, sanitized *string) (Sync, error) {
	var result Sync
	err := store.transaction(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		run, err := getSync(ctx, tx, syncID, true)
		if err != nil {
			return err
		}
		if err := assertCapturePermit(ctx, tx, run, permit); err != nil {
			return err
		}
		if run.Kind != expectedKind {
			return conflict("source capture kind is invalid")
		}
		if run.Status == SyncSucceeded || run.Status == SyncFailed || run.Status == SyncSuperseded {
			result = run
			return nil
		}
		if run.Status != SyncRunning {
			return conflict("source capture has not started")
		}
		source, err := getSource(ctx, tx, run.SourceID, true)
		if err != nil {
			return err
		}
		if expectedKind == Validation && resolved != nil {
			if err := validateNativeVersion(source.Kind, *resolved); err != nil {
				return err
			}
		}
		if revision != nil {
			if err := validateCandidate(source.Kind, *revision); err != nil {
				return err
			}
		}
		stale, err := store.captureStale(ctx, tx, source, run, expectedKind)
		if err != nil {
			return err
		}
		now, err := databaseTime(ctx, tx)
		if err != nil {
			return err
		}
		if stale {
			if _, err := tx.Exec(ctx, `UPDATE source_syncs SET status='SUPERSEDED',result_revision_id=NULL,resolved_native_version=NULL,sanitized_error=NULL,completed_at=$2 WHERE id=$1`, pgUUID(syncID), now); err != nil {
				return err
			}
			result, err = getSync(ctx, tx, syncID, false)
			if err != nil {
				return err
			}
			return recordSync(ctx, tx, result, nil, "source_sync.superseded")
		}
		if sanitized != nil {
			if _, err := tx.Exec(ctx, `UPDATE source_syncs SET status='FAILED',resolved_native_version=NULL,result_revision_id=NULL,sanitized_error=$2,completed_at=$3 WHERE id=$1`, pgUUID(syncID), *sanitized, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE sources SET health='UNHEALTHY',sanitized_error=$2,checked_at=$3,updated_at=$3,version=version+1 WHERE id=$1`, pgUUID(source.ID), *sanitized, now); err != nil {
				return err
			}
			updated, err := getSource(ctx, tx, source.ID, false)
			if err != nil {
				return err
			}
			if err := recordSource(ctx, tx, updated, nil, "source_sync.failed", "source.health_updated"); err != nil {
				return err
			}
			result, err = getSync(ctx, tx, syncID, false)
			if err != nil {
				return err
			}
			return recordSync(ctx, tx, result, nil, "source_sync.failed")
		}

		var resultRevisionID *ID
		if expectedKind == Validation {
			lifecycle := source.Lifecycle
			if lifecycle == Draft {
				lifecycle = Active
			}
			if _, err := tx.Exec(ctx, `
				UPDATE sources SET lifecycle=$2::varchar,health='HEALTHY',sanitized_error=NULL,
				checked_at=$3,validated_configuration_version=configuration_version,
				disabled_at=CASE WHEN $2::varchar='DISABLED' THEN disabled_at ELSE NULL END,
				updated_at=$3,version=version+1 WHERE id=$1
			`, pgUUID(source.ID), lifecycle, now); err != nil {
				return err
			}
		} else {
			if run.CandidateRevisionID == nil || revision == nil {
				return conflict("source revision candidate is invalid")
			}
			if revision.ArtifactKey != ArtifactKey(source.ID, *run.CandidateRevisionID) {
				return conflict("source revision artifact key is invalid")
			}
			inserted, created, err := insertOrReuseRevision(ctx, tx, source, *run.CandidateRevisionID, *revision, now)
			if err != nil {
				return err
			}
			resultRevisionID = &inserted
			if _, err := tx.Exec(ctx, `UPDATE sources SET current_revision_id=$2,health='HEALTHY',sanitized_error=NULL,checked_at=$3,updated_at=$3,version=version+1 WHERE id=$1`, pgUUID(source.ID), pgUUID(inserted), now); err != nil {
				return err
			}
			if created {
				createdRevision, err := getRevision(ctx, tx, inserted)
				if err != nil {
					return err
				}
				if err := recordRevision(ctx, tx, createdRevision); err != nil {
					return err
				}
			}
			value := strings.ToLower(revision.NativeVersion)
			resolved = &value
		}
		if _, err := tx.Exec(ctx, `UPDATE source_syncs SET status='SUCCEEDED',result_revision_id=$2,resolved_native_version=$3,sanitized_error=NULL,completed_at=$4 WHERE id=$1`, pgUUID(syncID), nullableUUID(resultRevisionID), resolved, now); err != nil {
			return err
		}
		updated, err := getSource(ctx, tx, source.ID, false)
		if err != nil {
			return err
		}
		if err := recordSource(ctx, tx, updated, nil, "source_sync.complete", "source.health_updated"); err != nil {
			return err
		}
		result, err = getSync(ctx, tx, syncID, false)
		if err != nil {
			return err
		}
		return recordSync(ctx, tx, result, nil, "source_sync.succeeded")
	})
	return result, translateUnique(err)
}

func (store *Store) captureStale(ctx context.Context, tx pgx.Tx, source Source, run Sync, expectedKind SyncKind) (bool, error) {
	credentialID, kind := sourceCredential(source)
	observedVersion, err := credentialVersion(ctx, tx, credentialID, kind, false)
	if err != nil {
		return false, err
	}
	var capturedID *ID
	var capturedVersion *int
	if run.Repository != nil {
		capturedID, capturedVersion = run.Repository.CredentialID, run.Repository.CredentialVersion
	} else {
		capturedID, capturedVersion = run.Website.CredentialID, run.Website.CredentialVersion
	}
	stale := source.ConfigurationVersion != run.CapturedConfigurationVersion ||
		!equalID(credentialID, capturedID) || !equalInt(observedVersion, capturedVersion) ||
		source.Lifecycle == Removed || expectedKind == Synchronization && source.Lifecycle != Active
	if source.Website != nil {
		if run.Website == nil || source.Website.AcquisitionMode != run.Website.AcquisitionMode || !equalID(source.Website.TinyFishCredentialID, run.Website.TinyFishCredentialID) {
			stale = true
		} else {
			var tinyVersion *int
			if source.Website.AcquisitionMode == TinyFishCrawl {
				tinyVersion, err = credentialVersion(ctx, tx, source.Website.TinyFishCredentialID, security.CredentialTinyFishAPIKey, false)
				if err != nil {
					return false, err
				}
			}
			if !equalInt(tinyVersion, run.Website.TinyFishCredentialVersion) {
				stale = true
			}
		}
	} else if run.Repository == nil {
		stale = true
	}
	return stale, nil
}

func assertCapturePermit(ctx context.Context, tx pgx.Tx, run Sync, permit jobs.Permit) error {
	if run.JobID != permit.JobID {
		return conflict("work permit does not belong to source capture")
	}
	var jobType, targetType, operationKeyValue string
	var target pgtype.UUID
	var payload []byte
	if err := tx.QueryRow(ctx, `SELECT job_type,target_type,target_id,payload,operation_key FROM jobs WHERE id=$1`, pgUUID(ID(permit.JobID))).Scan(&jobType, &targetType, &target, &payload, &operationKeyValue); err != nil {
		return err
	}
	expectedType := string(jobs.ValidateSource)
	if run.Kind == Synchronization {
		expectedType = string(jobs.SyncSource)
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return conflict("source capture job target is invalid")
	}
	if jobType != expectedType || targetType != "source" || ID(target.Bytes) != run.SourceID ||
		operationKeyValue != capturedOperationKey(run) || len(parsed) != 1 || parsed["source_sync_id"] != run.ID.String() {
		return conflict("source capture job target is invalid")
	}
	return nil
}

func capturedOperationKey(run Sync) string {
	prefix := "validate-source"
	if run.Kind == Synchronization {
		prefix = "sync-source"
	}
	var credentialVersion *int
	if run.Repository != nil {
		credentialVersion = run.Repository.CredentialVersion
	} else {
		credentialVersion = run.Website.CredentialVersion
	}
	credential := "public"
	if credentialVersion != nil {
		credential = fmt.Sprint(*credentialVersion)
	}
	result := fmt.Sprintf("%s:%s:%d:%s", prefix, run.SourceID.String(), run.CapturedConfigurationVersion, credential)
	if run.Website != nil {
		tiny := "public"
		if run.Website.TinyFishCredentialVersion != nil {
			tiny = fmt.Sprint(*run.Website.TinyFishCredentialVersion)
		}
		result += ":" + strings.ToLower(string(run.Website.AcquisitionMode)) + ":" + tiny
	}
	return result
}

func insertOrReuseRevision(ctx context.Context, tx pgx.Tx, source Source, candidateID ID, candidate RevisionCandidate, now time.Time) (ID, bool, error) {
	observedKind, observed := Root, ""
	if source.Repository != nil {
		observedKind, observed = source.Repository.Reference.Kind, source.Repository.Reference.Value
	} else if source.Website != nil {
		observed = source.Website.Remote.URL
	} else {
		return ID{}, false, errors.New("source configuration is invalid")
	}
	ignored, err := normalizeSourcePaths(candidate.IgnoredPaths)
	if err != nil {
		return ID{}, false, err
	}
	if ignored == nil {
		ignored = []string{}
	}
	encodedIgnored, _ := json.Marshal(ignored)
	var inserted pgtype.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO source_revisions(id,source_id,observed_ref_kind,observed_ref,
			native_version,fingerprint,artifact_key,file_count,byte_count,ignored_paths,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11)
		ON CONFLICT DO NOTHING RETURNING id
	`, pgUUID(candidateID), pgUUID(source.ID), observedKind, observed, strings.ToLower(candidate.NativeVersion), candidate.Fingerprint[:], candidate.ArtifactKey, candidate.FileCount, candidate.ByteCount, encodedIgnored, now).Scan(&inserted)
	if err == nil {
		if source.Website != nil {
			for _, page := range candidate.WebsitePages {
				if err := validatePage(page); err != nil {
					return ID{}, false, err
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO website_revision_pages(source_id,revision_id,canonical_url,
						content_path,content_sha256,evidence_uri,freshness,etag,last_modified,reused_from_revision_id)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
				`, pgUUID(source.ID), pgUUID(candidateID), page.CanonicalURL, page.ContentPath,
					page.ContentSHA256[:], page.EvidenceURI, page.Freshness, page.ETag,
					page.LastModified, nullableUUID(page.ReusedFromRevisionID)); err != nil {
					return ID{}, false, err
				}
			}
		}
		return candidateID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ID{}, false, err
	}
	var existing pgtype.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM source_revisions
		WHERE source_id=$1 AND native_version=$2 AND fingerprint=$3
		  AND artifact_purged_at IS NULL
		  AND NOT EXISTS(
			SELECT 1 FROM artifact_deletion_intents i
			WHERE i.kind='SOURCE_SNAPSHOT' AND i.resource_id=source_revisions.id
		  )
	`, pgUUID(source.ID), strings.ToLower(candidate.NativeVersion), candidate.Fingerprint[:]).Scan(&existing); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ID{}, false, conflict("source revision artifact key already exists")
		}
		return ID{}, false, err
	}
	return ID(existing.Bytes), false, nil
}

func validateNativeVersion(kind Kind, value string) error {
	value = strings.ToLower(value)
	if kind == Repository && commitPattern.MatchString(value) {
		return nil
	}
	if kind == Website && websiteVersionPattern.MatchString(value) {
		return nil
	}
	return errors.New("resolved source version is invalid")
}

func validatePage(page PageCapture) error {
	parsed, err := url.Parse(page.CanonicalURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || len(page.CanonicalURL) > 4096 {
		return errors.New("website page URL is invalid")
	}
	if err := sourcePath(page.ContentPath); err != nil {
		return err
	}
	if page.Freshness != "fresh" && page.Freshness != "reused" || (page.Freshness == "reused") != (page.ReusedFromRevisionID != nil) {
		return errors.New("website page freshness is invalid")
	}
	if !strings.HasPrefix(page.EvidenceURI, "web://") {
		return errors.New("website evidence URI is invalid")
	}
	return nil
}

func sourcePath(value string) error {
	_, err := normalizeSourcePaths([]string{value})
	return err
}

func getRevision(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id ID) (Revision, error) {
	value, err := scanRevision(querier.QueryRow(ctx, `
		SELECT id,source_id,observed_ref_kind,observed_ref,native_version,
		       fingerprint,artifact_key,file_count,byte_count,ignored_paths,created_at
		FROM source_revisions WHERE id=$1
	`, pgUUID(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	return value, err
}

func scanRevision(row scanner) (Revision, error) {
	var id, sourceID pgtype.UUID
	var refKind, observed, native, artifact string
	var fingerprint, ignoredRaw []byte
	var fileCount int32
	var byteCount int64
	var created pgtype.Timestamptz
	if err := row.Scan(&id, &sourceID, &refKind, &observed, &native, &fingerprint, &artifact, &fileCount, &byteCount, &ignoredRaw, &created); err != nil {
		return Revision{}, err
	}
	if len(fingerprint) != sha256.Size || !created.Valid {
		return Revision{}, errors.New("stored source revision is invalid")
	}
	var digest [sha256.Size]byte
	copy(digest[:], fingerprint)
	var ignored []string
	if err := json.Unmarshal(ignoredRaw, &ignored); err != nil {
		return Revision{}, err
	}
	return Revision{ID: ID(id.Bytes), SourceID: ID(sourceID.Bytes), ObservedRef: Reference{Kind: RefKind(refKind), Value: observed}, NativeVersion: native, Fingerprint: digest, ArtifactKey: artifact, FileCount: int(fileCount), ByteCount: byteCount, IgnoredPaths: ignored, CreatedAt: created.Time}, nil
}

func equalID(left, right *ID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalInt(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
