// Package knowledgebases owns knowledge-base configuration and lifecycle state.
package knowledgebases

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/jobs"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type ID [16]byte

type Name struct {
	Display string
	Key     string
}

type Access string

const (
	Public     Access = "PUBLIC"
	Restricted Access = "RESTRICTED"
)

type Lifecycle string

const (
	Active        Lifecycle = "ACTIVE"
	Archived      Lifecycle = "ARCHIVED"
	PendingDelete Lifecycle = "PENDING_DELETE"
	Deleted       Lifecycle = "DELETED"
)

var (
	ErrConflict        = errors.New("knowledge base mutation conflicts with current state")
	ErrTransition      = errors.New("knowledge base transition is invalid")
	ErrConfirmation    = errors.New("knowledge base deletion confirmation does not match")
	ErrPurgeNotReady   = errors.New("knowledge base purge is not ready")
	ErrNotFound        = errors.New("knowledge base not found")
	ErrInvalidName     = errors.New("knowledge base name must contain 1 to 255 characters")
	ErrNormalizedName  = errors.New("normalized knowledge base name must not exceed 255 characters")
	ErrInvalidLanguage = errors.New("language must contain 1 to 35 characters")
)

type stateError struct {
	message string
	kinds   []error
}

func (err *stateError) Error() string { return err.message }
func (err *stateError) Is(target error) bool {
	for _, kind := range err.kinds {
		if target == kind {
			return true
		}
	}
	return false
}

func conflict(message string) error {
	return &stateError{message: message, kinds: []error{ErrConflict}}
}

func transition(message string) error {
	return &stateError{message: message, kinds: []error{ErrConflict, ErrTransition}}
}

func confirmation() error {
	return &stateError{
		message: "knowledge base deletion confirmation does not match",
		kinds:   []error{ErrConflict, ErrConfirmation},
	}
}

func purgeNotReady() error {
	return &stateError{
		message: "knowledge base purge is not ready",
		kinds:   []error{ErrConflict, ErrPurgeNotReady},
	}
}

type KnowledgeBase struct {
	ID                ID
	Name              string
	Access            Access
	Lifecycle         Lifecycle
	Instructions      string
	Language          string
	PublishedWikiID   *[16]byte
	ArchivedAt        *time.Time
	DeleteRequestedAt *time.Time
	PurgeAfter        *time.Time
	DeletedAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Version           int32
}

type Deletion struct {
	KnowledgeBase KnowledgeBase
	PurgeJobID    jobs.JobID
}

type CreateCommand struct {
	Name         Name
	Access       Access
	Instructions string
	Language     string
}

type UpdateCommand struct {
	KnowledgeBaseID ID
	ExpectedVersion int32
	Name            *Name
	Access          *Access
	Instructions    *string
	Language        *string
	Lifecycle       *Lifecycle
}

type DeleteCommand struct {
	KnowledgeBaseID  ID
	ExpectedVersion  int32
	ConfirmationName string
}

type RestoreCommand struct {
	KnowledgeBaseID ID
	ExpectedVersion int32
}

func ParseName(value string) (Name, error) {
	if !utf8.ValidString(value) {
		return Name{}, ErrInvalidName
	}
	display := strings.TrimFunc(norm.NFKC.String(value), pythonWhitespace)
	if length := utf8.RuneCountInString(display); length < 1 || length > 255 {
		return Name{}, ErrInvalidName
	}
	key := cases.Fold().String(display)
	if utf8.RuneCountInString(key) > 255 {
		return Name{}, ErrNormalizedName
	}
	return Name{Display: display, Key: key}, nil
}

func Transition(current, target Lifecycle) (Lifecycle, error) {
	allowed := false
	switch current {
	case Active, Archived:
		allowed = target == current || target == Active || target == Archived || target == PendingDelete
	case PendingDelete:
		allowed = target == Active || target == Archived || target == Deleted
	case Deleted:
		allowed = false
	}
	if !allowed {
		return "", transition("knowledge base transition is invalid")
	}
	return target, nil
}

func RestoreLifecycle(archivedAt *time.Time) Lifecycle {
	if archivedAt != nil {
		return Archived
	}
	return Active
}

func ValidateCreate(command CreateCommand) error {
	parsed, err := ParseName(command.Name.Display)
	if err != nil {
		return err
	}
	if parsed != command.Name {
		return ErrInvalidName
	}
	if !validAccess(command.Access) {
		return errors.New("knowledge base access policy is invalid")
	}
	if !utf8.ValidString(command.Instructions) {
		return errors.New("knowledge base instructions are invalid")
	}
	return validateLanguage(command.Language)
}

func ValidateUpdate(command UpdateCommand) error {
	if command.ExpectedVersion <= 0 {
		return errors.New("expected_version must be positive")
	}
	if command.Name != nil {
		parsed, err := ParseName(command.Name.Display)
		if err != nil {
			return err
		}
		if parsed != *command.Name {
			return ErrInvalidName
		}
	}
	if command.Access != nil && !validAccess(*command.Access) {
		return errors.New("knowledge base access policy is invalid")
	}
	if command.Instructions != nil && !utf8.ValidString(*command.Instructions) {
		return errors.New("knowledge base instructions are invalid")
	}
	if command.Language != nil {
		if err := validateLanguage(*command.Language); err != nil {
			return err
		}
	}
	if command.Lifecycle != nil && *command.Lifecycle != Active && *command.Lifecycle != Archived {
		return errors.New("updates can only activate or archive")
	}
	if command.Name == nil && command.Access == nil && command.Instructions == nil &&
		command.Language == nil && command.Lifecycle == nil {
		return errors.New("knowledge base update must change a field")
	}
	return nil
}

func ValidateDelete(command DeleteCommand) error {
	if command.ExpectedVersion <= 0 {
		return errors.New("expected_version must be positive")
	}
	if !utf8.ValidString(command.ConfirmationName) {
		return errors.New("knowledge base deletion confirmation is invalid")
	}
	return nil
}

func ValidateRestore(command RestoreCommand) error {
	if command.ExpectedVersion <= 0 {
		return errors.New("expected_version must be positive")
	}
	return nil
}

func validateLanguage(value string) error {
	if !utf8.ValidString(value) || value == "" || value != strings.TrimFunc(value, pythonWhitespace) || utf8.RuneCountInString(value) > 35 {
		return ErrInvalidLanguage
	}
	return nil
}

func validAccess(value Access) bool { return value == Public || value == Restricted }

func validLifecycle(value Lifecycle) bool {
	switch value {
	case Active, Archived, PendingDelete, Deleted:
		return true
	default:
		return false
	}
}

func pythonWhitespace(value rune) bool {
	return unicode.IsSpace(value) || value >= '\x1c' && value <= '\x1f'
}

func (id ID) String() string {
	var encoded [36]byte
	hex.Encode(encoded[0:8], id[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], id[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], id[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], id[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], id[10:16])
	return string(encoded[:])
}

func ParseID(value string) (ID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return ID{}, errors.New("knowledge base ID must use canonical UUID form")
	}
	compact := strings.ReplaceAll(value, "-", "")
	var id ID
	if _, err := hex.Decode(id[:], []byte(compact)); err != nil {
		return ID{}, errors.New("knowledge base ID must use canonical UUID form")
	}
	return id, nil
}
