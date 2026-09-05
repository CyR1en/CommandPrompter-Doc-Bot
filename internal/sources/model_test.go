package sources

import (
	"strings"
	"testing"

	"github.com/cyr1en/ref0/internal/jobs"
)

func TestPythonSourceModelVectors(t *testing.T) {
	for _, raw := range []string{
		"https://GitHub.COM/OpenAI/codex.git/",
		"https://git.example.test:8443/team/repository.git",
		"https://[2001:db8::1]/team/repository.git",
	} {
		if _, err := ParseRepositoryRemote(raw); err != nil {
			t.Fatalf("valid repository remote %q: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://git.example/repository.git",
		" https://git.example/repository.git",
		"https://git.example/" + strings.Repeat("x", 2048),
		"https://user:secret@git.example/repository.git",
		"https://git.example/repository.git?token=secret",
		"https://git.example/../repository.git",
		"https://git.example/%2e%2e/repository.git",
		"https://bad_host.example/repository.git",
		"https://git.example./repository.git",
		"file:///tmp/repository",
	} {
		if _, err := ParseRepositoryRemote(raw); err == nil {
			t.Fatalf("hostile repository remote accepted: %q", raw)
		}
	}
	for _, value := range []string{"main", "release/2026", "feature.docs"} {
		if _, err := ParseReference(Branch, value); err != nil {
			t.Fatalf("valid branch %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "../main", "main..old", "refs//main", "topic.lock", "bad branch"} {
		if _, err := ParseReference(Branch, value); err == nil {
			t.Fatalf("invalid branch accepted: %q", value)
		}
	}
	commit, err := ParseReference(Commit, strings.Repeat("A", 40))
	if err != nil || commit.Value != strings.Repeat("a", 40) {
		t.Fatalf("commit=%+v err=%v", commit, err)
	}
	for _, pattern := range []string{"[abc].py", "docs/***/file", "../secret", "bad\npath"} {
		config := publicRepository(t)
		config.IncludePatterns = []string{pattern}
		if _, err := config.normalize(); err == nil {
			t.Fatalf("invalid pattern accepted: %q", pattern)
		}
	}
}

func TestPythonRemoteNormalizationHandlesIDNAAndPathRules(t *testing.T) {
	repository, err := ParseRepositoryRemote("https://faß.example/团队/repository///")
	if err != nil || repository.URL != "https://fass.example/团队/repository" || repository.Host != "fass.example" {
		t.Fatalf("repository=%+v err=%v", repository, err)
	}
	website, err := ParseWebsiteRemote("https://bücher.example/a/../product/")
	if err != nil || website.URL != "https://xn--bcher-kva.example/product/" || website.Host != "xn--bcher-kva.example" {
		t.Fatalf("website=%+v err=%v", website, err)
	}
	encoded, err := ParseWebsiteRemote("https://docs.example/%2e%2e/reference")
	if err != nil || encoded.URL != "https://docs.example/%2e%2e/reference" {
		t.Fatalf("encoded website=%+v err=%v", encoded, err)
	}
}

func TestPythonWebsiteConfigurationVectors(t *testing.T) {
	config := publicWebsite(t)
	normalized, err := config.normalize()
	if err != nil || normalized.AcquisitionMode != BuiltinCrawl || normalized.Limits != DefaultCrawlLimits() {
		t.Fatalf("default website config=%+v err=%v", normalized, err)
	}
	tinyID := testID(t, "10000000-0000-4000-8000-000000000001")
	tiny := config
	tiny.AcquisitionMode = TinyFishCrawl
	tiny.TinyFishCredentialID = &tinyID
	if _, err := tiny.normalize(); err != nil {
		t.Fatal(err)
	}
	private := tiny
	private.Privacy = Private
	if _, err := private.normalize(); err == nil {
		t.Fatal("private TinyFish website accepted")
	}
	direct := config
	direct.AcquisitionMode = DirectJSONAPI
	if _, err := direct.normalize(); err == nil {
		t.Fatal("direct JSON API accepted crawl defaults")
	}
	direct.Limits = DefaultCrawlLimits()
	direct.Limits.MaxPages, direct.Limits.MaxDepth = 1, 0
	if _, err := direct.normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleAndArtifactVectors(t *testing.T) {
	if got, err := Transition(Draft, Disabled); err != nil || got != Disabled {
		t.Fatalf("transition=%s err=%v", got, err)
	}
	if _, err := Transition(Removed, Active); !errorsIs(err, ErrTransition) {
		t.Fatalf("removed transition error=%v", err)
	}
	sourceID := testID(t, "10000000-0000-4000-8000-000000000001")
	revisionID := testID(t, "20000000-0000-4000-8000-000000000002")
	want := "sources/10000000-0000-4000-8000-000000000001/snapshots/20000000-0000-4000-8000-000000000002"
	if got := ArtifactKey(sourceID, revisionID); got != want {
		t.Fatalf("artifact key=%q", got)
	}
}

func publicRepository(t *testing.T) RepositoryConfiguration {
	t.Helper()
	name, _ := ParseName("Docs")
	remote, _ := ParseRepositoryRemote("https://git.example/docs.git")
	reference, _ := ParseReference(Branch, "main")
	return RepositoryConfiguration{Name: name, Privacy: Public, Remote: remote, Reference: reference}
}

func publicWebsite(t *testing.T) WebsiteConfiguration {
	t.Helper()
	name, _ := ParseName("Docs site")
	remote, _ := ParseWebsiteRemote("https://docs.example/")
	return WebsiteConfiguration{Name: name, Privacy: Public, Remote: remote}
}

func testID(t *testing.T, raw string) ID {
	t.Helper()
	id, err := jobs.ParseUUID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ID(id)
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		wrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = wrapped.Unwrap()
	}
	return false
}
