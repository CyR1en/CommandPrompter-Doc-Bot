package security

import (
	"strings"
	"testing"
)

const pythonPasswordVector = "scrypt$v=1$n=16384$r=8$p=1$AAECAwQFBgcICQoLDA0ODw$11kKyiyYAc8G7rp3KmncMc44YlkdllIqxOa7pq0fMaU"

func TestPasswordHashMatchesGoldenValue(t *testing.T) {
	var salt [passwordSaltBytes]byte
	for index := range salt {
		salt[index] = byte(index)
	}
	encoded, err := hashPasswordWithSalt("correct horse battery staple", salt)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if encoded != pythonPasswordVector {
		t.Fatalf("password hash mismatch\n got: %s\nwant: %s", encoded, pythonPasswordVector)
	}
	if !VerifyPassword("correct horse battery staple", pythonPasswordVector) {
		t.Fatal("Python password vector did not verify")
	}
	if VerifyPassword("wrong password", pythonPasswordVector) {
		t.Fatal("wrong password verified")
	}
}

func TestPasswordHashUsesRandomSaltAndExactFormat(t *testing.T) {
	const password = "correct horse battery staple"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if first == second {
		t.Fatal("password hashes reused a salt")
	}
	const prefix = "scrypt$v=1$n=16384$r=8$p=1$"
	if !strings.HasPrefix(first, prefix) || !strings.HasPrefix(second, prefix) {
		t.Fatalf("password format changed: %q / %q", first, second)
	}
	if !VerifyPassword(password, first) || !VerifyPassword(password, second) {
		t.Fatal("generated password hash did not verify")
	}
}

func TestMalformedOrUnknownPasswordHashesAreRejected(t *testing.T) {
	mutatedDigest := pythonPasswordVector[:len(pythonPasswordVector)-1] + "A"
	tests := []string{
		"not-a-password-hash",
		"scrypt$v=2$n=16384$r=8$p=1$bad$bad",
		"scrypt$v=1$n=1073741824$r=8$p=1$bad$bad",
		"scrypt$v=1$n=16384$r=8$p=1$bad$bad",
		pythonPasswordVector + "$extra",
		mutatedDigest,
	}
	for _, encoded := range tests {
		if VerifyPassword("correct horse battery staple", encoded) {
			t.Fatalf("malformed password hash verified: %q", encoded)
		}
	}
}
