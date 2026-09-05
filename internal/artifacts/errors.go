package artifacts

import (
	"errors"
	"os"
)

var (
	ErrValidation           = errors.New("artifact validation error")
	ErrImmutablePage        = errors.New("accepted page snapshot is immutable")
	ErrWikiDifferentContent = errors.New("wiki version already contains different content")
	ErrArtifactNotFound     = errors.New("artifact does not exist")
)

type Error struct {
	message string
	matches []error
}

func (err *Error) Error() string {
	return err.message
}

func (err *Error) Is(target error) bool {
	for _, candidate := range err.matches {
		if target == candidate {
			return true
		}
	}
	return false
}

func validationError(message string, kinds ...error) error {
	return &Error{
		message: message,
		matches: append([]error{ErrValidation}, kinds...),
	}
}

func immutablePageError() error {
	return validationError("accepted page snapshot is immutable", ErrImmutablePage)
}

func wikiDifferentContentError() error {
	return &Error{
		message: "wiki version already contains different content",
		matches: []error{ErrWikiDifferentContent, os.ErrExist},
	}
}

func notFoundError(message string) error {
	return &Error{
		message: message,
		matches: []error{ErrArtifactNotFound, os.ErrNotExist},
	}
}
