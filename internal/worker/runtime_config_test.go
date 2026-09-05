package worker

import (
	"strings"
	"testing"
	"time"
)

func TestRuntimeConfigFromEnvironmentPreservesWorkerContract(t *testing.T) {
	setRuntimeEnvironment(t)
	t.Setenv("APP_DATA_DIR", "/var/lib/ref0")
	t.Setenv("APP_VERSION", "2.3.4")
	t.Setenv("KNOWLEDGE_BASE_DELETE_GRACE_HOURS", "24")
	t.Setenv("RETENTION_SCAN_SECONDS", "12.5")
	t.Setenv("RETENTION_BATCH_SIZE", "55")
	t.Setenv("SOURCE_SNAPSHOT_RETENTION_DAYS", "31")
	t.Setenv("FAILED_DRAFT_RETENTION_DAYS", "15")
	t.Setenv("JOB_LOG_RETENTION_DAYS", "32")
	t.Setenv("EVENT_LOG_RETENTION_DAYS", "34")
	t.Setenv("AGENT_RUN_RETENTION_DAYS", "93")
	t.Setenv("DISCORD_CONTEXT_RETENTION_DAYS", "7")
	t.Setenv("OLD_WIKI_RETENTION_DAYS", "91")

	config, err := RuntimeConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.DataDir != "/var/lib/ref0" || config.ApplicationVersion != "2.3.4" ||
		config.DeleteGrace != 24*time.Hour || config.RetentionScanEvery != 12500*time.Millisecond ||
		config.RetentionPolicy.BatchSize != 55 || config.RetentionPolicy.SourceSnapshots != 31*24*time.Hour ||
		config.RetentionPolicy.FailedDrafts != 15*24*time.Hour || config.RetentionPolicy.JobLogs != 32*24*time.Hour ||
		config.RetentionPolicy.EventLog != 34*24*time.Hour || config.RetentionPolicy.AgentRuns != 93*24*time.Hour ||
		config.RetentionPolicy.DiscordContext != 7*24*time.Hour || config.RetentionPolicy.OldWikis != 91*24*time.Hour {
		t.Fatalf("runtime config=%+v", config)
	}
	if config.CapsuleSocketPaths != [2]string{"/run/capsule-slot-0/capsule.sock", "/run/capsule-slot-1/capsule.sock"} {
		t.Fatalf("capsule paths=%v", config.CapsuleSocketPaths)
	}
}

func TestRuntimeConfigRejectsInvalidEnvironment(t *testing.T) {
	tests := map[string]func(*testing.T){
		"runtime":       func(t *testing.T) { t.Setenv("DOCUMENTATION_AGENT_RUNTIME", "") },
		"capsule count": func(t *testing.T) { t.Setenv("PI_CAPSULE_SOCKET_PATHS", `[]`) },
		"capsule duplicate": func(t *testing.T) {
			t.Setenv("PI_CAPSULE_SOCKET_PATHS", `["/run/a/capsule.sock","/run/a/../a/capsule.sock"]`)
		},
		"capsule relative": func(t *testing.T) {
			t.Setenv("PI_CAPSULE_SOCKET_PATHS", `["relative/capsule.sock","/run/b/capsule.sock"]`)
		},
		"data directory":  func(t *testing.T) { t.Setenv("APP_DATA_DIR", "relative") },
		"version":         func(t *testing.T) { t.Setenv("APP_VERSION", "release candidate") },
		"retention scan":  func(t *testing.T) { t.Setenv("RETENTION_SCAN_SECONDS", "NaN") },
		"retention batch": func(t *testing.T) { t.Setenv("RETENTION_BATCH_SIZE", "1001") },
		"retention days":  func(t *testing.T) { t.Setenv("OLD_WIKI_RETENTION_DAYS", "0") },
		"Agent run retention": func(t *testing.T) {
			t.Setenv("AGENT_RUN_RETENTION_DAYS", "0")
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			setRuntimeEnvironment(t)
			change(t)
			if _, err := RuntimeConfigFromEnvironment(); err == nil {
				t.Fatal("invalid runtime environment was admitted")
			} else if strings.Contains(err.Error(), "/run/") {
				t.Fatalf("configuration error disclosed a path: %v", err)
			}
		})
	}
}

func TestRuntimeConfigDefaultsAgentRunRetentionToNinetyDays(t *testing.T) {
	setRuntimeEnvironment(t)
	config, err := RuntimeConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.RetentionPolicy.AgentRuns != 90*24*time.Hour {
		t.Fatalf("Agent run retention = %s", config.RetentionPolicy.AgentRuns)
	}
}

func setRuntimeEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DOCUMENTATION_AGENT_RUNTIME", "pi-capsule")
	t.Setenv("PI_CAPSULE_SOCKET_PATHS", `["/run/capsule-slot-0/capsule.sock","/run/capsule-slot-1/capsule.sock"]`)
	for _, name := range []string{
		"APP_DATA_DIR", "APP_VERSION", "KNOWLEDGE_BASE_DELETE_GRACE_HOURS",
		"RETENTION_SCAN_SECONDS", "RETENTION_BATCH_SIZE", "SOURCE_SNAPSHOT_RETENTION_DAYS",
		"FAILED_DRAFT_RETENTION_DAYS", "JOB_LOG_RETENTION_DAYS", "EVENT_LOG_RETENTION_DAYS", "AGENT_RUN_RETENTION_DAYS",
		"DISCORD_CONTEXT_RETENTION_DAYS", "OLD_WIKI_RETENTION_DAYS",
	} {
		t.Setenv(name, "")
	}
}
