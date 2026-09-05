package credentials

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/security"
)

const oracleKey = "v1:MTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTE"

func TestValidationAndSecretContainmentMatchPython(t *testing.T) {
	secret := credentialSecret(t, "provider-secret-one")
	command := CreateCommand{Kind: ProviderAPIKey, Label: "Provider key", Secret: secret}
	if err := ValidateCreate(command); err != nil {
		t.Fatalf("valid create: %v", err)
	}
	for _, label := range []string{"", " padded", "padded ", strings.Repeat("界", 256)} {
		command.Label = label
		if err := ValidateCreate(command); !errors.Is(err, ErrInvalidLabel) {
			t.Fatalf("label %q error = %v", label, err)
		}
	}
	command = CreateCommand{Kind: ProviderAPIKey, Label: "Provider key", Secret: credentialSecret(t, "short")}
	if err := ValidateCreate(command); err == nil || err.Error() != "provider API keys must contain at least 8 characters" {
		t.Fatalf("short provider error = %v", err)
	}
	if rendered := fmt.Sprintf("%#v", CreateCommand{Kind: ProviderAPIKey, Label: "Provider", Secret: secret}); strings.Contains(rendered, secret.Reveal()) {
		t.Fatalf("command formatting leaked secret: %s", rendered)
	}
	if _, err := json.Marshal(CreateCommand{Kind: ProviderAPIKey, Label: "Provider", Secret: secret}); !errors.Is(err, security.ErrSecretSerialization) {
		t.Fatalf("command serialization error = %v", err)
	}
}

func TestCredentialDigestAndSnapshotGoldenValues(t *testing.T) {
	vault, err := security.NewCredentialVault(oracleKey, "")
	if err != nil {
		t.Fatal(err)
	}
	digests, err := vault.KeyedDigests(
		[]byte("credential.create"), []byte("PROVIDER_API_KEY"),
		[]byte("Provider key"), []byte("provider-secret-one"),
	)
	if err != nil || hex.EncodeToString(digests[0]) != "b4721045885a3389049a36c8074cfd8d559793136eded8b4a487795283a05820" {
		t.Fatalf("create digest = %x, %v", digests[0], err)
	}
	id, err := ParseID("00112233-4455-6677-8899-aabbccddeeff")
	if err != nil || id.String() != "00112233-4455-6677-8899-aabbccddeeff" {
		t.Fatalf("credential ID = %s, %v", id, err)
	}
	digests, err = vault.KeyedDigests(
		[]byte("credential.rotate"), id[:], []byte("provider-secret-two"),
	)
	if err != nil || hex.EncodeToString(digests[0]) != "7064c1020d09ceb22c128ccf28d170266152ee34fcb652529ec0802ebe47f4f3" {
		t.Fatalf("rotate digest = %x, %v", digests[0], err)
	}
	created := time.Date(2026, 8, 30, 12, 34, 56, 123456000, time.FixedZone("UTC", 0))
	snapshot := metadataSnapshot(Metadata{
		ID: id, Kind: ProviderAPIKey, Label: "Provider key", MaskedValue: MaskedValue,
		SecretVersion: 1, KeyID: "v1", CreatedAt: created,
	})
	if snapshot["created_at"] != "2026-08-30T12:34:56.123456+00:00" || snapshot["rotated_at"] != nil || snapshot["kind"] != "provider_api_key" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func credentialSecret(t *testing.T, value string) *security.SecretValue {
	t.Helper()
	secret, err := security.NewSecretValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}
