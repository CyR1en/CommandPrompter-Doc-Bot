package docgen

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func pgUUID(id ID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(id), Valid: true} }

func nullableUUID[T ~[16]byte](id *T) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: [16]byte(*id), Valid: true}
}

func newID() (ID, error) {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		return ID{}, err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}

func databaseClock(ctx context.Context, database rowQueryer) (time.Time, error) {
	var value pgtype.Timestamptz
	if err := database.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&value); err != nil || !value.Valid {
		if err == nil {
			err = errors.New("database clock did not return a timestamp")
		}
		return time.Time{}, err
	}
	return value.Time, nil
}

func detailTx(ctx context.Context, database interface {
	rowQueryer
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, runID RunID, lock bool) (RunDetail, error) {
	query := `
		SELECT id, knowledge_base_id, status, prepare_job_id,
		       knowledge_base_version, instructions, language,
		       prior_wiki_version_id, plan_digest, published_wiki_version_id,
		       sanitized_error, created_at, updated_at, completed_at,
		       planner_model_calls, planner_input_tokens,
		       planner_output_tokens, planner_total_tokens,
		       planner_truncated_tool_results
		FROM documentation_runs WHERE id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var (
		id, knowledgeBaseID, prepareJobID, priorID, publishedID pgtype.UUID
		status                                                  string
		knowledgeBaseVersion                                    int
		instructions, language                                  string
		planDigest                                              []byte
		sanitized                                               pgtype.Text
		createdAt, updatedAt                                    pgtype.Timestamptz
		completedAt                                             pgtype.Timestamptz
		planner                                                 ModelUsage
	)
	err := database.QueryRow(ctx, query, pgUUID(ID(runID))).Scan(
		&id, &knowledgeBaseID, &status, &prepareJobID, &knowledgeBaseVersion,
		&instructions, &language, &priorID, &planDigest, &publishedID,
		&sanitized, &createdAt, &updatedAt, &completedAt,
		&planner.ModelCalls, &planner.InputTokens, &planner.OutputTokens, &planner.TotalTokens,
		&planner.TruncatedToolResults,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunDetail{}, notFound("documentation run does not exist")
	}
	if err != nil {
		return RunDetail{}, err
	}
	run := Run{
		ID: RunID(id.Bytes), KnowledgeBaseID: ID(knowledgeBaseID.Bytes), Status: RunStatus(status),
		PrepareJobID: jobs.JobID(prepareJobID.Bytes), KnowledgeBaseVersion: knowledgeBaseVersion,
		Instructions: instructions, Language: language, PlanDigest: append([]byte(nil), planDigest...),
		CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time, PlannerUsage: planner,
	}
	if priorID.Valid {
		value := WikiVersionID(priorID.Bytes)
		run.PriorWikiVersionID = &value
	}
	if publishedID.Valid {
		value := WikiVersionID(publishedID.Bytes)
		run.PublishedWikiVersionID = &value
	}
	if sanitized.Valid {
		value := sanitized.String
		run.SanitizedError = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		run.CompletedAt = &value
	}

	sourceRows, err := database.Query(ctx, `
		SELECT drs.source_id, drs.source_revision_id, drs.fingerprint,
		       drs.native_version, s.kind
		FROM documentation_run_sources drs
		JOIN sources s ON s.id=drs.source_id
		WHERE drs.run_id=$1 ORDER BY drs.source_id
	`, pgUUID(ID(runID)))
	if err != nil {
		return RunDetail{}, err
	}
	for sourceRows.Next() {
		var sourceID, revisionID pgtype.UUID
		var fingerprint []byte
		var commit, kind string
		if err = sourceRows.Scan(&sourceID, &revisionID, &fingerprint, &commit, &kind); err != nil {
			sourceRows.Close()
			return RunDetail{}, err
		}
		if len(fingerprint) != 32 {
			sourceRows.Close()
			return RunDetail{}, errors.New("stored documentation source fingerprint is invalid")
		}
		var digest [32]byte
		copy(digest[:], fingerprint)
		run.Sources = append(run.Sources, CapturedSource{SourceID: ID(sourceID.Bytes), RevisionID: ID(revisionID.Bytes), Fingerprint: digest, Commit: commit, Kind: kind})
	}
	if err = sourceRows.Err(); err != nil {
		sourceRows.Close()
		return RunDetail{}, err
	}
	sourceRows.Close()

	modelRows, err := database.Query(ctx, `
		SELECT role, model_profile_id, model_profile_version_id, profile_version,
		       provider_endpoint_id, captured_endpoint_configuration_version,
		       captured_credential_version, reasoning_effort, max_concurrent_tasks
		FROM documentation_run_models WHERE run_id=$1 ORDER BY role
	`, pgUUID(ID(runID)))
	if err != nil {
		return RunDetail{}, err
	}
	for modelRows.Next() {
		var role, effort string
		var profileID, profileVersionID, endpointID pgtype.UUID
		var profileVersion, configurationVersion int
		var credentialVersion pgtype.Int4
		var maxConcurrentTasks int
		if err = modelRows.Scan(&role, &profileID, &profileVersionID, &profileVersion, &endpointID, &configurationVersion, &credentialVersion, &effort, &maxConcurrentTasks); err != nil {
			modelRows.Close()
			return RunDetail{}, err
		}
		value := CapturedModel{Role: providers.ModelRole(role), ProfileID: providers.ProfileID(profileID.Bytes), ProfileVersionID: providers.ProfileVersionID(profileVersionID.Bytes), ProfileVersion: profileVersion, EndpointID: providers.EndpointID(endpointID.Bytes), EndpointConfigurationVersion: configurationVersion, ReasoningEffort: providers.Effort(effort), MaxConcurrentTasks: maxConcurrentTasks}
		if credentialVersion.Valid {
			selected := int(credentialVersion.Int32)
			value.CredentialVersion = &selected
		}
		run.Models = append(run.Models, value)
	}
	if err = modelRows.Err(); err != nil {
		modelRows.Close()
		return RunDetail{}, err
	}
	modelRows.Close()

	pageRows, err := database.Query(ctx, `
		SELECT id, run_id, job_id, position, slug, title, purpose,
		       related_pages, source_seed_paths, status, submission_digest,
		       content_sha256, claims_sha256, sanitized_error, attempt_count,
		       created_at, updated_at, completed_at,
		       model_calls, input_tokens, output_tokens, total_tokens,
		       truncated_tool_results
		FROM documentation_pages WHERE run_id=$1 ORDER BY position
	`, pgUUID(ID(runID)))
	if err != nil {
		return RunDetail{}, err
	}
	detail := RunDetail{Run: run, Pages: []Page{}}
	for pageRows.Next() {
		page, scanErr := scanPage(pageRows)
		if scanErr != nil {
			pageRows.Close()
			return RunDetail{}, scanErr
		}
		detail.Pages = append(detail.Pages, page)
	}
	if err = pageRows.Err(); err != nil {
		pageRows.Close()
		return RunDetail{}, err
	}
	pageRows.Close()
	if err = detail.Run.Validate(); err != nil {
		return RunDetail{}, fmt.Errorf("stored documentation run is invalid: %w", err)
	}
	return detail, nil
}

type scanner interface{ Scan(...any) error }

func scanPage(row scanner) (Page, error) {
	var id, runID, jobID pgtype.UUID
	var value Page
	var relatedJSON, seedsJSON []byte
	var status string
	var submissionDigest, contentDigest, claimsDigest []byte
	var sanitized pgtype.Text
	var createdAt, updatedAt, completedAt pgtype.Timestamptz
	err := row.Scan(&id, &runID, &jobID, &value.Position, &value.Target.Slug, &value.Target.Title, &value.Target.Purpose,
		&relatedJSON, &seedsJSON, &status, &submissionDigest, &contentDigest, &claimsDigest, &sanitized,
		&value.AttemptCount, &createdAt, &updatedAt, &completedAt,
		&value.Usage.ModelCalls, &value.Usage.InputTokens, &value.Usage.OutputTokens, &value.Usage.TotalTokens,
		&value.Usage.TruncatedToolResults)
	if err != nil {
		return Page{}, err
	}
	if err = json.Unmarshal(relatedJSON, &value.Target.RelatedPages); err != nil {
		return Page{}, errors.New("stored related page set is invalid")
	}
	var rawSeeds []struct {
		SourceID string `json:"source_id"`
		Path     string `json:"path"`
	}
	if err = json.Unmarshal(seedsJSON, &rawSeeds); err != nil {
		return Page{}, errors.New("stored source seed set is invalid")
	}
	for _, raw := range rawSeeds {
		selected, parseErr := ParseID(raw.SourceID)
		if parseErr != nil {
			return Page{}, errors.New("stored source seed set is invalid")
		}
		value.Target.SourceSeedPaths = append(value.Target.SourceSeedPaths, SourceSeedPath{SourceID: selected, Path: raw.Path})
	}
	value.ID, value.RunID, value.JobID = PageID(id.Bytes), RunID(runID.Bytes), jobs.JobID(jobID.Bytes)
	value.Status = PageStatus(status)
	value.SubmissionDigest = append([]byte(nil), submissionDigest...)
	value.ContentSHA256 = append([]byte(nil), contentDigest...)
	value.ClaimsSHA256 = append([]byte(nil), claimsDigest...)
	if sanitized.Valid {
		selected := sanitized.String
		value.SanitizedError = &selected
	}
	value.CreatedAt, value.UpdatedAt = createdAt.Time, updatedAt.Time
	if completedAt.Valid {
		selected := completedAt.Time
		value.CompletedAt = &selected
	}
	if err = value.Validate(); err != nil {
		return Page{}, fmt.Errorf("stored documentation page is invalid: %w", err)
	}
	return value, nil
}

func getPageTx(ctx context.Context, database rowQueryer, pageID PageID, lock bool) (Page, error) {
	query := `
		SELECT id, run_id, job_id, position, slug, title, purpose,
		       related_pages, source_seed_paths, status, submission_digest,
		       content_sha256, claims_sha256, sanitized_error, attempt_count,
		       created_at, updated_at, completed_at,
		       model_calls, input_tokens, output_tokens, total_tokens,
		       truncated_tool_results
		FROM documentation_pages WHERE id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	value, err := scanPage(database.QueryRow(ctx, query, pgUUID(ID(pageID))))
	if errors.Is(err, pgx.ErrNoRows) {
		return Page{}, notFound("documentation page does not exist")
	}
	return value, err
}

func assertJob(ctx context.Context, tx pgx.Tx, permit jobs.Permit, jobType jobs.Type, targetType string, targetID ID, payload map[string]any) error {
	var storedType, storedTargetType string
	var storedTargetID pgtype.UUID
	var storedPayload []byte
	if err := tx.QueryRow(ctx, `SELECT job_type, target_type, target_id, payload FROM jobs WHERE id=$1`, pgUUID(ID(permit.JobID))).Scan(&storedType, &storedTargetType, &storedTargetID, &storedPayload); err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var expectedValue, storedValue any
	if json.Unmarshal(encoded, &expectedValue) != nil || json.Unmarshal(storedPayload, &storedValue) != nil ||
		storedType != string(jobType) || storedTargetType != targetType || storedTargetID.Bytes != [16]byte(targetID) || !deepJSONEqual(expectedValue, storedValue) {
		return conflict("documentation job target is invalid")
	}
	return nil
}

func deepJSONEqual(first, second any) bool {
	firstBytes, _ := json.Marshal(first)
	secondBytes, _ := json.Marshal(second)
	return string(firstBytes) == string(secondBytes)
}

func recordRun(ctx context.Context, tx pgx.Tx, detail RunDetail, actorID *ID, eventType string) error {
	snapshot := runSnapshot(detail)
	requestID, err := newID()
	if err != nil {
		return err
	}
	targetID := [16]byte(detail.Run.ID)
	actorType := "system"
	var actor *[16]byte
	if actorID != nil {
		selected := [16]byte(*actorID)
		actor = &selected
		actorType = "operator"
	}
	action := strings.ReplaceAll(eventType, ".", "_")
	if err = events.AppendAudit(ctx, tx, events.AuditEvent{ActorType: actorType, ActorID: actor, Action: action, TargetType: "documentation_run", TargetID: &targetID, RequestID: [16]byte(requestID), Details: snapshot}); err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{Type: eventType, ResourceType: "documentation_run", ResourceID: targetID, Snapshot: snapshot})
}

func runSnapshot(detail RunDetail) map[string]any {
	pageUsage := make([]map[string]any, len(detail.Pages))
	for index, page := range detail.Pages {
		pageUsage[index] = map[string]any{"page_id": page.ID.String(), "model_calls": page.Usage.ModelCalls, "input_tokens": page.Usage.InputTokens, "output_tokens": page.Usage.OutputTokens, "total_tokens": page.Usage.TotalTokens, "truncated_tool_results": page.Usage.TruncatedToolResults}
	}
	return map[string]any{
		"id": detail.Run.ID.String(), "knowledge_base_id": detail.Run.KnowledgeBaseID.String(),
		"status": strings.ToLower(string(detail.Run.Status)), "source_count": len(detail.Run.Sources), "model_count": len(detail.Run.Models), "page_count": len(detail.Pages),
		"usage": usageSnapshot(detail.Usage()), "planner_usage": usageSnapshot(detail.Run.PlannerUsage), "page_usage": pageUsage,
		"published_wiki_version_id": optionalWikiID(detail.Run.PublishedWikiVersionID), "sanitized_error": optionalString(detail.Run.SanitizedError),
	}
}

func usageSnapshot(value ModelUsage) map[string]any {
	return map[string]any{"model_calls": value.ModelCalls, "input_tokens": value.InputTokens, "output_tokens": value.OutputTokens, "total_tokens": value.TotalTokens, "truncated_tool_results": value.TruncatedToolResults}
}
func optionalWikiID(value *WikiVersionID) any {
	if value == nil {
		return nil
	}
	return value.String()
}
func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func pythonISO(value time.Time) string {
	base := value.Format("2006-01-02T15:04:05")
	if micros := value.Nanosecond() / 1000; micros != 0 {
		base += fmt.Sprintf(".%06d", micros)
	}
	_, offset := value.Zone()
	sign := '+'
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	return fmt.Sprintf("%s%c%02d:%02d", base, sign, offset/3600, offset%3600/60)
}
