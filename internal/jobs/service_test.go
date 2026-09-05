package jobs

import (
	"testing"
	"time"
)

func TestPublicSnapshotIsSecretSafeAndPythonCompatible(t *testing.T) {
	created := time.Date(2026, 8, 30, 1, 2, 3, 456_789_000, time.UTC)
	finished := time.Date(2026, 8, 30, 1, 2, 4, 0, time.UTC)
	value := Snapshot{
		ID: JobID{1}, Type: SyncSource, TargetType: "source", TargetID: UUID{2},
		Status: Succeeded, AttemptCount: 1, MaxAttempts: 3, Progress: 100,
		LeaseGeneration: 1, Result: map[string]any{"outcome": "safe"},
		CreatedAt: created, UpdatedAt: finished, FinishedAt: &finished,
	}
	public := PublicSnapshot(value)
	if len(public) != 18 || public["job_type"] != "sync_source" || public["status"] != "succeeded" ||
		public["created_at"] != "2026-08-30T01:02:03.456789+00:00" ||
		public["finished_at"] != "2026-08-30T01:02:04+00:00" {
		t.Fatalf("public snapshot=%#v", public)
	}
	for _, forbidden := range []string{"payload", "operation_key"} {
		if _, found := public[forbidden]; found {
			t.Fatalf("public snapshot included %s", forbidden)
		}
	}
}

func TestCancellationResultConvergence(t *testing.T) {
	for _, test := range []struct {
		expected Status
		current  Status
		want     bool
	}{
		{Cancelled, Cancelled, true},
		{CancelRequested, CancelRequested, true},
		{CancelRequested, Cancelled, true},
		{CancelRequested, Succeeded, false},
		{Cancelled, CancelRequested, false},
	} {
		if got := cancellationResultApplies(test.expected, test.current); got != test.want {
			t.Fatalf("resultApplies(%s,%s)=%v", test.expected, test.current, got)
		}
	}
}
