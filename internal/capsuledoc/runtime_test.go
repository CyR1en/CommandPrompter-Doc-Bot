package capsuledoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/capsule"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/providers"
)

type staticResolver struct {
	root string
	err  error
	keys []string
}

func (resolver *staticResolver) ResolveArtifactKey(key string) (string, error) {
	resolver.keys = append(resolver.keys, key)
	return resolver.root, resolver.err
}

type fakeProviderReader struct{}

func (fakeProviderReader) GetProfile(context.Context, providers.ProfileID) (providers.Profile, error) {
	return providers.Profile{}, errors.New("unused")
}

func (fakeProviderReader) GetEndpoint(context.Context, providers.EndpointID) (providers.Endpoint, error) {
	return providers.Endpoint{}, errors.New("unused")
}

type sessionResult struct {
	invocation capsule.Invocation
	err        error
}

type scriptedFactory struct {
	results       []sessionResult
	roles         []capsule.Role
	systemPrompts []string
	prompts       []string
	toolCounts    []int
}

type scriptedSession struct {
	factory *scriptedFactory
	index   int
}

func (factory *scriptedFactory) NewSession(role capsule.Role, systemPrompt string, tools []capsule.Tool, _ map[string]any) (modelSession, error) {
	index := len(factory.roles)
	if index >= len(factory.results) {
		return nil, errors.New("unexpected session")
	}
	factory.roles = append(factory.roles, role)
	factory.systemPrompts = append(factory.systemPrompts, systemPrompt)
	factory.toolCounts = append(factory.toolCounts, len(tools))
	return &scriptedSession{factory: factory, index: index}, nil
}

func (session *scriptedSession) Invoke(_ context.Context, prompt string) (capsule.Invocation, error) {
	session.factory.prompts = append(session.factory.prompts, prompt)
	result := session.factory.results[session.index]
	return result.invocation, result.err
}

func testID(t *testing.T, value string) docgen.ID {
	t.Helper()
	id, err := docgen.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func testDetail(t *testing.T, root string) (docgen.RunDetail, *Runtime) {
	t.Helper()
	sourceID := testID(t, "11111111-1111-4111-8111-111111111111")
	revisionID := testID(t, "22222222-2222-4222-8222-222222222222")
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "service.py"), []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := &staticResolver{root: root}
	detail := docgen.RunDetail{Run: docgen.Run{
		Instructions: "Document supported behavior", Language: "en",
		Sources: []docgen.CapturedSource{{
			SourceID: sourceID, RevisionID: revisionID, Commit: strings.Repeat("a", 40), Kind: "REPOSITORY",
		}},
		Models: []docgen.CapturedModel{{Role: providers.DocumentationPlanner, MaxConcurrentTasks: 1}, {Role: providers.DocumentationWriter, MaxConcurrentTasks: 1}},
	}}
	runtime := &Runtime{
		sources: resolver, applicationVersion: "test", options: DefaultOptions(),
	}
	return detail, runtime
}

func validPlanOutput(sourceID string) map[string]any {
	return map[string]any{"pages": []any{map[string]any{
		"slug": "overview", "title": "Overview", "purpose": "Explain the system",
		"related_pages": []any{}, "source_seed_paths": []any{map[string]any{"source_id": sourceID, "path": "app/service.py"}},
	}}}
}

func testWebsiteDetail(t *testing.T, root string) (docgen.RunDetail, *Runtime, docgen.ID) {
	t.Helper()
	sourceID := testID(t, "33333333-3333-4333-8333-333333333333")
	revisionID := testID(t, "44444444-4444-4444-8444-444444444444")
	commit := strings.Repeat("b", 64)
	if err := os.MkdirAll(filepath.Join(root, "pages"), 0o700); err != nil {
		t.Fatal(err)
	}
	pages := []struct {
		url, path string
	}{
		{"https://docs.example/guide", "pages/guide.md"},
		{"https://docs.example/guide.md", "pages/guide-alternate.md"},
		{"https://docs.example/reference", "pages/reference.md"},
		{"https://docs.example/llms.txt", "pages/llms.md"},
	}
	manifestPages := make([]any, 0, len(pages))
	for _, page := range pages {
		content := page.url + "\n"
		if strings.HasSuffix(page.url, "/llms.txt") {
			content = "- [Guide](https://docs.example/guide.md)\n- [Reference](https://docs.example/reference.md)\n"
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(page.path)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		manifestPages = append(manifestPages, map[string]any{"canonical_url": page.url, "content_path": page.path})
	}
	manifest, err := json.Marshal(map[string]any{"native_version": commit, "pages": manifestPages})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "website-manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	detail := docgen.RunDetail{Run: docgen.Run{
		Instructions: "Preserve the website documentation", Language: "en",
		Sources: []docgen.CapturedSource{{SourceID: sourceID, RevisionID: revisionID, Commit: commit, Kind: "WEBSITE"}},
		Models:  []docgen.CapturedModel{{Role: providers.DocumentationPlanner, MaxConcurrentTasks: 1}, {Role: providers.DocumentationWriter, MaxConcurrentTasks: 1}},
	}}
	return detail, &Runtime{sources: &staticResolver{root: root}, applicationVersion: "test", options: DefaultOptions()}, sourceID
}

func websitePlanOutput(sourceID string, paths ...string) map[string]any {
	seeds := make([]any, 0, len(paths))
	for _, path := range paths {
		seeds = append(seeds, map[string]any{"source_id": sourceID, "path": path})
	}
	return map[string]any{"pages": []any{map[string]any{
		"slug": "guide", "title": "Guide", "purpose": "Document the website",
		"related_pages": []any{}, "source_seed_paths": seeds,
	}}}
}

func TestPlanRejectsIncompleteCanonicalWebsiteCoverage(t *testing.T) {
	detail, runtime, sourceID := testWebsiteDetail(t, t.TempDir())
	planner := &scriptedFactory{results: []sessionResult{
		{invocation: capsule.Invocation{Output: websitePlanOutput(sourceID.String(), "pages/guide-alternate.md"), Usage: capsule.Usage{ModelCalls: 1}}},
		{invocation: capsule.Invocation{Output: websitePlanOutput(sourceID.String(), "pages/guide-alternate.md", "pages/reference.md"), Usage: capsule.Usage{ModelCalls: 1}}},
	}}
	runtime.bind = func(_ context.Context, captured docgen.CapturedModel) (modelFactory, error) {
		if captured.Role == providers.DocumentationPlanner {
			return planner, nil
		}
		return &scriptedFactory{}, nil
	}
	plan, usage, err := runtime.Plan(context.Background(), detail)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelCalls != 2 || len(plan.Pages) != 1 || len(plan.Pages[0].SourceSeedPaths) != 2 {
		t.Fatalf("usage=%+v plan=%+v", usage, plan)
	}
	if len(planner.prompts) != 2 || !strings.Contains(planner.prompts[0], `"required_source_paths"`) ||
		!strings.Contains(planner.prompts[0], "pages/guide-alternate.md") || !strings.Contains(planner.prompts[0], "pages/reference.md") ||
		strings.Contains(planner.prompts[0], `"path": "pages/guide.md"`) || strings.Contains(planner.prompts[0], "pages/llms.md") {
		t.Fatalf("website inventory was not exact: %#v", planner.prompts)
	}
}

func TestWriterRejectsUncitedWebsiteSeed(t *testing.T) {
	detail, runtime, sourceID := testWebsiteDetail(t, t.TempDir())
	guide, _ := docgen.NewSourceSeedPath(sourceID, "pages/guide-alternate.md")
	reference, _ := docgen.NewSourceSeedPath(sourceID, "pages/reference.md")
	target := docgen.PlannedPage{Slug: "guide", Title: "Guide", Purpose: "Document the website", SourceSeedPaths: []docgen.SourceSeedPath{guide, reference}}
	page := docgen.Page{Target: target, CreatedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
	detail.Pages = []docgen.Page{page}
	output := func(paths ...string) map[string]any {
		claims := make([]any, 0, len(paths))
		var markdown strings.Builder
		markdown.WriteString("---\ntype: Concept\ntitle: Guide\ndescription: Complete website guide.\n---\n\n# Guide\n")
		for index, path := range paths {
			claimID := fmt.Sprintf("guide__fact_%d", index+1)
			markdown.WriteString("\nDocumented fact.[^" + claimID + "]\n")
			claims = append(claims, map[string]any{
				"id": claimID, "statement": "Documented fact.",
				"evidence": []any{map[string]any{"id": claimID + "_source", "source_id": sourceID.String(), "path": path, "start_line": 1, "end_line": 1}},
			})
		}
		return map[string]any{"slug": "guide", "markdown": markdown.String(), "claims": claims}
	}
	writer := &scriptedFactory{results: []sessionResult{
		{invocation: capsule.Invocation{Output: output("pages/guide-alternate.md"), Usage: capsule.Usage{ModelCalls: 1}}},
		{invocation: capsule.Invocation{Output: output("pages/guide-alternate.md", "pages/reference.md"), Usage: capsule.Usage{ModelCalls: 1}}},
	}}
	runtime.bind = func(_ context.Context, _ docgen.CapturedModel) (modelFactory, error) { return writer, nil }
	submission, usage, err := runtime.GeneratePage(context.Background(), detail, page, "")
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelCalls != 2 || len(submission.Claims) != 2 || len(writer.prompts) != 2 || !strings.Contains(writer.prompts[1], "structured output was rejected") {
		t.Fatalf("usage=%+v submission=%+v prompts=%#v", usage, submission, writer.prompts)
	}
}

func TestPlanBindsBothModelsBeforeInvokingAndRetriesInvalidOutput(t *testing.T) {
	detail, runtime := testDetail(t, t.TempDir())
	sourceID := detail.Run.Sources[0].SourceID.String()
	invalid := validPlanOutput(sourceID)
	invalid["pages"].([]any)[0].(map[string]any)["related_pages"] = []any{"missing"}
	planner := &scriptedFactory{results: []sessionResult{
		{invocation: capsule.Invocation{Output: invalid, Usage: capsule.Usage{ModelCalls: 1, InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}},
		{invocation: capsule.Invocation{Output: validPlanOutput(sourceID), Usage: capsule.Usage{ModelCalls: 1, InputTokens: 12, OutputTokens: 3, TotalTokens: 15}}},
	}}
	bound := []providers.ModelRole{}
	runtime.bind = func(_ context.Context, captured docgen.CapturedModel) (modelFactory, error) {
		bound = append(bound, captured.Role)
		if captured.Role == providers.DocumentationPlanner {
			return planner, nil
		}
		return &scriptedFactory{}, nil
	}
	plan, usage, err := runtime.Plan(context.Background(), detail)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pages) != 1 || plan.Pages[0].Slug != "overview" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if fmt.Sprint(bound) != "[DOCUMENTATION_PLANNER DOCUMENTATION_WRITER]" {
		t.Fatalf("captured models were not fenced in order: %v", bound)
	}
	if usage != (docgen.ModelUsage{ModelCalls: 2, InputTokens: 22, OutputTokens: 5, TotalTokens: 27}) {
		t.Fatalf("usage was not accumulated: %#v", usage)
	}
	if len(planner.prompts) != 2 || !strings.Contains(planner.prompts[1], "previous structured output was rejected") {
		t.Fatalf("validation correction was not sent: %#v", planner.prompts)
	}
	if planner.roles[0] != capsule.Planner || planner.systemPrompts[0] != plannerSystemPrompt || planner.toolCounts[0] != 4 {
		t.Fatal("planner session did not receive the exact bounded capability set")
	}
}

func TestPlanFailureCarriesUsageOnlyInRuntimeFailure(t *testing.T) {
	detail, runtime := testDetail(t, t.TempDir())
	planner := &scriptedFactory{results: []sessionResult{
		{err: &capsule.InvocationError{Usage: capsule.Usage{ModelCalls: 1, InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}},
		{err: &capsule.InvocationError{Usage: capsule.Usage{ModelCalls: 1, InputTokens: 4, OutputTokens: 1, TotalTokens: 5}}},
	}}
	runtime.bind = func(_ context.Context, captured docgen.CapturedModel) (modelFactory, error) {
		if captured.Role == providers.DocumentationPlanner {
			return planner, nil
		}
		return &scriptedFactory{}, nil
	}
	_, returnedUsage, err := runtime.Plan(context.Background(), detail)
	if returnedUsage != (docgen.ModelUsage{}) {
		t.Fatalf("failure usage was duplicated in the return value: %#v", returnedUsage)
	}
	var failure *docgen.RuntimeFailure
	if !errors.As(err, &failure) || failure.Usage != (docgen.ModelUsage{ModelCalls: 2, InputTokens: 7, OutputTokens: 3, TotalTokens: 10}) {
		t.Fatalf("unexpected runtime failure: %#v %v", failure, err)
	}
	if len(planner.prompts) != 2 || !strings.HasSuffix(planner.prompts[1], missingOutputCorrection) {
		t.Fatal("missing-output retry correction was not exact")
	}
}

func TestGenerateAndValidatePageProjectsCapturedEvidence(t *testing.T) {
	detail, runtime := testDetail(t, t.TempDir())
	target := docgen.PlannedPage{Slug: "overview", Title: "Overview", Purpose: "Explain the system"}
	page := docgen.Page{Target: target, CreatedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
	detail.Pages = []docgen.Page{page}
	sourceID := detail.Run.Sources[0].SourceID.String()
	output := map[string]any{
		"slug":     "overview",
		"markdown": "---\ntype: Concept\ntitle: Overview\ndescription: A concise overview.\n---\n\n# Overview\n\nThe service has behavior.[^overview__behavior]\n",
		"claims": []any{map[string]any{
			"id": "overview__behavior", "statement": "The service has behavior.",
			"evidence": []any{map[string]any{
				"id": "overview__behavior_source", "source_id": sourceID, "path": "app/service.py",
				"start_line": 1, "end_line": 2,
			}},
		}},
	}
	writer := &scriptedFactory{results: []sessionResult{{invocation: capsule.Invocation{
		Output: output, Usage: capsule.Usage{ModelCalls: 1, InputTokens: 8, OutputTokens: 7, TotalTokens: 15},
	}}}}
	runtime.bind = func(_ context.Context, captured docgen.CapturedModel) (modelFactory, error) {
		if captured.Role != providers.DocumentationWriter {
			return nil, errors.New("wrong role")
		}
		return writer, nil
	}
	correction := "The previous page failed deterministic validation. Return a complete corrected page matching the accepted target, Claims, and evidence."
	submission, usage, err := runtime.GeneratePage(context.Background(), detail, page, correction)
	if err != nil {
		t.Fatal(err)
	}
	if usage.TotalTokens != 15 || !strings.Contains(writer.prompts[0], "Correction required: "+correction) {
		t.Fatalf("writer request was not captured correctly: %#v %q", usage, writer.prompts[0])
	}
	validated, err := runtime.ValidatePage(context.Background(), detail, page, submission)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Slug != "overview" || !strings.Contains(validated.Markdown, "[^overview__behavior]: The service has behavior.") {
		t.Fatalf("unexpected validated page: %#v", validated)
	}
}

func TestPageInstructionsTruncateUTF8PrefixToExactBound(t *testing.T) {
	correction := "The previous page failed deterministic validation. Return a complete corrected page matching the accepted target, Claims, and evidence."
	value := pageInstructions(strings.Repeat("é", 20_000), correction)
	if len([]byte(value)) > 32_768 || len([]byte(value)) < 32_767 || !utf8.ValidString(value) || !strings.HasSuffix(value, "\n\nCorrection required: "+correction) {
		t.Fatalf("combined instructions were not bounded: bytes=%d valid=%v", len([]byte(value)), utf8.ValidString(value))
	}
}

func TestNewRuntimeRejectsPoolBeforeTopologyValidation(t *testing.T) {
	slot, err := capsule.NewSlot("one", filepath.Join(t.TempDir(), "capsule.sock"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := capsule.NewSlotPool([]capsule.Slot{slot})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRuntime(fakeProviderReader{}, &staticResolver{root: t.TempDir()}, pool, nil, "test", Options{})
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("unvalidated pool was accepted: %v", err)
	}
}
