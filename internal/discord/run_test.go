package discord

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeConfigFromEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://ref0:secret@127.0.0.1:5432/ref0?sslmode=disable")
	t.Setenv("APP_MASTER_KEY", "active:"+strings.Repeat("A", 43))
	t.Setenv("APP_PREVIOUS_MASTER_KEYS", "")
	t.Setenv("APP_DATA_DIR", "/var/lib/ref0")
	t.Setenv("DISCORD_SUPERVISOR_SCAN_SECONDS", "0.25")
	t.Setenv("DISCORD_CONTEXT_IDLE_MINUTES", "43200")

	config, err := RuntimeConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.dataDir != "/var/lib/ref0" || config.refreshEvery != 250*time.Millisecond ||
		config.idleExpiry != maximumConversationExpiry || config.poolConfig == nil || config.vault == nil {
		t.Fatalf("config=%+v", config)
	}
}

func TestRuntimeConfigDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://ref0:secret@127.0.0.1:5432/ref0?sslmode=disable")
	t.Setenv("APP_MASTER_KEY", "active:"+strings.Repeat("A", 43))
	t.Setenv("APP_PREVIOUS_MASTER_KEYS", "")
	t.Setenv("APP_DATA_DIR", "")
	t.Setenv("DISCORD_SUPERVISOR_SCAN_SECONDS", "")
	t.Setenv("DISCORD_CONTEXT_IDLE_MINUTES", "")

	config, err := RuntimeConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.dataDir != defaultDiscordDataDir || config.refreshEvery != defaultSupervisorScan ||
		config.idleExpiry != defaultConversationExpiry {
		t.Fatalf("config=%+v", config)
	}
}

func TestDiscordRuntimeDurationBounds(t *testing.T) {
	for _, value := range []string{"0", "0.09", "60.1", "nan", "inf", "invalid"} {
		t.Run("seconds_"+value, func(t *testing.T) {
			t.Setenv("DISCORD_SCAN_TEST", value)
			if _, err := discordSeconds("DISCORD_SCAN_TEST", time.Second); err == nil {
				t.Fatalf("accepted %q", value)
			}
		})
	}
	for _, value := range []string{"0", "43201", "1.5", "invalid"} {
		t.Run("minutes_"+value, func(t *testing.T) {
			t.Setenv("DISCORD_IDLE_TEST", value)
			if _, err := discordMinutes("DISCORD_IDLE_TEST", time.Minute); err == nil {
				t.Fatalf("accepted %q", value)
			}
		})
	}
}
