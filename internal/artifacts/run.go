package artifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"unicode/utf8"
)

type RunStore struct {
	root artifactRoot
}

func NewRunStore(dataRoot string) (*RunStore, error) {
	root, err := newArtifactRoot(dataRoot)
	if err != nil {
		return nil, err
	}
	return &RunStore{root: root}, nil
}

func (store *RunStore) SavePage(knowledgeBaseID, runID ID, page Page) error {
	if !utf8.ValidString(page.Slug) ||
		!utf8.ValidString(page.Title) ||
		!utf8.ValidString(page.Description) ||
		!utf8.ValidString(page.PageType) ||
		!utf8.ValidString(page.Markdown) {
		return validationError("accepted page metadata is invalid")
	}
	slug, err := NormalizePageSlug(page.Slug)
	if err != nil {
		return err
	}
	page.ClaimsJSON = bytes.Clone(page.ClaimsJSON)
	base := store.runBase(knowledgeBaseID, runID)
	if err := store.root.mkdir(base); err != nil {
		return err
	}
	runLock, err := artifactOpenLock(filepath.Join(base, ".page-snapshots.lock"))
	if err != nil {
		return err
	}
	defer runLock.Close()
	if err := artifactLock(runLock, false); err != nil {
		return err
	}
	defer artifactUnlock(runLock)
	directory := filepath.Join(base, "page-snapshots")
	if err := store.root.mkdir(directory); err != nil {
		return err
	}
	lockDirectory := filepath.Join(base, ".page-locks")
	if err := store.root.mkdir(lockDirectory); err != nil {
		return err
	}
	lockName := sha256.Sum256([]byte(slug))
	pageLock, err := artifactOpenLock(filepath.Join(lockDirectory, hex.EncodeToString(lockName[:])+".lock"))
	if err != nil {
		return err
	}
	defer pageLock.Close()
	if err := artifactLock(pageLock, true); err != nil {
		return err
	}
	defer artifactUnlock(pageLock)
	metadata, err := runPageMetadata(page)
	if err != nil {
		return validationError("accepted page metadata is invalid")
	}
	writes := []struct {
		path    string
		content []byte
	}{
		{filepath.Join(directory, filepath.FromSlash(slug)+".md"), []byte(page.Markdown)},
		{filepath.Join(directory, ".claims", filepath.FromSlash(slug)+".json"), page.ClaimsJSON},
		{filepath.Join(directory, ".metadata", filepath.FromSlash(slug)+".json"), metadata},
	}
	for _, write := range writes {
		if err := store.root.mkdir(filepath.Dir(write.path)); err != nil {
			return err
		}
		if err := cleanupAtomicResidue(write.path); err != nil {
			return err
		}
	}
	for _, write := range writes {
		info, exists, statErr := artifactLstat(write.path)
		if statErr != nil {
			return statErr
		}
		if exists && (info.Mode()&os.ModeSymlink != 0 ||
			!info.Mode().IsRegular() ||
			!artifactFilesEqual(write.path, write.content)) {
			return immutablePageError()
		}
	}
	for _, write := range writes {
		if _, exists, statErr := artifactLstat(write.path); statErr != nil {
			return statErr
		} else if !exists {
			if err := store.atomicCreate(write.path, write.content); err != nil {
				return err
			}
		}
	}
	return artifactFsyncDirectory(directory)
}

func (store *RunStore) LoadPage(knowledgeBaseID, runID ID, rawSlug string) (Page, error) {
	slug, err := NormalizePageSlug(rawSlug)
	if err != nil {
		return Page{}, err
	}
	directory := filepath.Join(store.runBase(knowledgeBaseID, runID), "page-snapshots")
	if !store.root.contains(directory) {
		return Page{}, validationError("run artifact path escaped its data root")
	}
	markdownPath := filepath.Join(directory, filepath.FromSlash(slug)+".md")
	claimsPath := filepath.Join(directory, ".claims", filepath.FromSlash(slug)+".json")
	metadataPath := filepath.Join(directory, ".metadata", filepath.FromSlash(slug)+".json")
	for _, selected := range []string{markdownPath, claimsPath, metadataPath} {
		if err := store.root.rejectSymlinkSegments(selected); err != nil {
			return Page{}, notFoundError("accepted page snapshot is missing")
		}
	}
	markdown, err := artifactReadRegular(
		markdownPath,
		"accepted page snapshot is missing",
	)
	if err != nil {
		return Page{}, err
	}
	claims, err := artifactReadRegular(
		claimsPath,
		"accepted page snapshot is missing",
	)
	if err != nil {
		return Page{}, err
	}
	metadataBytes, err := artifactReadRegular(
		metadataPath,
		"accepted page snapshot is missing",
	)
	if err != nil {
		return Page{}, err
	}
	metadata, err := parseRunPageMetadata(metadataBytes)
	if err != nil || metadata.Slug != slug {
		return Page{}, validationError("accepted page metadata is invalid")
	}
	contentDigest := sha256.Sum256(markdown)
	claimsDigest := sha256.Sum256(claims)
	if metadata.ContentSHA256 != hex.EncodeToString(contentDigest[:]) ||
		metadata.ClaimsSHA256 != hex.EncodeToString(claimsDigest[:]) {
		return Page{}, validationError("accepted page snapshot hash does not match")
	}
	if !utf8.Valid(markdown) {
		return Page{}, validationError("accepted page metadata is invalid")
	}
	return Page{
		Slug:          slug,
		Title:         metadata.Title,
		Description:   metadata.Description,
		PageType:      metadata.PageType,
		Markdown:      string(markdown),
		ContentSHA256: contentDigest,
		ClaimsJSON:    bytes.Clone(claims),
		ClaimsSHA256:  claimsDigest,
	}, nil
}

func (store *RunStore) DiscardRun(knowledgeBaseID, runID ID) error {
	base := store.runBase(knowledgeBaseID, runID)
	exists, err := store.root.existingDirectory(base)
	if err != nil || !exists {
		return err
	}
	runLock, err := artifactOpenLock(filepath.Join(base, ".page-snapshots.lock"))
	if err != nil {
		return err
	}
	defer runLock.Close()
	if err := artifactLock(runLock, true); err != nil {
		return err
	}
	defer artifactUnlock(runLock)
	directory := filepath.Join(base, "page-snapshots")
	info, exists, err := artifactLstat(directory)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return validationError("run artifact path is invalid")
	}
	if err := artifactRemoveTree(directory); err != nil {
		return err
	}
	return artifactFsyncDirectory(base)
}

func (store *RunStore) runBase(knowledgeBaseID, runID ID) string {
	return filepath.Join(
		store.root.path,
		"knowledge-bases",
		knowledgeBaseID.String(),
		"runs",
		runID.String(),
	)
}

func (store *RunStore) atomicCreate(path string, content []byte) error {
	parent := filepath.Dir(path)
	if err := store.root.mkdir(parent); err != nil {
		return err
	}
	prefix := "." + filepath.Base(path) + "."
	temporary, err := os.CreateTemp(parent, prefix)
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}()
	if err := artifactWriteAll(temporary, content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryName, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if !artifactFilesEqual(path, content) {
			return immutablePageError()
		}
	}
	return artifactFsyncDirectory(parent)
}

func cleanupAtomicResidue(path string) error {
	parent := filepath.Dir(path)
	prefix := "." + filepath.Base(path) + "."
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if len(entry.Name()) <= len(prefix) || entry.Name()[:len(prefix)] != prefix {
			continue
		}
		candidate := filepath.Join(parent, entry.Name())
		info, err := os.Lstat(candidate)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return immutablePageError()
		}
		if err := os.Remove(candidate); err != nil {
			return err
		}
	}
	return artifactFsyncDirectory(parent)
}

type runPageMetadataValue struct {
	ClaimsSHA256  string `json:"claims_sha256"`
	ContentSHA256 string `json:"content_sha256"`
	Description   string `json:"description"`
	Format        string `json:"format"`
	PageType      string `json:"page_type"`
	Slug          string `json:"slug"`
	Title         string `json:"title"`
}

func runPageMetadata(page Page) ([]byte, error) {
	value := runPageMetadataValue{
		ClaimsSHA256:  hex.EncodeToString(page.ClaimsSHA256[:]),
		ContentSHA256: hex.EncodeToString(page.ContentSHA256[:]),
		Description:   page.Description,
		Format:        "ref0-accepted-page/v1",
		PageType:      page.PageType,
		Slug:          page.Slug,
		Title:         page.Title,
	}
	return marshalDeterministicJSON(value)
}

func parseRunPageMetadata(encoded []byte) (runPageMetadataValue, error) {
	var value runPageMetadataValue
	if !utf8.Valid(encoded) || json.Unmarshal(encoded, &value) != nil {
		return value, errors.New("invalid metadata")
	}
	if value.Slug == "" || value.Title == "" || value.Description == "" || value.PageType == "" {
		return value, errors.New("invalid metadata")
	}
	return value, nil
}
