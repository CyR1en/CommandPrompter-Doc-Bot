package sources

import (
	"context"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/cyr1en/ref0/internal/worker"
)

type HandlerService interface {
	Begin(context.Context, ID, jobs.Permit) (Sync, error)
	CompleteValidation(context.Context, ValidationCompletion, jobs.Permit) (Sync, error)
	CompleteSync(context.Context, SyncCompletion, jobs.Permit) (Sync, error)
}

type HandlerExecution interface {
	Validate(context.Context, Sync) ValidationCompletion
	Sync(context.Context, Sync) SyncCompletion
	DiscardReusedCandidate(Sync) error
}

func Handlers(service HandlerService, execution HandlerExecution) (worker.Registry, error) {
	if service == nil || execution == nil {
		return nil, errors.New("source handler dependencies are incomplete")
	}
	validate := func(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
		if command.Type != jobs.ValidateSource {
			return nil, errors.New("source validation job is invalid")
		}
		id, err := syncID(command)
		if err != nil {
			return nil, err
		}
		run, err := service.Begin(ctx, id, permit)
		if err != nil {
			return nil, err
		}
		if jobs.UUID(run.SourceID) != command.TargetID || run.Kind != Validation {
			return nil, errors.New("source validation job target is invalid")
		}
		if run.Status != SyncRunning {
			return syncResult(run), nil
		}
		completion := execution.Validate(ctx, run)
		completed, err := service.CompleteValidation(ctx, completion, permit)
		if err != nil {
			return nil, err
		}
		if completed.Status == SyncFailed {
			message := "source_validation:failed"
			if completion.SanitizedError != nil {
				message = *completion.SanitizedError
			}
			return nil, &worker.HandlerFailure{SanitizedError: message, Retryable: completion.Retryable}
		}
		return syncResult(completed), nil
	}
	synchronize := func(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
		if command.Type != jobs.SyncSource {
			return nil, errors.New("source sync job is invalid")
		}
		id, err := syncID(command)
		if err != nil {
			return nil, err
		}
		run, err := service.Begin(ctx, id, permit)
		if err != nil {
			return nil, err
		}
		if jobs.UUID(run.SourceID) != command.TargetID || run.Kind != Synchronization {
			return nil, errors.New("source sync job target is invalid")
		}
		if run.Status != SyncRunning {
			if err := execution.DiscardReusedCandidate(run); err != nil {
				return nil, cleanupFailure(err)
			}
			return syncResult(run), nil
		}
		completion := execution.Sync(ctx, run)
		completed, err := service.CompleteSync(ctx, completion, permit)
		if err != nil {
			return nil, err
		}
		if completed.Status == SyncFailed {
			message := "source_sync:failed"
			if completion.SanitizedError != nil {
				message = *completion.SanitizedError
			}
			return nil, &worker.HandlerFailure{SanitizedError: message, Retryable: completion.Retryable}
		}
		if err := execution.DiscardReusedCandidate(completed); err != nil {
			return nil, cleanupFailure(err)
		}
		return syncResult(completed), nil
	}
	return worker.Registry{jobs.ValidateSource: validate, jobs.SyncSource: synchronize}, nil
}

func syncID(command jobs.Command) (ID, error) {
	if command.TargetType != "source" || len(command.Payload) != 1 {
		return ID{}, errors.New("source capture job is invalid")
	}
	raw, ok := command.Payload["source_sync_id"].(string)
	if !ok {
		return ID{}, errors.New("source capture job is invalid")
	}
	id, err := jobs.ParseUUID(raw)
	if err != nil {
		return ID{}, errors.New("source capture job is invalid")
	}
	return ID(id), nil
}

func syncResult(run Sync) map[string]any {
	return map[string]any{
		"source_sync_id":          run.ID.String(),
		"status":                  stringLower(run.Status),
		"resolved_native_version": pointerValue(run.ResolvedNativeVersion),
		"result_revision_id":      idString(run.ResultRevisionID),
	}
}

func cleanupFailure(err error) error {
	if errors.Is(err, sourcefiles.ErrSourceStorage) {
		return &worker.HandlerFailure{SanitizedError: "source_sync:cleanup", Retryable: true}
	}
	return err
}

func stringLower(value SyncStatus) string {
	text := string(value)
	buffer := make([]byte, len(text))
	for index := range text {
		character := text[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		buffer[index] = character
	}
	return string(buffer)
}

func pointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func RunPolling(ctx context.Context, store interface {
	ScheduleDue(context.Context, int) ([]Sync, error)
}, scanEvery time.Duration, batchSize int, onError func(error)) error {
	if store == nil || scanEvery <= 0 {
		return errors.New("source polling dependencies are invalid")
	}
	if batchSize < 1 || batchSize > 50 {
		return errors.New("source poll batch size must be between 1 and 50")
	}
	ticker := time.NewTicker(scanEvery)
	defer ticker.Stop()
	for {
		if _, err := store.ScheduleDue(ctx, batchSize); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
