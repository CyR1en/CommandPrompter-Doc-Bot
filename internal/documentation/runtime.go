package docgen

import (
	"errors"
	"github.com/cyr1en/ref0/internal/sourcefiles"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runtime is the complete construction seam for central worker wiring. The
// caller supplies the Pi/capsule-backed AgentRuntime and the process-wide job
// queue; this package owns PostgreSQL state, artifact stores, and four handlers.
type Runtime struct {
	Queue         *jobs.Store
	Store         *Store
	RunArtifacts  *artifacts.RunStore
	WikiArtifacts *artifacts.WikiStore
	Handlers      *Handlers
}

func NewRuntime(pool *pgxpool.Pool, queue *jobs.Store, vault *security.CredentialVault, dataRoot string, agent AgentRuntime) (*Runtime, error) {
	if pool == nil || queue == nil || vault == nil || agent == nil {
		return nil, errors.New("documentation runtime dependencies are incomplete")
	}
	runArtifacts, err := artifacts.NewRunStore(dataRoot)
	if err != nil {
		return nil, err
	}
	wikiArtifacts, err := artifacts.NewWikiStore(dataRoot)
	if err != nil {
		return nil, err
	}
	evidenceArtifacts, err := sourcefiles.NewStore(dataRoot)
	if err != nil {
		return nil, err
	}
	store, err := NewStore(pool, queue, vault, runArtifacts, wikiArtifacts, evidenceArtifacts)
	if err != nil {
		return nil, err
	}
	handlers, err := NewHandlers(store, agent, runArtifacts, wikiArtifacts)
	if err != nil {
		return nil, err
	}
	return &Runtime{Queue: queue, Store: store, RunArtifacts: runArtifacts, WikiArtifacts: wikiArtifacts, Handlers: handlers}, nil
}
