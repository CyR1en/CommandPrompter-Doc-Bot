package worker

import (
	"os"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
)

func TestConfigFromEnvironmentDefaultsAndOverrides(t *testing.T) {
	clearWorkerEnvironment(t)

	config, err := ConfigFromEnvironment()
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if config.WorkerID != "worker-1" || config.LeaseFor != time.Minute || config.HeartbeatEvery != 20*time.Second || config.PollEvery != time.Second || config.RetryBackoff != 5*time.Second {
		t.Fatalf("unexpected defaults: %#v", config)
	}

	t.Setenv("WORKER_ID", "worker-west")
	t.Setenv("JOB_LEASE_SECONDS", "90")
	t.Setenv("JOB_POLL_INTERVAL_SECONDS", "0.25")
	t.Setenv("JOB_RETRY_BACKOFF_SECONDS", "1.5")
	config, err = ConfigFromEnvironment()
	if err != nil {
		t.Fatalf("overrides: %v", err)
	}
	if config.WorkerID != jobs.WorkerID("worker-west") || config.LeaseFor != 90*time.Second || config.HeartbeatEvery != 30*time.Second || config.PollEvery != 250*time.Millisecond || config.RetryBackoff != 1500*time.Millisecond {
		t.Fatalf("unexpected overrides: %#v", config)
	}
}

func TestConfigFromEnvironmentRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "empty worker", key: "WORKER_ID", value: ""},
		{name: "fractional lease", key: "JOB_LEASE_SECONDS", value: "1.5"},
		{name: "long lease", key: "JOB_LEASE_SECONDS", value: "3601"},
		{name: "zero poll", key: "JOB_POLL_INTERVAL_SECONDS", value: "0"},
		{name: "nan poll", key: "JOB_POLL_INTERVAL_SECONDS", value: "NaN"},
		{name: "long retry", key: "JOB_RETRY_BACKOFF_SECONDS", value: "3601"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearWorkerEnvironment(t)
			t.Setenv(test.key, test.value)
			if _, err := ConfigFromEnvironment(); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func clearWorkerEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"WORKER_ID",
		"JOB_LEASE_SECONDS",
		"JOB_POLL_INTERVAL_SECONDS",
		"JOB_RETRY_BACKOFF_SECONDS",
	} {
		value, present := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}
