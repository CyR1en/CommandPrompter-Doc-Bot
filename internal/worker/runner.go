package worker

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
)

const (
	unknownJobError = "job type is not implemented"
	unexpectedError = "job handler failed"
)

type Queue interface {
	Claim(context.Context, jobs.WorkerID, time.Duration) (*jobs.Permit, error)
	GetCommand(context.Context, jobs.Permit) (jobs.Command, error)
	Heartbeat(context.Context, jobs.Permit, time.Duration) error
	CompleteAcceptedResult(context.Context, jobs.Permit, map[string]any) error
	RetryAfter(context.Context, jobs.Permit, string, time.Duration) (jobs.Status, error)
	Fail(context.Context, jobs.Permit, string) error
	AcknowledgeCancel(context.Context, jobs.Permit) error
}

var _ Queue = (*jobs.Store)(nil)

type Handler func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error)

type Registry map[jobs.Type]Handler

type HandlerFailure struct {
	SanitizedError string
	Retryable      bool
}

func (failure *HandlerFailure) Error() string {
	return failure.SanitizedError
}

type Runner struct {
	queue    Queue
	handlers Registry
	config   Config
	logger   *slog.Logger
}

func NewRunner(queue Queue, handlers Registry, config Config, logger *slog.Logger) (*Runner, error) {
	if queue == nil {
		return nil, errors.New("job queue must not be nil")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		queue:    queue,
		handlers: cloneRegistry(handlers),
		config:   config,
		logger:   logger,
	}, nil
}

func (runner *Runner) Run(ctx context.Context) error {
	for {
		worked, err := runner.RunOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			runner.logger.Error("job_runner_iteration_failed",
				"worker_id", runner.config.WorkerID,
				"outcome", "failed",
				"error_class", errorClass(err),
			)
			worked = false
		}
		if worked {
			continue
		}
		if err := waitFor(ctx, runner.config.PollEvery); err != nil {
			return nil
		}
	}
}

func (runner *Runner) RunOnce(ctx context.Context) (bool, error) {
	permit, err := runner.queue.Claim(ctx, runner.config.WorkerID, runner.config.LeaseFor)
	if err != nil {
		return false, err
	}
	if permit == nil {
		return false, nil
	}

	command, err := runner.queue.GetCommand(ctx, *permit)
	if err != nil {
		if errors.Is(err, jobs.ErrStalePermit) {
			return true, runner.adoptCancellation(ctx, *permit)
		}
		return true, err
	}
	handler := runner.handlers[command.Type]
	if handler == nil {
		return true, runner.retry(ctx, *permit, unknownJobError)
	}
	return true, runner.execute(ctx, handler, command, *permit)
}

type handlerResult struct {
	value map[string]any
	err   error
}

type handlerPanic struct{}

func (handlerPanic) Error() string { return "job handler panicked" }

func (runner *Runner) execute(ctx context.Context, handler Handler, command jobs.Command, permit jobs.Permit) error {
	workCtx, stop := context.WithCancel(ctx)
	defer stop()

	handlerDone := make(chan handlerResult, 1)
	go func() {
		handlerDone <- invokeHandler(workCtx, handler, command, permit)
	}()
	heartbeatDone := make(chan error, 1)
	go func() {
		heartbeatDone <- runner.heartbeat(workCtx, permit)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-heartbeatDone:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		stop()
		return runner.adoptCancellation(ctx, permit)
	case result := <-handlerDone:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		stop()
		select {
		case <-heartbeatDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		return runner.acceptHandlerResult(ctx, command, permit, result)
	}
}

func invokeHandler(ctx context.Context, handler Handler, command jobs.Command, permit jobs.Permit) (result handlerResult) {
	defer func() {
		if recover() != nil {
			result = handlerResult{err: handlerPanic{}}
		}
	}()
	result.value, result.err = handler(ctx, command, permit)
	return result
}

func (runner *Runner) acceptHandlerResult(ctx context.Context, command jobs.Command, permit jobs.Permit, result handlerResult) error {
	if result.err == nil {
		err := runner.queue.CompleteAcceptedResult(ctx, permit, result.value)
		if errors.Is(err, jobs.ErrStalePermit) {
			return runner.adoptCancellation(ctx, permit)
		}
		if err == nil {
			runner.logger.Info("job_completed", runner.logFields(command, permit, "succeeded")...)
		}
		return err
	}
	if errors.Is(result.err, jobs.ErrStalePermit) {
		return runner.adoptCancellation(ctx, permit)
	}

	var failure *HandlerFailure
	if errors.As(result.err, &failure) && failure != nil {
		if failure.Retryable {
			return runner.retry(ctx, permit, failure.SanitizedError)
		}
		err := runner.queue.Fail(ctx, permit, failure.SanitizedError)
		if errors.Is(err, jobs.ErrStalePermit) {
			return runner.adoptCancellation(ctx, permit)
		}
		return err
	}

	runner.logger.Error("job_handler_failed",
		append(runner.logFields(command, permit, "retry"), "error_class", errorClass(result.err))...,
	)
	return runner.retry(ctx, permit, unexpectedError)
}

func (runner *Runner) heartbeat(ctx context.Context, permit jobs.Permit) error {
	ticker := time.NewTicker(runner.config.HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := runner.queue.Heartbeat(ctx, permit, runner.config.LeaseFor); err != nil {
				return err
			}
		}
	}
}

func (runner *Runner) retry(ctx context.Context, permit jobs.Permit, sanitizedError string) error {
	_, err := runner.queue.RetryAfter(ctx, permit, sanitizedError, runner.config.RetryBackoff)
	if errors.Is(err, jobs.ErrStalePermit) {
		return runner.adoptCancellation(ctx, permit)
	}
	return err
}

func (runner *Runner) adoptCancellation(ctx context.Context, permit jobs.Permit) error {
	err := runner.queue.AcknowledgeCancel(ctx, permit)
	if errors.Is(err, jobs.ErrStalePermit) {
		return nil
	}
	return err
}

func (runner *Runner) logFields(command jobs.Command, permit jobs.Permit, outcome string) []any {
	return []any{
		"worker_id", permit.WorkerID,
		"job_id", permit.JobID.String(),
		"job_type", command.Type,
		"target_type", command.TargetType,
		"target_id", command.TargetID.String(),
		"outcome", outcome,
	}
}

func cloneRegistry(handlers Registry) Registry {
	cloned := make(Registry, len(handlers))
	for jobType, handler := range handlers {
		cloned[jobType] = handler
	}
	return cloned
}

func errorClass(err error) string {
	if err == nil {
		return ""
	}
	typeOf := reflect.TypeOf(err)
	for typeOf.Kind() == reflect.Pointer {
		typeOf = typeOf.Elem()
	}
	if typeOf.Name() == "" {
		return typeOf.Kind().String()
	}
	return typeOf.Name()
}

func waitFor(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
