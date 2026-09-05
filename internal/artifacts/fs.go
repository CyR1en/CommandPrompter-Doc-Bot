package artifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type artifactRoot struct {
	path string
}

func newArtifactRoot(raw string) (artifactRoot, error) {
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return artifactRoot{}, err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return artifactRoot{}, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return artifactRoot{}, validationError("artifact data root is invalid")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return artifactRoot{}, validationError("artifact data root is invalid")
	}
	return artifactRoot{path: filepath.Clean(resolved)}, nil
}

func (root artifactRoot) contains(path string) bool {
	relative, err := filepath.Rel(root.path, filepath.Clean(path))
	return err == nil &&
		!filepath.IsAbs(relative) &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (root artifactRoot) mkdir(path string) error {
	if !root.contains(path) {
		return validationError("artifact path escaped its data root")
	}
	relative, err := filepath.Rel(root.path, path)
	if err != nil {
		return validationError("artifact path escaped its data root")
	}
	current := root.path
	if relative == "." {
		return nil
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, exists, err := artifactLstat(current)
		if err != nil {
			return err
		}
		if !exists {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, exists, err = artifactLstat(current)
			if err != nil || !exists {
				return validationError("artifact path is invalid")
			}
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return validationError("artifact path is invalid")
		}
	}
	return nil
}

func (root artifactRoot) existingDirectory(path string) (bool, error) {
	if !root.contains(path) {
		return false, validationError("artifact path escaped its data root")
	}
	info, exists, err := artifactLstat(path)
	if err != nil || !exists {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, validationError("artifact path is invalid")
	}
	if err = root.rejectSymlinkSegments(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (root artifactRoot) rejectSymlinkSegments(path string) error {
	if !root.contains(path) {
		return validationError("artifact path escaped its data root")
	}
	relative, err := filepath.Rel(root.path, path)
	if err != nil {
		return err
	}
	current := root.path
	if relative == "." {
		return nil
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return validationError("artifact path is invalid")
		}
	}
	return nil
}

func artifactLstat(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return info, true, nil
}

func artifactOpenNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags|syscall.O_NOFOLLOW, mode)
}

func artifactReadRegular(path, missingMessage string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, notFoundError(missingMessage)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, notFoundError(missingMessage)
	}
	descriptor, err := artifactOpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer descriptor.Close()
	return io.ReadAll(descriptor)
}

func artifactFsyncDirectory(path string) error {
	descriptor, err := artifactOpenNoFollow(path, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer descriptor.Close()
	return descriptor.Sync()
}

func artifactWriteAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := writer.Write(content)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}

func artifactWriteExclusive(path string, content []byte) error {
	descriptor, err := artifactOpenNoFollow(
		path,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o666,
	)
	if err != nil {
		return err
	}
	if err := artifactWriteAll(descriptor, content); err != nil {
		_ = descriptor.Close()
		return err
	}
	if err := descriptor.Sync(); err != nil {
		_ = descriptor.Close()
		return err
	}
	return descriptor.Close()
}

func artifactOpenLock(path string) (*os.File, error) {
	descriptor, err := artifactOpenNoFollow(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	descriptorInfo, descriptorErr := descriptor.Stat()
	if err != nil ||
		descriptorErr != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		!os.SameFile(info, descriptorInfo) {
		_ = descriptor.Close()
		return nil, validationError("artifact publication lock is invalid")
	}
	if err := descriptor.Sync(); err != nil {
		_ = descriptor.Close()
		return nil, err
	}
	if err := artifactFsyncDirectory(filepath.Dir(path)); err != nil {
		_ = descriptor.Close()
		return nil, err
	}
	return descriptor, nil
}

func artifactLock(file *os.File, exclusive bool) error {
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	return syscall.Flock(int(file.Fd()), mode)
}

func artifactUnlock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func artifactRemoveTree(root string) error {
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return os.Chmod(path, 0o600)
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(root)
}

func artifactFileDigest(path string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	descriptor, err := artifactOpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return result, err
	}
	defer descriptor.Close()
	digest := sha256.New()
	if _, err := io.CopyBuffer(digest, descriptor, make([]byte, 64*1024)); err != nil {
		return result, err
	}
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func artifactFilesEqual(path string, expected []byte) bool {
	actual, err := artifactReadRegular(path, "artifact file does not exist")
	return err == nil && bytes.Equal(actual, expected)
}

func marshalDeterministicJSON(value any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return unescapeJSONLineSeparators(encoded.Bytes()), nil
}

func unescapeJSONLineSeparators(encoded []byte) []byte {
	result := make([]byte, 0, len(encoded))
	for index := 0; index < len(encoded); {
		if index+6 <= len(encoded) &&
			(encoded[index] == '\\') &&
			(bytes.Equal(encoded[index:index+6], []byte(`\u2028`)) ||
				bytes.Equal(encoded[index:index+6], []byte(`\u2029`))) {
			preceding := 0
			for selected := index - 1; selected >= 0 && encoded[selected] == '\\'; selected-- {
				preceding++
			}
			if preceding%2 == 0 {
				if encoded[index+5] == '8' {
					result = append(result, []byte("\u2028")...)
				} else {
					result = append(result, []byte("\u2029")...)
				}
				index += 6
				continue
			}
		}
		result = append(result, encoded[index])
		index++
	}
	return result
}
