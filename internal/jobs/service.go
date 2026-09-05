package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const cancellationTTL = 24 * time.Hour

type ActorID [16]byte

// Service exposes sanitized operator reads and atomic, idempotent cancellation.
type Service struct {
	pool  *pgxpool.Pool
	store *Store
	vault *security.CredentialVault
}

func NewService(
	pool *pgxpool.Pool,
	vault *security.CredentialVault,
	terminalCallback TerminalCallback,
) (*Service, error) {
	if pool == nil || vault == nil {
		return nil, errors.New("job service dependencies are incomplete")
	}
	return &Service{pool: pool, store: NewStore(pool, terminalCallback), vault: vault}, nil
}

func (service *Service) Queue() *Store {
	return service.store
}

func (service *Service) List(ctx context.Context, options ListOptions) ([]Snapshot, error) {
	return service.store.List(ctx, options)
}

func (service *Service) Get(ctx context.Context, id JobID) (Snapshot, error) {
	return service.store.Get(ctx, id)
}

func (service *Service) Cancel(
	ctx context.Context,
	id JobID,
	actorID ActorID,
	requestKey string,
) (Snapshot, error) {
	if requestKey == "" {
		return Snapshot{}, errors.New("idempotency key is required")
	}
	digests, err := service.vault.KeyedDigests([]byte("job.cancel"), id[:])
	if err != nil || len(digests) == 0 {
		return Snapshot{}, errors.New("compute cancellation digest")
	}
	requestDigests, err := cancellationDigests(digests)
	if err != nil {
		return Snapshot{}, err
	}
	request := idempotency.Request{
		Scope:           "operator:" + UUID(actorID).String(),
		Key:             requestKey,
		Operation:       "job.cancel",
		Digest:          requestDigests[0],
		AcceptedDigests: requestDigests[1:],
		TTL:             cancellationTTL,
	}

	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		if _, cancelErr := service.store.RequestCancelTx(ctx, tx, id); cancelErr != nil {
			return idempotency.Result{}, cancelErr
		}
		value, getErr := service.store.GetTx(ctx, tx, id)
		if getErr != nil {
			return idempotency.Result{}, getErr
		}
		if auditErr := appendCancellationAudit(ctx, tx, actorID, value); auditErr != nil {
			return idempotency.Result{}, auditErr
		}
		return idempotency.Result{
			Type: "job_cancel:" + string(value.Status),
			ID:   [16]byte(id),
		}, nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	value, err := service.store.GetTx(ctx, tx, JobID(result.ID))
	if err != nil {
		return Snapshot{}, err
	}
	expected, ok := strings.CutPrefix(result.Type, "job_cancel:")
	if !ok || expected == "" || !cancellationResultApplies(Status(expected), value.Status) {
		return Snapshot{}, ErrJobConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return Snapshot{}, err
	}
	return value, nil
}

func cancellationDigests(values [][]byte) ([]idempotency.Digest, error) {
	result := make([]idempotency.Digest, len(values))
	for index, value := range values {
		if len(value) != len(result[index]) {
			return nil, errors.New("cancellation digest is invalid")
		}
		copy(result[index][:], value)
	}
	return result, nil
}

func cancellationResultApplies(expected, current Status) bool {
	return current == expected || expected == CancelRequested && current == Cancelled
}

func appendCancellationAudit(ctx context.Context, tx pgx.Tx, actorID ActorID, value Snapshot) error {
	details, err := json.Marshal(PublicSnapshot(value))
	if err != nil {
		return errors.New("encode cancellation audit")
	}
	requestID, err := newAuditUUID()
	if err != nil {
		return errors.New("generate cancellation audit ID")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_events (
			actor_type, actor_id, action, target_type, target_id,
			request_id, details, created_at
		) VALUES (
			'operator', $1, 'job.cancel', 'job', $2,
			$3, $4::jsonb, clock_timestamp()
		)
	`,
		pgtype.UUID{Bytes: [16]byte(actorID), Valid: true},
		pgtype.UUID{Bytes: [16]byte(value.ID), Valid: true},
		pgtype.UUID{Bytes: requestID, Valid: true},
		string(details),
	)
	if err != nil {
		return fmt.Errorf("append cancellation audit: %w", err)
	}
	return nil
}

func newAuditUUID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return [16]byte{}, err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}

// PublicSnapshot is the only job representation permitted in audit details or
// operator responses. It deliberately omits payload and operation_key.
func PublicSnapshot(value Snapshot) map[string]any {
	return map[string]any{
		"id":               value.ID.String(),
		"job_type":         strings.ToLower(string(value.Type)),
		"target_type":      value.TargetType,
		"target_id":        value.TargetID.String(),
		"status":           strings.ToLower(string(value.Status)),
		"attempt_count":    value.AttemptCount,
		"max_attempts":     value.MaxAttempts,
		"progress":         value.Progress,
		"lease_owner":      value.LeaseOwner,
		"lease_expires_at": optionalPythonTime(value.LeaseExpiresAt),
		"lease_generation": value.LeaseGeneration,
		"not_before":       optionalPythonTime(value.NotBefore),
		"result":           value.Result,
		"sanitized_error":  value.SanitizedError,
		"created_at":       pythonISOTime(value.CreatedAt),
		"updated_at":       pythonISOTime(value.UpdatedAt),
		"started_at":       optionalPythonTime(value.StartedAt),
		"finished_at":      optionalPythonTime(value.FinishedAt),
	}
}

func optionalPythonTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return pythonISOTime(*value)
}

func pythonISOTime(value time.Time) string {
	value = value.UTC()
	result := value.Format("2006-01-02T15:04:05")
	if value.Nanosecond() != 0 {
		result += fmt.Sprintf(".%06d", value.Nanosecond()/1_000)
	}
	return result + "+00:00"
}
