package knowledgebases

import (
	"context"
	"errors"
	"strings"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/worker"
)

type PurgeService interface {
	Purge(context.Context, ID, jobs.Permit) (KnowledgeBase, error)
}

// Handlers exposes the closed worker dispatch entry for deferred knowledge-base
// deletion. The service revalidates the leased permit and durable job target in
// the same transaction as the purge.
func Handlers(service PurgeService) (worker.Registry, error) {
	if service == nil {
		return nil, errors.New("knowledge base handler dependencies are incomplete")
	}
	purge := func(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
		if command.Type != jobs.PurgeKnowledgeBase || command.TargetType != "knowledge_base" || len(command.Payload) != 0 {
			return nil, errors.New("purge command is invalid")
		}
		value, err := service.Purge(ctx, ID(command.TargetID), permit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"knowledge_base_id": value.ID.String(),
			"lifecycle":         strings.ToLower(string(value.Lifecycle)),
		}, nil
	}
	return worker.Registry{jobs.PurgeKnowledgeBase: purge}, nil
}
