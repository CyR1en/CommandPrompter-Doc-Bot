package security

import (
	"bytes"
	"encoding/base64"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const (
	pythonVectorKey         = "v1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	pythonVectorPreviousKey = "old:Hx4dHBsaGRgXFhUUExIREA8ODQwLCgkIBwYFBAMCAQA"
)

func TestCredentialKeyParserRejectsInvalidAndDuplicateKeys(t *testing.T) {
	keyA := repeatedKey("v1", 0x61)
	tests := []struct {
		name     string
		active   string
		previous string
	}{
		{"missing separator", "missing-separator", ""},
		{"invalid ID", "bad id!:AAAA", ""},
		{"invalid base64", "v1:not-base64!", ""},
		{"wrong length", keyA[:len(keyA)-2], ""},
		{"31 byte key", encodedKey("v1", bytes.Repeat([]byte{0x61}, 31), false), ""},
		{"33 byte key", encodedKey("v1", bytes.Repeat([]byte{0x61}, 33), false), ""},
		{"ID over 128 bytes", strings.Repeat("a", 129) + ":" + strings.SplitN(keyA, ":", 2)[1], ""},
		{"duplicate ID", keyA, repeatedKey("v1", 0x62)},
		{"duplicate material", keyA, repeatedKey("v2", 0x61)},
		{"duplicate previous ID", keyA, repeatedKey("v2", 0x62) + "," + repeatedKey("v2", 0x63)},
		{"empty previous", keyA, ","},
		{"whitespace previous", keyA, " "},
		{"standard base64", "v1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)), ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCredentialVault(test.active, test.previous); err == nil {
				t.Fatal("expected invalid credential key configuration")
			}
		})
	}

	vault, err := NewCredentialVault("  "+pythonVectorKey+"\t", " "+pythonVectorPreviousKey+" ")
	if err != nil {
		t.Fatalf("trim valid keys: %v", err)
	}
	if vault.ActiveKeyID() != "v1" {
		t.Fatalf("active key ID = %q", vault.ActiveKeyID())
	}
	maxID := strings.Repeat("a", 128)
	padded := encodedKey(maxID, bytes.Repeat([]byte{0xa5}, credentialKeyBytes), true)
	vault, err = NewCredentialVault(padded, "")
	if err != nil || vault.ActiveKeyID() != maxID {
		t.Fatalf("valid maximum key ID or padded key rejected: %v", err)
	}
}

func TestCredentialEncryptionMatchesRef0Vectors(t *testing.T) {
	vault := mustVault(t, pythonVectorKey, "")
	credentialID := credentialIDFromHex(t, "00112233445566778899aabbccddeeff")
	secret := mustSecret(t, "sëcret-🔐")
	var nonce [credentialNonceBytes]byte
	copy(nonce[:], decodeHex(t, "000102030405060708090a0b"))

	envelope, err := vault.encryptWithNonce(
		credentialID,
		CredentialProviderAPIKey,
		7,
		secret,
		nonce,
	)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	wantAAD := "00000019726566302e63726564656e7469616c2e61657367636d2e7631" +
		"0000001000112233445566778899aabbccddeeff" +
		"0000001050524f56494445525f4150495f4b4559" +
		"000000080000000000000007000000027631"
	if got := hex.EncodeToString(credentialAAD(credentialID, CredentialProviderAPIKey, 7, "v1")); got != wantAAD {
		t.Fatalf("AAD mismatch\n got: %s\nwant: %s", got, wantAAD)
	}
	wantCiphertext := "34c17d78b780b6367dde031be7e7c873df0e23793e09307a9523ecbf"
	if got := hex.EncodeToString(envelope.Ciphertext()); got != wantCiphertext {
		t.Fatalf("ciphertext mismatch\n got: %s\nwant: %s", got, wantCiphertext)
	}
	decrypted, err := vault.Decrypt(envelope)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted.Reveal() != secret.Reveal() {
		t.Fatal("decrypted secret differs")
	}
	if envelope.CredentialID() != credentialID ||
		envelope.Kind() != CredentialProviderAPIKey ||
		envelope.SecretVersion() != 7 ||
		envelope.KeyID() != "v1" {
		t.Fatal("envelope metadata differs")
	}

	nonceCopy := envelope.Nonce()
	ciphertextCopy := envelope.Ciphertext()
	nonceCopy[0] ^= 1
	ciphertextCopy[0] ^= 1
	if envelope.Nonce()[0] != 0 || hex.EncodeToString(envelope.Ciphertext()) != wantCiphertext {
		t.Fatal("envelope accessors exposed mutable storage")
	}
}

func TestPreviousKeyDecryptsAndAADTamperingFailsClosed(t *testing.T) {
	oldVault := mustVault(t, pythonVectorPreviousKey, "")
	rotated := mustVault(t, pythonVectorKey, pythonVectorPreviousKey)
	credentialID := credentialIDFromHex(t, "00112233445566778899aabbccddeeff")
	secret := mustSecret(t, "repository-secret")
	var nonce [credentialNonceBytes]byte
	copy(nonce[:], decodeHex(t, "000102030405060708090a0b"))
	original, err := oldVault.encryptWithNonce(
		credentialID,
		CredentialRepositoryHTTPS,
		1,
		secret,
		nonce,
	)
	if err != nil {
		t.Fatalf("encrypt with previous key: %v", err)
	}
	decrypted, err := rotated.Decrypt(original)
	if err != nil || decrypted.Reveal() != secret.Reveal() {
		t.Fatalf("decrypt previous key: %v", err)
	}

	tamperedCiphertext := original.Ciphertext()
	tamperedCiphertext[len(tamperedCiphertext)-1] ^= 1
	tampered := original
	tampered.ciphertext = tamperedCiphertext
	wrongID := original
	wrongID.credentialID[0] ^= 1
	wrongKind := original
	wrongKind.kind = CredentialDiscordBotToken
	wrongVersion := original
	wrongVersion.secretVersion++
	wrongKnownKey := original
	wrongKnownKey.keyID = "v1"
	for _, envelope := range []CredentialEnvelope{
		tampered,
		wrongID,
		wrongKind,
		wrongVersion,
		wrongKnownKey,
	} {
		if _, decryptErr := rotated.Decrypt(envelope); !errors.Is(decryptErr, ErrCredentialDecryption) {
			t.Fatalf("tampered envelope error = %v", decryptErr)
		}
	}
	unknown := original
	unknown.keyID = "missing"
	if _, decryptErr := rotated.Decrypt(unknown); !errors.Is(decryptErr, ErrCredentialKeyUnavailable) {
		t.Fatalf("unknown key error = %v", decryptErr)
	}
}

func TestEncryptionUsesUniqueNonces(t *testing.T) {
	vault := mustVault(t, pythonVectorKey, "")
	credentialID := credentialIDFromHex(t, "00112233445566778899aabbccddeeff")
	secret := mustSecret(t, "same-secret")
	first, err := vault.Encrypt(credentialID, CredentialProviderAPIKey, 1, secret)
	if err != nil {
		t.Fatalf("first encryption: %v", err)
	}
	second, err := vault.Encrypt(credentialID, CredentialProviderAPIKey, 1, secret)
	if err != nil {
		t.Fatalf("second encryption: %v", err)
	}
	if bytes.Equal(first.Nonce(), second.Nonce()) || bytes.Equal(first.Ciphertext(), second.Ciphertext()) {
		t.Fatal("encryption reused a nonce or ciphertext")
	}
}

func TestKeyedDigestsMatchRef0VectorsAndKeyOrder(t *testing.T) {
	vault := mustVault(t, pythonVectorKey, pythonVectorPreviousKey)
	digests, err := vault.KeyedDigests([]byte("credential.create"), []byte("secret-one"))
	if err != nil {
		t.Fatalf("keyed digests: %v", err)
	}
	want := []string{
		"74a36d558df3f6febd12b291e6ab1c31bd0e490042f4fd783132266616035f55",
		"8e9ecf8542945068a659ab305353e8abf267ff6db05978d07c2ed38ec7d6aa4e",
	}
	if len(digests) != len(want) {
		t.Fatalf("digest count = %d", len(digests))
	}
	for index, digest := range digests {
		if got := hex.EncodeToString(digest); got != want[index] {
			t.Fatalf("digest %d = %s, want %s", index, got, want[index])
		}
	}
	replayed, err := vault.KeyedDigests([]byte("credential.create"), []byte("secret-one"))
	if err != nil || !bytes.Equal(digests[0], replayed[0]) {
		t.Fatal("same digest input was not stable")
	}
	changed, err := vault.KeyedDigests([]byte("credential.create"), []byte("secret-two"))
	if err != nil || bytes.Equal(digests[0], changed[0]) {
		t.Fatal("digest did not bind secret input")
	}
}

func TestSecretRedactsAndRejectsSerialization(t *testing.T) {
	const sentinel = "secret-that-must-not-serialize"
	secret := mustSecret(t, sentinel)
	secretCopy := *secret
	holder := struct {
		Secret *SecretValue `json:"secret"`
	}{Secret: secret}
	for _, rendered := range []string{
		fmt.Sprint(secret),
		fmt.Sprint(secretCopy),
		fmt.Sprintf("%+v", secret),
		fmt.Sprintf("%+v", secretCopy),
		fmt.Sprintf("%#v", secret),
		fmt.Sprintf("%#v", secretCopy),
		fmt.Sprintf("%+v", holder),
		fmt.Sprintf("%#v", holder),
		slog.AnyValue(secret).Resolve().String(),
		slog.AnyValue(secretCopy).Resolve().String(),
	} {
		if strings.Contains(rendered, sentinel) {
			t.Fatalf("formatted secret leaked: %s", rendered)
		}
	}
	if _, err := json.Marshal(secret); !errors.Is(err, ErrSecretSerialization) {
		t.Fatalf("JSON secret error = %v", err)
	}
	if _, err := json.Marshal(holder); !errors.Is(err, ErrSecretSerialization) {
		t.Fatalf("JSON holder error = %v", err)
	}
	if _, err := json.Marshal(secretCopy); !errors.Is(err, ErrSecretSerialization) {
		t.Fatalf("JSON copied secret error = %v", err)
	}
	if _, err := secret.MarshalText(); !errors.Is(err, ErrSecretSerialization) {
		t.Fatalf("text secret error = %v", err)
	}
	if _, err := secret.MarshalBinary(); !errors.Is(err, ErrSecretSerialization) {
		t.Fatalf("binary secret error = %v", err)
	}
	var encoded bytes.Buffer
	if err := gob.NewEncoder(&encoded).Encode(secret); !errors.Is(err, ErrSecretSerialization) {
		t.Fatalf("gob secret error = %v", err)
	}
	encoded.Reset()
	if err := gob.NewEncoder(&encoded).Encode(secretCopy); !errors.Is(err, ErrSecretSerialization) {
		t.Fatalf("gob copied secret error = %v", err)
	}
	if _, err := json.Marshal(CredentialEnvelope{}); err == nil {
		t.Fatal("credential envelope unexpectedly serialized")
	}
}

func TestCredentialSecretValidation(t *testing.T) {
	if _, err := NewSecretValue(""); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("empty secret error = %v", err)
	}
	short := mustSecret(t, "short")
	if err := ValidateCredentialSecret(CredentialProviderAPIKey, short); err == nil {
		t.Fatal("short provider key was accepted")
	} else if !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("short provider key error does not classify as invalid: %v", err)
	}
	if err := ValidateCredentialSecret(CredentialTinyFishAPIKey, short); err == nil {
		t.Fatal("short TinyFish key was accepted")
	} else if !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("short TinyFish key error does not classify as invalid: %v", err)
	}
	if err := ValidateCredentialSecret(CredentialRepositoryHTTPS, short); err != nil {
		t.Fatalf("short repository secret: %v", err)
	}
	if MaskedCredential != "••••" {
		t.Fatalf("credential mask = %q", MaskedCredential)
	}
}

func repeatedKey(keyID string, value byte) string {
	return encodedKey(keyID, bytes.Repeat([]byte{value}, credentialKeyBytes), false)
}

func encodedKey(keyID string, material []byte, padded bool) string {
	encoding := base64.RawURLEncoding
	if padded {
		encoding = base64.URLEncoding
	}
	return keyID + ":" + encoding.EncodeToString(material)
}

func mustVault(t *testing.T, active, previous string) *CredentialVault {
	t.Helper()
	vault, err := NewCredentialVault(active, previous)
	if err != nil {
		t.Fatalf("new credential vault: %v", err)
	}
	return vault
}

func mustSecret(t *testing.T, value string) *SecretValue {
	t.Helper()
	secret, err := NewSecretValue(value)
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	return secret
}

func credentialIDFromHex(t *testing.T, value string) CredentialID {
	t.Helper()
	decoded := decodeHex(t, value)
	var credentialID CredentialID
	if len(decoded) != len(credentialID) {
		t.Fatalf("credential ID length = %d", len(decoded))
	}
	copy(credentialID[:], decoded)
	return credentialID
}

func decodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return decoded
}
