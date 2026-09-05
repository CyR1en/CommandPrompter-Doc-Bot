package sourcefiles

import "errors"

var (
	ErrSourceStorage  = errors.New("source storage error")
	ErrSnapshotExists = errors.New("immutable snapshot already exists")
)

// StorageError preserves the Python distinction between containment/storage
// failures and immutable-destination conflicts while retaining exact messages.
type StorageError struct {
	message string
	kind    error
}

func (err *StorageError) Error() string {
	return err.message
}

func (err *StorageError) Is(target error) bool {
	return target == ErrSourceStorage || target == err.kind
}

func storageError(message string) error {
	return &StorageError{message: message}
}

func snapshotExistsError() error {
	return &StorageError{
		message: "immutable snapshot already exists",
		kind:    ErrSnapshotExists,
	}
}
