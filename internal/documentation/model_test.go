package docgen

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPagePlanDigestAndClosedLinksMatchGoldenValues(t *testing.T) {
	sourceID := mustID(t, "00112233-4455-6677-8899-aabbccddeeff")
	plan := PagePlan{Pages: []PlannedPage{
		{Slug: "architecture/request-flow", Title: "Request flow", Purpose: "Explain request handling.", RelatedPages: []string{"operations"}, SourceSeedPaths: []SourceSeedPath{{SourceID: sourceID, Path: "app/service.py"}}},
		{Slug: "operations", Title: "Operations", Purpose: "Explain runtime operations.", RelatedPages: []string{"architecture/request-flow"}, SourceSeedPaths: []SourceSeedPath{}},
	}}
	digest, err := plan.SemanticDigest()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(digest[:]), "57f8107cd30ac620a690369a4e4f51691bc1a070538fc9d18f8b4878eaec67b2"; got != want {
		t.Fatalf("digest=%s want=%s", got, want)
	}
	if got := plan.Pages[0].SourceSeedPaths[0].VirtualPath(); got != "/sources/00112233-4455-6677-8899-aabbccddeeff/app/service.py" {
		t.Fatalf("virtual path=%q", got)
	}
	broken := plan
	broken.Pages = append([]PlannedPage(nil), plan.Pages...)
	broken.Pages[1].RelatedPages = []string{"missing"}
	if err = broken.Validate(); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("closed link error=%v", err)
	}
	collision := PagePlan{Pages: []PlannedPage{{Slug: "operations", Title: "Operations", Purpose: "Top level."}, {Slug: "operations/deploy", Title: "Deploy", Purpose: "Deploy."}}}
	if err = collision.Validate(); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "files and directories") {
		t.Fatalf("collision error=%v", err)
	}
}

func TestPagePlanDigestDistinguishesLineSeparatorFromLiteralEscape(t *testing.T) {
	plan := PagePlan{Pages: []PlannedPage{{
		Slug: "edge", Title: "Edge", Purpose: "actual:\u2028 literal:\\u2028",
		RelatedPages: []string{}, SourceSeedPaths: []SourceSeedPath{},
	}}}
	digest, err := plan.SemanticDigest()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(digest[:]), "30f83679c0b67b448f69022f39ca85928b2dd7abb134b2d10fd4084b487ba33c"; got != want {
		t.Fatalf("digest=%s want=%s", got, want)
	}
}

func TestEvidenceResourceCanonicalVectors(t *testing.T) {
	sourceID := mustID(t, "00112233-4455-6677-8899-aabbccddeeff")
	start, end := 2, 8
	resource, err := NewEvidenceResource(sourceID, strings.Repeat("A", 40), "docs/request flow.md", &start, &end, "repo")
	if err != nil {
		t.Fatal(err)
	}
	want := "repo://00112233-4455-6677-8899-aabbccddeeff@" + strings.Repeat("a", 40) + "/docs/request%20flow.md#L2-L8"
	if got := resource.Value(); got != want {
		t.Fatalf("resource=%q want=%q", got, want)
	}
	parsed, err := ParseEvidenceResource(want)
	if err != nil || parsed.Value() != want {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
	if _, err = ParseEvidenceResource(strings.Replace(want, "docs/", "%64ocs/", 1)); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("noncanonical error=%v", err)
	}
	web, err := NewEvidenceResource(sourceID, strings.Repeat("b", 64), "https://docs.example/path?q=one", nil, nil, "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(web.Value(), "https%3A%2F%2Fdocs.example%2Fpath%3Fq%3Done") {
		t.Fatalf("web resource=%q", web.Value())
	}
}

type staticSourceReader struct{ content []byte }

func (reader staticSourceReader) ReadSourceFile(context.Context, ID, string) ([]byte, error) {
	return append([]byte(nil), reader.content...), nil
}

func TestValidateConceptPageProjectsVerifiedClaims(t *testing.T) {
	sourceID := mustID(t, "00112233-4455-6677-8899-aabbccddeeff")
	revisionID := mustID(t, "11111111-2222-3333-4444-555555555555")
	var fingerprint [32]byte
	for i := range fingerprint {
		fingerprint[i] = 's'
	}
	start, end := 1, 2
	location := EvidenceLocation{SourceID: sourceID, SourceRevisionID: revisionID, SourceVersion: fingerprint, Commit: strings.Repeat("a", 40), Path: "app/service.py", StartLine: &start, EndLine: &end}
	target := PlannedPage{Slug: "architecture/request-flow", Title: "Request flow", Purpose: "Explain request handling.", RelatedPages: []string{"operations"}}
	claim := Claim{ID: "claim_request_flow", Statement: "The service validates requests.", Evidence: []ClaimEvidence{{ID: "evidence_request_flow", Location: location}}}
	markdown := "---\ntype: Architecture\ntitle: Request flow\ndescription: How requests move through the application.\nproducer_note: preserved\ngenerated: {by: untrusted, at: never}\nsources: [{id: invented, resource: invented}]\n---\n\n# Request flow\n\nThe service validates requests.[^claim_request_flow]\n\n```mermaid\nflowchart LR\n  A[Request] --> B[Validation]\n```\n"
	page, err := ValidateConceptPage(context.Background(), target, PageSubmission{Slug: target.Slug, Markdown: markdown, Claims: []Claim{claim}}, map[ID]CapturedSource{sourceID: {SourceID: sourceID, RevisionID: revisionID, Fingerprint: fingerprint, Commit: strings.Repeat("a", 40), Kind: "REPOSITORY"}}, staticSourceReader{[]byte("first line\nsecond line\n")}, time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"producer_note: preserved", "ref0-doc-platform/1.0.0", "process:claim-validator", "repo://", "[Operations](../operations.md)", "[^claim_request_flow]: The service validates requests.", "```mermaid"} {
		if !strings.Contains(page.Markdown, expected) {
			t.Fatalf("markdown missing %q:\n%s", expected, page.Markdown)
		}
	}
	if strings.Contains(page.Markdown, "untrusted") || strings.Contains(page.Markdown, "invented") {
		t.Fatalf("untrusted provenance survived:\n%s", page.Markdown)
	}
	if got, want := hex.EncodeToString(page.ContentSHA256[:]), "19b600801ed63fa3d96cecd439efa70616732e12aa91f84f0be89a1baed671e4"; got != want {
		t.Fatalf("content digest=%s want=%s\n%s", got, want, page.Markdown)
	}
	if got, want := hex.EncodeToString(page.ClaimsSHA256[:]), "ec80619feb8ee2ae2930b5ac6bba11f08bf48cc0d867b709a79c3cfe0c097da3"; got != want {
		t.Fatalf("claims digest=%s want=%s", got, want)
	}
	wantClaimsPrefix := "{\"claims\":[{\"evidence\":[{\"id\":\"evidence_request_flow\",\"resource\":\"repo://"
	if !strings.HasPrefix(string(page.ClaimsJSON), wantClaimsPrefix) || !strings.HasSuffix(string(page.ClaimsJSON), "}]}\n") {
		t.Fatalf("claims=%s", page.ClaimsJSON)
	}
	invalid := strings.Replace(markdown, "flowchart LR", "not-a-diagram", 1)
	invalidPage, err := ValidateConceptPage(context.Background(), target, PageSubmission{Slug: target.Slug, Markdown: invalid, Claims: []Claim{claim}}, map[ID]CapturedSource{sourceID: {SourceID: sourceID, RevisionID: revisionID, Fingerprint: fingerprint, Commit: strings.Repeat("a", 40), Kind: "REPOSITORY"}}, staticSourceReader{[]byte("first\nsecond\n")}, time.Now(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(invalidPage.Markdown, "```mermaid") || !strings.Contains(invalidPage.Markdown, "Diagram source (not rendered):") {
		t.Fatalf("invalid mermaid=%s", invalidPage.Markdown)
	}
}

func TestDeterministicWikiVersionMatchesUUID5Oracle(t *testing.T) {
	runID := RunID(mustID(t, "11111111-2222-3333-4444-555555555555"))
	if got, want := deterministicWikiVersionID(runID).String(), "b2800bbc-698d-5f14-9adf-aaa0259da748"; got != want {
		t.Fatalf("version=%s want=%s", got, want)
	}
}

func mustID(t *testing.T, raw string) ID {
	t.Helper()
	id, err := ParseID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
