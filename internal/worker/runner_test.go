package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
)

func TestRunnerDispatchesAndCompletesAcceptedResult(t *testing.T) {
	queue := newQueueStub()
	want := map[string]any{"page_count": 4}
	called := false
	runner := testRunner(t, queue, Registry{
		jobs.GeneratePage: func(_ context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
			called = true
			if !reflect.DeepEqual(command, queue.command) || permit != *queue.permit {
				t.Fatal("handler received the wrong command or permit")
			}
			return want, nil
		},
	}, testRunnerConfig())

	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.completions) != 1 || !reflect.DeepEqual(queue.completions[0], want) {
		t.Fatalf("completions = %#v", queue.completions)
	}
}

func TestRunnerUnknownTypeRetriesClosed(t *testing.T) {
	queue := newQueueStub()
	runner := testRunner(t, queue, nil, testRunnerConfig())

	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.retries) != 1 || queue.retries[0].sanitizedError != unknownJobError || queue.retries[0].delay != testRunnerConfig().RetryBackoff {
		t.Fatalf("retries = %#v", queue.retries)
	}
}

func TestRunnerHonorsExplicitHandlerFailureDecision(t *testing.T) {
	tests := []struct {
		name      string
		retryable bool
	}{
		{name: "retryable", retryable: true},
		{name: "nonretryable", retryable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queue := newQueueStub()
			runner := testRunner(t, queue, Registry{
				jobs.GeneratePage: func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error) {
					return nil, &HandlerFailure{SanitizedError: "provider_discovery:timeout", Retryable: test.retryable}
				},
			}, testRunnerConfig())

			worked, err := runner.RunOnce(context.Background())
			if err != nil || !worked {
				t.Fatalf("RunOnce() = %v, %v", worked, err)
			}
			queue.mu.Lock()
			defer queue.mu.Unlock()
			if test.retryable {
				if len(queue.retries) != 1 || queue.retries[0].sanitizedError != "provider_discovery:timeout" || len(queue.failures) != 0 {
					t.Fatalf("retry/fail calls = %#v / %#v", queue.retries, queue.failures)
				}
			} else if len(queue.failures) != 1 || queue.failures[0] != "provider_discovery:timeout" || len(queue.retries) != 0 {
				t.Fatalf("retry/fail calls = %#v / %#v", queue.retries, queue.failures)
			}
		})
	}
}

func TestRunnerSanitizesUnexpectedHandlerFailureAndLogs(t *testing.T) {
	const secret = "handler-secret-sentinel"
	queue := newQueueStub()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	runner, err := NewRunner(queue, Registry{
		jobs.GeneratePage: func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error) {
			return nil, errors.New(secret)
		},
	}, testRunnerConfig(), logger)
	if err != nil {
		t.Fatal(err)
	}

	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
	queue.mu.Lock()
	if len(queue.retries) != 1 || queue.retries[0].sanitizedError != unexpectedError {
		t.Fatalf("retries = %#v", queue.retries)
	}
	queue.mu.Unlock()
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("logs contain handler secret: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "job_handler_failed") || !strings.Contains(logs.String(), "errorString") {
		t.Fatalf("missing safe failure metadata: %s", logs.String())
	}
}

func TestRunnerSanitizesHandlerPanic(t *testing.T) {
	const secret = "panic-secret-sentinel"
	queue := newQueueStub()
	var logs bytes.Buffer
	runner, err := NewRunner(queue, Registry{
		jobs.GeneratePage: func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error) {
			panic(secret)
		},
	}, testRunnerConfig(), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}

	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
	queue.mu.Lock()
	if len(queue.retries) != 1 || queue.retries[0].sanitizedError != unexpectedError {
		t.Fatalf("retries = %#v", queue.retries)
	}
	queue.mu.Unlock()
	if strings.Contains(logs.String(), secret) || !strings.Contains(logs.String(), "handlerPanic") {
		t.Fatalf("unsafe or incomplete panic log: %s", logs.String())
	}
}

func TestRunnerSanitizesIterationErrorAndContinues(t *testing.T) {
	const secret = "database-secret-sentinel"
	queue := newQueueStub()
	var calls atomic.Int32
	secondClaim := make(chan struct{})
	queue.claimFn = func(context.Context, jobs.WorkerID, time.Duration) (*jobs.Permit, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New(secret)
		}
		close(secondClaim)
		return nil, nil
	}
	config := testRunnerConfig()
	config.PollEvery = time.Millisecond
	var logs bytes.Buffer
	runner, err := NewRunner(queue, nil, config, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	select {
	case <-secondClaim:
	case <-time.After(time.Second):
		t.Fatal("runner did not continue after claim failure")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), secret) || !strings.Contains(logs.String(), "job_runner_iteration_failed") {
		t.Fatalf("unsafe or incomplete iteration log: %s", logs.String())
	}
}

func TestRunnerHeartbeatCancellationStopsHandlerAndAcknowledges(t *testing.T) {
	queue := newQueueStub()
	queue.heartbeatFn = func(context.Context, jobs.Permit, time.Duration) error {
		return jobs.ErrStalePermit
	}
	handlerStarted := make(chan struct{})
	handlerStopped := make(chan struct{})
	runner := testRunner(t, queue, Registry{
		jobs.GeneratePage: func(ctx context.Context, _ jobs.Command, _ jobs.Permit) (map[string]any, error) {
			close(handlerStarted)
			<-ctx.Done()
			close(handlerStopped)
			return nil, ctx.Err()
		},
	}, testRunnerConfig())

	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
	<-handlerStarted
	select {
	case <-handlerStopped:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop after heartbeat lost the permit")
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.acknowledgements != 1 || len(queue.completions) != 0 || len(queue.retries) != 0 || len(queue.failures) != 0 {
		t.Fatalf("ack/complete/retry/fail = %d/%d/%d/%d", queue.acknowledgements, len(queue.completions), len(queue.retries), len(queue.failures))
	}
}

func TestRunnerAcceptsCompletionAfterCancellationRequest(t *testing.T) {
	queue := newQueueStub()
	completionStarted := make(chan struct{})
	cancellationRequested := make(chan struct{})
	queue.completeFn = func(context.Context, jobs.Permit, map[string]any) error {
		close(completionStarted)
		<-cancellationRequested
		return nil
	}
	runner := testRunner(t, queue, Registry{
		jobs.GeneratePage: func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error) {
			return map[string]any{"accepted": true}, nil
		},
	}, testRunnerConfig())
	done := make(chan error, 1)
	go func() {
		_, err := runner.RunOnce(context.Background())
		done <- err
	}()

	<-completionStarted
	close(cancellationRequested)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.completions) != 1 || queue.acknowledgements != 0 {
		t.Fatalf("complete/ack calls = %d/%d", len(queue.completions), queue.acknowledgements)
	}
}

func TestRunnerStaleCompletionAdoptsCancellationWithoutOverwrite(t *testing.T) {
	queue := newQueueStub()
	queue.completeFn = func(context.Context, jobs.Permit, map[string]any) error {
		return jobs.ErrStalePermit
	}
	runner := testRunner(t, queue, Registry{
		jobs.GeneratePage: func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error) {
			return map[string]any{}, nil
		},
	}, testRunnerConfig())

	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = %v, %v", worked, err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.acknowledgements != 1 || len(queue.retries) != 0 || len(queue.failures) != 0 {
		t.Fatalf("ack/retry/fail = %d/%d/%d", queue.acknowledgements, len(queue.retries), len(queue.failures))
	}
}

func TestRunnerShutdownLeavesLeaseForReclamation(t *testing.T) {
	queue := newQueueStub()
	config := testRunnerConfig()
	config.LeaseFor = 2 * time.Second
	config.HeartbeatEvery = time.Second
	handlerStarted := make(chan struct{})
	handlerStopped := make(chan struct{})
	runner := testRunner(t, queue, Registry{
		jobs.GeneratePage: func(ctx context.Context, _ jobs.Command, _ jobs.Permit) (map[string]any, error) {
			close(handlerStarted)
			<-ctx.Done()
			close(handlerStopped)
			return nil, ctx.Err()
		},
	}, config)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		worked, err := runner.RunOnce(ctx)
		if !worked && err == nil {
			err = errors.New("claimed work was not reported")
		}
		done <- err
	}()

	<-handlerStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce error = %v", err)
	}
	select {
	case <-handlerStopped:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop on shutdown")
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.acknowledgements != 0 || len(queue.completions) != 0 || len(queue.retries) != 0 || len(queue.failures) != 0 {
		t.Fatalf("shutdown mutated lease: ack/complete/retry/fail = %d/%d/%d/%d", queue.acknowledgements, len(queue.completions), len(queue.retries), len(queue.failures))
	}
}

func TestRunnerPollsAtConfiguredBound(t *testing.T) {
	queue := newQueueStub()
	queue.permit = nil
	var calls atomic.Int32
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	queue.claimFn = func(context.Context, jobs.WorkerID, time.Duration) (*jobs.Permit, error) {
		switch calls.Add(1) {
		case 1:
			close(firstEntered)
			<-releaseFirst
		case 2:
			close(secondEntered)
		}
		return nil, nil
	}
	config := testRunnerConfig()
	config.PollEvery = 40 * time.Millisecond
	runner := testRunner(t, queue, nil, config)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	<-firstEntered
	close(releaseFirst)
	select {
	case <-secondEntered:
		t.Fatal("runner polled again without waiting")
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("runner did not poll again")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("claim calls = %d", got)
	}
}

func testRunner(t *testing.T, queue Queue, handlers Registry, config Config) *Runner {
	t.Helper()
	runner, err := NewRunner(queue, handlers, config, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func testRunnerConfig() Config {
	return Config{
		WorkerID:       "worker-test",
		LeaseFor:       500 * time.Millisecond,
		HeartbeatEvery: 5 * time.Millisecond,
		PollEvery:      10 * time.Millisecond,
		RetryBackoff:   time.Second,
	}
}

type retryCall struct {
	sanitizedError string
	delay          time.Duration
}

type queueStub struct {
	mu               sync.Mutex
	permit           *jobs.Permit
	command          jobs.Command
	claimFn          func(context.Context, jobs.WorkerID, time.Duration) (*jobs.Permit, error)
	getCommandFn     func(context.Context, jobs.Permit) (jobs.Command, error)
	heartbeatFn      func(context.Context, jobs.Permit, time.Duration) error
	completeFn       func(context.Context, jobs.Permit, map[string]any) error
	retryFn          func(context.Context, jobs.Permit, string, time.Duration) (jobs.Status, error)
	failFn           func(context.Context, jobs.Permit, string) error
	acknowledgeFn    func(context.Context, jobs.Permit) error
	claimCalls       int
	heartbeatCalls   int
	completions      []map[string]any
	retries          []retryCall
	failures         []string
	acknowledgements int
}

func newQueueStub() *queueStub {
	return &queueStub{
		permit: &jobs.Permit{
			JobID:           jobs.JobID{0: 1},
			WorkerID:        "worker-test",
			LeaseGeneration: 1,
		},
		command: jobs.Command{
			Type:         jobs.GeneratePage,
			TargetType:   "page",
			TargetID:     jobs.UUID{0: 2},
			Payload:      map[string]any{"page": 1},
			OperationKey: "page:1",
			MaxAttempts:  3,
		},
	}
}

func (queue *queueStub) Claim(ctx context.Context, workerID jobs.WorkerID, leaseFor time.Duration) (*jobs.Permit, error) {
	queue.mu.Lock()
	queue.claimCalls++
	claimFn := queue.claimFn
	permit := queue.permit
	queue.mu.Unlock()
	if claimFn != nil {
		return claimFn(ctx, workerID, leaseFor)
	}
	return permit, nil
}

func (queue *queueStub) GetCommand(ctx context.Context, permit jobs.Permit) (jobs.Command, error) {
	if queue.getCommandFn != nil {
		return queue.getCommandFn(ctx, permit)
	}
	return queue.command, nil
}

func (queue *queueStub) Heartbeat(ctx context.Context, permit jobs.Permit, leaseFor time.Duration) error {
	queue.mu.Lock()
	queue.heartbeatCalls++
	heartbeatFn := queue.heartbeatFn
	queue.mu.Unlock()
	if heartbeatFn != nil {
		return heartbeatFn(ctx, permit, leaseFor)
	}
	return nil
}

func (queue *queueStub) CompleteAcceptedResult(ctx context.Context, permit jobs.Permit, result map[string]any) error {
	queue.mu.Lock()
	queue.completions = append(queue.completions, result)
	completeFn := queue.completeFn
	queue.mu.Unlock()
	if completeFn != nil {
		return completeFn(ctx, permit, result)
	}
	return nil
}

func (queue *queueStub) RetryAfter(ctx context.Context, permit jobs.Permit, sanitizedError string, delay time.Duration) (jobs.Status, error) {
	queue.mu.Lock()
	queue.retries = append(queue.retries, retryCall{sanitizedError: sanitizedError, delay: delay})
	retryFn := queue.retryFn
	queue.mu.Unlock()
	if retryFn != nil {
		return retryFn(ctx, permit, sanitizedError, delay)
	}
	return jobs.RetryWait, nil
}

func (queue *queueStub) Fail(ctx context.Context, permit jobs.Permit, sanitizedError string) error {
	queue.mu.Lock()
	queue.failures = append(queue.failures, sanitizedError)
	failFn := queue.failFn
	queue.mu.Unlock()
	if failFn != nil {
		return failFn(ctx, permit, sanitizedError)
	}
	return nil
}

func (queue *queueStub) AcknowledgeCancel(ctx context.Context, permit jobs.Permit) error {
	queue.mu.Lock()
	queue.acknowledgements++
	acknowledgeFn := queue.acknowledgeFn
	queue.mu.Unlock()
	if acknowledgeFn != nil {
		return acknowledgeFn(ctx, permit)
	}
	return nil
}
