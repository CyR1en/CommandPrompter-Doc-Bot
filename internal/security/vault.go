package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	credentialKeyBytes   = 32
	credentialNonceBytes = 12
)

var (
	credentialKeyIDPattern = regexp.MustCompile(`\A[A-Za-z0-9._-]+\z`)
	credentialKeyPattern   = regexp.MustCompile(`\A[A-Za-z0-9_-]+={0,2}\z`)
	// Domain separation prevents ciphertext and request digests from being
	// accepted across distinct ref0 cryptographic purposes.
	credentialAADDomain    = []byte("ref0.credential.aesgcm.v1")
	credentialDigestDomain = []byte("ref0.credential.idempotency.v1")

	ErrCredentialKeyUnavailable = errors.New("credential key is unavailable")
	ErrCredentialDecryption     = errors.New("credential decryption failed")
	ErrCredentialEnvelope       = errors.New("credential envelope is invalid")
)

type CredentialID [16]byte

type CredentialEnvelope struct {
	credentialID  CredentialID
	kind          CredentialKind
	secretVersion int64
	keyID         string
	nonce         [credentialNonceBytes]byte
	ciphertext    []byte
}

func NewCredentialEnvelope(
	credentialID CredentialID,
	kind CredentialKind,
	secretVersion int64,
	keyID string,
	nonce []byte,
	ciphertext []byte,
) (CredentialEnvelope, error) {
	if !kind.valid() || secretVersion <= 0 || len(nonce) != credentialNonceBytes {
		return CredentialEnvelope{}, ErrCredentialEnvelope
	}
	envelope := CredentialEnvelope{
		credentialID:  credentialID,
		kind:          kind,
		secretVersion: secretVersion,
		keyID:         keyID,
		ciphertext:    append([]byte(nil), ciphertext...),
	}
	copy(envelope.nonce[:], nonce)
	return envelope, nil
}

func (envelope CredentialEnvelope) CredentialID() CredentialID {
	return envelope.credentialID
}

func (envelope CredentialEnvelope) Kind() CredentialKind {
	return envelope.kind
}

func (envelope CredentialEnvelope) SecretVersion() int64 {
	return envelope.secretVersion
}

func (envelope CredentialEnvelope) KeyID() string {
	return envelope.keyID
}

func (envelope CredentialEnvelope) Nonce() []byte {
	return append([]byte(nil), envelope.nonce[:]...)
}

func (envelope CredentialEnvelope) Ciphertext() []byte {
	return append([]byte(nil), envelope.ciphertext...)
}

func (CredentialEnvelope) MarshalJSON() ([]byte, error) {
	return nil, errors.New("CredentialEnvelope cannot be serialized")
}

type credentialKey struct {
	id  string
	key [credentialKeyBytes]byte
}

type CredentialVault struct {
	activeID string
	keys     []credentialKey
	byID     map[string][credentialKeyBytes]byte
}

func NewCredentialVault(active, previous string) (*CredentialVault, error) {
	activeID, activeKey, err := decodeCredentialKey(strings.TrimSpace(active))
	if err != nil {
		return nil, err
	}
	vault := &CredentialVault{
		activeID: activeID,
		keys:     []credentialKey{{id: activeID, key: activeKey}},
		byID:     map[string][credentialKeyBytes]byte{activeID: activeKey},
	}
	if previous == "" {
		return vault, nil
	}
	for _, raw := range strings.Split(previous, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return nil, errors.New("previous credential key is empty")
		}
		keyID, key, decodeErr := decodeCredentialKey(entry)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if _, exists := vault.byID[keyID]; exists {
			return nil, errors.New("credential key IDs must be unique")
		}
		for _, existing := range vault.keys {
			if subtle.ConstantTimeCompare(existing.key[:], key[:]) == 1 {
				return nil, errors.New("credential keys must be unique")
			}
		}
		vault.keys = append(vault.keys, credentialKey{id: keyID, key: key})
		vault.byID[keyID] = key
	}
	return vault, nil
}

func (vault *CredentialVault) ActiveKeyID() string {
	return vault.activeID
}

func (vault *CredentialVault) Encrypt(
	credentialID CredentialID,
	kind CredentialKind,
	secretVersion int64,
	secret *SecretValue,
) (CredentialEnvelope, error) {
	var nonce [credentialNonceBytes]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return CredentialEnvelope{}, fmt.Errorf("generate credential nonce: %w", err)
	}
	return vault.encryptWithNonce(credentialID, kind, secretVersion, secret, nonce)
}

func (vault *CredentialVault) encryptWithNonce(
	credentialID CredentialID,
	kind CredentialKind,
	secretVersion int64,
	secret *SecretValue,
	nonce [credentialNonceBytes]byte,
) (CredentialEnvelope, error) {
	if !kind.valid() || secretVersion <= 0 || secret == nil {
		return CredentialEnvelope{}, ErrCredentialEnvelope
	}
	key := vault.byID[vault.activeID]
	gcm, err := credentialGCM(key)
	if err != nil {
		return CredentialEnvelope{}, err
	}
	aad := credentialAAD(credentialID, kind, secretVersion, vault.activeID)
	ciphertext := gcm.Seal(nil, nonce[:], []byte(secret.value), aad)
	return NewCredentialEnvelope(
		credentialID,
		kind,
		secretVersion,
		vault.activeID,
		nonce[:],
		ciphertext,
	)
}

func (vault *CredentialVault) Decrypt(envelope CredentialEnvelope) (*SecretValue, error) {
	key, exists := vault.byID[envelope.keyID]
	if !exists {
		return nil, ErrCredentialKeyUnavailable
	}
	if !envelope.kind.valid() || envelope.secretVersion <= 0 {
		return nil, ErrCredentialDecryption
	}
	gcm, err := credentialGCM(key)
	if err != nil {
		return nil, ErrCredentialDecryption
	}
	plaintext, err := gcm.Open(
		nil,
		envelope.nonce[:],
		envelope.ciphertext,
		credentialAAD(
			envelope.credentialID,
			envelope.kind,
			envelope.secretVersion,
			envelope.keyID,
		),
	)
	if err != nil || !utf8.Valid(plaintext) {
		clear(plaintext)
		return nil, ErrCredentialDecryption
	}
	value := string(plaintext)
	clear(plaintext)
	return NewSecretValue(value)
}

func (vault *CredentialVault) KeyedDigests(parts ...[]byte) ([][]byte, error) {
	allParts := make([][]byte, 1, len(parts)+1)
	allParts[0] = credentialDigestDomain
	allParts = append(allParts, parts...)
	message, err := encodeCredentialParts(allParts...)
	if err != nil {
		return nil, err
	}
	digests := make([][]byte, 0, len(vault.keys))
	for _, entry := range vault.keys {
		digest := hmac.New(sha256.New, entry.key[:])
		_, _ = digest.Write(message)
		digests = append(digests, digest.Sum(nil))
	}
	return digests, nil
}

func credentialGCM(key [credentialKeyBytes]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func credentialAAD(
	credentialID CredentialID,
	kind CredentialKind,
	secretVersion int64,
	keyID string,
) []byte {
	version := make([]byte, 8)
	binary.BigEndian.PutUint64(version, uint64(secretVersion))
	encoded, err := encodeCredentialParts(
		credentialAADDomain,
		credentialID[:],
		[]byte(kind),
		version,
		[]byte(keyID),
	)
	if err != nil {
		panic(err)
	}
	return encoded
}

func encodeCredentialParts(parts ...[]byte) ([]byte, error) {
	total := 0
	for _, part := range parts {
		if uint64(len(part)) > math.MaxUint32 || total > math.MaxInt-4-len(part) {
			return nil, errors.New("credential value is too large")
		}
		total += 4 + len(part)
	}
	encoded := make([]byte, 0, total)
	for _, part := range parts {
		encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(part)))
		encoded = append(encoded, part...)
	}
	return encoded, nil
}

func decodeCredentialKey(entry string) (string, [credentialKeyBytes]byte, error) {
	var key [credentialKeyBytes]byte
	keyID, encoded, found := strings.Cut(entry, ":")
	if !found {
		return "", key, errors.New("credential key must use key_id:base64url")
	}
	if len(keyID) > 128 || !credentialKeyIDPattern.MatchString(keyID) {
		return "", key, errors.New("credential key ID is invalid")
	}
	if !credentialKeyPattern.MatchString(encoded) {
		return "", key, errors.New("credential key encoding is invalid")
	}
	padded := encoded + strings.Repeat("=", (4-len(encoded)%4)%4)
	decoded, err := base64.URLEncoding.DecodeString(padded)
	if err != nil {
		return "", key, errors.New("credential key encoding is invalid")
	}
	defer clear(decoded)
	if len(decoded) != credentialKeyBytes {
		return "", key, errors.New("credential key must contain 32 bytes")
	}
	copy(key[:], decoded)
	return keyID, key, nil
}
