package jobs

import (
	"context"
	"errors"
	"time"

	dbsqlc "github.com/cyr1en/ref0/internal/database/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrModelBusy = errors.New("provider concurrency limit reached")

// AcquireModelCall shares admission with durable model jobs across processes.
// ponytail: an endpoint uses its strictest model limit; add a separate endpoint
// setting only when operators need independent limits for models on one endpoint.
func (store *Store) AcquireModelCall(ctx context.Context, profileID UUID, timeout time.Duration) (func(), error) {
	if timeout <= 0 || timeout > time.Minute {
		return nil, errors.New("invalid model call timeout")
	}
	waiting, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		id, err := store.tryModelCall(waiting, profileID, timeout)
		if err != nil {
			if waiting.Err() != nil {
				return nil, ErrModelBusy
			}
			return nil, err
		}
		if id.Valid {
			return func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer stop()
				_, _ = store.pool.Exec(cleanup, `DELETE FROM provider_call_leases WHERE id=$1`, id)
			}, nil
		}
		select {
		case <-waiting.Done():
			return nil, ErrModelBusy
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (store *Store) tryModelCall(ctx context.Context, profileID UUID, timeout time.Duration) (pgtype.UUID, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return pgtype.UUID{}, err
	}
	defer tx.Rollback(ctx)
	if err = dbsqlc.New(tx).AcquireJobAdmissionLock(ctx); err != nil {
		return pgtype.UUID{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM provider_call_leases WHERE expires_at <= clock_timestamp()`); err != nil {
		return pgtype.UUID{}, err
	}
	var endpoint pgtype.UUID
	var capacity, occupied int
	err = tx.QueryRow(ctx, `
 SELECT profile.endpoint_id,
  (SELECT min(version.max_concurrent_tasks) FROM model_profiles model
   JOIN model_profile_versions version ON version.id=model.current_version_id
   WHERE model.endpoint_id=profile.endpoint_id),
  (SELECT count(*) FROM provider_call_leases call WHERE call.endpoint_id=profile.endpoint_id AND call.expires_at>clock_timestamp())
  + (SELECT count(*) FROM jobs active JOIN model_profiles model ON active.concurrency_key='model-profile:'||model.id::text
     WHERE model.endpoint_id=profile.endpoint_id AND active.status IN ('LEASED','CANCEL_REQUESTED') AND active.lease_expires_at>clock_timestamp())
 FROM model_profiles profile WHERE profile.id=$1
 `, toPGUUID(profileID)).Scan(&endpoint, &capacity, &occupied)
	if err != nil {
		return pgtype.UUID{}, err
	}
	var id pgtype.UUID
	if occupied < capacity {
		err = tx.QueryRow(ctx, `INSERT INTO provider_call_leases(endpoint_id,expires_at) VALUES($1,clock_timestamp()+$2*interval '1 millisecond') RETURNING id`, endpoint, (timeout + 5*time.Second).Milliseconds()).Scan(&id)
		if err != nil {
			return pgtype.UUID{}, err
		}
	}
	return id, tx.Commit(ctx)
}
