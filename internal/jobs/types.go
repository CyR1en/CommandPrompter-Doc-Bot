package jobs

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Type string

const (
	ValidateSource     Type = "VALIDATE_SOURCE"
	SyncSource         Type = "SYNC_SOURCE"
	PrepareRun         Type = "PREPARE_RUN"
	PlanRun            Type = "PLAN_RUN"
	GeneratePage       Type = "GENERATE_PAGE"
	FinalizeRun        Type = "FINALIZE_RUN"
	DiscoverEndpoint   Type = "DISCOVER_ENDPOINT"
	ProbeModel         Type = "PROBE_MODEL"
	RefreshDiscord     Type = "REFRESH_DISCORD"
	PurgeKnowledgeBase Type = "PURGE_KNOWLEDGE_BASE"
	ApplyRetention     Type = "APPLY_RETENTION"
)

var validTypes = map[Type]struct{}{
	ValidateSource: {}, SyncSource: {}, PrepareRun: {}, PlanRun: {},
	GeneratePage: {}, FinalizeRun: {}, DiscoverEndpoint: {}, ProbeModel: {},
	RefreshDiscord: {}, PurgeKnowledgeBase: {}, ApplyRetention: {},
}

func ValidType(value Type) bool {
	_, valid := validTypes[value]
	return valid
}

type Status string

const (
	Pending         Status = "PENDING"
	Leased          Status = "LEASED"
	Succeeded       Status = "SUCCEEDED"
	RetryWait       Status = "RETRY_WAIT"
	Failed          Status = "FAILED"
	CancelRequested Status = "CANCEL_REQUESTED"
	Cancelled       Status = "CANCELLED"
)

func ValidStatus(value Status) bool {
	switch value {
	case Pending, Leased, Succeeded, RetryWait, Failed, CancelRequested, Cancelled:
		return true
	default:
		return false
	}
}

type UUID [16]byte
type JobID UUID
type WorkerID string

func ParseUUID(value string) (UUID, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return UUID{}, errors.New("UUID must use canonical form")
	}
	raw := strings.ReplaceAll(value, "-", "")
	var id UUID
	if _, err := hex.Decode(id[:], []byte(raw)); err != nil {
		return UUID{}, errors.New("UUID must use canonical form")
	}
	return id, nil
}

func (id UUID) String() string {
	var raw [32]byte
	hex.Encode(raw[:], id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:])
}

func (id JobID) String() string { return UUID(id).String() }

type Command struct {
	Type             Type
	TargetType       string
	TargetID         UUID
	Payload          map[string]any
	OperationKey     string
	MaxAttempts      int32
	NotBefore        *time.Time
	ConcurrencyKey   string
	ConcurrencyLimit int32
}

func (command Command) validate() error {
	if _, ok := validTypes[command.Type]; !ok {
		return fmt.Errorf("invalid job type %q", command.Type)
	}
	if command.TargetType == "" {
		return errors.New("target type must not be empty")
	}
	if command.OperationKey == "" {
		return errors.New("operation key must not be empty")
	}
	if command.MaxAttempts <= 0 {
		return errors.New("max attempts must be positive")
	}
	if command.ConcurrencyKey == "" && command.ConcurrencyLimit != 0 ||
		command.ConcurrencyKey != "" && (len(command.ConcurrencyKey) > 512 || command.ConcurrencyLimit < 1 || command.ConcurrencyLimit > 32) {
		return errors.New("job concurrency key and limit are invalid")
	}
	if command.Payload == nil {
		command.Payload = map[string]any{}
	}
	return nil
}

type Permit struct {
	JobID           JobID
	WorkerID        WorkerID
	LeaseGeneration int64
}

func (permit Permit) validate() error {
	if permit.WorkerID == "" {
		return errors.New("worker ID must not be empty")
	}
	if permit.LeaseGeneration <= 0 {
		return errors.New("lease generation must be positive")
	}
	return nil
}

type Snapshot struct {
	ID              JobID
	Type            Type
	TargetType      string
	TargetID        UUID
	Status          Status
	AttemptCount    int32
	MaxAttempts     int32
	Progress        int32
	LeaseOwner      *string
	LeaseExpiresAt  *time.Time
	LeaseGeneration int64
	NotBefore       *time.Time
	Result          map[string]any
	SanitizedError  *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

var (
	ErrStalePermit = errors.New("work permit is stale")
	ErrJobNotFound = errors.New("job does not exist")
	ErrJobConflict = errors.New("job state does not accept the operation")
)

func retryStatus(attemptCount, maxAttempts int32) Status {
	if attemptCount < maxAttempts {
		return RetryWait
	}
	return Failed
}
