package docgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const documentationIdempotencyTTL = 24 * time.Hour

type Queue interface {
	EnqueueTx(context.Context, pgx.Tx, jobs.Command) (jobs.JobID, error)
	AssertPermit(context.Context, pgx.Tx, jobs.Permit) error
}

type Digester interface {
	KeyedDigests(...[]byte) ([][]byte, error)
}

type Store struct {
	pool            *pgxpool.Pool
	queue           Queue
	digester        Digester
	runArtifacts    *artifacts.RunStore
	wikiArtifacts   *artifacts.WikiStore
	sourceArtifacts *sourcefiles.Store
}

func NewStore(pool *pgxpool.Pool, queue Queue, digester Digester, runArtifacts *artifacts.RunStore, wikiArtifacts *artifacts.WikiStore, sourceArtifacts *sourcefiles.Store) (*Store, error) {
	if pool == nil || queue == nil || digester == nil || runArtifacts == nil || wikiArtifacts == nil || sourceArtifacts == nil {
		return nil, errors.New("documentation store dependencies are incomplete")
	}
	return &Store{pool: pool, queue: queue, digester: digester, runArtifacts: runArtifacts, wikiArtifacts: wikiArtifacts, sourceArtifacts: sourceArtifacts}, nil
}

func (store *Store) RequestGeneration(ctx context.Context, knowledgeBaseID ID, expectedVersion int, actor ID, requestKey string) (jobs.JobID, error) {
	if expectedVersion <= 0 {
		return jobs.JobID{}, errors.New("expected_version must be positive")
	}
	var version [8]byte
	binary.BigEndian.PutUint64(version[:], uint64(expectedVersion))
	digests, err := store.digester.KeyedDigests([]byte("documentation.generate"), knowledgeBaseID[:], version[:])
	if err != nil || len(digests) == 0 {
		return jobs.JobID{}, errors.New("compute documentation request digest")
	}
	request := idempotency.Request{Scope: "operator:" + actor.String(), Key: requestKey, Operation: "documentation.generate", TTL: documentationIdempotencyTTL}
	for index, raw := range digests {
		if len(raw) != sha256.Size {
			return jobs.JobID{}, errors.New("documentation request digest is invalid")
		}
		var digest idempotency.Digest
		copy(digest[:], raw)
		if index == 0 {
			request.Digest = digest
		} else {
			request.AcceptedDigests = append(request.AcceptedDigests, digest)
		}
	}
	var jobID jobs.JobID
	err = store.withTx(ctx, func(tx pgx.Tx) error {
		result, executeErr := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			createdJob, detail, createErr := store.newRunTx(ctx, tx, knowledgeBaseID, expectedVersion, &actor, "")
			if createErr != nil {
				return idempotency.Result{}, createErr
			}
			if createErr = recordRun(ctx, tx, detail, &actor, "documentation_run.requested"); createErr != nil {
				return idempotency.Result{}, createErr
			}
			return idempotency.Result{Type: "job", ID: [16]byte(createdJob)}, nil
		})
		if executeErr != nil {
			return executeErr
		}
		if result.Type != "job" {
			return idempotency.ErrConflict
		}
		jobID = jobs.JobID(result.ID)
		return nil
	})
	return jobID, err
}

func (store *Store) ListRuns(ctx context.Context, knowledgeBaseID *ID, limit, offset int) ([]RunDetail, error) {
	if limit < 1 || limit > 100 || offset < 0 || offset > 10_000 {
		return nil, errors.New("documentation run page is out of bounds")
	}
	query := "SELECT id FROM documentation_runs"
	arguments := []any{}
	if knowledgeBaseID != nil {
		query += " WHERE knowledge_base_id=$1"
		arguments = append(arguments, pgUUID(*knowledgeBaseID))
	}
	query += fmt.Sprintf(" ORDER BY created_at DESC, id LIMIT %d OFFSET %d", limit, offset)
	rows, err := store.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []RunID{}
	for rows.Next() {
		var id pgtype.UUID
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, RunID(id.Bytes))
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := make([]RunDetail, 0, len(ids))
	for _, id := range ids {
		value, getErr := store.GetRun(ctx, id)
		if getErr != nil {
			return nil, getErr
		}
		result = append(result, value)
	}
	return result, nil
}

func (store *Store) GetRun(ctx context.Context, runID RunID) (RunDetail, error) {
	return detailTx(ctx, store.pool, runID, false)
}

func (store *Store) Prepare(ctx context.Context, knowledgeBaseID ID, permit jobs.Permit) (RunDetail, error) {
	var result RunDetail
	err := store.withTx(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		var runID pgtype.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM documentation_runs WHERE prepare_job_id=$1 AND knowledge_base_id=$2 FOR UPDATE`, pgUUID(ID(permit.JobID)), pgUUID(knowledgeBaseID)).Scan(&runID); errors.Is(err, pgx.ErrNoRows) {
			return conflict("prepare job does not belong to run")
		} else if err != nil {
			return err
		}
		if err := assertJob(ctx, tx, permit, jobs.PrepareRun, "knowledge_base", knowledgeBaseID, map[string]any{"run_id": ID(runID.Bytes).String()}); err != nil {
			return err
		}
		detail, err := detailTx(ctx, tx, RunID(runID.Bytes), true)
		if err != nil {
			return err
		}
		if detail.Run.Status != RunPreparing {
			result = detail
			return nil
		}
		current, err := store.configurationCurrentTx(ctx, tx, detail.Run)
		if err != nil {
			return err
		}
		if !current {
			result, err = store.interruptTx(ctx, tx, detail, "documentation:source_drift")
			return err
		}
		noOp, err := store.isNoOpTx(ctx, tx, detail.Run)
		if err != nil {
			return err
		}
		if noOp {
			result, err = store.terminalTx(ctx, tx, detail.Run.ID, RunNoOp, nil)
			if err != nil {
				return err
			}
			return store.followUpContentDriftTx(ctx, tx, detail.Run)
		}
		planner, plannerOK := capturedModelForRole(detail.Run.Models, providers.DocumentationPlanner)
		_, writerOK := capturedModelForRole(detail.Run.Models, providers.DocumentationWriter)
		if !plannerOK || !writerOK {
			selected := "documentation:model_assignment_missing"
			result, err = store.terminalTx(ctx, tx, detail.Run.ID, RunFailed, &selected)
			return err
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET status='PLANNING', updated_at=$2 WHERE id=$1`, pgUUID(ID(detail.Run.ID)), now); err != nil {
			return err
		}
		if _, err = store.queue.EnqueueTx(ctx, tx, modelCommand(jobs.Command{Type: jobs.PlanRun, TargetType: "documentation_run", TargetID: jobs.UUID(detail.Run.ID), Payload: map[string]any{"run_id": detail.Run.ID.String()}, OperationKey: "plan-run:" + detail.Run.ID.String(), MaxAttempts: 3}, planner)); err != nil {
			return err
		}
		result, err = detailTx(ctx, tx, detail.Run.ID, false)
		if err != nil {
			return err
		}
		return recordRun(ctx, tx, result, nil, "documentation_run.planning")
	})
	return result, err
}

func (store *Store) AcceptPlan(ctx context.Context, runID RunID, plan PagePlan, permit jobs.Permit, usage ModelUsage) (RunDetail, error) {
	if err := plan.Validate(); err != nil {
		return RunDetail{}, err
	}
	if err := usage.Validate(); err != nil {
		return RunDetail{}, err
	}
	digest, _ := plan.SemanticDigest()
	var result RunDetail
	err := store.withTx(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		if err := assertJob(ctx, tx, permit, jobs.PlanRun, "documentation_run", ID(runID), map[string]any{"run_id": runID.String()}); err != nil {
			return err
		}
		detail, err := detailTx(ctx, tx, runID, true)
		if err != nil {
			return err
		}
		if detail.Run.Status == RunGenerating {
			if !bytes.Equal(detail.Run.PlanDigest, digest[:]) {
				return conflict("accepted page plan is immutable")
			}
			result = detail
			return nil
		}
		if detail.Run.Status != RunPlanning {
			return conflict("run cannot accept a page plan")
		}
		if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET planner_model_calls=$2, planner_input_tokens=$3, planner_output_tokens=$4, planner_total_tokens=$5, planner_truncated_tool_results=$6 WHERE id=$1`, pgUUID(ID(runID)), usage.ModelCalls, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.TruncatedToolResults); err != nil {
			return err
		}
		current, err := store.configurationCurrentTx(ctx, tx, detail.Run)
		if err != nil {
			return err
		}
		if !current {
			result, err = store.interruptTx(ctx, tx, detail, "documentation:source_drift")
			return err
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET status='GENERATING', plan_digest=$2, planner_model_calls=$3, planner_input_tokens=$4, planner_output_tokens=$5, planner_total_tokens=$6, planner_truncated_tool_results=$7, updated_at=$8 WHERE id=$1`, pgUUID(ID(runID)), digest[:], usage.ModelCalls, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.TruncatedToolResults, now); err != nil {
			return err
		}
		writer, writerOK := capturedModelForRole(detail.Run.Models, providers.DocumentationWriter)
		if !writerOK {
			return conflict("documentation writer model is unavailable")
		}
		for position, target := range plan.Pages {
			pageID, idErr := newID()
			if idErr != nil {
				return idErr
			}
			jobID, enqueueErr := store.queue.EnqueueTx(ctx, tx, modelCommand(jobs.Command{Type: jobs.GeneratePage, TargetType: "documentation_page", TargetID: jobs.UUID(pageID), Payload: map[string]any{"run_id": runID.String(), "page_id": pageID.String()}, OperationKey: fmt.Sprintf("generate-page:%s:%d:%x", runID.String(), position, digest), MaxAttempts: 3}, writer))
			if enqueueErr != nil {
				return enqueueErr
			}
			related, _ := json.Marshal(nonNilStrings(target.RelatedPages))
			seeds := make([]map[string]string, len(target.SourceSeedPaths))
			for index, seed := range target.SourceSeedPaths {
				seeds[index] = map[string]string{"source_id": seed.SourceID.String(), "path": seed.Path}
			}
			encodedSeeds, _ := json.Marshal(seeds)
			if _, err = tx.Exec(ctx, `INSERT INTO documentation_pages (id,run_id,job_id,position,slug,title,purpose,related_pages,source_seed_paths,status,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,'PENDING',$10,$10)`, pgUUID(pageID), pgUUID(ID(runID)), pgUUID(ID(jobID)), position, target.Slug, target.Title, target.Purpose, string(related), string(encodedSeeds), now); err != nil {
				return err
			}
		}
		result, err = detailTx(ctx, tx, runID, false)
		if err != nil {
			return err
		}
		return recordRun(ctx, tx, result, nil, "documentation_run.generating")
	})
	return result, err
}

func modelCommand(command jobs.Command, model CapturedModel) jobs.Command {
	command.ConcurrencyKey = "model-profile:" + model.ProfileID.String()
	command.ConcurrencyLimit = int32(model.MaxConcurrentTasks)
	return command
}

func (store *Store) BeginPage(ctx context.Context, page Page, permit jobs.Permit) (RunDetail, error) {
	var result RunDetail
	err := store.withTx(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		if err := assertJob(ctx, tx, permit, jobs.GeneratePage, "documentation_page", ID(page.ID), map[string]any{"run_id": page.RunID.String(), "page_id": page.ID.String()}); err != nil {
			return err
		}
		detail, err := detailTx(ctx, tx, page.RunID, true)
		if err != nil {
			return err
		}
		currentPage, err := getPageTx(ctx, tx, page.ID, true)
		if err != nil {
			return err
		}
		if detail.Run.Status != RunGenerating {
			result = detail
			return nil
		}
		current, err := store.configurationCurrentTx(ctx, tx, detail.Run)
		if err != nil {
			return err
		}
		if !current {
			result, err = store.interruptTx(ctx, tx, detail, "documentation:source_drift")
			return err
		}
		if currentPage.Status == PageComplete || currentPage.Status == PageSkipped {
			result = detail
			return nil
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE documentation_pages SET status='RUNNING', attempt_count=attempt_count+1, updated_at=$2 WHERE id=$1`, pgUUID(ID(page.ID)), now); err != nil {
			return err
		}
		result, err = detailTx(ctx, tx, page.RunID, false)
		return err
	})
	return result, err
}

func (store *Store) CompletePage(ctx context.Context, page Page, accepted artifacts.Page, permit jobs.Permit, usage ModelUsage) (RunDetail, error) {
	if err := usage.Validate(); err != nil {
		return RunDetail{}, err
	}
	var result RunDetail
	err := store.withTx(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		if page.JobID != permit.JobID {
			return conflict("page permit does not match page")
		}
		if err := assertJob(ctx, tx, permit, jobs.GeneratePage, "documentation_page", ID(page.ID), map[string]any{"run_id": page.RunID.String(), "page_id": page.ID.String()}); err != nil {
			return err
		}
		detail, err := detailTx(ctx, tx, page.RunID, true)
		if err != nil {
			return err
		}
		currentPage, err := getPageTx(ctx, tx, page.ID, true)
		if err != nil {
			return err
		}
		if currentPage.Status == PageComplete {
			if !bytes.Equal(currentPage.ContentSHA256, accepted.ContentSHA256[:]) || !bytes.Equal(currentPage.ClaimsSHA256, accepted.ClaimsSHA256[:]) {
				return conflict("accepted page result is immutable")
			}
			result = detail
			return nil
		}
		if detail.Run.Status != RunGenerating {
			result = detail
			return nil
		}
		if accepted.Slug != currentPage.Target.Slug {
			return validation("accepted page does not match its target")
		}
		if _, err = tx.Exec(ctx, `UPDATE documentation_pages SET model_calls=$2,input_tokens=$3,output_tokens=$4,total_tokens=$5,truncated_tool_results=$6 WHERE id=$1`, pgUUID(ID(page.ID)), usage.ModelCalls, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.TruncatedToolResults); err != nil {
			return err
		}
		current, err := store.configurationCurrentTx(ctx, tx, detail.Run)
		if err != nil {
			return err
		}
		if !current {
			result, err = store.interruptTx(ctx, tx, detail, "documentation:source_drift")
			return err
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return err
		}
		submissionDigest := sha256.Sum256(append(append([]byte(nil), accepted.ContentSHA256[:]...), accepted.ClaimsSHA256[:]...))
		if _, err = tx.Exec(ctx, `UPDATE documentation_pages SET status='COMPLETE',submission_digest=$2,content_sha256=$3,claims_sha256=$4,sanitized_error=NULL,model_calls=$5,input_tokens=$6,output_tokens=$7,total_tokens=$8,truncated_tool_results=$9,updated_at=$10,completed_at=$10 WHERE id=$1`, pgUUID(ID(page.ID)), submissionDigest[:], accepted.ContentSHA256[:], accepted.ClaimsSHA256[:], usage.ModelCalls, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.TruncatedToolResults, now); err != nil {
			return err
		}
		result, err = store.maybeFinalizeTx(ctx, tx, page.RunID, now)
		return err
	})
	return result, err
}

func (store *Store) SkipPage(ctx context.Context, page Page, sanitizedError string, permit jobs.Permit, usage ModelUsage) (RunDetail, error) {
	if sanitizedError == "" || len(sanitizedError) > 1000 {
		return RunDetail{}, errors.New("sanitized page error is invalid")
	}
	if err := usage.Validate(); err != nil {
		return RunDetail{}, err
	}
	var result RunDetail
	err := store.withTx(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		if page.JobID != permit.JobID {
			return conflict("page permit does not match page")
		}
		if err := assertJob(ctx, tx, permit, jobs.GeneratePage, "documentation_page", ID(page.ID), map[string]any{"run_id": page.RunID.String(), "page_id": page.ID.String()}); err != nil {
			return err
		}
		detail, err := detailTx(ctx, tx, page.RunID, true)
		if err != nil {
			return err
		}
		currentPage, err := getPageTx(ctx, tx, page.ID, true)
		if err != nil {
			return err
		}
		if currentPage.Status == PageComplete || currentPage.Status == PageSkipped || detail.Run.Status != RunGenerating {
			result = detail
			return nil
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE documentation_pages SET status='SKIPPED',sanitized_error=$2,model_calls=$3,input_tokens=$4,output_tokens=$5,total_tokens=$6,truncated_tool_results=$7,updated_at=$8,completed_at=$8 WHERE id=$1`, pgUUID(ID(page.ID)), sanitizedError, usage.ModelCalls, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.TruncatedToolResults, now); err != nil {
			return err
		}
		result, err = store.maybeFinalizeTx(ctx, tx, page.RunID, now)
		return err
	})
	return result, err
}

func (store *Store) BeginFinalization(ctx context.Context, runID RunID, permit jobs.Permit) (RunDetail, error) {
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
		if detail.Run.Status != RunFinalizing {
			result = detail
			return nil
		}
		for _, page := range detail.Pages {
			if page.Status == PageSkipped {
				result, err = store.interruptTx(ctx, tx, detail, "documentation:page_skipped")
				return err
			}
		}
		current, err := store.configurationCurrentTx(ctx, tx, detail.Run)
		if err != nil {
			return err
		}
		if !current {
			result, err = store.interruptTx(ctx, tx, detail, "documentation:source_drift")
			return err
		}
		result = detail
		return nil
	})
	return result, err
}

func (store *Store) FailRun(ctx context.Context, runID RunID, sanitizedError string, permit jobs.Permit, usage ModelUsage) (RunDetail, error) {
	if sanitizedError == "" || len(sanitizedError) > 1000 {
		return RunDetail{}, errors.New("sanitized run error is invalid")
	}
	if err := usage.Validate(); err != nil {
		return RunDetail{}, err
	}
	var result RunDetail
	err := store.withTx(ctx, func(tx pgx.Tx) error {
		if err := store.queue.AssertPermit(ctx, tx, permit); err != nil {
			return err
		}
		var jobType, targetType string
		var targetID pgtype.UUID
		var payload []byte
		if err := tx.QueryRow(ctx, `SELECT job_type,target_type,target_id,payload FROM jobs WHERE id=$1`, pgUUID(ID(permit.JobID))).Scan(&jobType, &targetType, &targetID, &payload); err != nil {
			return err
		}
		expected, _ := json.Marshal(map[string]any{"run_id": runID.String()})
		if (jobType != string(jobs.PlanRun) && jobType != string(jobs.FinalizeRun)) || targetType != "documentation_run" || targetID.Bytes != [16]byte(runID) || !jsonBytesEqual(payload, expected) {
			return conflict("documentation job target is invalid")
		}
		detail, err := detailTx(ctx, tx, runID, true)
		if err != nil {
			return err
		}
		if detail.Run.Status.Terminal() {
			result = detail
			return nil
		}
		if detail.Run.Status == RunPlanning {
			if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET planner_model_calls=$2,planner_input_tokens=$3,planner_output_tokens=$4,planner_total_tokens=$5,planner_truncated_tool_results=$6 WHERE id=$1`, pgUUID(ID(runID)), usage.ModelCalls, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.TruncatedToolResults); err != nil {
				return err
			}
		}
		result, err = store.terminalTx(ctx, tx, runID, RunFailed, &sanitizedError)
		return err
	})
	return result, err
}

func (store *Store) maybeFinalizeTx(ctx context.Context, tx pgx.Tx, runID RunID, now time.Time) (RunDetail, error) {
	var remaining int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM documentation_pages WHERE run_id=$1 AND status IN ('PENDING','RUNNING')`, pgUUID(ID(runID))).Scan(&remaining); err != nil {
		return RunDetail{}, err
	}
	if remaining == 0 {
		if _, err := tx.Exec(ctx, `UPDATE documentation_runs SET status='FINALIZING',updated_at=$2 WHERE id=$1 AND status='GENERATING'`, pgUUID(ID(runID)), now); err != nil {
			return RunDetail{}, err
		}
		if _, err := store.queue.EnqueueTx(ctx, tx, jobs.Command{Type: jobs.FinalizeRun, TargetType: "documentation_run", TargetID: jobs.UUID(runID), Payload: map[string]any{"run_id": runID.String()}, OperationKey: "finalize-run:" + runID.String(), MaxAttempts: 3}); err != nil {
			return RunDetail{}, err
		}
	}
	value, err := detailTx(ctx, tx, runID, false)
	if err != nil {
		return RunDetail{}, err
	}
	return value, recordRun(ctx, tx, value, nil, "documentation_page.completed")
}

func (store *Store) terminalTx(ctx context.Context, tx pgx.Tx, runID RunID, status RunStatus, sanitizedError *string) (RunDetail, error) {
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return RunDetail{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE documentation_runs SET status=$2,sanitized_error=$3,updated_at=$4,completed_at=$4 WHERE id=$1`, pgUUID(ID(runID)), string(status), sanitizedError, now); err != nil {
		return RunDetail{}, err
	}
	value, err := detailTx(ctx, tx, runID, false)
	if err != nil {
		return RunDetail{}, err
	}
	return value, recordRun(ctx, tx, value, nil, "documentation_run."+strings.ToLower(string(status)))
}

func (store *Store) interruptTx(ctx context.Context, tx pgx.Tx, detail RunDetail, sanitizedError string) (RunDetail, error) {
	interrupted, err := store.terminalTx(ctx, tx, detail.Run.ID, RunInterrupted, &sanitizedError)
	if err != nil {
		return RunDetail{}, err
	}
	var lifecycle string
	var version int
	if err = tx.QueryRow(ctx, `SELECT lifecycle,version FROM knowledge_bases WHERE id=$1 FOR UPDATE`, pgUUID(detail.Run.KnowledgeBaseID)).Scan(&lifecycle, &version); err != nil {
		return RunDetail{}, err
	}
	if lifecycle == "ACTIVE" {
		_, _, err = store.newRunTx(ctx, tx, detail.Run.KnowledgeBaseID, version, nil, fmt.Sprintf("prepare-run-follow-up:%s:%d:%s", detail.Run.KnowledgeBaseID.String(), version, detail.Run.ID.String()))
	}
	return interrupted, err
}

func (store *Store) withTx(ctx context.Context, operation func(pgx.Tx) error) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = operation(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func jsonBytesEqual(first, second []byte) bool {
	var left, right any
	return json.Unmarshal(first, &left) == nil && json.Unmarshal(second, &right) == nil && deepJSONEqual(left, right)
}
