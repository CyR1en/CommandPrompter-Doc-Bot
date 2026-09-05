package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	dbsqlc "github.com/cyr1en/ref0/internal/database/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TerminalCallback func(context.Context, pgx.Tx, Snapshot) error

type Store struct {
	pool             *pgxpool.Pool
	terminalCallback TerminalCallback
}

func NewStore(pool *pgxpool.Pool, terminalCallback TerminalCallback) *Store {
	return &Store{pool: pool, terminalCallback: terminalCallback}
}

func (store *Store) Enqueue(ctx context.Context, command Command) (JobID, error) {
	var id JobID
	err := store.transaction(ctx, func(tx pgx.Tx, _ *dbsqlc.Queries) error {
		var err error
		id, err = store.EnqueueTx(ctx, tx, command)
		return err
	})
	return id, err
}

// EnqueueTx creates or adopts an active operation-key job inside a
// caller-owned transaction so domain state, audit/resource events,
// idempotency records, and job creation can commit atomically.
func (store *Store) EnqueueTx(ctx context.Context, tx pgx.Tx, command Command) (JobID, error) {
	return store.enqueueTx(ctx, tx, command, nil)
}

// EnqueueTxAt is EnqueueTx with a caller-owned database timestamp. Domain
// mutations that already read clock_timestamp after taking their row lock use
// this to keep the job row and its initial events on that same write time.
func (store *Store) EnqueueTxAt(ctx context.Context, tx pgx.Tx, command Command, now time.Time) (JobID, error) {
	if now.IsZero() {
		return JobID{}, errors.New("enqueue time must not be zero")
	}
	return store.enqueueTx(ctx, tx, command, &now)
}

func (store *Store) enqueueTx(ctx context.Context, tx pgx.Tx, command Command, now *time.Time) (JobID, error) {
	if tx == nil {
		return JobID{}, errors.New("transaction must not be nil")
	}
	if command.MaxAttempts == 0 {
		command.MaxAttempts = 3
	}
	if command.Payload == nil {
		command.Payload = map[string]any{}
	}
	if err := command.validate(); err != nil {
		return JobID{}, err
	}
	payload, err := json.Marshal(command.Payload)
	if err != nil {
		return JobID{}, errors.New("job payload is not valid JSON")
	}

	queries := dbsqlc.New(tx)
	var currentTime pgtype.Timestamptz
	if now == nil {
		currentTime, err = queries.DatabaseClock(ctx)
		if err != nil {
			return JobID{}, err
		}
	} else {
		currentTime = pgtype.Timestamptz{Time: *now, Valid: true}
	}
	for {
		inserted, err := queries.InsertJob(ctx, dbsqlc.InsertJobParams{
			JobType:          string(command.Type),
			TargetType:       command.TargetType,
			TargetID:         toPGUUID(command.TargetID),
			Payload:          payload,
			OperationKey:     command.OperationKey,
			ConcurrencyKey:   command.ConcurrencyKey,
			ConcurrencyLimit: command.ConcurrencyLimit,
			MaxAttempts:      command.MaxAttempts,
			NotBefore:        toPGTime(command.NotBefore),
			CurrentTime:      currentTime,
		})
		if err == nil {
			id := jobIDFromPG(inserted)
			if err = store.recordEvent(ctx, queries, id, nil, "ENQUEUED", currentTime); err != nil {
				return JobID{}, err
			}
			return id, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return JobID{}, err
		}
		active, findErr := queries.FindActiveJobIDByOperationKey(ctx, command.OperationKey)
		if findErr == nil {
			return jobIDFromPG(active), nil
		}
		if !errors.Is(findErr, pgx.ErrNoRows) {
			return JobID{}, findErr
		}
	}
}

func (store *Store) Claim(ctx context.Context, workerID WorkerID, leaseFor time.Duration) (*Permit, error) {
	if workerID == "" {
		return nil, errors.New("worker ID must not be empty")
	}
	if leaseFor.Microseconds() <= 0 {
		return nil, errors.New("lease duration must be positive")
	}
	return store.claim(ctx, workerID, leaseFor, nil)
}

func (store *Store) claim(ctx context.Context, workerID WorkerID, leaseFor time.Duration, now *time.Time) (*Permit, error) {
	var permit *Permit
	err := store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		if err := queries.AcquireJobAdmissionLock(ctx); err != nil {
			return err
		}
		for {
			row, err := queries.LockEligibleJob(ctx, toPGTime(now))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			currentTime, err := store.clock(ctx, queries, now)
			if err != nil {
				return err
			}
			id := jobIDFromPG(row.ID)
			if Status(row.Status) == CancelRequested {
				if err := store.cancelLocked(ctx, tx, queries, cancellationRow{
					id: row.ID, attemptCount: row.AttemptCount, leaseGeneration: row.LeaseGeneration,
				}, currentTime, true); err != nil {
					return err
				}
				continue
			}
			if Status(row.Status) == Leased && row.AttemptCount >= row.MaxAttempts {
				if err := store.expireExhausted(ctx, tx, queries, row, currentTime); err != nil {
					return err
				}
				continue
			}

			attemptNumber := row.AttemptCount + 1
			leaseGeneration := row.LeaseGeneration + 1
			if Status(row.Status) == Leased {
				if _, err := queries.ExpireAttempt(ctx, dbsqlc.ExpireAttemptParams{
					CurrentTime: currentTime, JobID: row.ID, LeaseGeneration: row.LeaseGeneration,
				}); err != nil {
					return err
				}
			}
			if _, err := queries.LeaseJob(ctx, dbsqlc.LeaseJobParams{
				AttemptNumber:     attemptNumber,
				WorkerID:          pgtype.Text{String: string(workerID), Valid: true},
				CurrentTime:       currentTime,
				LeaseMicroseconds: leaseFor.Microseconds(),
				LeaseGeneration:   leaseGeneration,
				JobID:             row.ID,
			}); err != nil {
				return err
			}
			if err := queries.InsertJobAttempt(ctx, dbsqlc.InsertJobAttemptParams{
				JobID: row.ID, AttemptNumber: attemptNumber, LeaseGeneration: leaseGeneration,
				WorkerID: string(workerID), CurrentTime: currentTime,
			}); err != nil {
				return err
			}
			if err := store.recordEvent(ctx, queries, id, &attemptNumber, "CLAIMED", currentTime); err != nil {
				return err
			}
			permit = &Permit{JobID: id, WorkerID: workerID, LeaseGeneration: leaseGeneration}
			return nil
		}
	})
	return permit, err
}

func (store *Store) GetCommand(ctx context.Context, permit Permit) (Command, error) {
	var command Command
	err := store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		if _, _, err := store.lockPermit(ctx, queries, permit, []Status{Leased}, nil); err != nil {
			return err
		}
		row, err := queries.GetJobCommand(ctx, toPGJobID(permit.JobID))
		if err != nil {
			return err
		}
		payload := map[string]any{}
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return errors.New("stored job payload is invalid")
		}
		command = Command{
			Type: Type(row.JobType), TargetType: row.TargetType, TargetID: uuidFromPG(row.TargetID),
			Payload: payload, OperationKey: row.OperationKey, MaxAttempts: row.MaxAttempts,
			NotBefore: timePointer(row.NotBefore), ConcurrencyKey: row.ConcurrencyKey,
			ConcurrencyLimit: row.ConcurrencyLimit,
		}
		return nil
	})
	return command, err
}

func (store *Store) Heartbeat(ctx context.Context, permit Permit, leaseFor time.Duration) error {
	if leaseFor.Microseconds() <= 0 {
		return errors.New("lease duration must be positive")
	}
	return store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		row, currentTime, err := store.lockPermit(ctx, queries, permit, []Status{Leased}, nil)
		if err != nil {
			return err
		}
		if _, err := queries.HeartbeatJob(ctx, dbsqlc.HeartbeatJobParams{
			CurrentTime: currentTime, LeaseMicroseconds: leaseFor.Microseconds(), JobID: row.ID,
		}); err != nil {
			return err
		}
		if _, err := queries.HeartbeatAttempt(ctx, dbsqlc.HeartbeatAttemptParams{
			CurrentTime: currentTime, JobID: row.ID, LeaseGeneration: permit.LeaseGeneration,
		}); err != nil {
			return err
		}
		return store.recordEvent(ctx, queries, permit.JobID, &row.AttemptCount, "HEARTBEAT", currentTime)
	})
}

func (store *Store) Succeed(ctx context.Context, permit Permit, result map[string]any) error {
	return store.complete(ctx, permit, result, []Status{Leased})
}

func (store *Store) CompleteAcceptedResult(ctx context.Context, permit Permit, result map[string]any) error {
	return store.complete(ctx, permit, result, []Status{Leased, CancelRequested})
}

func (store *Store) complete(ctx context.Context, permit Permit, result map[string]any, statuses []Status) error {
	encoded, err := objectJSON(result)
	if err != nil {
		return err
	}
	return store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		row, currentTime, err := store.lockPermit(ctx, queries, permit, statuses, nil)
		if err != nil {
			return err
		}
		return store.finish(ctx, tx, queries, permit, row.AttemptCount, Succeeded, "SUCCEEDED", currentTime, encoded, nil, nil)
	})
}

func (store *Store) RetryAt(ctx context.Context, permit Permit, sanitizedError string, notBefore time.Time) (Status, error) {
	var status Status
	err := store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		row, currentTime, err := store.lockPermit(ctx, queries, permit, []Status{Leased}, nil)
		if err != nil {
			return err
		}
		status = retryStatus(row.AttemptCount, row.MaxAttempts)
		if status == RetryWait && !notBefore.After(currentTime.Time) {
			return errors.New("not-before must be in the future")
		}
		var retryAt *time.Time
		if status == RetryWait {
			retryAt = &notBefore
		}
		outcome := "FAILED"
		if status == RetryWait {
			outcome = "RETRY"
		}
		return store.finish(ctx, tx, queries, permit, row.AttemptCount, status, outcome, currentTime, nil, &sanitizedError, retryAt)
	})
	return status, err
}

func (store *Store) RetryAfter(ctx context.Context, permit Permit, sanitizedError string, delay time.Duration) (Status, error) {
	if delay <= 0 {
		return "", errors.New("retry delay must be positive")
	}
	var status Status
	err := store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		row, currentTime, err := store.lockPermit(ctx, queries, permit, []Status{Leased}, nil)
		if err != nil {
			return err
		}
		status = retryStatus(row.AttemptCount, row.MaxAttempts)
		var retryAt *time.Time
		if status == RetryWait {
			value := currentTime.Time.Add(delay)
			retryAt = &value
		}
		outcome := "FAILED"
		if status == RetryWait {
			outcome = "RETRY"
		}
		return store.finish(ctx, tx, queries, permit, row.AttemptCount, status, outcome, currentTime, nil, &sanitizedError, retryAt)
	})
	return status, err
}

func (store *Store) Fail(ctx context.Context, permit Permit, sanitizedError string) error {
	return store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		row, currentTime, err := store.lockPermit(ctx, queries, permit, []Status{Leased}, nil)
		if err != nil {
			return err
		}
		return store.finish(ctx, tx, queries, permit, row.AttemptCount, Failed, "FAILED", currentTime, nil, &sanitizedError, nil)
	})
}

func (store *Store) RequestCancel(ctx context.Context, id JobID) (Status, error) {
	var result Status
	err := store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		var err error
		result, err = store.requestCancel(ctx, tx, queries, id)
		return err
	})
	return result, err
}

// RequestCancelTx applies cancellation inside a caller-owned transaction so
// idempotency, audit, and queue state can commit atomically.
func (store *Store) RequestCancelTx(ctx context.Context, tx pgx.Tx, id JobID) (Status, error) {
	if tx == nil {
		return "", errors.New("transaction must not be nil")
	}
	return store.requestCancel(ctx, tx, dbsqlc.New(tx), id)
}

func (store *Store) requestCancel(
	ctx context.Context,
	tx pgx.Tx,
	queries *dbsqlc.Queries,
	id JobID,
) (Status, error) {
	row, err := queries.LockJobForCancellation(ctx, toPGJobID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrJobNotFound
	}
	if err != nil {
		return "", err
	}
	currentTime, err := queries.DatabaseClock(ctx)
	if err != nil {
		return "", err
	}
	status := Status(row.Status)
	cancellation := cancellationRow{row.ID, row.AttemptCount, row.LeaseGeneration}
	switch status {
	case Pending, RetryWait:
		return Cancelled, store.cancelLocked(ctx, tx, queries, cancellation, currentTime, false)
	case Leased:
		if !row.LeaseExpiresAt.Valid || !row.LeaseExpiresAt.Time.After(currentTime.Time) {
			return Cancelled, store.cancelLocked(ctx, tx, queries, cancellation, currentTime, true)
		}
		if _, err := queries.RequestJobCancellation(ctx, dbsqlc.RequestJobCancellationParams{CurrentTime: currentTime, JobID: row.ID}); err != nil {
			return "", err
		}
		return CancelRequested, store.recordEvent(ctx, queries, id, &row.AttemptCount, string(CancelRequested), currentTime)
	case CancelRequested:
		if !row.LeaseExpiresAt.Valid || !row.LeaseExpiresAt.Time.After(currentTime.Time) {
			return Cancelled, store.cancelLocked(ctx, tx, queries, cancellation, currentTime, true)
		}
		return CancelRequested, nil
	case Cancelled:
		return Cancelled, nil
	default:
		return "", fmt.Errorf("%w: cannot cancel a %s job", ErrJobConflict, status)
	}
}

func (store *Store) AcknowledgeCancel(ctx context.Context, permit Permit) error {
	return store.transaction(ctx, func(tx pgx.Tx, queries *dbsqlc.Queries) error {
		row, currentTime, err := store.lockPermit(ctx, queries, permit, []Status{CancelRequested}, nil)
		if err != nil {
			return err
		}
		return store.cancelLocked(ctx, tx, queries, cancellationRow{row.ID, row.AttemptCount, row.LeaseGeneration}, currentTime, true)
	})
}

// AssertPermit fences domain writes when called inside the same transaction.
func (store *Store) AssertPermit(ctx context.Context, tx pgx.Tx, permit Permit) error {
	_, _, err := store.lockPermit(ctx, dbsqlc.New(tx), permit, []Status{Leased}, nil)
	return err
}

func (store *Store) Get(ctx context.Context, id JobID) (Snapshot, error) {
	row, err := dbsqlc.New(store.pool).GetJob(ctx, toPGJobID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrJobNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot(row)
}

func (store *Store) transaction(ctx context.Context, operation func(pgx.Tx, *dbsqlc.Queries) error) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := operation(tx, dbsqlc.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *Store) clock(ctx context.Context, queries *dbsqlc.Queries, now *time.Time) (pgtype.Timestamptz, error) {
	if now != nil {
		return pgtype.Timestamptz{Time: *now, Valid: true}, nil
	}
	return queries.DatabaseClock(ctx)
}

func (store *Store) lockPermit(ctx context.Context, queries *dbsqlc.Queries, permit Permit, statuses []Status, now *time.Time) (*dbsqlc.LockPermitJobRow, pgtype.Timestamptz, error) {
	if err := permit.validate(); err != nil {
		return nil, pgtype.Timestamptz{}, err
	}
	row, err := queries.LockPermitJob(ctx, toPGJobID(permit.JobID))
	currentTime, clockErr := store.clock(ctx, queries, now)
	if clockErr != nil {
		return nil, pgtype.Timestamptz{}, clockErr
	}
	if err != nil || !containsStatus(statuses, Status(valueOrEmpty(row, err))) || row.LeaseOwner.String != string(permit.WorkerID) || !row.LeaseOwner.Valid || row.LeaseGeneration != permit.LeaseGeneration || !row.LeaseExpiresAt.Valid || !row.LeaseExpiresAt.Time.After(currentTime.Time) {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, pgtype.Timestamptz{}, err
		}
		return nil, pgtype.Timestamptz{}, ErrStalePermit
	}
	return row, currentTime, nil
}

func valueOrEmpty(row *dbsqlc.LockPermitJobRow, err error) string {
	if err != nil || row == nil {
		return ""
	}
	return row.Status
}

func containsStatus(values []Status, wanted Status) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type cancellationRow struct {
	id              pgtype.UUID
	attemptCount    int32
	leaseGeneration int64
}

func (store *Store) cancelLocked(ctx context.Context, tx pgx.Tx, queries *dbsqlc.Queries, row cancellationRow, currentTime pgtype.Timestamptz, finishAttempt bool) error {
	if finishAttempt {
		if _, err := queries.FinishAttempt(ctx, dbsqlc.FinishAttemptParams{
			CurrentTime:     currentTime,
			Outcome:         pgtype.Text{String: string(Cancelled), Valid: true},
			JobID:           row.id,
			LeaseGeneration: row.leaseGeneration,
		}); err != nil {
			return err
		}
	}
	if _, err := queries.CancelJob(ctx, dbsqlc.CancelJobParams{CurrentTime: currentTime, JobID: row.id}); err != nil {
		return err
	}
	id := jobIDFromPG(row.id)
	var attempt *int32
	if finishAttempt {
		attempt = &row.attemptCount
	}
	if err := store.recordEvent(ctx, queries, id, attempt, string(Cancelled), currentTime); err != nil {
		return err
	}
	return store.notifyTerminal(ctx, tx, queries, id)
}

func (store *Store) expireExhausted(ctx context.Context, tx pgx.Tx, queries *dbsqlc.Queries, row *dbsqlc.LockEligibleJobRow, currentTime pgtype.Timestamptz) error {
	const message = "lease expired after final attempt"
	if _, err := queries.ExpireAttempt(ctx, dbsqlc.ExpireAttemptParams{
		CurrentTime:     currentTime,
		SanitizedError:  pgtype.Text{String: message, Valid: true},
		JobID:           row.ID,
		LeaseGeneration: row.LeaseGeneration,
	}); err != nil {
		return err
	}
	permit := Permit{JobID: jobIDFromPG(row.ID), LeaseGeneration: row.LeaseGeneration}
	if _, err := queries.FinishJob(ctx, dbsqlc.FinishJobParams{
		Status: string(Failed), SanitizedError: pgtype.Text{String: message, Valid: true},
		CurrentTime: currentTime, Terminal: true, JobID: row.ID,
	}); err != nil {
		return err
	}
	if err := store.recordEvent(ctx, queries, permit.JobID, &row.AttemptCount, string(Failed), currentTime); err != nil {
		return err
	}
	return store.notifyTerminal(ctx, tx, queries, permit.JobID)
}

func (store *Store) finish(ctx context.Context, tx pgx.Tx, queries *dbsqlc.Queries, permit Permit, attemptNumber int32, status Status, outcome string, currentTime pgtype.Timestamptz, result []byte, sanitizedError *string, notBefore *time.Time) error {
	terminal := status == Succeeded || status == Failed || status == Cancelled
	if _, err := queries.FinishJob(ctx, dbsqlc.FinishJobParams{
		Status: string(status), NotBefore: toPGTime(notBefore), Result: result,
		SanitizedError: toPGText(sanitizedError), CurrentTime: currentTime,
		Terminal: terminal, JobID: toPGJobID(permit.JobID),
	}); err != nil {
		return err
	}
	if _, err := queries.FinishAttempt(ctx, dbsqlc.FinishAttemptParams{
		CurrentTime: currentTime, Outcome: pgtype.Text{String: outcome, Valid: true},
		SanitizedError: toPGText(sanitizedError), JobID: toPGJobID(permit.JobID),
		LeaseGeneration: permit.LeaseGeneration,
	}); err != nil {
		return err
	}
	if err := store.recordEvent(ctx, queries, permit.JobID, &attemptNumber, string(status), currentTime); err != nil {
		return err
	}
	if status == Failed || status == Cancelled {
		return store.notifyTerminal(ctx, tx, queries, permit.JobID)
	}
	return nil
}

func (store *Store) recordEvent(ctx context.Context, queries *dbsqlc.Queries, id JobID, attemptNumber *int32, eventKind string, currentTime pgtype.Timestamptz) error {
	if err := queries.InsertJobEvent(ctx, dbsqlc.InsertJobEventParams{
		AttemptNumber: toPGInt4(attemptNumber), EventKind: eventKind,
		CurrentTime: currentTime, JobID: toPGJobID(id),
	}); err != nil {
		return err
	}
	if err := queries.AcquireEventSequenceLock(ctx); err != nil {
		return err
	}
	return queries.InsertJobResourceEvent(ctx, dbsqlc.InsertJobResourceEventParams{
		EventKind: eventKind, CurrentTime: currentTime, JobID: toPGJobID(id),
	})
}

func (store *Store) notifyTerminal(ctx context.Context, tx pgx.Tx, queries *dbsqlc.Queries, id JobID) error {
	if store.terminalCallback == nil {
		return nil
	}
	row, err := queries.GetJob(ctx, toPGJobID(id))
	if err != nil {
		return err
	}
	value, err := snapshot(row)
	if err != nil {
		return err
	}
	return store.terminalCallback(ctx, tx, value)
}

func snapshot(row *dbsqlc.Job) (Snapshot, error) {
	result := map[string]any(nil)
	if row.Result != nil {
		if err := json.Unmarshal(row.Result, &result); err != nil {
			return Snapshot{}, errors.New("stored job result is invalid")
		}
	}
	return Snapshot{
		ID: jobIDFromPG(row.ID), Type: Type(row.JobType), TargetType: row.TargetType,
		TargetID: uuidFromPG(row.TargetID), Status: Status(row.Status),
		AttemptCount: row.AttemptCount, MaxAttempts: row.MaxAttempts, Progress: row.Progress,
		LeaseOwner: textPointer(row.LeaseOwner), LeaseExpiresAt: timePointer(row.LeaseExpiresAt),
		LeaseGeneration: row.LeaseGeneration, NotBefore: timePointer(row.NotBefore), Result: result,
		SanitizedError: textPointer(row.SanitizedError), CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time, StartedAt: timePointer(row.StartedAt), FinishedAt: timePointer(row.FinishedAt),
	}, nil
}

func objectJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("job result is not valid JSON")
	}
	return encoded, nil
}

func toPGUUID(id UUID) pgtype.UUID     { return pgtype.UUID{Bytes: [16]byte(id), Valid: true} }
func toPGJobID(id JobID) pgtype.UUID   { return toPGUUID(UUID(id)) }
func uuidFromPG(id pgtype.UUID) UUID   { return UUID(id.Bytes) }
func jobIDFromPG(id pgtype.UUID) JobID { return JobID(id.Bytes) }

func toPGTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func toPGText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func textPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func toPGInt4(value *int32) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *value, Valid: true}
}
