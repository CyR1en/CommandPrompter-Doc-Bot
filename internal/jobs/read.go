package jobs

import (
	"context"
	"errors"

	dbsqlc "github.com/cyr1en/ref0/internal/database/sqlc"
	"github.com/jackc/pgx/v5"
)

type ListOptions struct {
	Limit  int32
	Offset int32
	Status *Status
	Type   *Type
}

func (store *Store) List(ctx context.Context, options ListOptions) ([]Snapshot, error) {
	if options.Limit < 1 || options.Limit > 100 || options.Offset < 0 || options.Offset > 10_000 {
		return nil, errors.New("job page is out of bounds")
	}
	status := ""
	if options.Status != nil {
		if !ValidStatus(*options.Status) {
			return nil, errors.New("job status is invalid")
		}
		status = string(*options.Status)
	}
	jobType := ""
	if options.Type != nil {
		if !ValidType(*options.Type) {
			return nil, errors.New("job type is invalid")
		}
		jobType = string(*options.Type)
	}
	rows, err := store.pool.Query(ctx, `
		SELECT id, job_type, target_type, target_id, payload, operation_key,
		       status, attempt_count, max_attempts, progress, lease_owner,
		       lease_expires_at, lease_generation, not_before, result,
		       sanitized_error, created_at, updated_at, started_at, finished_at
		FROM jobs
		WHERE ($1::varchar = '' OR status = $1::varchar)
		  AND ($2::varchar = '' OR job_type = $2::varchar)
		ORDER BY created_at, id
		LIMIT $3 OFFSET $4
	`, status, jobType, options.Limit, options.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Snapshot{}
	for rows.Next() {
		var row dbsqlc.Job
		if err := rows.Scan(
			&row.ID, &row.JobType, &row.TargetType, &row.TargetID, &row.Payload,
			&row.OperationKey, &row.Status, &row.AttemptCount, &row.MaxAttempts,
			&row.Progress, &row.LeaseOwner, &row.LeaseExpiresAt, &row.LeaseGeneration,
			&row.NotBefore, &row.Result, &row.SanitizedError, &row.CreatedAt,
			&row.UpdatedAt, &row.StartedAt, &row.FinishedAt,
		); err != nil {
			return nil, err
		}
		value, err := snapshot(&row)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *Store) GetTx(ctx context.Context, tx pgx.Tx, id JobID) (Snapshot, error) {
	row, err := dbsqlc.New(tx).GetJob(ctx, toPGJobID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrJobNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot(row)
}
