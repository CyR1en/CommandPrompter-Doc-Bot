package docgen

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/jobs"
)

type handlerStoreFake struct {
	detail           RunDetail
	completed        artifacts.Page
	completedUsage   ModelUsage
	publishedVersion WikiVersionID
	failedCode       string
}

func (fake *handlerStoreFake) Prepare(context.Context, ID, jobs.Permit) (RunDetail, error) {
	return fake.detail, nil
}
func (fake *handlerStoreFake) GetRun(context.Context, RunID) (RunDetail, error) {
	return fake.detail, nil
}
func (fake *handlerStoreFake) AcceptPlan(_ context.Context, _ RunID, _ PagePlan, _ jobs.Permit, usage ModelUsage) (RunDetail, error) {
	fake.detail.Run.Status = RunGenerating
	fake.detail.Run.PlannerUsage = usage
	return fake.detail, nil
}
func (fake *handlerStoreFake) BeginPage(context.Context, Page, jobs.Permit) (RunDetail, error) {
	fake.detail.Pages[0].Status = PageRunning
	return fake.detail, nil
}
func (fake *handlerStoreFake) CompletePage(_ context.Context, _ Page, page artifacts.Page, _ jobs.Permit, usage ModelUsage) (RunDetail, error) {
	fake.completed = page
	fake.completedUsage = usage
	fake.detail.Pages[0].Status = PageComplete
	return fake.detail, nil
}
func (fake *handlerStoreFake) SkipPage(_ context.Context, _ Page, code string, _ jobs.Permit, _ ModelUsage) (RunDetail, error) {
	fake.failedCode = code
	fake.detail.Pages[0].Status = PageSkipped
	return fake.detail, nil
}
func (fake *handlerStoreFake) BeginFinalization(context.Context, RunID, jobs.Permit) (RunDetail, error) {
	return fake.detail, nil
}
func (fake *handlerStoreFake) Publish(_ context.Context, _ RunID, version WikiVersionID, _ artifacts.PublishedWikiBundle, _ []artifacts.Page, _ jobs.Permit) (RunDetail, error) {
	fake.publishedVersion = version
	fake.detail.Run.Status = RunPublished
	fake.detail.Run.PublishedWikiVersionID = &version
	return fake.detail, nil
}
func (fake *handlerStoreFake) FailRun(_ context.Context, _ RunID, code string, _ jobs.Permit, _ ModelUsage) (RunDetail, error) {
	fake.failedCode = code
	fake.detail.Run.Status = RunFailed
	fake.detail.Run.SanitizedError = &code
	return fake.detail, nil
}

type correctingRuntime struct {
	calls       int
	corrections []string
	accepted    artifacts.Page
}

func (runtime *correctingRuntime) Plan(context.Context, RunDetail) (PagePlan, ModelUsage, error) {
	return PagePlan{}, ModelUsage{}, errors.New("unused")
}
func (runtime *correctingRuntime) GeneratePage(_ context.Context, _ RunDetail, page Page, correction string) (PageSubmission, ModelUsage, error) {
	runtime.calls++
	runtime.corrections = append(runtime.corrections, correction)
	return PageSubmission{Slug: page.Target.Slug, Markdown: "# draft", Claims: []Claim{}}, ModelUsage{ModelCalls: 1, InputTokens: 2, OutputTokens: 3, TotalTokens: 5}, nil
}
func (runtime *correctingRuntime) ValidatePage(context.Context, RunDetail, Page, PageSubmission) (artifacts.Page, error) {
	if runtime.calls == 1 {
		return artifacts.Page{}, validation("page title does not match the accepted plan")
	}
	return runtime.accepted, nil
}

func TestGeneratePageRetriesOneDeterministicCorrectionAndPersistsUsage(t *testing.T) {
	root := t.TempDir()
	runArtifacts, err := artifacts.NewRunStore(root)
	if err != nil {
		t.Fatal(err)
	}
	wikiArtifacts, err := artifacts.NewWikiStore(root)
	if err != nil {
		t.Fatal(err)
	}
	runID := RunID(mustID(t, "11111111-2222-3333-4444-555555555555"))
	pageID := PageID(mustID(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"))
	jobID := jobs.JobID(mustID(t, "99999999-8888-4777-8666-555555555555"))
	kbID := mustID(t, "00112233-4455-6677-8899-aabbccddeeff")
	target := PlannedPage{Slug: "overview", Title: "Overview", Purpose: "Explain the system."}
	page := Page{ID: pageID, RunID: runID, JobID: jobID, Target: target, Status: PagePending}
	store := &handlerStoreFake{detail: RunDetail{Run: Run{ID: runID, KnowledgeBaseID: kbID, Status: RunGenerating}, Pages: []Page{page}}}
	claims := []byte("{\"claims\":[]}\n")
	accepted := artifacts.Page{Slug: "overview", Title: "Overview", Description: "System overview.", PageType: "Concept", Markdown: "# Overview\n", ContentSHA256: sha256.Sum256([]byte("# Overview\n")), ClaimsJSON: claims, ClaimsSHA256: sha256.Sum256(claims)}
	runtime := &correctingRuntime{accepted: accepted}
	handlers, err := NewHandlers(store, runtime, runArtifacts, wikiArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	command := jobs.Command{Type: jobs.GeneratePage, TargetType: "documentation_page", TargetID: jobs.UUID(pageID), Payload: map[string]any{"run_id": runID.String(), "page_id": pageID.String()}}
	result, err := handlers.GeneratePage(context.Background(), command, jobs.Permit{JobID: jobID, WorkerID: "worker", LeaseGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 2 || len(runtime.corrections) != 2 || runtime.corrections[0] != "" || runtime.corrections[1] != correctionInstruction {
		t.Fatalf("corrections=%q", runtime.corrections)
	}
	if store.completed.Slug != "overview" || store.completedUsage != (ModelUsage{ModelCalls: 2, InputTokens: 4, OutputTokens: 6, TotalTokens: 10}) {
		t.Fatalf("page=%+v usage=%+v", store.completed, store.completedUsage)
	}
	if result["status"] != "generating" {
		t.Fatalf("result=%v", result)
	}
	loaded, err := runArtifacts.LoadPage(artifacts.ID(kbID), artifacts.ID(runID), "overview")
	if err != nil || loaded.Markdown != accepted.Markdown {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

type unusedRuntime struct{}

func (unusedRuntime) Plan(context.Context, RunDetail) (PagePlan, ModelUsage, error) {
	return PagePlan{}, ModelUsage{}, errors.New("unused")
}
func (unusedRuntime) GeneratePage(context.Context, RunDetail, Page, string) (PageSubmission, ModelUsage, error) {
	return PageSubmission{}, ModelUsage{}, errors.New("unused")
}
func (unusedRuntime) ValidatePage(context.Context, RunDetail, Page, PageSubmission) (artifacts.Page, error) {
	return artifacts.Page{}, errors.New("unused")
}

func TestFinalizePublishesDeterministicVersionThroughArtifactBoundary(t *testing.T) {
	root := t.TempDir()
	runArtifacts, err := artifacts.NewRunStore(root)
	if err != nil {
		t.Fatal(err)
	}
	wikiArtifacts, err := artifacts.NewWikiStore(root)
	if err != nil {
		t.Fatal(err)
	}
	runID := RunID(mustID(t, "11111111-2222-3333-4444-555555555555"))
	pageID := PageID(mustID(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"))
	jobID := jobs.JobID(mustID(t, "99999999-8888-4777-8666-555555555555"))
	kbID := mustID(t, "00112233-4455-6677-8899-aabbccddeeff")
	claims := []byte("{\"claims\":[]}\n")
	accepted := artifacts.Page{Slug: "overview", Title: "Overview", Description: "System overview.", PageType: "Concept", Markdown: "# Overview\n", ContentSHA256: sha256.Sum256([]byte("# Overview\n")), ClaimsJSON: claims, ClaimsSHA256: sha256.Sum256(claims)}
	if err = runArtifacts.SavePage(artifacts.ID(kbID), artifacts.ID(runID), accepted); err != nil {
		t.Fatal(err)
	}
	store := &handlerStoreFake{detail: RunDetail{Run: Run{ID: runID, KnowledgeBaseID: kbID, Status: RunFinalizing}, Pages: []Page{{ID: pageID, RunID: runID, JobID: jobID, Target: PlannedPage{Slug: "overview", Title: "Overview", Purpose: "Explain."}, Status: PageComplete}}}}
	handlers, err := NewHandlers(store, unusedRuntime{}, runArtifacts, wikiArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	command := jobs.Command{Type: jobs.FinalizeRun, TargetType: "documentation_run", TargetID: jobs.UUID(runID), Payload: map[string]any{"run_id": runID.String()}}
	result, err := handlers.Finalize(context.Background(), command, jobs.Permit{JobID: jobID, WorkerID: "worker", LeaseGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := store.publishedVersion.String(), "b2800bbc-698d-5f14-9adf-aaa0259da748"; got != want {
		t.Fatalf("version=%s want=%s", got, want)
	}
	if result["status"] != "published" || result["published_wiki_version_id"] != store.publishedVersion.String() {
		t.Fatalf("result=%v", result)
	}
}
