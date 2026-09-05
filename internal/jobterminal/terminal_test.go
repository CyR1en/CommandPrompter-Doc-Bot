package jobterminal

import (
	"context"
	"testing"

	"github.com/cyr1en/ref0/internal/jobs"
)

func TestTerminalDispatchCoversEveryJobType(t *testing.T) {
	tests := []struct {
		name    string
		jobType jobs.Type
	}{
		{"validate source", jobs.ValidateSource},
		{"sync source", jobs.SyncSource},
		{"prepare documentation", jobs.PrepareRun},
		{"plan documentation", jobs.PlanRun},
		{"generate documentation page", jobs.GeneratePage},
		{"finalize documentation", jobs.FinalizeRun},
		{"discover provider", jobs.DiscoverEndpoint},
		{"probe provider", jobs.ProbeModel},
		{"refresh Discord", jobs.RefreshDiscord},
		{"purge knowledge base", jobs.PurgeKnowledgeBase},
		{"apply retention", jobs.ApplyRetention},
	}
	if len(callbacks) != len(tests) {
		t.Fatalf("terminal callbacks=%d job types=%d", len(callbacks), len(tests))
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !jobs.ValidType(test.jobType) || callbacks[test.jobType] == nil {
				t.Fatalf("job type %s has no terminal callback", test.jobType)
			}
		})
	}
}

func TestTerminalDispatchRejectsInvalidInvocation(t *testing.T) {
	tests := []struct {
		name string
		job  jobs.Snapshot
	}{
		{"nonterminal status", jobs.Snapshot{Type: jobs.ApplyRetention, Status: jobs.RetryWait}},
		{"unknown job type", jobs.Snapshot{Type: jobs.Type("UNKNOWN"), Status: jobs.Failed}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Callback(context.Background(), nil, test.job); err == nil {
				t.Fatal("invalid terminal callback invocation succeeded")
			}
		})
	}
	if err := Callback(context.Background(), nil, jobs.Snapshot{Type: jobs.ApplyRetention, Status: jobs.Cancelled}); err != nil {
		t.Fatalf("resource-free retention callback: %v", err)
	}
}
