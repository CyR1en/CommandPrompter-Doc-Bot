package retention

import (
	"context"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
)

type fakeRetentionApply struct {
	result map[string]any
	permit jobs.Permit
}

func (fake *fakeRetentionApply) Apply(_ context.Context, permit jobs.Permit) (map[string]any, error) {
	fake.permit = permit
	return fake.result, nil
}

func TestPolicyAndHandlerContract(t *testing.T) {
	valid := Policy{
		SourceSnapshots: 30 * 24 * time.Hour, FailedDrafts: 14 * 24 * time.Hour,
		JobLogs: 30 * 24 * time.Hour, EventLog: 30 * 24 * time.Hour, AgentRuns: 90 * 24 * time.Hour,
		DiscordContext: 30 * 24 * time.Hour,
		OldWikis:       90 * 24 * time.Hour, BatchSize: 100,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalidDuration := valid
	invalidDuration.AgentRuns = 0
	if err := invalidDuration.Validate(); err == nil || err.Error() != "retention durations must be positive" {
		t.Fatalf("duration err = %v", err)
	}
	invalidBatch := valid
	invalidBatch.BatchSize = 1_001
	if err := invalidBatch.Validate(); err == nil || err.Error() != "retention batch size must be between 1 and 1000" {
		t.Fatalf("batch err = %v", err)
	}
	service := &fakeRetentionApply{result: map[string]any{"job_logs": 2}}
	registry, err := Handlers(service)
	if err != nil {
		t.Fatal(err)
	}
	permit := jobs.Permit{JobID: jobs.JobID{1}, WorkerID: "worker", LeaseGeneration: 1}
	result, err := registry[jobs.ApplyRetention](context.Background(), jobs.Command{
		Type: jobs.ApplyRetention, TargetType: "system", TargetID: TargetID,
		Payload: map[string]any{}, OperationKey: OperationKey,
	}, permit)
	if err != nil || result["job_logs"] != 2 || service.permit != permit {
		t.Fatalf("result=%v permit=%+v err=%v", result, service.permit, err)
	}
	_, err = registry[jobs.ApplyRetention](context.Background(), jobs.Command{
		Type: jobs.ApplyRetention, TargetType: "system", TargetID: TargetID,
		Payload: map[string]any{"unexpected": true}, OperationKey: OperationKey,
	}, permit)
	if err == nil || err.Error() != "retention command is invalid" {
		t.Fatalf("command err = %v", err)
	}
}

func TestRunSchedulingRejectsInvalidIntervalAndStops(t *testing.T) {
	service := &fakeRetentionScheduler{scheduled: make(chan struct{}, 1)}
	if err := RunScheduling(context.Background(), service, 0, nil); err == nil {
		t.Fatal("accepted zero retention interval")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunScheduling(ctx, service, time.Hour, nil) }()
	<-service.scheduled
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type fakeRetentionScheduler struct{ scheduled chan struct{} }

func (service *fakeRetentionScheduler) Schedule(context.Context) (jobs.JobID, error) {
	select {
	case service.scheduled <- struct{}{}:
	default:
	}
	return jobs.JobID{}, nil
}
