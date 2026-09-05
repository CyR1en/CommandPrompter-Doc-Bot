package workerruntime

import (
	"context"
	"errors"
	"testing"

	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/worker"
)

func workerHandler(context.Context, jobs.Command, jobs.Permit) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func TestCompleteRegistryRequiresExactlyTheClosedJobSet(t *testing.T) {
	providerRegistry, err := adaptProviderRegistry(map[jobs.Type]providers.Handler{
		jobs.DiscoverEndpoint: workerHandler,
		jobs.ProbeModel:       workerHandler,
	})
	if err != nil {
		t.Fatal(err)
	}
	documentationRegistry, err := adaptDocumentationRegistry(map[jobs.Type]docgen.Handler{
		jobs.PrepareRun:   workerHandler,
		jobs.PlanRun:      workerHandler,
		jobs.GeneratePage: workerHandler,
		jobs.FinalizeRun:  workerHandler,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := completeRegistry(
		worker.Registry{jobs.ValidateSource: workerHandler, jobs.SyncSource: workerHandler},
		providerRegistry,
		documentationRegistry,
		worker.Registry{jobs.RefreshDiscord: workerHandler},
		worker.Registry{jobs.PurgeKnowledgeBase: workerHandler},
		worker.Registry{jobs.ApplyRetention: workerHandler},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry) != 11 {
		t.Fatalf("handler count=%d", len(registry))
	}
	for _, jobType := range expectedJobTypes {
		if !jobs.ValidType(jobType) || registry[jobType] == nil {
			t.Fatalf("missing valid job type %s", jobType)
		}
	}

	delete(registry, jobs.PlanRun)
	if _, err = completeRegistry(registry); err == nil {
		t.Fatal("incomplete registry was accepted")
	}
	registry[jobs.PlanRun] = workerHandler
	if _, err = completeRegistry(registry, worker.Registry{jobs.PlanRun: workerHandler}); err == nil {
		t.Fatal("duplicate registry entry was accepted")
	}
	if _, err = completeRegistry(registry, worker.Registry{jobs.Type("NOT_A_JOB"): workerHandler}); err == nil {
		t.Fatal("invalid job type was accepted")
	}
	registry[jobs.PlanRun] = nil
	if _, err = completeRegistry(registry); err == nil {
		t.Fatal("nil handler was accepted")
	}
}

func TestDomainHandlerFailuresAreAdaptedWithoutLosingPolicy(t *testing.T) {
	providerRegistry, err := adaptProviderRegistry(map[jobs.Type]providers.Handler{
		jobs.DiscoverEndpoint: func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error) {
			return nil, &providers.HandlerFailure{SanitizedError: "provider:retry", Retryable: true}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = providerRegistry[jobs.DiscoverEndpoint](context.Background(), jobs.Command{}, jobs.Permit{})
	assertWorkerFailure(t, err, "provider:retry", true)

	documentationRegistry, err := adaptDocumentationRegistry(map[jobs.Type]docgen.Handler{
		jobs.PlanRun: func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error) {
			return nil, &docgen.HandlerFailure{SanitizedError: "documentation:failed"}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = documentationRegistry[jobs.PlanRun](context.Background(), jobs.Command{}, jobs.Permit{})
	assertWorkerFailure(t, err, "documentation:failed", false)

	if _, err = adaptProviderRegistry(map[jobs.Type]providers.Handler{jobs.ProbeModel: nil}); err == nil {
		t.Fatal("nil provider handler was accepted")
	}
	if _, err = adaptDocumentationRegistry(map[jobs.Type]docgen.Handler{jobs.PrepareRun: nil}); err == nil {
		t.Fatal("nil documentation handler was accepted")
	}
}

func assertWorkerFailure(t *testing.T, err error, message string, retryable bool) {
	t.Helper()
	var failure *worker.HandlerFailure
	if !errors.As(err, &failure) || failure == nil {
		t.Fatalf("failure type=%T", err)
	}
	if failure.SanitizedError != message || failure.Retryable != retryable {
		t.Fatalf("failure=%#v", failure)
	}
}
