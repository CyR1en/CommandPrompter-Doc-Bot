package artifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cyr1en/ref0/internal/knowledgebases"
)

func TestKnowledgeBasePurgerRemovesOnlySelectedArtifactsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	purger, err := NewKnowledgeBasePurger(root)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeBaseID := purgeTestID(t, "10000000-0000-4000-8000-000000000001")
	sourceID := purgeTestID(t, "20000000-0000-4000-8000-000000000002")
	retainedID := purgeTestID(t, "30000000-0000-4000-8000-000000000003")
	selected := []string{
		filepath.Join(root, "knowledge-bases", knowledgeBaseID.String(), "wiki", "page.md"),
		filepath.Join(root, "sources", sourceID.String(), "revisions", "content.txt"),
	}
	retained := filepath.Join(root, "sources", retainedID.String(), "revisions", "content.txt")
	for _, path := range append(selected, retained) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := purger.Purge(context.Background(), knowledgeBaseID, []knowledgebases.ID{sourceID}); err != nil {
		t.Fatal(err)
	}
	if err := purger.Purge(context.Background(), knowledgeBaseID, []knowledgebases.ID{sourceID}); err != nil {
		t.Fatalf("idempotent purge: %v", err)
	}
	for _, path := range selected {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("selected artifact remains at %s: %v", path, err)
		}
	}
	if content, err := os.ReadFile(retained); err != nil || string(content) != "artifact" {
		t.Fatalf("unselected artifact changed: %q, %v", content, err)
	}
}

func TestKnowledgeBasePurgerRejectsEscapingSymlinksWithoutFollowingTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sources")); err != nil {
		t.Fatal(err)
	}
	purger, err := NewKnowledgeBasePurger(root)
	if err != nil {
		t.Fatal(err)
	}
	id := purgeTestID(t, "10000000-0000-4000-8000-000000000001")
	if err := purger.Purge(context.Background(), id, []knowledgebases.ID{id}); !errors.Is(err, ErrValidation) {
		t.Fatalf("escaping source directory error=%v", err)
	}
	if content, err := os.ReadFile(outsideFile); err != nil || string(content) != "outside" {
		t.Fatalf("outside target changed: %q, %v", content, err)
	}

	if err := os.Remove(filepath.Join(root, "sources")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "sources", id.String())
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := purger.Purge(context.Background(), id, []knowledgebases.ID{id}); err != nil {
		t.Fatalf("target symlink purge: %v", err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target symlink remains: %v", err)
	}
	if content, err := os.ReadFile(outsideFile); err != nil || string(content) != "outside" {
		t.Fatalf("target symlink was followed: %q, %v", content, err)
	}
}

func TestKnowledgeBasePurgerValidatesRootAndHonorsCancellation(t *testing.T) {
	if _, err := NewKnowledgeBasePurger("relative"); !errors.Is(err, ErrValidation) {
		t.Fatalf("relative root error=%v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	purger, err := NewKnowledgeBasePurger(missing)
	if err != nil {
		t.Fatal(err)
	}
	if err := purger.Purge(context.Background(), knowledgebases.ID{}, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing root error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := purger.Purge(ctx, knowledgebases.ID{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled purge error=%v", err)
	}

	if runtime.GOOS != "windows" {
		root := t.TempDir()
		link := filepath.Join(t.TempDir(), "root-link")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		linked, err := NewKnowledgeBasePurger(link)
		if err != nil {
			t.Fatal(err)
		}
		if err := linked.Purge(context.Background(), knowledgebases.ID{}, nil); !errors.Is(err, ErrValidation) {
			t.Fatalf("symlink root error=%v", err)
		}
	}
}

func purgeTestID(t *testing.T, value string) knowledgebases.ID {
	t.Helper()
	id, err := knowledgebases.ParseID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
