package docgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type capturedSourceRow struct {
	SourceID, RevisionID ID
	ConfigurationVersion int
	Fingerprint          [sha256.Size]byte
	NativeVersion, Kind  string
}

type capturedModelRow struct {
	Role                                    string
	ProfileID, ProfileVersionID, EndpointID ID
	ProfileVersion, ConfigurationVersion    int
	CredentialVersion                       *int
	ReasoningEffort                         string
	MaxConcurrentTasks                      int
}

func (store *Store) newRunTx(ctx context.Context, tx pgx.Tx, knowledgeBaseID ID, expectedVersion int, actor *ID, operationKey string) (jobs.JobID, RunDetail, error) {
	var (
		storedID                          pgtype.UUID
		lifecycle, instructions, language string
		publishedWikiID                   pgtype.UUID
		version                           int
	)
	if err := tx.QueryRow(ctx, `SELECT id,lifecycle,instructions,language,published_wiki_id,version FROM knowledge_bases WHERE id=$1 AND lifecycle <> 'DELETED' FOR UPDATE`, pgUUID(knowledgeBaseID)).Scan(&storedID, &lifecycle, &instructions, &language, &publishedWikiID, &version); errors.Is(err, pgx.ErrNoRows) {
		return jobs.JobID{}, RunDetail{}, notFound("knowledge base does not exist")
	} else if err != nil {
		return jobs.JobID{}, RunDetail{}, err
	}
	if expectedVersion > 0 && version != expectedVersion {
		return jobs.JobID{}, RunDetail{}, conflict("knowledge base version is stale")
	}
	if lifecycle != "ACTIVE" {
		return jobs.JobID{}, RunDetail{}, conflict("knowledge base cannot be generated")
	}
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM documentation_runs
			WHERE knowledge_base_id=$1 AND status IN ('PREPARING','PLANNING','GENERATING','FINALIZING')
		)
	`, pgUUID(knowledgeBaseID)).Scan(&active); err != nil {
		return jobs.JobID{}, RunDetail{}, err
	}
	if active {
		return jobs.JobID{}, RunDetail{}, conflict("documentation run is already active")
	}

	sources, err := captureSourcesTx(ctx, tx, knowledgeBaseID)
	if err != nil {
		return jobs.JobID{}, RunDetail{}, err
	}
	if len(sources) == 0 {
		return jobs.JobID{}, RunDetail{}, conflict("all active sources require a current immutable revision")
	}
	models, err := captureModelsTx(ctx, tx, knowledgeBaseID)
	if err != nil {
		return jobs.JobID{}, RunDetail{}, err
	}
	if operationKey == "" {
		operationKey = fmt.Sprintf("prepare-run:%s:%d", knowledgeBaseID.String(), version)
	}
	rawRunID, err := newID()
	if err != nil {
		return jobs.JobID{}, RunDetail{}, err
	}
	runID := RunID(rawRunID)
	jobID, err := store.queue.EnqueueTx(ctx, tx, jobs.Command{Type: jobs.PrepareRun, TargetType: "knowledge_base", TargetID: jobs.UUID(knowledgeBaseID), Payload: map[string]any{"run_id": runID.String()}, OperationKey: operationKey, MaxAttempts: 3})
	if err != nil {
		return jobs.JobID{}, RunDetail{}, err
	}
	var existingID pgtype.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM documentation_runs WHERE prepare_job_id=$1`, pgUUID(ID(jobID))).Scan(&existingID)
	if err == nil {
		detail, detailErr := detailTx(ctx, tx, RunID(existingID.Bytes), false)
		return jobID, detail, detailErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return jobs.JobID{}, RunDetail{}, err
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return jobs.JobID{}, RunDetail{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO documentation_runs (id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,instructions,language,prior_wiki_version_id,created_at,updated_at) VALUES ($1,$2,'PREPARING',$3,$4,$5,$6,$7,$8,$8)`, pgUUID(rawRunID), pgUUID(knowledgeBaseID), pgUUID(ID(jobID)), version, instructions, language, publishedWikiID, now); err != nil {
		return jobs.JobID{}, RunDetail{}, err
	}
	for _, source := range sources {
		if _, err = tx.Exec(ctx, `INSERT INTO documentation_run_sources (run_id,source_id,source_revision_id,fingerprint,native_version,configuration_version) VALUES ($1,$2,$3,$4,$5,$6)`, pgUUID(rawRunID), pgUUID(source.SourceID), pgUUID(source.RevisionID), source.Fingerprint[:], source.NativeVersion, source.ConfigurationVersion); err != nil {
			return jobs.JobID{}, RunDetail{}, err
		}
	}
	for _, model := range models {
		if _, err = tx.Exec(ctx, `INSERT INTO documentation_run_models (run_id,role,model_profile_id,model_profile_version_id,profile_version,provider_endpoint_id,captured_endpoint_configuration_version,captured_credential_version,reasoning_effort,max_concurrent_tasks) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, pgUUID(rawRunID), model.Role, pgUUID(model.ProfileID), pgUUID(model.ProfileVersionID), model.ProfileVersion, pgUUID(model.EndpointID), model.ConfigurationVersion, model.CredentialVersion, model.ReasoningEffort, model.MaxConcurrentTasks); err != nil {
			return jobs.JobID{}, RunDetail{}, err
		}
	}
	detail, err := detailTx(ctx, tx, runID, false)
	return jobID, detail, err
}

func captureSourcesTx(ctx context.Context, tx pgx.Tx, knowledgeBaseID ID) ([]capturedSourceRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id,s.current_revision_id,r.fingerprint,r.native_version,s.kind,s.configuration_version
		FROM sources s LEFT JOIN source_revisions r ON r.source_id=s.id AND r.id=s.current_revision_id
		WHERE s.knowledge_base_id=$1 AND s.lifecycle='ACTIVE' ORDER BY s.id
	`, pgUUID(knowledgeBaseID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []capturedSourceRow{}
	for rows.Next() {
		var sourceID, revisionID pgtype.UUID
		var fingerprint []byte
		var configurationVersion int
		var nativeVersion, kind pgtype.Text
		if err = rows.Scan(&sourceID, &revisionID, &fingerprint, &nativeVersion, &kind, &configurationVersion); err != nil {
			return nil, err
		}
		if !revisionID.Valid || len(fingerprint) != sha256.Size || !nativeVersion.Valid || !kind.Valid {
			return nil, conflict("all active sources require a current immutable revision")
		}
		var digest [sha256.Size]byte
		copy(digest[:], fingerprint)
		values = append(values, capturedSourceRow{SourceID: ID(sourceID.Bytes), RevisionID: ID(revisionID.Bytes), Fingerprint: digest, NativeVersion: nativeVersion.String, Kind: kind.String, ConfigurationVersion: configurationVersion})
	}
	return values, rows.Err()
}

func captureModelsTx(ctx context.Context, tx pgx.Tx, knowledgeBaseID ID) ([]capturedModelRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.role,a.reasoning_effort,p.id,p.current_version_id,v.version_number,
		       v.max_concurrent_tasks,
		       e.id,e.configuration_version,c.secret_version
		FROM model_assignments a
		JOIN model_profiles p ON p.id=a.model_profile_id
		JOIN model_profile_versions v ON v.profile_id=p.id AND v.id=p.current_version_id
		JOIN provider_endpoints e ON e.id=p.endpoint_id
		LEFT JOIN credentials c ON c.id=e.credential_id
		WHERE a.knowledge_base_id=$1 AND e.lifecycle='ACTIVE' AND p.availability <> 'UNAVAILABLE'
		  AND (e.credential_id IS NULL OR c.deleted_at IS NULL)
		ORDER BY a.role
	`, pgUUID(knowledgeBaseID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []capturedModelRow{}
	for rows.Next() {
		var value capturedModelRow
		var profileID, profileVersionID, endpointID pgtype.UUID
		var credential pgtype.Int4
		if err = rows.Scan(&value.Role, &value.ReasoningEffort, &profileID, &profileVersionID, &value.ProfileVersion, &value.MaxConcurrentTasks, &endpointID, &value.ConfigurationVersion, &credential); err != nil {
			return nil, err
		}
		value.ProfileID, value.ProfileVersionID, value.EndpointID = ID(profileID.Bytes), ID(profileVersionID.Bytes), ID(endpointID.Bytes)
		if credential.Valid {
			selected := int(credential.Int32)
			value.CredentialVersion = &selected
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *Store) configurationCurrentTx(ctx context.Context, tx pgx.Tx, run Run) (bool, error) {
	var lifecycle, instructions, language string
	var version int
	if err := tx.QueryRow(ctx, `SELECT lifecycle,instructions,language,version FROM knowledge_bases WHERE id=$1 AND lifecycle <> 'DELETED' FOR UPDATE`, pgUUID(run.KnowledgeBaseID)).Scan(&lifecycle, &instructions, &language, &version); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if lifecycle != "ACTIVE" || version != run.KnowledgeBaseVersion || instructions != run.Instructions || language != run.Language {
		return false, nil
	}
	var sourcesCurrent bool
	if err := tx.QueryRow(ctx, `
	 SELECT NOT EXISTS (
	  SELECT 1 FROM sources source FULL JOIN (SELECT * FROM documentation_run_sources WHERE run_id=$2) captured
	   ON captured.source_id=source.id
	  WHERE (source.knowledge_base_id=$1 AND source.lifecycle='ACTIVE' OR captured.run_id=$2)
	   AND (source.lifecycle IS DISTINCT FROM 'ACTIVE' OR captured.run_id IS NULL
	        OR source.configuration_version IS DISTINCT FROM captured.configuration_version)
	 )`, pgUUID(run.KnowledgeBaseID), pgUUID(ID(run.ID))).Scan(&sourcesCurrent); err != nil {
		return false, err
	}
	if !sourcesCurrent {
		return false, nil
	}
	for _, captured := range run.Models {
		var profileID, endpointID, currentVersionID pgtype.UUID
		var effort string
		var configurationVersion int
		var credentialVersion pgtype.Int4
		var deletedAt pgtype.Timestamptz
		err := tx.QueryRow(ctx, `
			SELECT a.model_profile_id,a.reasoning_effort,p.endpoint_id,p.current_version_id,
			       e.configuration_version,c.secret_version,c.deleted_at
			FROM model_assignments a JOIN model_profiles p ON p.id=a.model_profile_id
			JOIN provider_endpoints e ON e.id=p.endpoint_id LEFT JOIN credentials c ON c.id=e.credential_id
			WHERE a.knowledge_base_id=$1 AND a.role=$2
		`, pgUUID(run.KnowledgeBaseID), string(captured.Role)).Scan(&profileID, &effort, &endpointID, &currentVersionID, &configurationVersion, &credentialVersion, &deletedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var selectedCredential *int
		if credentialVersion.Valid {
			selected := int(credentialVersion.Int32)
			selectedCredential = &selected
		}
		if profileID.Bytes != [16]byte(captured.ProfileID) || effort != string(captured.ReasoningEffort) || endpointID.Bytes != [16]byte(captured.EndpointID) || currentVersionID.Bytes != [16]byte(captured.ProfileVersionID) || configurationVersion != captured.EndpointConfigurationVersion || !equalOptionalInt(selectedCredential, captured.CredentialVersion) || captured.CredentialVersion != nil && deletedAt.Valid {
			return false, nil
		}
	}
	return true, nil
}

func (store *Store) isNoOpTx(ctx context.Context, tx pgx.Tx, run Run) (bool, error) {
	if run.PriorWikiVersionID == nil {
		return false, nil
	}
	var priorRunID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT documentation_run_id FROM wiki_versions WHERE id=$1 AND knowledge_base_id=$2`, pgUUID(ID(*run.PriorWikiVersionID)), pgUUID(run.KnowledgeBaseID)).Scan(&priorRunID); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	prior, err := detailTx(ctx, tx, RunID(priorRunID.Bytes), false)
	if err != nil {
		return false, err
	}
	if prior.Run.Instructions != run.Instructions || prior.Run.Language != run.Language ||
		len(prior.Run.Sources) != len(run.Sources) || !sameDocumentationModels(prior.Run.Models, run.Models) {
		return false, nil
	}
	for index, source := range prior.Run.Sources {
		if source.SourceID != run.Sources[index].SourceID || !bytes.Equal(source.Fingerprint[:], run.Sources[index].Fingerprint[:]) {
			return false, nil
		}
	}
	var sameConfiguration bool
	if err = tx.QueryRow(ctx, `SELECT NOT EXISTS (
	 SELECT 1 FROM documentation_run_sources current JOIN documentation_run_sources prior ON prior.source_id=current.source_id
	 WHERE current.run_id=$1 AND prior.run_id=$2 AND current.configuration_version<>prior.configuration_version
	)`, pgUUID(ID(run.ID)), pgUUID(ID(prior.Run.ID))).Scan(&sameConfiguration); err != nil {
		return false, err
	}
	if !sameConfiguration {
		return false, nil
	}
	return store.retainedWikiHealthyTx(ctx, tx, prior.Run, *run.PriorWikiVersionID)
}

func sameDocumentationModels(first, second []CapturedModel) bool {
	for _, role := range []providers.ModelRole{providers.DocumentationPlanner, providers.DocumentationWriter} {
		left, leftOK := capturedModelForRole(first, role)
		right, rightOK := capturedModelForRole(second, role)
		if !leftOK || !rightOK || left.ProfileID != right.ProfileID || left.ProfileVersionID != right.ProfileVersionID ||
			left.ProfileVersion != right.ProfileVersion || left.EndpointID != right.EndpointID ||
			left.EndpointConfigurationVersion != right.EndpointConfigurationVersion ||
			left.MaxConcurrentTasks != right.MaxConcurrentTasks ||
			!equalOptionalInt(left.CredentialVersion, right.CredentialVersion) || left.ReasoningEffort != right.ReasoningEffort {
			return false
		}
	}
	return true
}

func capturedModelForRole(values []CapturedModel, role providers.ModelRole) (CapturedModel, bool) {
	for _, value := range values {
		if value.Role == role {
			return value, true
		}
	}
	return CapturedModel{}, false
}

func equalOptionalInt(first, second *int) bool {
	return first == nil && second == nil || first != nil && second != nil && *first == *second
}

func sortedCapturedSources(values []CapturedSource) []CapturedSource {
	result := append([]CapturedSource(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].SourceID.String() < result[j].SourceID.String() })
	return result
}
