package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

const (
	passwordVersion   = 1
	passwordN         = 1 << 14
	passwordR         = 8
	passwordP         = 1
	passwordSaltBytes = 16
	passwordKeyBytes  = 32
)

func HashPassword(password string) (string, error) {
	var salt [passwordSaltBytes]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	return hashPasswordWithSalt(password, salt)
}

func hashPasswordWithSalt(password string, salt [passwordSaltBytes]byte) (string, error) {
	if !utf8.ValidString(password) {
		return "", errors.New("password is not valid UTF-8")
	}
	digest, err := scrypt.Key(
		[]byte(password),
		salt[:],
		passwordN,
		passwordR,
		passwordP,
		passwordKeyBytes,
	)
	if err != nil {
		return "", fmt.Errorf("derive password hash: %w", err)
	}
	defer clear(digest)
	return fmt.Sprintf(
		"scrypt$v=%d$n=%d$r=%d$p=%d$%s$%s",
		passwordVersion,
		passwordN,
		passwordR,
		passwordP,
		base64.RawURLEncoding.EncodeToString(salt[:]),
		base64.RawURLEncoding.EncodeToString(digest),
	), nil
}

func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 7 ||
		parts[0] != "scrypt" ||
		parts[1] != "v=1" ||
		parts[2] != "n=16384" ||
		parts[3] != "r=8" ||
		parts[4] != "p=1" ||
		!utf8.ValidString(password) {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[5])
	if err != nil || len(salt) != passwordSaltBytes {
		return false
	}
	expected, err := base64.RawURLEncoding.DecodeString(parts[6])
	if err != nil || len(expected) != passwordKeyBytes {
		return false
	}
	actual, err := scrypt.Key(
		[]byte(password),
		salt,
		passwordN,
		passwordR,
		passwordP,
		passwordKeyBytes,
	)
	if err != nil {
		return false
	}
	defer clear(actual)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
