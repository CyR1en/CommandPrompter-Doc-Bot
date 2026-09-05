package database

import (
	"net/url"
	"strings"
	"testing"
)

func TestURLFromEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", "database.internal")
	t.Setenv("POSTGRES_PORT", "6432")
	t.Setenv("POSTGRES_DB", "ref0_test")
	t.Setenv("POSTGRES_USER", "operator@example.com")
	t.Setenv("POSTGRES_PASSWORD", "p@ss:/word")

	value, err := URLFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if parsed.Host != "database.internal:6432" || parsed.Path != "/ref0_test" || parsed.User.Username() != "operator@example.com" || password != "p@ss:/word" {
		t.Fatalf("unexpected database URL components: %s", parsed.Redacted())
	}
	if strings.Contains(parsed.Redacted(), "p@ss:/word") {
		t.Fatal("redacted URL exposed the password")
	}
}

func TestURLFromEnvironmentRejectsInvalidPort(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_PASSWORD", "secret")
	t.Setenv("POSTGRES_PORT", "0")
	if _, err := URLFromEnvironment(); err == nil {
		t.Fatal("expected invalid port error")
	}
}
