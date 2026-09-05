package knowledgebases

import (
	"context"
	"errors"
	"testing"

	"github.com/cyr1en/ref0/internal/jobs"
)

func TestPurgeHandlerValidatesCommandAndPreservesPermit(t *testing.T) {
	id, err := ParseID("10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := jobs.ParseUUID("20000000-0000-4000-8000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	permit := jobs.Permit{JobID: jobs.JobID(jobID), WorkerID: "purger", LeaseGeneration: 7}
	service := &fakePurgeService{
		value:  KnowledgeBase{ID: id, Lifecycle: Deleted},
		permit: permit,
	}
	registry, err := Handlers(service)
	if err != nil {
		t.Fatal(err)
	}
	command := jobs.Command{
		Type: jobs.PurgeKnowledgeBase, TargetType: "knowledge_base",
		TargetID: jobs.UUID(id), Payload: map[string]any{},
	}
	result, err := registry[jobs.PurgeKnowledgeBase](context.Background(), command, permit)
	if err != nil {
		t.Fatal(err)
	}
	if service.calls != 1 || service.id != id || service.received != permit ||
		result["knowledge_base_id"] != id.String() || result["lifecycle"] != "deleted" {
		t.Fatalf("service=%+v result=%v", service, result)
	}

	for name, mutate := range map[string]func(*jobs.Command){
		"wrong type":   func(value *jobs.Command) { value.Type = jobs.SyncSource },
		"wrong target": func(value *jobs.Command) { value.TargetType = "source" },
		"payload":      func(value *jobs.Command) { value.Payload["unexpected"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := command
			invalid.Payload = map[string]any{}
			mutate(&invalid)
			if _, err := registry[jobs.PurgeKnowledgeBase](context.Background(), invalid, permit); err == nil {
				t.Fatal("invalid command was accepted")
			}
		})
	}
	if service.calls != 1 {
		t.Fatalf("invalid commands reached service: calls=%d", service.calls)
	}
}

func TestPurgeHandlersRequireServiceAndPropagateStalePermit(t *testing.T) {
	if _, err := Handlers(nil); err == nil {
		t.Fatal("nil purge service was accepted")
	}
	service := &fakePurgeService{err: jobs.ErrStalePermit}
	registry, err := Handlers(service)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry[jobs.PurgeKnowledgeBase](context.Background(), jobs.Command{
		Type: jobs.PurgeKnowledgeBase, TargetType: "knowledge_base", Payload: map[string]any{},
	}, jobs.Permit{WorkerID: "purger", LeaseGeneration: 1})
	if !errors.Is(err, jobs.ErrStalePermit) {
		t.Fatalf("stale permit error=%v", err)
	}
}

type fakePurgeService struct {
	value    KnowledgeBase
	err      error
	permit   jobs.Permit
	received jobs.Permit
	id       ID
	calls    int
}

func (service *fakePurgeService) Purge(_ context.Context, id ID, permit jobs.Permit) (KnowledgeBase, error) {
	service.calls++
	service.id = id
	service.received = permit
	if service.permit != (jobs.Permit{}) && permit != service.permit {
		return KnowledgeBase{}, jobs.ErrStalePermit
	}
	return service.value, service.err
}

var _ PurgeService = (*fakePurgeService)(nil)
