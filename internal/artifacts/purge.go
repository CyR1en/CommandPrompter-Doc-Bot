package artifacts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cyr1en/ref0/internal/knowledgebases"
)

// KnowledgeBasePurger removes only one knowledge base's published artifacts
// and the explicitly captured source artifacts owned by that knowledge base.
// It intentionally does not create the application-data root.
type KnowledgeBasePurger struct {
	configuredRoot string
}

func NewKnowledgeBasePurger(dataRoot string) (*KnowledgeBasePurger, error) {
	if !filepath.IsAbs(dataRoot) {
		return nil, validationError("application data root must be absolute")
	}
	return &KnowledgeBasePurger{configuredRoot: filepath.Clean(dataRoot)}, nil
}

func (purger *KnowledgeBasePurger) Purge(
	ctx context.Context,
	knowledgeBaseID knowledgebases.ID,
	sourceIDs []knowledgebases.ID,
) error {
	if purger == nil {
		return errors.New("artifact purger is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(purger.configuredRoot)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("application data root is unavailable: %w", os.ErrNotExist)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return validationError("application data root must be a directory")
	}
	root, err := filepath.EvalSymlinks(purger.configuredRoot)
	if err != nil {
		return validationError("application data root must be a directory")
	}
	root = filepath.Clean(root)
	if err = purger.remove(ctx, root, "knowledge-bases", knowledgeBaseID.String()); err != nil {
		return err
	}
	for _, sourceID := range sourceIDs {
		if err = purger.remove(ctx, root, "sources", sourceID.String()); err != nil {
			return err
		}
	}
	return nil
}

func (*KnowledgeBasePurger) remove(ctx context.Context, root, category, resourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Join(root, category)
	resolvedParent, exists, err := purgeParent(parent)
	if err != nil {
		return err
	}
	if !artifactPathContains(root, resolvedParent) {
		return validationError("artifact purge path escaped its data root")
	}
	if !exists {
		return nil
	}
	path := filepath.Join(parent, resourceID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		err = artifactRemoveTree(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		return err
	}
	return artifactFsyncDirectory(resolvedParent)
}

func purgeParent(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(path), false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 && !info.IsDir() {
		return "", false, validationError("artifact purge path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false, validationError("artifact purge path is invalid")
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil || !resolvedInfo.IsDir() {
		return "", false, validationError("artifact purge path is invalid")
	}
	return filepath.Clean(resolved), true, nil
}

func artifactPathContains(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && !filepath.IsAbs(relative) && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

var _ knowledgebases.ArtifactPurger = (*KnowledgeBasePurger)(nil)
