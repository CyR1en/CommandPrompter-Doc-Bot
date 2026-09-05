package agents

import (
	"context"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPostgresEngine wires the production execution store, exact-version
// credential reader, and network-restricted OpenAI-compatible model adapter.
func NewPostgresEngine(
	pool *pgxpool.Pool,
	artifacts SourceArtifactResolver,
	vault *security.CredentialVault,
	modelOptions OpenAIModelOptions,
	engineOptions EngineOptions,
) (*Engine, error) {
	store, err := NewPostgresExecutionStore(pool, artifacts, vault)
	if err != nil {
		return nil, err
	}
	secrets, err := credentials.NewSecretReader(pool, vault)
	if err != nil {
		return nil, err
	}
	model, err := NewOpenAIModel(secrets, modelOptions)
	if err != nil {
		return nil, err
	}
	queue := jobs.NewStore(pool, nil)
	model.admit = func(ctx context.Context, profileID providers.ProfileID, timeout time.Duration) (func(), error) {
		release, err := queue.AcquireModelCall(ctx, jobs.UUID(profileID), timeout)
		if errors.Is(err, jobs.ErrModelBusy) {
			return nil, ErrModelRateLimit
		}
		return release, err
	}
	return NewEngine(store, store, model, engineOptions)
}
