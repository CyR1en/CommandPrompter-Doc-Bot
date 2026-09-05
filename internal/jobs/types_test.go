package jobs

import "testing"

func TestUUIDRoundTrip(t *testing.T) {
	const value = "2fa9261e-06e5-44ca-a222-64307fa29a55"
	id, err := ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	if id.String() != value {
		t.Fatalf("round trip = %q", id.String())
	}
	if _, err := ParseUUID("2fa9261e06e544caa22264307fa29a55"); err == nil {
		t.Fatal("accepted non-canonical UUID")
	}
}

func TestRetryStatus(t *testing.T) {
	if retryStatus(1, 3) != RetryWait || retryStatus(3, 3) != Failed {
		t.Fatal("retry exhaustion contract changed")
	}
}

func TestJobContractValues(t *testing.T) {
	want := []Type{
		ValidateSource, SyncSource, PrepareRun, PlanRun, GeneratePage,
		FinalizeRun, DiscoverEndpoint, ProbeModel, RefreshDiscord,
		PurgeKnowledgeBase, ApplyRetention,
	}
	if len(validTypes) != len(want) {
		t.Fatalf("job type count = %d", len(validTypes))
	}
	for _, value := range want {
		if _, ok := validTypes[value]; !ok {
			t.Fatalf("missing job type %q", value)
		}
	}
}

func TestCommandConcurrencyContract(t *testing.T) {
	base := Command{Type: GeneratePage, TargetType: "documentation_page", OperationKey: "page", MaxAttempts: 3}
	for _, command := range []Command{
		func() Command { value := base; value.ConcurrencyLimit = 1; return value }(),
		func() Command { value := base; value.ConcurrencyKey = "model-profile:test"; return value }(),
		func() Command {
			value := base
			value.ConcurrencyKey = "model-profile:test"
			value.ConcurrencyLimit = 33
			return value
		}(),
	} {
		if err := command.validate(); err == nil {
			t.Fatalf("invalid concurrency command was accepted: %+v", command)
		}
	}
	valid := base
	valid.ConcurrencyKey, valid.ConcurrencyLimit = "model-profile:test", 2
	if err := valid.validate(); err != nil {
		t.Fatalf("valid concurrency command was rejected: %v", err)
	}
}
