package api

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

const testMetricsBearerToken = "metrics-test-token-not-a-secret-000000000000"

func TestConfigFromEnvironmentPreservesDatabaseEnvironment(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_HOST", "database.internal")
	t.Setenv("POSTGRES_PORT", "5544")
	t.Setenv("POSTGRES_DB", "ref0")
	t.Setenv("POSTGRES_USER", "ref0")
	t.Setenv("POSTGRES_PASSWORD", "p@ss:/word")
	t.Setenv("API_HOST", "127.0.0.1")
	t.Setenv("API_PORT", "8123")
	t.Setenv("APP_DATA_DIR", "/var/lib/ref0")
	t.Setenv("APP_VERSION", "1.2.3")
	t.Setenv("APP_BOOTSTRAP_TOKEN", "one-time-bootstrap-token")
	t.Setenv("OPERATOR_SESSION_TTL_MINUTES", "60")
	t.Setenv("BOOTSTRAP_TOKEN_TTL_MINUTES", "15")
	t.Setenv("KNOWLEDGE_BASE_DELETE_GRACE_HOURS", "48")
	t.Setenv("PUBLIC_ORIGIN", "https://control.test")

	config, err := ConfigFromEnvironment()
	if err != nil {
		t.Fatalf("ConfigFromEnvironment() error = %v", err)
	}
	if config.address != "127.0.0.1:8123" {
		t.Fatalf("address = %q", config.address)
	}
	if config.dataDir != "/var/lib/ref0" || config.version != "1.2.3" {
		t.Fatalf("unexpected public config: dataDir=%q version=%q", config.dataDir, config.version)
	}
	if config.bootstrapToken == nil || config.bootstrapToken.Reveal() != "one-time-bootstrap-token" ||
		config.metricsBearerToken == nil || config.metricsBearerToken.Reveal() != testMetricsBearerToken ||
		config.sessionTTL != time.Hour || config.bootstrapTokenTTL != 15*time.Minute ||
		config.sessionCookieMaxAge != 3600 || !config.sessionCookieSecure || config.deleteGrace != 48*time.Hour {
		t.Fatal("operator auth configuration was not preserved")
	}
	connection := config.poolConfig.ConnConfig
	if connection.Host != "database.internal" || connection.Port != 5544 ||
		connection.Database != "ref0" || connection.User != "ref0" ||
		connection.Password != "p@ss:/word" {
		t.Fatal("POSTGRES_* settings were not preserved")
	}
}

func TestConfigFromEnvironmentFailsClosedWithoutDisclosingSecrets(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T)
	}{
		{
			name: "missing database credentials",
			change: func(t *testing.T) {
				t.Setenv("DATABASE_URL", "")
				t.Setenv("POSTGRES_PASSWORD", "")
			},
		},
		{
			name: "invalid database URL",
			change: func(t *testing.T) {
				t.Setenv("DATABASE_URL", "://database-secret-sentinel")
			},
		},
		{
			name: "missing master key",
			change: func(t *testing.T) {
				t.Setenv("APP_MASTER_KEY", "")
			},
		},
		{
			name: "invalid master key",
			change: func(t *testing.T) {
				t.Setenv("APP_MASTER_KEY", "secret-id:not-the-secret-sentinel!")
			},
		},
		{
			name: "duplicate previous key material",
			change: func(t *testing.T) {
				encoded := encodedKey(0)
				t.Setenv("APP_MASTER_KEY", "active:"+encoded)
				t.Setenv("APP_PREVIOUS_MASTER_KEYS", "previous:"+encoded)
			},
		},
		{
			name: "invalid port",
			change: func(t *testing.T) {
				t.Setenv("API_PORT", "65536")
			},
		},
		{
			name: "invalid version",
			change: func(t *testing.T) {
				t.Setenv("APP_VERSION", "release candidate")
			},
		},
		{
			name: "invalid public origin",
			change: func(t *testing.T) {
				t.Setenv("PUBLIC_ORIGIN", "://origin-secret-sentinel")
			},
		},
		{
			name: "insecure public origin",
			change: func(t *testing.T) {
				t.Setenv("PUBLIC_ORIGIN", "http://control.test")
			},
		},
		{
			name: "missing metrics bearer token",
			change: func(t *testing.T) {
				t.Setenv("METRICS_BEARER_TOKEN", "")
			},
		},
		{
			name: "invalid metrics bearer token",
			change: func(t *testing.T) {
				t.Setenv("METRICS_BEARER_TOKEN", "metrics-secret-sentinel contains spaces 0123456789")
			},
		},
		{
			name: "invalid session TTL",
			change: func(t *testing.T) {
				t.Setenv("OPERATOR_SESSION_TTL_MINUTES", "0")
			},
		},
		{
			name: "invalid bootstrap TTL",
			change: func(t *testing.T) {
				t.Setenv("BOOTSTRAP_TOKEN_TTL_MINUTES", "not-a-number")
			},
		},
		{
			name: "invalid knowledge base delete grace",
			change: func(t *testing.T) {
				t.Setenv("KNOWLEDGE_BASE_DELETE_GRACE_HOURS", "0")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			test.change(t)
			_, err := ConfigFromEnvironment()
			if err == nil {
				t.Fatal("ConfigFromEnvironment() succeeded")
			}
			for _, secret := range []string{
				"database-secret-sentinel",
				"not-the-secret-sentinel",
				encodedKey(0),
				"origin-secret-sentinel",
				"metrics-secret-sentinel",
			} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error disclosed a configured secret: %q", err)
				}
			}
		})
	}
}

func TestConfigFromEnvironmentAllowsHTTPOnlyForLoopbackDevelopment(t *testing.T) {
	for _, origin := range []string{
		"http://localhost:8000",
		"http://dashboard.localhost:8000",
		"http://127.0.0.1:8000",
		"http://[::1]:8000",
	} {
		t.Run(origin, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv("PUBLIC_ORIGIN", origin)
			config, err := ConfigFromEnvironment()
			if err != nil {
				t.Fatalf("ConfigFromEnvironment() error = %v", err)
			}
			if config.sessionCookieSecure {
				t.Fatal("loopback HTTP session cookie is secure")
			}
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgresql://ref0:database-password@127.0.0.1/ref0")
	t.Setenv("POSTGRES_PASSWORD", "")
	t.Setenv("APP_MASTER_KEY", "active:"+encodedKey(0))
	t.Setenv("APP_PREVIOUS_MASTER_KEYS", "previous:"+encodedKey(1))
	t.Setenv("APP_BOOTSTRAP_TOKEN", "")
	t.Setenv("APP_DATA_DIR", "")
	t.Setenv("APP_FRONTEND_DIR", "")
	t.Setenv("APP_VERSION", "")
	t.Setenv("API_HOST", "")
	t.Setenv("API_PORT", "")
	t.Setenv("OPERATOR_SESSION_TTL_MINUTES", "")
	t.Setenv("BOOTSTRAP_TOKEN_TTL_MINUTES", "")
	t.Setenv("KNOWLEDGE_BASE_DELETE_GRACE_HOURS", "")
	t.Setenv("PUBLIC_ORIGIN", "")
	t.Setenv("METRICS_BEARER_TOKEN", testMetricsBearerToken)
}

func encodedKey(value byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
}
