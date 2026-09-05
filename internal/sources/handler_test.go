package sources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/worker"
)

func TestSourceHandlersPreservePermitAndRetryDecision(t *testing.T) {
	run := handlerRun(t, Validation)
	permit := jobs.Permit{JobID: run.JobID, WorkerID: "source-worker", LeaseGeneration: 1}
	service := &fakeHandlerService{run: run, permit: permit}
	execution := &fakeHandlerExecution{validation: validationFailure(run.ID, "source_validation:connection", true)}
	registry, err := Handlers(service, execution)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry[jobs.ValidateSource](context.Background(), handlerCommand(run), permit)
	var failure *worker.HandlerFailure
	if !errors.As(err, &failure) || failure.SanitizedError != "source_validation:connection" || !failure.Retryable {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}
	if service.completed != 1 || execution.calls != 1 {
		t.Fatalf("completed=%d calls=%d", service.completed, execution.calls)
	}
}

func TestSourceHandlersRejectStalePermitBeforeExecution(t *testing.T) {
	run := handlerRun(t, Synchronization)
	permit := jobs.Permit{JobID: run.JobID, WorkerID: "source-worker", LeaseGeneration: 1}
	service := &fakeHandlerService{run: run, permit: permit}
	execution := &fakeHandlerExecution{}
	registry, _ := Handlers(service, execution)
	stale := permit
	stale.LeaseGeneration++
	if _, err := registry[jobs.SyncSource](context.Background(), handlerCommand(run), stale); !errors.Is(err, jobs.ErrStalePermit) {
		t.Fatalf("stale error=%v", err)
	}
	if execution.calls != 0 || service.completed != 0 {
		t.Fatalf("execution=%d completed=%d", execution.calls, service.completed)
	}
}

func TestSourceHandlersRejectMalformedCommand(t *testing.T) {
	run := handlerRun(t, Validation)
	permit := jobs.Permit{JobID: run.JobID, WorkerID: "source-worker", LeaseGeneration: 1}
	service := &fakeHandlerService{run: run, permit: permit}
	execution := &fakeHandlerExecution{}
	registry, _ := Handlers(service, execution)
	command := handlerCommand(run)
	command.Payload["extra"] = true
	if _, err := registry[jobs.ValidateSource](context.Background(), command, permit); err == nil {
		t.Fatal("malformed source command was accepted")
	}
	if execution.calls != 0 {
		t.Fatal("malformed command reached execution")
	}
}

type fakeHandlerService struct {
	run       Sync
	permit    jobs.Permit
	completed int
}

func (service *fakeHandlerService) Begin(_ context.Context, id ID, permit jobs.Permit) (Sync, error) {
	if permit != service.permit {
		return Sync{}, jobs.ErrStalePermit
	}
	if id != service.run.ID {
		return Sync{}, ErrNotFound
	}
	return service.run, nil
}

func (service *fakeHandlerService) CompleteValidation(_ context.Context, completion ValidationCompletion, permit jobs.Permit) (Sync, error) {
	if permit != service.permit {
		return Sync{}, jobs.ErrStalePermit
	}
	service.completed++
	result := service.run
	result.CompletedAt = result.StartedAt
	result.SanitizedError = completion.SanitizedError
	result.ResolvedNativeVersion = completion.ResolvedNativeVersion
	result.Status = SyncSucceeded
	if completion.SanitizedError != nil {
		result.Status = SyncFailed
	}
	return result, nil
}

func (service *fakeHandlerService) CompleteSync(_ context.Context, completion SyncCompletion, permit jobs.Permit) (Sync, error) {
	if permit != service.permit {
		return Sync{}, jobs.ErrStalePermit
	}
	service.completed++
	result := service.run
	result.CompletedAt = result.StartedAt
	result.SanitizedError = completion.SanitizedError
	result.Status = SyncSucceeded
	if completion.SanitizedError != nil {
		result.Status = SyncFailed
	}
	return result, nil
}

type fakeHandlerExecution struct {
	validation ValidationCompletion
	calls      int
}

func (execution *fakeHandlerExecution) Validate(_ context.Context, run Sync) ValidationCompletion {
	execution.calls++
	if execution.validation.SyncID == (ID{}) {
		value := "a000000000000000000000000000000000000000"
		return ValidationCompletion{SyncID: run.ID, ResolvedNativeVersion: &value}
	}
	return execution.validation
}

func (execution *fakeHandlerExecution) Sync(_ context.Context, run Sync) SyncCompletion {
	execution.calls++
	return SyncCompletion{SyncID: run.ID, SanitizedError: stringValue("source_sync:test")}
}

func (*fakeHandlerExecution) DiscardReusedCandidate(Sync) error { return nil }

func handlerRun(t *testing.T, kind SyncKind) Sync {
	t.Helper()
	now := time.Now()
	sourceID := testID(t, "10000000-0000-4000-8000-000000000001")
	syncID := testID(t, "20000000-0000-4000-8000-000000000002")
	jobUUID := testID(t, "30000000-0000-4000-8000-000000000003")
	repository := publicRepository(t)
	run := Sync{ID: syncID, SourceID: sourceID, JobID: jobs.JobID(jobUUID), Kind: kind, CapturedSourceVersion: 2, CapturedConfigurationVersion: 1, Repository: &CapturedRepository{Privacy: Public, Remote: repository.Remote, Reference: repository.Reference}, Status: SyncRunning, CreatedAt: now, StartedAt: &now}
	if kind == Synchronization {
		candidate := testID(t, "40000000-0000-4000-8000-000000000004")
		run.CandidateRevisionID = &candidate
	}
	return run
}

func handlerCommand(run Sync) jobs.Command {
	jobType := jobs.ValidateSource
	if run.Kind == Synchronization {
		jobType = jobs.SyncSource
	}
	return jobs.Command{Type: jobType, TargetType: "source", TargetID: jobs.UUID(run.SourceID), Payload: map[string]any{"source_sync_id": run.ID.String()}, OperationKey: "source-test", MaxAttempts: 3}
}
