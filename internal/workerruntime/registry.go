package workerruntime

import (
	"context"
	"errors"
	"fmt"

	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/worker"
)

var expectedJobTypes = [...]jobs.Type{
	jobs.ValidateSource,
	jobs.SyncSource,
	jobs.PrepareRun,
	jobs.PlanRun,
	jobs.GeneratePage,
	jobs.FinalizeRun,
	jobs.DiscoverEndpoint,
	jobs.ProbeModel,
	jobs.RefreshDiscord,
	jobs.PurgeKnowledgeBase,
	jobs.ApplyRetention,
}

func completeRegistry(registries ...worker.Registry) (worker.Registry, error) {
	merged := worker.Registry{}
	for _, registry := range registries {
		for jobType, handler := range registry {
			if !jobs.ValidType(jobType) || handler == nil {
				return nil, errors.New("worker registry contains an invalid handler")
			}
			if _, duplicate := merged[jobType]; duplicate {
				return nil, fmt.Errorf("worker registry contains duplicate job type %s", jobType)
			}
			merged[jobType] = handler
		}
	}
	if len(merged) != len(expectedJobTypes) {
		return nil, errors.New("worker registry is incomplete")
	}
	for _, jobType := range expectedJobTypes {
		if merged[jobType] == nil {
			return nil, errors.New("worker registry is incomplete")
		}
	}
	return merged, nil
}

func adaptProviderRegistry(registry map[jobs.Type]providers.Handler) (worker.Registry, error) {
	adapted := make(worker.Registry, len(registry))
	for jobType, providerHandler := range registry {
		if providerHandler == nil {
			return nil, errors.New("provider registry contains an invalid handler")
		}
		handler := providerHandler
		adapted[jobType] = func(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
			result, err := handler(ctx, command, permit)
			var failure *providers.HandlerFailure
			if errors.As(err, &failure) && failure != nil {
				return nil, &worker.HandlerFailure{
					SanitizedError: failure.SanitizedError,
					Retryable:      failure.Retryable,
				}
			}
			return result, err
		}
	}
	return adapted, nil
}

func adaptDocumentationRegistry(registry map[jobs.Type]docgen.Handler) (worker.Registry, error) {
	adapted := make(worker.Registry, len(registry))
	for jobType, documentationHandler := range registry {
		if documentationHandler == nil {
			return nil, errors.New("documentation registry contains an invalid handler")
		}
		handler := documentationHandler
		adapted[jobType] = func(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
			result, err := handler(ctx, command, permit)
			var failure *docgen.HandlerFailure
			if errors.As(err, &failure) && failure != nil {
				return nil, &worker.HandlerFailure{
					SanitizedError: failure.SanitizedError,
					Retryable:      failure.Retryable,
				}
			}
			return result, err
		}
	}
	return adapted, nil
}
