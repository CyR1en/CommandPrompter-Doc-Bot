package docgen

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"strings"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/jobs"
)

const correctionInstruction = "The previous page failed deterministic validation. Return a complete corrected page matching the accepted target, Claims, and evidence."

var ErrAgentRuntime = errors.New("documentation agent runtime failed")

// RuntimeFailure carries only aggregate usage across the process boundary.
// Its error text is fixed so provider and prompt data cannot leak.
type RuntimeFailure struct{ Usage ModelUsage }

func (*RuntimeFailure) Error() string { return "documentation agent runtime failed" }
func (*RuntimeFailure) Unwrap() error { return ErrAgentRuntime }

type AgentRuntime interface {
	Plan(context.Context, RunDetail) (PagePlan, ModelUsage, error)
	GeneratePage(context.Context, RunDetail, Page, string) (PageSubmission, ModelUsage, error)
	ValidatePage(context.Context, RunDetail, Page, PageSubmission) (artifacts.Page, error)
}

type HandlerStore interface {
	Prepare(context.Context, ID, jobs.Permit) (RunDetail, error)
	GetRun(context.Context, RunID) (RunDetail, error)
	AcceptPlan(context.Context, RunID, PagePlan, jobs.Permit, ModelUsage) (RunDetail, error)
	BeginPage(context.Context, Page, jobs.Permit) (RunDetail, error)
	CompletePage(context.Context, Page, artifacts.Page, jobs.Permit, ModelUsage) (RunDetail, error)
	SkipPage(context.Context, Page, string, jobs.Permit, ModelUsage) (RunDetail, error)
	BeginFinalization(context.Context, RunID, jobs.Permit) (RunDetail, error)
	Publish(context.Context, RunID, WikiVersionID, artifacts.PublishedWikiBundle, []artifacts.Page, jobs.Permit) (RunDetail, error)
	FailRun(context.Context, RunID, string, jobs.Permit, ModelUsage) (RunDetail, error)
}

type RunArtifactStore interface {
	SavePage(artifacts.ID, artifacts.ID, artifacts.Page) error
	LoadPage(artifacts.ID, artifacts.ID, string) (artifacts.Page, error)
}

type WikiArtifactStore interface {
	Publish(artifacts.ID, artifacts.ID, artifacts.ID, []artifacts.Page, []artifacts.SourceRevision) (artifacts.PublishedWikiBundle, error)
}

type Handler func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error)

type HandlerFailure struct {
	SanitizedError string
	Retryable      bool
}

func (failure *HandlerFailure) Error() string { return failure.SanitizedError }

type Handlers struct {
	store         HandlerStore
	runtime       AgentRuntime
	runArtifacts  RunArtifactStore
	wikiArtifacts WikiArtifactStore
}

func NewHandlers(store HandlerStore, runtime AgentRuntime, runArtifacts RunArtifactStore, wikiArtifacts WikiArtifactStore) (*Handlers, error) {
	if store == nil || runtime == nil || runArtifacts == nil || wikiArtifacts == nil {
		return nil, errors.New("documentation handler dependencies are incomplete")
	}
	return &Handlers{store: store, runtime: runtime, runArtifacts: runArtifacts, wikiArtifacts: wikiArtifacts}, nil
}

func (handlers *Handlers) Registry() map[jobs.Type]Handler {
	return map[jobs.Type]Handler{jobs.PrepareRun: handlers.Prepare, jobs.PlanRun: handlers.Plan, jobs.GeneratePage: handlers.GeneratePage, jobs.FinalizeRun: handlers.Finalize}
}

func (handlers *Handlers) Prepare(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
	runID, err := runCommand(command, jobs.PrepareRun, "knowledge_base")
	if err != nil {
		return nil, err
	}
	detail, err := handlers.store.Prepare(ctx, ID(command.TargetID), permit)
	if err != nil {
		return nil, err
	}
	if detail.Run.ID != runID {
		return nil, errors.New("prepare job resolved a different documentation run")
	}
	if detail.Run.Status == RunFailed {
		return nil, &HandlerFailure{SanitizedError: fallback(detail.Run.SanitizedError, "documentation:failed")}
	}
	return runResult(detail), nil
}

func (handlers *Handlers) Plan(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
	runID, err := runCommand(command, jobs.PlanRun, "documentation_run")
	if err != nil {
		return nil, err
	}
	if ID(command.TargetID) != ID(runID) {
		return nil, errors.New("documentation planning job target is invalid")
	}
	detail, err := handlers.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if detail.Run.Status != RunPlanning {
		if detail.Run.Status == RunFailed {
			return nil, &HandlerFailure{SanitizedError: fallback(detail.Run.SanitizedError, "documentation:failed")}
		}
		return runResult(detail), nil
	}
	plan, usage, runtimeErr := handlers.runtime.Plan(ctx, detail)
	if runtimeErr != nil {
		if errors.Is(runtimeErr, context.Canceled) || errors.Is(runtimeErr, context.DeadlineExceeded) {
			return nil, runtimeErr
		}
		var failure *RuntimeFailure
		if !errors.As(runtimeErr, &failure) {
			return nil, runtimeErr
		}
		usage = usage.Add(failure.Usage)
	}
	if runtimeErr != nil || plan.Validate() != nil {
		failed, failErr := handlers.store.FailRun(ctx, runID, "documentation:planning_failed", permit, usage)
		if failErr != nil {
			return nil, failErr
		}
		_ = failed
		return nil, &HandlerFailure{SanitizedError: "documentation:planning_failed"}
	}
	accepted, err := handlers.store.AcceptPlan(ctx, runID, plan, permit, usage)
	if err != nil {
		return nil, err
	}
	if accepted.Run.Status == RunFailed {
		return nil, &HandlerFailure{SanitizedError: fallback(accepted.Run.SanitizedError, "documentation:failed")}
	}
	return runResult(accepted), nil
}

func (handlers *Handlers) GeneratePage(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
	runID, pageID, err := pageCommand(command)
	if err != nil {
		return nil, err
	}
	if ID(command.TargetID) != ID(pageID) {
		return nil, errors.New("documentation page job target is invalid")
	}
	detail, err := handlers.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	page, ok := findPage(detail, pageID)
	if !ok {
		return nil, errors.New("documentation page is outside its run")
	}
	detail, err = handlers.store.BeginPage(ctx, page, permit)
	if err != nil {
		return nil, err
	}
	page, ok = findPage(detail, pageID)
	if !ok {
		return nil, errors.New("documentation page is outside its run")
	}
	if detail.Run.Status != RunGenerating || page.Status == PageComplete || page.Status == PageSkipped {
		return runResult(detail), nil
	}
	accepted, loadErr := handlers.runArtifacts.LoadPage(artifacts.ID(detail.Run.KnowledgeBaseID), artifacts.ID(runID), page.Target.Slug)
	if loadErr != nil {
		accepted = artifacts.Page{}
	}
	usage := ModelUsage{}
	if accepted.Slug == "" {
		correction := ""
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			submission, callUsage, generateErr := handlers.runtime.GeneratePage(ctx, detail, page, correction)
			usage = usage.Add(callUsage)
			if generateErr != nil {
				var failure *RuntimeFailure
				if errors.As(generateErr, &failure) {
					usage = usage.Add(failure.Usage)
				}
				lastErr = generateErr
				break
			}
			accepted, lastErr = handlers.runtime.ValidatePage(ctx, detail, page, submission)
			if lastErr == nil {
				break
			}
			if !errors.Is(lastErr, ErrValidation) {
				break
			}
			correction = boundedCorrection(lastErr)
		}
		if lastErr != nil {
			if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
				return nil, lastErr
			}
			code := "documentation_page:worker_failed"
			if errors.Is(lastErr, ErrValidation) || errors.Is(lastErr, ErrAgentRuntime) {
				code = "documentation_page:generation_failed"
			}
			skipped, skipErr := handlers.store.SkipPage(ctx, page, code, permit, usage)
			if skipErr != nil {
				return nil, skipErr
			}
			return runResult(skipped), nil
		}
		if err = handlers.runArtifacts.SavePage(artifacts.ID(detail.Run.KnowledgeBaseID), artifacts.ID(runID), accepted); err != nil {
			return nil, err
		}
	}
	completed, err := handlers.store.CompletePage(ctx, page, accepted, permit, usage)
	if err != nil {
		return nil, err
	}
	return runResult(completed), nil
}

func (handlers *Handlers) Finalize(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
	runID, err := runCommand(command, jobs.FinalizeRun, "documentation_run")
	if err != nil {
		return nil, err
	}
	if ID(command.TargetID) != ID(runID) {
		return nil, errors.New("documentation finalization job target is invalid")
	}
	detail, err := handlers.store.BeginFinalization(ctx, runID, permit)
	if err != nil {
		return nil, err
	}
	if detail.Run.Status != RunFinalizing {
		if detail.Run.Status == RunFailed {
			return nil, &HandlerFailure{SanitizedError: fallback(detail.Run.SanitizedError, "documentation:failed")}
		}
		return runResult(detail), nil
	}
	pages := make([]artifacts.Page, 0, len(detail.Pages))
	for _, page := range detail.Pages {
		if page.Status != PageComplete {
			continue
		}
		accepted, loadErr := handlers.runArtifacts.LoadPage(artifacts.ID(detail.Run.KnowledgeBaseID), artifacts.ID(runID), page.Target.Slug)
		if loadErr != nil {
			return handlers.failPublication(ctx, runID, permit)
		}
		pages = append(pages, accepted)
	}
	versionID := deterministicWikiVersionID(runID)
	revisions := make([]artifacts.SourceRevision, len(detail.Run.Sources))
	for index, source := range detail.Run.Sources {
		revisions[index] = artifacts.SourceRevision{"source_id": source.SourceID.String(), "revision_id": source.RevisionID.String(), "fingerprint": fmt.Sprintf("%x", source.Fingerprint), "commit": source.Commit}
	}
	bundle, err := handlers.wikiArtifacts.Publish(artifacts.ID(detail.Run.KnowledgeBaseID), artifacts.ID(runID), artifacts.ID(versionID), pages, revisions)
	if err != nil {
		return handlers.failPublication(ctx, runID, permit)
	}
	published, err := handlers.store.Publish(ctx, runID, versionID, bundle, pages, permit)
	if err != nil {
		return handlers.failPublication(ctx, runID, permit)
	}
	if published.Run.Status == RunFailed {
		return nil, &HandlerFailure{SanitizedError: fallback(published.Run.SanitizedError, "documentation:failed")}
	}
	return runResult(published), nil
}

func (handlers *Handlers) failPublication(ctx context.Context, runID RunID, permit jobs.Permit) (map[string]any, error) {
	_, err := handlers.store.FailRun(ctx, runID, "documentation:publication_failed", permit, ModelUsage{})
	if err != nil {
		return nil, err
	}
	return nil, &HandlerFailure{SanitizedError: "documentation:publication_failed"}
}

func runCommand(command jobs.Command, expected jobs.Type, targetType string) (RunID, error) {
	if command.Type != expected || command.TargetType != targetType || len(command.Payload) != 1 {
		return RunID{}, errors.New("documentation run job is invalid")
	}
	raw, ok := command.Payload["run_id"].(string)
	if !ok {
		return RunID{}, errors.New("documentation run job is invalid")
	}
	id, err := ParseID(raw)
	if err != nil {
		return RunID{}, errors.New("documentation run job is invalid")
	}
	return RunID(id), nil
}

func pageCommand(command jobs.Command) (RunID, PageID, error) {
	if command.Type != jobs.GeneratePage || command.TargetType != "documentation_page" || len(command.Payload) != 2 {
		return RunID{}, PageID{}, errors.New("documentation page job is invalid")
	}
	runRaw, runOK := command.Payload["run_id"].(string)
	pageRaw, pageOK := command.Payload["page_id"].(string)
	runID, runErr := ParseID(runRaw)
	pageID, pageErr := ParseID(pageRaw)
	if !runOK || !pageOK || runErr != nil || pageErr != nil {
		return RunID{}, PageID{}, errors.New("documentation page job is invalid")
	}
	return RunID(runID), PageID(pageID), nil
}

func findPage(detail RunDetail, id PageID) (Page, bool) {
	for _, page := range detail.Pages {
		if page.ID == id {
			return page, true
		}
	}
	return Page{}, false
}

func runResult(detail RunDetail) map[string]any {
	var published any
	if detail.Run.PublishedWikiVersionID != nil {
		published = detail.Run.PublishedWikiVersionID.String()
	}
	return map[string]any{"documentation_run_id": detail.Run.ID.String(), "status": strings.ToLower(string(detail.Run.Status)), "published_wiki_version_id": published}
}

func fallback(value *string, selected string) string {
	if value == nil || *value == "" {
		return selected
	}
	return *value
}

func boundedCorrection(cause error) string {
	// Error text is intentionally excluded: validation details can contain
	// provider-controlled content. The concrete runtime bounds combined
	// instructions to 32,768 bytes when adding its "Correction required" suffix.
	return correctionInstruction
}

func deterministicWikiVersionID(runID RunID) WikiVersionID {
	namespace := ID{0xdc, 0x25, 0x9c, 0x57, 0x30, 0x6e, 0x4e, 0xed, 0xa9, 0xd6, 0xc0, 0x68, 0x9c, 0xe2, 0xf9, 0x0b}
	digest := sha1.Sum(append(namespace[:], []byte(runID.String())...))
	var id ID
	copy(id[:], digest[:16])
	id[6] = id[6]&0x0f | 0x50
	id[8] = id[8]&0x3f | 0x80
	return WikiVersionID(id)
}
