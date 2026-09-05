package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/security"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	redactedSessionToken         = "SessionToken(<redacted>)"
	redactedAuthenticatedSession = "AuthenticatedSession(<redacted>)"
)

var (
	csrfDomain            = []byte("ref0.csrf.v1\x00")
	ErrAuthentication     = errors.New("authentication failed")
	ErrBootstrapDenied    = errors.New("bootstrap denied")
	ErrCSRF               = errors.New("request verification failed")
	ErrServiceUnavailable = errors.New("authentication service unavailable")
	ErrInvalidUsername    = errors.New("username is invalid")
	ErrInvalidToken       = errors.New("session token is invalid")
)

type OperatorID [16]byte

type SessionID [16]byte

type Username struct {
	Display string
	Key     string
}

func ParseUsername(value string) (Username, error) {
	if !utf8.ValidString(value) {
		return Username{}, ErrInvalidUsername
	}
	display := strings.TrimFunc(norm.NFKC.String(value), pythonWhitespace)
	if length := utf8.RuneCountInString(display); length < 1 || length > 255 {
		return Username{}, ErrInvalidUsername
	}
	key := cases.Fold().String(display)
	if utf8.RuneCountInString(key) > 255 {
		return Username{}, ErrInvalidUsername
	}
	return Username{Display: display, Key: key}, nil
}

func pythonWhitespace(value rune) bool {
	return unicode.IsSpace(value) || value >= '\x1c' && value <= '\x1f'
}

type SessionToken struct {
	value string
}

func NewSessionToken(value string) (SessionToken, error) {
	if value == "" || !utf8.ValidString(value) {
		return SessionToken{}, ErrInvalidToken
	}
	return SessionToken{value: value}, nil
}

func (token SessionToken) Reveal() string {
	return token.value
}

func (SessionToken) String() string {
	return redactedSessionToken
}

func (SessionToken) GoString() string {
	return redactedSessionToken
}

func (SessionToken) LogValue() slog.Value {
	return slog.StringValue(redactedSessionToken)
}

func (SessionToken) MarshalJSON() ([]byte, error) {
	return nil, errors.New("SessionToken cannot be serialized")
}

func (SessionToken) MarshalText() ([]byte, error) {
	return nil, errors.New("SessionToken cannot be serialized")
}

type BootstrapCommand struct {
	Username       Username
	Password       *security.SecretValue
	BootstrapToken *security.SecretValue
}

type LoginCommand struct {
	Username Username
	Password *security.SecretValue
}

type Operator struct {
	ID       OperatorID
	Username string
}

type OperatorSession struct {
	ID         SessionID
	Operator   Operator
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

type AuthenticatedSession struct {
	Session   OperatorSession
	Token     SessionToken
	CSRFToken string
}

func (AuthenticatedSession) String() string {
	return redactedAuthenticatedSession
}

func (AuthenticatedSession) GoString() string {
	return redactedAuthenticatedSession
}

func (AuthenticatedSession) LogValue() slog.Value {
	return slog.StringValue(redactedAuthenticatedSession)
}

func (AuthenticatedSession) MarshalJSON() ([]byte, error) {
	return nil, errors.New("AuthenticatedSession cannot be serialized")
}

func DigestToken(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

func CSRFTokenFor(token SessionToken, sessionID SessionID) string {
	digest := hmac.New(sha256.New, []byte(token.value))
	_, _ = digest.Write(csrfDomain)
	_, _ = digest.Write(sessionID[:])
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}

func CSRFTokenMatches(token SessionToken, sessionID SessionID, submitted string) bool {
	return hmac.Equal([]byte(CSRFTokenFor(token, sessionID)), []byte(submitted))
}

func (id OperatorID) String() string {
	return formatUUID([16]byte(id))
}

func (id SessionID) String() string {
	return formatUUID([16]byte(id))
}

func formatUUID(id [16]byte) string {
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

var _ json.Marshaler = SessionToken{}
var _ json.Marshaler = AuthenticatedSession{}
