package security

import (
	"encoding/json"
	"errors"
	"log/slog"
	"unicode/utf8"
)

const (
	MaskedCredential             = "••••"
	MinProviderAPIKeyLength      = 8
	MinTinyFishAPIKeyLength      = 8
	redactedSecretRepresentation = "SecretValue(<redacted>)"
)

type CredentialKind string

const (
	CredentialRepositoryHTTPS CredentialKind = "REPOSITORY_HTTPS"
	CredentialWebsiteHeader   CredentialKind = "WEBSITE_HEADER"
	CredentialProviderAPIKey  CredentialKind = "PROVIDER_API_KEY"
	CredentialDiscordBotToken CredentialKind = "DISCORD_BOT_TOKEN"
	CredentialTinyFishAPIKey  CredentialKind = "TINYFISH_API_KEY"
)

var (
	ErrInvalidCredentialKind = errors.New("credential kind is invalid")
	ErrInvalidSecret         = errors.New("secret must not be empty")
	ErrSecretSerialization   = errors.New("SecretValue cannot be serialized")
)

func (kind CredentialKind) valid() bool {
	switch kind {
	case CredentialRepositoryHTTPS,
		CredentialWebsiteHeader,
		CredentialProviderAPIKey,
		CredentialDiscordBotToken,
		CredentialTinyFishAPIKey:
		return true
	default:
		return false
	}
}

// SecretValue keeps plaintext out of formatting, logging, and serializers.
type SecretValue struct {
	value string
}

func NewSecretValue(value string) (*SecretValue, error) {
	if value == "" || !utf8.ValidString(value) {
		return nil, ErrInvalidSecret
	}
	return &SecretValue{value: value}, nil
}

func (secret *SecretValue) Reveal() string {
	return secret.value
}

func (SecretValue) String() string {
	return redactedSecretRepresentation
}

func (SecretValue) GoString() string {
	return redactedSecretRepresentation
}

func (SecretValue) LogValue() slog.Value {
	return slog.StringValue(redactedSecretRepresentation)
}

func (SecretValue) MarshalJSON() ([]byte, error) {
	return nil, ErrSecretSerialization
}

func (SecretValue) MarshalText() ([]byte, error) {
	return nil, ErrSecretSerialization
}

func (SecretValue) MarshalBinary() ([]byte, error) {
	return nil, ErrSecretSerialization
}

func (SecretValue) GobEncode() ([]byte, error) {
	return nil, ErrSecretSerialization
}

func ValidateCredentialSecret(kind CredentialKind, secret *SecretValue) error {
	if !kind.valid() {
		return ErrInvalidCredentialKind
	}
	if secret == nil {
		return ErrInvalidSecret
	}
	length := utf8.RuneCountInString(secret.value)
	if kind == CredentialProviderAPIKey && length < MinProviderAPIKeyLength {
		return &credentialSecretError{"provider API keys must contain at least 8 characters"}
	}
	if kind == CredentialTinyFishAPIKey && length < MinTinyFishAPIKeyLength {
		return &credentialSecretError{"TinyFish API keys must contain at least 8 characters"}
	}
	return nil
}

// credentialSecretError preserves the exact domain message while making every
// rejected secret recognizable at public boundaries without string matching.
type credentialSecretError struct {
	message string
}

func (err *credentialSecretError) Error() string { return err.message }

func (*credentialSecretError) Is(target error) bool { return target == ErrInvalidSecret }

var _ json.Marshaler = SecretValue{}
