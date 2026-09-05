package auth

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/cyr1en/ref0/internal/security"
)

func TestUsernameMatchesPythonNormalization(t *testing.T) {
	tests := []struct {
		value   string
		display string
		key     string
	}{
		{"  ＯＰＥＲＡＴＯＲ  ", "OPERATOR", "operator"},
		{"Straße", "Straße", "strasse"},
		{"\u212Aelvin", "Kelvin", "kelvin"},
		{"\x1cOperator\x1f", "Operator", "operator"},
	}
	for _, test := range tests {
		username, err := ParseUsername(test.value)
		if err != nil {
			t.Fatalf("ParseUsername(%q): %v", test.value, err)
		}
		if username.Display != test.display || username.Key != test.key {
			t.Fatalf("ParseUsername(%q) = %#v", test.value, username)
		}
	}
	for _, value := range []string{
		"   ",
		strings.Repeat("a", 256),
		strings.Repeat("a", 254) + "ß",
		string([]byte{0xff}),
	} {
		if _, err := ParseUsername(value); err == nil {
			t.Fatalf("invalid username %q was accepted", value)
		}
	}
}

func TestSessionDigestAndCSRFMatchRef0Vectors(t *testing.T) {
	token, err := NewSessionToken("session-token")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString("00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	var sessionID SessionID
	copy(sessionID[:], decoded)

	digest := DigestToken(token.Reveal())
	if got := hex.EncodeToString(digest[:]); got != "c101e911469c969171040b50d70543313cf968fdef5bacc780776f8fb399ab36" {
		t.Fatalf("token digest = %s", got)
	}
	const expectedCSRF = "iKF7Wzmq3dm2ZH8pQwXB_8ugcKsdpmtwobyn3w_g3u0"
	if got := CSRFTokenFor(token, sessionID); got != expectedCSRF {
		t.Fatalf("CSRF token = %s", got)
	}
	if !CSRFTokenMatches(token, sessionID, expectedCSRF) || CSRFTokenMatches(token, sessionID, "wrong") {
		t.Fatal("CSRF comparison did not bind token and session ID")
	}
	if sessionID.String() != "00112233-4455-6677-8899-aabbccddeeff" {
		t.Fatalf("session ID = %s", sessionID.String())
	}
}

func TestSessionTokenAndCommandsDoNotFormatOrSerializeSecrets(t *testing.T) {
	const tokenValue = "session-secret-sentinel"
	token, err := NewSessionToken(tokenValue)
	if err != nil {
		t.Fatal(err)
	}
	password, err := security.NewSecretValue("password-secret-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	command := LoginCommand{Username: Username{Display: "Operator", Key: "operator"}, Password: password}
	authenticated := AuthenticatedSession{Token: token, CSRFToken: "csrf-secret-sentinel"}
	for _, rendered := range []string{
		fmt.Sprint(token),
		fmt.Sprintf("%+v", token),
		fmt.Sprintf("%#v", token),
		fmt.Sprintf("%+v", command),
		fmt.Sprintf("%#v", command),
		fmt.Sprintf("%+v", authenticated),
		fmt.Sprintf("%#v", authenticated),
		slog.AnyValue(token).Resolve().String(),
		slog.AnyValue(authenticated).Resolve().String(),
	} {
		if strings.Contains(rendered, tokenValue) || strings.Contains(rendered, password.Reveal()) ||
			strings.Contains(rendered, authenticated.CSRFToken) {
			t.Fatalf("secret formatted: %s", rendered)
		}
	}
	if _, err = json.Marshal(token); err == nil {
		t.Fatal("session token serialized")
	}
	if _, err = json.Marshal(authenticated); err == nil {
		t.Fatal("authenticated session serialized")
	}
}

func TestDummyPasswordHashUsesRef0Sentinel(t *testing.T) {
	if !security.VerifyPassword("ref0-dummy-password", dummyPasswordHash) {
		t.Fatal("dummy password hash does not match the ref0 sentinel")
	}
	if security.VerifyPassword("another-password", dummyPasswordHash) {
		t.Fatal("dummy password hash accepted another password")
	}
}
