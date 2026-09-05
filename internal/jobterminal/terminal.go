// Package jobterminal composes the domain recovery invoked by the durable job
// queue when work becomes irreversibly failed or cancelled.
package jobterminal

import (
	"context"
	"errors"

	"github.com/cyr1en/ref0/internal/discord"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/knowledgebases"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/sources"
	"github.com/jackc/pgx/v5"
)

var callbacks = map[jobs.Type]jobs.TerminalCallback{
	jobs.ValidateSource:     sources.TerminalCallback,
	jobs.SyncSource:         sources.TerminalCallback,
	jobs.PrepareRun:         docgen.TerminalCallback,
	jobs.PlanRun:            docgen.TerminalCallback,
	jobs.GeneratePage:       docgen.TerminalCallback,
	jobs.FinalizeRun:        docgen.TerminalCallback,
	jobs.DiscoverEndpoint:   providers.TerminalCallback,
	jobs.ProbeModel:         providers.TerminalCallback,
	jobs.RefreshDiscord:     discord.TerminalCallback,
	jobs.PurgeKnowledgeBase: knowledgebases.TerminalCallback,
	jobs.ApplyRetention:     noOwnedResource,
}

// Callback is the only terminal callback installed on shared job stores. The
// callback runs in the transaction that terminalizes the job, so a domain
// transition failure rolls the job transition back as well.
func Callback(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	if job.Status != jobs.Failed && job.Status != jobs.Cancelled {
		return errors.New("job terminal callback received a nonterminal outcome")
	}
	callback, exists := callbacks[job.Type]
	if !exists || callback == nil {
		return errors.New("job terminal callback is not registered")
	}
	return callback(ctx, tx, job)
}

func noOwnedResource(context.Context, pgx.Tx, jobs.Snapshot) error { return nil }
