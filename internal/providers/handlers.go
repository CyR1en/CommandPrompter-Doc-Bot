package providers

import (
	"context"
	"errors"
	"fmt"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CaptureStore interface {
	BeginDiscovery(context.Context, DiscoveryRunID, jobs.Permit) (DiscoveryRun, error)
	CompleteDiscovery(context.Context, CompleteDiscovery, jobs.Permit) (DiscoveryRun, error)
	GetEndpoint(context.Context, EndpointID) (Endpoint, error)
	BeginProbe(context.Context, ProbeRunID, jobs.Permit) (ProbeRun, error)
	CompleteProbe(context.Context, CompleteProbe, jobs.Permit) (ProbeRun, error)
	GetProfile(context.Context, ProfileID) (Profile, error)
}

type CaptureExecutor interface {
	Discover(context.Context, Endpoint, DiscoveryRun) (CompleteDiscovery, error)
	Probe(context.Context, Endpoint, Profile, ProbeRun) (CompleteProbe, error)
}

type Handler func(context.Context, jobs.Command, jobs.Permit) (map[string]any, error)

// HandlerFailure is safe to expose to the job state. Runtime adapters should
// translate it to their worker package's retry/fail error without logging the
// wrapped request or provider response.
type HandlerFailure struct {
	SanitizedError string
	Retryable      bool
}

func (failure *HandlerFailure) Error() string { return failure.SanitizedError }

type Handlers struct {
	store     CaptureStore
	execution CaptureExecutor
}

func NewHandlers(store CaptureStore, execution CaptureExecutor) (*Handlers, error) {
	if store == nil || execution == nil {
		return nil, errors.New("provider handler dependencies are incomplete")
	}
	return &Handlers{store: store, execution: execution}, nil
}

func (handlers *Handlers) Registry() map[jobs.Type]Handler {
	return map[jobs.Type]Handler{
		jobs.DiscoverEndpoint: handlers.Discover,
		jobs.ProbeModel:       handlers.Probe,
	}
}

func (handlers *Handlers) Discover(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
	runID, err := captureRunID(command, jobs.DiscoverEndpoint, "provider_endpoint", "discovery_run_id")
	if err != nil {
		return nil, err
	}
	run, err := handlers.store.BeginDiscovery(ctx, DiscoveryRunID(runID), permit)
	if err != nil {
		return nil, err
	}
	if [16]byte(run.EndpointID) != [16]byte(command.TargetID) {
		return nil, errors.New("provider discovery job target is invalid")
	}
	if run.Status != CaptureRunning {
		return discoveryResult(run), nil
	}
	endpoint, err := handlers.store.GetEndpoint(ctx, run.EndpointID)
	if err != nil {
		return nil, err
	}
	completion, err := handlers.execution.Discover(ctx, endpoint, run)
	if err != nil {
		return nil, err
	}
	completed, err := handlers.store.CompleteDiscovery(ctx, completion, permit)
	if err != nil {
		return nil, err
	}
	if completed.Status == CaptureFailed {
		return nil, &HandlerFailure{SanitizedError: fallbackError(completion.SanitizedError, "provider_discovery:failed"), Retryable: completion.Retryable}
	}
	return discoveryResult(completed), nil
}

func (handlers *Handlers) Probe(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
	runID, err := captureRunID(command, jobs.ProbeModel, "model_profile", "probe_run_id")
	if err != nil {
		return nil, err
	}
	run, err := handlers.store.BeginProbe(ctx, ProbeRunID(runID), permit)
	if err != nil {
		return nil, err
	}
	if [16]byte(run.ProfileID) != [16]byte(command.TargetID) {
		return nil, errors.New("provider probe job target is invalid")
	}
	if run.Status != CaptureRunning {
		return probeResult(run), nil
	}
	profile, err := handlers.store.GetProfile(ctx, run.ProfileID)
	if err != nil {
		return nil, err
	}
	endpoint, err := handlers.store.GetEndpoint(ctx, profile.EndpointID)
	if err != nil {
		return nil, err
	}
	completion, err := handlers.execution.Probe(ctx, endpoint, profile, run)
	if err != nil {
		return nil, err
	}
	completed, err := handlers.store.CompleteProbe(ctx, completion, permit)
	if err != nil {
		return nil, err
	}
	if completed.Status == CaptureFailed {
		return nil, &HandlerFailure{SanitizedError: fallbackError(completion.SanitizedError, "provider_probe:failed"), Retryable: completion.Retryable}
	}
	return probeResult(completed), nil
}

type Runtime struct {
	Store     *Store
	Execution *Execution
	Handlers  *Handlers
}

// NewRuntime is the sole construction seam needed by a worker process. Route
// registration needs only Runtime.Store; worker registration adapts the two
// values returned by Runtime.Handlers.Registry into its local handler type.
func NewRuntime(pool *pgxpool.Pool, vault *security.CredentialVault, options ExecutionOptions) (*Runtime, error) {
	store, err := NewStore(pool, vault)
	if err != nil {
		return nil, err
	}
	reader, err := credentials.NewSecretReader(pool, vault)
	if err != nil {
		return nil, err
	}
	execution, err := NewExecution(reader, options)
	if err != nil {
		return nil, err
	}
	handlers, err := NewHandlers(store, execution)
	if err != nil {
		return nil, err
	}
	return &Runtime{Store: store, Execution: execution, Handlers: handlers}, nil
}

func captureRunID(command jobs.Command, expectedType jobs.Type, targetType, field string) (ID, error) {
	if command.Type != expectedType || command.TargetType != targetType || len(command.Payload) != 1 {
		return ID{}, fmt.Errorf("provider %s job is invalid", field)
	}
	raw, ok := command.Payload[field].(string)
	if !ok {
		return ID{}, fmt.Errorf("provider %s job is invalid", field)
	}
	id, err := ParseID(raw)
	if err != nil {
		return ID{}, fmt.Errorf("provider %s job is invalid", field)
	}
	return id, nil
}

func discoveryResult(run DiscoveryRun) map[string]any {
	return map[string]any{
		"discovery_run_id": run.ID.String(), "status": stringsLower(run.Status),
		"model_count": run.ModelCount,
	}
}

func probeResult(run ProbeRun) map[string]any {
	var findings any
	if run.Findings != nil {
		findings = run.Findings
	}
	var resultingVersion any
	if run.ResultingVersionID != nil {
		resultingVersion = run.ResultingVersionID.String()
	}
	return map[string]any{
		"probe_run_id": run.ID.String(), "status": stringsLower(run.Status),
		"findings": findings, "resulting_version_id": resultingVersion,
	}
}

func stringsLower[T ~string](value T) string {
	result := []byte(string(value))
	for index, character := range result {
		if character >= 'A' && character <= 'Z' {
			result[index] += 'a' - 'A'
		}
	}
	return string(result)
}

func fallbackError(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
