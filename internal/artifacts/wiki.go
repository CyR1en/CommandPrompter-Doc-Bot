package artifacts

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const maximumWikiFiles = 2000

var (
	claimIDPattern      = regexp.MustCompile(`\A[a-z][a-z0-9_-]{0,127}\z`)
	markdownLinkPattern = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
)

type WikiStore struct {
	root artifactRoot
}

func NewWikiStore(dataRoot string) (*WikiStore, error) {
	root, err := newArtifactRoot(dataRoot)
	if err != nil {
		return nil, err
	}
	return &WikiStore{root: root}, nil
}

func WikiArtifactKey(knowledgeBaseID, wikiVersionID ID) string {
	return "knowledge-bases/" + knowledgeBaseID.String() + "/wiki/" + wikiVersionID.String()
}

func (store *WikiStore) Publish(
	knowledgeBaseID ID,
	runID ID,
	wikiVersionID ID,
	pages []Page,
	sourceRevisions []SourceRevision,
) (PublishedWikiBundle, error) {
	selected, revisions, err := validateWikiInputs(pages, sourceRevisions)
	if err != nil {
		return PublishedWikiBundle{}, err
	}
	parent := store.wikiParent(knowledgeBaseID)
	if err := store.root.mkdir(parent); err != nil {
		return PublishedWikiBundle{}, err
	}
	lock, err := store.openVersionLock(parent, wikiVersionID)
	if err != nil {
		return PublishedWikiBundle{}, err
	}
	defer lock.Close()
	defer artifactUnlock(lock)
	if err := store.cleanupWikiStaging(parent, wikiVersionID); err != nil {
		return PublishedWikiBundle{}, err
	}
	staging, err := os.MkdirTemp(parent, "."+wikiVersionID.String()+".stage.")
	if err != nil {
		return PublishedWikiBundle{}, err
	}
	defer artifactRemoveTree(staging)
	if err := store.writeBundle(staging, runID, selected, revisions); err != nil {
		return PublishedWikiBundle{}, err
	}
	manifest, err := artifactReadRegular(
		filepath.Join(staging, ".page-manifest.json"),
		"wiki file does not exist",
	)
	if err != nil {
		return PublishedWikiBundle{}, err
	}
	digest := sha256.Sum256(manifest)
	destination := store.wikiPath(knowledgeBaseID, wikiVersionID)
	if _, exists, err := artifactLstat(destination); err != nil {
		return PublishedWikiBundle{}, err
	} else if exists {
		if err := assertSameBundle(staging, destination); err != nil {
			return PublishedWikiBundle{}, err
		}
		return PublishedWikiBundle{
			ArtifactKey:    WikiArtifactKey(knowledgeBaseID, wikiVersionID),
			ManifestSHA256: digest,
			PageCount:      len(selected),
		}, nil
	}
	if err := os.Rename(staging, destination); err != nil {
		return PublishedWikiBundle{}, err
	}
	if err := artifactFsyncDirectory(parent); err != nil {
		return PublishedWikiBundle{}, err
	}
	return PublishedWikiBundle{
		ArtifactKey:    WikiArtifactKey(knowledgeBaseID, wikiVersionID),
		ManifestSHA256: digest,
		PageCount:      len(selected),
	}, nil
}

func (store *WikiStore) Validate(
	knowledgeBaseID ID,
	runID ID,
	wikiVersionID ID,
	pages []Page,
	sourceRevisions []SourceRevision,
	expectedManifestSHA256 []byte,
) (PublishedWikiBundle, error) {
	if len(pages) == 0 || len(expectedManifestSHA256) != sha256.Size {
		return PublishedWikiBundle{}, validationError("retained wiki expectation is invalid")
	}
	selected, revisions, err := validateWikiInputs(pages, sourceRevisions)
	if err != nil {
		return PublishedWikiBundle{}, err
	}
	parent := store.wikiParent(knowledgeBaseID)
	if err := store.root.mkdir(parent); err != nil {
		return PublishedWikiBundle{}, err
	}
	lock, err := store.openVersionLock(parent, wikiVersionID)
	if err != nil {
		return PublishedWikiBundle{}, err
	}
	defer lock.Close()
	defer artifactUnlock(lock)
	if err := store.cleanupWikiStaging(parent, wikiVersionID); err != nil {
		return PublishedWikiBundle{}, err
	}
	destination := store.wikiPath(knowledgeBaseID, wikiVersionID)
	if _, exists, err := artifactLstat(destination); err != nil {
		return PublishedWikiBundle{}, err
	} else if !exists {
		return PublishedWikiBundle{}, notFoundError("wiki version does not exist")
	}
	staging, err := os.MkdirTemp(parent, "."+wikiVersionID.String()+".validate.")
	if err != nil {
		return PublishedWikiBundle{}, err
	}
	defer artifactRemoveTree(staging)
	if err := store.writeBundle(staging, runID, selected, revisions); err != nil {
		return PublishedWikiBundle{}, err
	}
	manifest, err := artifactReadRegular(
		filepath.Join(staging, ".page-manifest.json"),
		"wiki file does not exist",
	)
	if err != nil {
		return PublishedWikiBundle{}, err
	}
	digest := sha256.Sum256(manifest)
	if subtle.ConstantTimeCompare(digest[:], expectedManifestSHA256) != 1 {
		return PublishedWikiBundle{}, validationError("retained wiki manifest is stale")
	}
	if err := assertSameBundle(staging, destination); err != nil {
		return PublishedWikiBundle{}, err
	}
	return PublishedWikiBundle{
		ArtifactKey:    WikiArtifactKey(knowledgeBaseID, wikiVersionID),
		ManifestSHA256: digest,
		PageCount:      len(selected),
	}, nil
}

func (store *WikiStore) ReadPage(knowledgeBaseID, wikiVersionID ID, rawSlug string) ([]byte, error) {
	slug, err := NormalizePageSlug(rawSlug)
	if err != nil {
		return nil, err
	}
	return store.readWikiFile(
		knowledgeBaseID,
		wikiVersionID,
		filepath.FromSlash(slug)+".md",
	)
}

func (store *WikiStore) ReadClaims(knowledgeBaseID, wikiVersionID ID, rawSlug string) ([]byte, error) {
	slug, err := NormalizePageSlug(rawSlug)
	if err != nil {
		return nil, err
	}
	return store.readWikiFile(
		knowledgeBaseID,
		wikiVersionID,
		filepath.Join(".claims", filepath.FromSlash(slug)+".json"),
	)
}

func (store *WikiStore) ExportZIP(
	knowledgeBaseID ID,
	wikiVersionID ID,
	destination string,
) (returnErr error) {
	root := store.wikiPath(knowledgeBaseID, wikiVersionID)
	if err := store.root.rejectSymlinkSegments(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notFoundError("wiki version does not exist")
		}
		return validationError("wiki bundle contains a symlink")
	}
	files, err := bundleFiles(root)
	if err != nil {
		if errors.Is(err, ErrWikiDifferentContent) {
			return notFoundError("wiki version does not exist")
		}
		return err
	}
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(output)
	defer func() {
		if err := archive.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
		if err := output.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	paths := make([]string, 0, len(files))
	for relative := range files {
		paths = append(paths, relative)
	}
	slices.Sort(paths)
	for _, relative := range paths {
		content, err := artifactReadRegular(files[relative], "wiki file does not exist")
		if err != nil {
			return err
		}
		writer, err := archive.CreateHeader(&zip.FileHeader{
			Name:   relative,
			Method: zip.Deflate,
		})
		if err != nil {
			return err
		}
		if _, err := writer.Write(content); err != nil {
			return err
		}
	}
	return nil
}

func (store *WikiStore) Discard(knowledgeBaseID, wikiVersionID ID) error {
	parent := store.wikiParent(knowledgeBaseID)
	exists, err := store.root.existingDirectory(parent)
	if err != nil || !exists {
		return err
	}
	lock, err := store.openVersionLock(parent, wikiVersionID)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer artifactUnlock(lock)
	if err := store.cleanupWikiStaging(parent, wikiVersionID); err != nil {
		return err
	}
	root := store.wikiPath(knowledgeBaseID, wikiVersionID)
	info, exists, err := artifactLstat(root)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return validationError("wiki artifact path is invalid")
	}
	if err := artifactRemoveTree(root); err != nil {
		return err
	}
	return artifactFsyncDirectory(parent)
}

func (store *WikiStore) wikiParent(knowledgeBaseID ID) string {
	return filepath.Join(
		store.root.path,
		"knowledge-bases",
		knowledgeBaseID.String(),
		"wiki",
	)
}

func (store *WikiStore) wikiPath(knowledgeBaseID, wikiVersionID ID) string {
	return filepath.Join(store.wikiParent(knowledgeBaseID), wikiVersionID.String())
}

func (store *WikiStore) openVersionLock(parent string, wikiVersionID ID) (*os.File, error) {
	descriptor, err := artifactOpenLock(
		filepath.Join(parent, "."+wikiVersionID.String()+".publishing.lock"),
	)
	if err != nil {
		return nil, err
	}
	if err := artifactLock(descriptor, true); err != nil {
		_ = descriptor.Close()
		return nil, err
	}
	return descriptor, nil
}

func (store *WikiStore) cleanupWikiStaging(parent string, wikiVersionID ID) error {
	prefix := "." + wikiVersionID.String() + "."
	lockName := prefix + "publishing.lock"
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == lockName || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		selected := filepath.Join(parent, entry.Name())
		info, err := os.Lstat(selected)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return validationError("artifact staging path is invalid")
		}
		if err := artifactRemoveTree(selected); err != nil {
			return err
		}
	}
	return artifactFsyncDirectory(parent)
}

func (store *WikiStore) readWikiFile(
	knowledgeBaseID ID,
	wikiVersionID ID,
	relative string,
) ([]byte, error) {
	selected := filepath.Join(store.wikiPath(knowledgeBaseID, wikiVersionID), relative)
	if !store.root.contains(selected) || store.root.rejectSymlinkSegments(selected) != nil {
		return nil, notFoundError("wiki file does not exist")
	}
	return artifactReadRegular(selected, "wiki file does not exist")
}

func validateWikiInputs(
	pages []Page,
	sourceRevisions []SourceRevision,
) ([]Page, []SourceRevision, error) {
	if len(pages) == 0 {
		return nil, nil, validationError("a wiki must contain at least one page")
	}
	selected := make([]Page, len(pages))
	observedSlugs := make(map[string]struct{}, len(pages))
	for index, page := range pages {
		if !utf8.ValidString(page.Markdown) ||
			!utf8.ValidString(page.Title) ||
			!utf8.ValidString(page.Description) ||
			!utf8.ValidString(page.PageType) {
			return nil, nil, validationError("wiki page is invalid")
		}
		slug, err := NormalizePageSlug(page.Slug)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := observedSlugs[slug]; exists {
			return nil, nil, validationError("wiki page slugs must be unique")
		}
		observedSlugs[slug] = struct{}{}
		page.Slug = slug
		page.ClaimsJSON = bytes.Clone(page.ClaimsJSON)
		selected[index] = page
	}
	for slug := range observedSlugs {
		parts := strings.Split(slug, "/")
		for depth := 1; depth < len(parts); depth++ {
			if _, exists := observedSlugs[strings.Join(parts[:depth], "/")]; exists {
				return nil, nil, validationError("wiki page slugs cannot be both files and directories")
			}
		}
	}
	if err := validateUniqueClaimIDs(selected); err != nil {
		return nil, nil, err
	}
	revisions := make([]SourceRevision, len(sourceRevisions))
	for index, revision := range sourceRevisions {
		if revision == nil {
			return nil, nil, validationError("source revision reference is invalid")
		}
		cloned := make(SourceRevision, len(revision))
		for key, value := range revision {
			if !utf8.ValidString(key) || !utf8.ValidString(value) {
				return nil, nil, validationError("source revision reference is invalid")
			}
			cloned[key] = value
		}
		revisions[index] = cloned
	}
	return selected, revisions, nil
}

func validateUniqueClaimIDs(pages []Page) error {
	observed := make(map[string]struct{})
	for _, page := range pages {
		if !utf8.Valid(page.ClaimsJSON) {
			return validationError("page Claim snapshot is invalid")
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(page.ClaimsJSON, &value); err != nil || value == nil {
			return validationError("page Claim snapshot is invalid")
		}
		rawClaims, exists := value["claims"]
		if !exists || bytes.Equal(bytes.TrimSpace(rawClaims), []byte("null")) {
			return validationError("page Claim snapshot is invalid")
		}
		var claims []json.RawMessage
		if err := json.Unmarshal(rawClaims, &claims); err != nil {
			return validationError("page Claim snapshot is invalid")
		}
		for _, rawClaim := range claims {
			var claim map[string]json.RawMessage
			if err := json.Unmarshal(rawClaim, &claim); err != nil || claim == nil {
				return validationError("page Claim snapshot is invalid")
			}
			var claimID string
			if err := json.Unmarshal(claim["id"], &claimID); err != nil || !claimIDPattern.MatchString(claimID) {
				return validationError("page Claim snapshot is invalid")
			}
			if _, exists := observed[claimID]; exists {
				return validationError("wiki Claim IDs must be unique across pages")
			}
			observed[claimID] = struct{}{}
		}
	}
	return nil
}

func (store *WikiStore) writeBundle(
	root string,
	runID ID,
	pages []Page,
	sourceRevisions []SourceRevision,
) error {
	knownPaths := make(map[string]struct{})
	manifestPages := make([]wikiManifestPage, 0, len(pages))
	pagePaths := make([]string, 0, len(pages))
	for _, pageValue := range pages {
		pagePath := pageValue.Slug + ".md"
		pagePaths = append(pagePaths, pagePath)
		knownPaths[pagePath] = struct{}{}
		if err := store.writeBundleFile(root, pagePath, []byte(pageValue.Markdown)); err != nil {
			return err
		}
		if err := store.writeBundleFile(root, ".claims/"+pageValue.Slug+".json", pageValue.ClaimsJSON); err != nil {
			return err
		}
		manifestPages = append(manifestPages, wikiManifestPage{
			ClaimsSHA256:  hex.EncodeToString(pageValue.ClaimsSHA256[:]),
			ContentSHA256: hex.EncodeToString(pageValue.ContentSHA256[:]),
			Path:          pagePath,
			Slug:          pageValue.Slug,
			Title:         pageValue.Title,
		})
	}
	indexes := directoryIndexes(pagePaths)
	indexPaths := make([]string, 0, len(indexes))
	for indexPath := range indexes {
		indexPaths = append(indexPaths, indexPath)
	}
	slices.Sort(indexPaths)
	for _, indexPath := range indexPaths {
		knownPaths[indexPath] = struct{}{}
		if err := store.writeBundleFile(root, indexPath, []byte(indexes[indexPath])); err != nil {
			return err
		}
	}
	rootIndex := wikiRootIndex(pages)
	if err := store.writeBundleFile(root, "index.md", []byte(rootIndex)); err != nil {
		return err
	}
	knownPaths["index.md"] = struct{}{}
	if err := validateWikiLinks(root, knownPaths); err != nil {
		return err
	}
	manifest, err := marshalDeterministicJSON(wikiManifest{
		Format: "ref0-page-manifest/v1",
		Pages:  manifestPages,
		RunID:  runID.String(),
	})
	if err != nil {
		return err
	}
	if err := store.writeBundleFile(root, ".page-manifest.json", manifest); err != nil {
		return err
	}
	sort.SliceStable(sourceRevisions, func(first, second int) bool {
		firstSource := sourceRevisions[first]["source_id"]
		secondSource := sourceRevisions[second]["source_id"]
		if firstSource != secondSource {
			return firstSource < secondSource
		}
		return sourceRevisions[first]["revision_id"] < sourceRevisions[second]["revision_id"]
	})
	lastUpdate, err := marshalDeterministicJSON(wikiLastUpdate{
		Format:          "ref0-last-update/v1",
		RunID:           runID.String(),
		SourceRevisions: sourceRevisions,
	})
	if err != nil {
		return err
	}
	if err := store.writeBundleFile(root, ".last-update.json", lastUpdate); err != nil {
		return err
	}
	if err := store.writeBundleFile(
		root,
		"log.md",
		[]byte("# Update log\n\nGenerated by the documentation platform.\n"),
	); err != nil {
		return err
	}
	return fsyncBundleDirectories(root)
}

func (store *WikiStore) writeBundleFile(root, relative string, content []byte) error {
	destination := filepath.Join(root, filepath.FromSlash(relative))
	if !store.root.contains(destination) {
		return validationError("wiki artifact path escaped its data root")
	}
	if err := store.root.mkdir(filepath.Dir(destination)); err != nil {
		return err
	}
	return artifactWriteExclusive(destination, content)
}

type wikiManifestPage struct {
	ClaimsSHA256  string `json:"claims_sha256"`
	ContentSHA256 string `json:"content_sha256"`
	Path          string `json:"path"`
	Slug          string `json:"slug"`
	Title         string `json:"title"`
}

type wikiManifest struct {
	Format string             `json:"format"`
	Pages  []wikiManifestPage `json:"pages"`
	RunID  string             `json:"run_id"`
}

type wikiLastUpdate struct {
	Format          string           `json:"format"`
	RunID           string           `json:"run_id"`
	SourceRevisions []SourceRevision `json:"source_revisions"`
}

func directoryIndexes(pagePaths []string) map[string]string {
	directories := make(map[string]struct{})
	for _, pagePath := range pagePaths {
		current := path.Dir(pagePath)
		for current != "." && current != "/" {
			directories[current] = struct{}{}
			current = path.Dir(current)
		}
	}
	result := make(map[string]string, len(directories))
	for directory := range directories {
		children := make([]string, 0)
		for _, pagePath := range pagePaths {
			if path.Dir(pagePath) == directory {
				children = append(children, pagePath)
			}
		}
		slices.Sort(children)
		lines := []string{"# " + humanTitle(path.Base(directory)), ""}
		for _, child := range children {
			name := path.Base(child)
			stem := strings.TrimSuffix(name, path.Ext(name))
			lines = append(lines, "- ["+humanTitle(stem)+"]("+name+")")
		}
		result[directory+"/index.md"] = strings.Join(lines, "\n") + "\n"
	}
	return result
}

func wikiRootIndex(pages []Page) string {
	type link struct {
		title  string
		target string
	}
	topLevel := make(map[string]link)
	order := make([]string, 0)
	for _, pageValue := range pages {
		first, remainder, nested := strings.Cut(pageValue.Slug, "/")
		_ = remainder
		if nested {
			if _, exists := topLevel[first]; !exists {
				topLevel[first] = link{humanTitle(first), first + "/index.md"}
				order = append(order, first)
			}
		} else {
			if _, exists := topLevel[first]; !exists {
				order = append(order, first)
			}
			topLevel[first] = link{pageValue.Title, pageValue.Slug + ".md"}
		}
	}
	links := make([]link, 0, len(topLevel))
	for _, key := range order {
		links = append(links, topLevel[key])
	}
	sort.SliceStable(links, func(first, second int) bool {
		return links[first].title < links[second].title
	})
	lines := make([]string, 0, len(links))
	for _, value := range links {
		lines = append(lines, "- ["+value.title+"]("+value.target+")")
	}
	return "---\nokf_version: '0.2'\ntitle: Knowledge base\n---\n\n# Knowledge base\n\n" +
		strings.Join(lines, "\n") + "\n"
}

func humanTitle(value string) string {
	value = strings.ReplaceAll(value, "-", " ")
	result := []byte(value)
	previousCased := false
	for index, character := range result {
		if character >= 'a' && character <= 'z' {
			if !previousCased {
				result[index] = character - ('a' - 'A')
			}
			previousCased = true
			continue
		}
		previousCased = false
	}
	return string(result)
}

func validateWikiLinks(root string, knownPaths map[string]struct{}) error {
	paths := make([]string, 0, len(knownPaths))
	for known := range knownPaths {
		paths = append(paths, known)
	}
	slices.Sort(paths)
	for _, pagePath := range paths {
		body, err := artifactReadRegular(
			filepath.Join(root, filepath.FromSlash(pagePath)),
			"wiki file does not exist",
		)
		if err != nil || !utf8.Valid(body) {
			return validationError("wiki link source is invalid")
		}
		matches := markdownLinkPattern.FindAllSubmatchIndex(body, -1)
		for _, match := range matches {
			if match[0] > 0 && body[match[0]-1] == '!' {
				continue
			}
			rawTarget := string(body[match[2]:match[3]])
			target := strings.SplitN(rawTarget, "#", 2)[0]
			target = strings.SplitN(target, "?", 2)[0]
			if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if strings.HasPrefix(target, "/") || strings.Contains(target, `\`) {
				return validationError("wiki link is not relative")
			}
			parts := make([]string, 0)
			pageDirectory := path.Dir(pagePath)
			if pageDirectory != "." {
				parts = append(parts, strings.Split(pageDirectory, "/")...)
			}
			for _, part := range strings.Split(target, "/") {
				switch part {
				case "", ".":
					continue
				case "..":
					if len(parts) == 0 {
						return validationError("wiki link escapes the bundle")
					}
					parts = parts[:len(parts)-1]
				default:
					parts = append(parts, part)
				}
			}
			normalized := strings.Join(parts, "/")
			if _, exists := knownPaths[normalized]; !exists {
				return validationError("wiki link target does not exist: " + target)
			}
		}
	}
	return nil
}

func assertSameBundle(expected, retained string) error {
	expectedFiles, err := bundleFiles(expected)
	if err != nil {
		return wikiDifferentContentError()
	}
	retainedFiles, err := bundleFiles(retained)
	if err != nil || len(expectedFiles) != len(retainedFiles) {
		return wikiDifferentContentError()
	}
	for relative, expectedPath := range expectedFiles {
		retainedPath, exists := retainedFiles[relative]
		if !exists {
			return wikiDifferentContentError()
		}
		expectedDigest, expectedErr := artifactFileDigest(expectedPath)
		retainedDigest, retainedErr := artifactFileDigest(retainedPath)
		if expectedErr != nil || retainedErr != nil ||
			subtle.ConstantTimeCompare(expectedDigest[:], retainedDigest[:]) != 1 {
			return wikiDifferentContentError()
		}
	}
	return nil
}

func bundleFiles(root string) (map[string]string, error) {
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, wikiDifferentContentError()
	}
	result := make(map[string]string)
	if err := filepath.WalkDir(root, func(selected string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return wikiDifferentContentError()
		}
		if entry.IsDir() {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() {
			return wikiDifferentContentError()
		}
		relative, err := filepath.Rel(root, selected)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = selected
		if len(result) > maximumWikiFiles {
			return wikiDifferentContentError()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func fsyncBundleDirectories(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(selected string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return validationError("wiki bundle contains a symlink")
		}
		if entry.IsDir() {
			directories = append(directories, selected)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(directories, func(first, second int) bool {
		return strings.Count(directories[first], string(filepath.Separator)) >
			strings.Count(directories[second], string(filepath.Separator))
	})
	for _, directory := range directories {
		if err := artifactFsyncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}
