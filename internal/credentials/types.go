// Package credentials owns encrypted credential metadata and exact-version
// secret leases. Plaintext values remain inside security.SecretValue.
package credentials

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/security"
)

const MaskedValue = security.MaskedCredential

type ID [16]byte
type Kind = security.CredentialKind

const (
	RepositoryHTTPS Kind = security.CredentialRepositoryHTTPS
	WebsiteHeader   Kind = security.CredentialWebsiteHeader
	ProviderAPIKey  Kind = security.CredentialProviderAPIKey
	DiscordBotToken Kind = security.CredentialDiscordBotToken
	TinyFishAPIKey  Kind = security.CredentialTinyFishAPIKey
)

var (
	ErrNotFound          = errors.New("credential not found")
	ErrSecretUnavailable = errors.New("provider credential is unavailable")
	ErrInvalidLabel      = errors.New("label must contain 1 to 255 characters")
)

type Metadata struct {
	ID            ID
	Kind          Kind
	Label         string
	MaskedValue   string
	SecretVersion int32
	KeyID         string
	CreatedAt     time.Time
	RotatedAt     *time.Time
}

type CreateCommand struct {
	Kind   Kind
	Label  string
	Secret *security.SecretValue
}

type RotateCommand struct {
	CredentialID ID
	Secret       *security.SecretValue
}

func ValidateCreate(command CreateCommand) error {
	if !utf8.ValidString(command.Label) ||
		command.Label != strings.TrimFunc(command.Label, pythonWhitespace) ||
		utf8.RuneCountInString(command.Label) < 1 ||
		utf8.RuneCountInString(command.Label) > 255 {
		return ErrInvalidLabel
	}
	return security.ValidateCredentialSecret(command.Kind, command.Secret)
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
		return ID{}, errors.New("credential ID must use canonical UUID form")
	}
	compact := strings.ReplaceAll(value, "-", "")
	var id ID
	if _, err := hex.Decode(id[:], []byte(compact)); err != nil {
		return ID{}, errors.New("credential ID must use canonical UUID form")
	}
	return id, nil
}

func pythonWhitespace(value rune) bool {
	return unicode.IsSpace(value) || value >= '\x1c' && value <= '\x1f'
}

func validateMetadata(value Metadata) error {
	if !validKind(value.Kind) {
		return security.ErrInvalidCredentialKind
	}
	if value.MaskedValue != MaskedValue {
		return errors.New("credential mask must be fixed")
	}
	if value.SecretVersion <= 0 {
		return errors.New("secret_version must be positive")
	}
	return nil
}

func validKind(value Kind) bool {
	switch value {
	case RepositoryHTTPS, WebsiteHeader, ProviderAPIKey, DiscordBotToken, TinyFishAPIKey:
		return true
	default:
		return false
	}
}

func (value Metadata) String() string {
	return fmt.Sprintf("CredentialMetadata(%s, %s, %q, %s)", value.ID, value.Kind, value.Label, value.MaskedValue)
}
