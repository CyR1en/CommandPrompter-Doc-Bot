-- name: DatabaseClock :one
SELECT clock_timestamp()::timestamptz;

-- name: InsertJob :one
INSERT INTO jobs (
    job_type,
    target_type,
    target_id,
    payload,
    operation_key,
    concurrency_key,
    concurrency_limit,
    max_attempts,
    not_before,
    created_at,
    updated_at
) VALUES (
    sqlc.arg(job_type),
    sqlc.arg(target_type),
    sqlc.arg(target_id),
    sqlc.arg(payload)::jsonb,
    sqlc.arg(operation_key),
    sqlc.arg(concurrency_key),
    sqlc.arg(concurrency_limit),
    sqlc.arg(max_attempts),
    sqlc.narg(not_before)::timestamptz,
    sqlc.arg('current_time'),
    sqlc.arg('current_time')
)
ON CONFLICT (operation_key)
WHERE status IN ('PENDING', 'LEASED', 'RETRY_WAIT', 'CANCEL_REQUESTED')
DO NOTHING
RETURNING id;

-- name: FindActiveJobIDByOperationKey :one
SELECT id
FROM jobs
WHERE operation_key = sqlc.arg(operation_key)
  AND status IN ('PENDING', 'LEASED', 'RETRY_WAIT', 'CANCEL_REQUESTED');

-- name: LockEligibleJob :one
SELECT candidate.id, candidate.status, candidate.attempt_count, candidate.max_attempts, candidate.lease_generation
FROM jobs candidate
WHERE ((
        status = 'PENDING'
        AND attempt_count < max_attempts
        AND (
            not_before IS NULL
            OR not_before <= COALESCE(sqlc.narg(now)::timestamptz, clock_timestamp())
        )
    ) OR (
        status = 'RETRY_WAIT'
        AND attempt_count < max_attempts
        AND not_before <= COALESCE(sqlc.narg(now)::timestamptz, clock_timestamp())
    ) OR (
        status IN ('LEASED', 'CANCEL_REQUESTED')
        AND lease_expires_at <= COALESCE(sqlc.narg(now)::timestamptz, clock_timestamp())
    )) AND (
        candidate.concurrency_key = ''
        OR (
            SELECT count(*)
            FROM jobs active
            WHERE active.concurrency_key = candidate.concurrency_key
              AND active.status IN ('LEASED', 'CANCEL_REQUESTED')
              AND active.lease_expires_at > COALESCE(sqlc.narg(now)::timestamptz, clock_timestamp())
        ) < candidate.concurrency_limit
    )
AND NOT EXISTS (
    SELECT 1 FROM model_profiles profile
    WHERE candidate.concurrency_key = 'model-profile:' || profile.id::text
      AND (
          (SELECT count(*) FROM provider_call_leases call
           WHERE call.endpoint_id=profile.endpoint_id AND call.expires_at > COALESCE(sqlc.narg(now)::timestamptz, clock_timestamp()))
          + (SELECT count(*) FROM jobs active JOIN model_profiles model ON active.concurrency_key='model-profile:' || model.id::text
             WHERE model.endpoint_id=profile.endpoint_id AND active.status IN ('LEASED','CANCEL_REQUESTED')
               AND active.lease_expires_at > COALESCE(sqlc.narg(now)::timestamptz, clock_timestamp()))
      ) >= (SELECT min(version.max_concurrent_tasks) FROM model_profiles model
            JOIN model_profile_versions version ON version.id=model.current_version_id
            WHERE model.endpoint_id=profile.endpoint_id)
)
ORDER BY candidate.created_at, candidate.id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: AcquireJobAdmissionLock :exec
SELECT pg_advisory_xact_lock(hashtextextended('ref0.job-queue.admission', 0));

-- name: ExpireAttempt :execrows
UPDATE job_attempts
SET heartbeat_at = sqlc.arg('current_time'),
    outcome = 'LEASE_EXPIRED',
    sanitized_error = sqlc.narg(sanitized_error)::text,
    finished_at = sqlc.arg('current_time')
WHERE job_id = sqlc.arg(job_id)
  AND lease_generation = sqlc.arg(lease_generation);

-- name: LeaseJob :execrows
UPDATE jobs
SET status = 'LEASED',
    attempt_count = sqlc.arg(attempt_number),
    lease_owner = sqlc.arg(worker_id),
    lease_expires_at = sqlc.arg('current_time')::timestamptz + sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond',
    lease_generation = sqlc.arg(lease_generation),
    not_before = NULL,
    updated_at = sqlc.arg('current_time'),
    started_at = COALESCE(started_at, sqlc.arg('current_time'))
WHERE id = sqlc.arg(job_id);

-- name: InsertJobAttempt :exec
INSERT INTO job_attempts (
    job_id,
    attempt_number,
    lease_generation,
    worker_id,
    heartbeat_at,
    started_at
) VALUES (
    sqlc.arg(job_id),
    sqlc.arg(attempt_number),
    sqlc.arg(lease_generation),
    sqlc.arg(worker_id),
    sqlc.arg('current_time'),
    sqlc.arg('current_time')
);

-- name: GetJob :one
SELECT *
FROM jobs
WHERE id = sqlc.arg(job_id);

-- name: GetJobCommand :one
SELECT job_type, target_type, target_id, payload, operation_key, max_attempts, not_before,
       concurrency_key, concurrency_limit
FROM jobs
WHERE id = sqlc.arg(job_id);

-- name: LockPermitJob :one
SELECT id, status, attempt_count, max_attempts, lease_owner, lease_expires_at, lease_generation
FROM jobs
WHERE id = sqlc.arg(job_id)
FOR UPDATE;

-- name: HeartbeatJob :execrows
UPDATE jobs
SET lease_expires_at = sqlc.arg('current_time')::timestamptz + sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond',
    updated_at = sqlc.arg('current_time')
WHERE id = sqlc.arg(job_id);

-- name: HeartbeatAttempt :execrows
UPDATE job_attempts
SET heartbeat_at = sqlc.arg('current_time')
WHERE job_id = sqlc.arg(job_id)
  AND lease_generation = sqlc.arg(lease_generation);

-- name: FinishJob :execrows
UPDATE jobs
SET status = sqlc.arg(status)::varchar(24),
    progress = CASE WHEN sqlc.arg(status)::varchar(24) = 'SUCCEEDED'::varchar(24) THEN 100 ELSE progress END,
    lease_owner = NULL,
    lease_expires_at = NULL,
    not_before = sqlc.narg(not_before)::timestamptz,
    result = sqlc.narg(result)::jsonb,
    sanitized_error = sqlc.narg(sanitized_error)::text,
    updated_at = sqlc.arg('current_time')::timestamptz,
    finished_at = CASE WHEN sqlc.arg(terminal)::boolean THEN sqlc.arg('current_time')::timestamptz ELSE NULL::timestamptz END
WHERE id = sqlc.arg(job_id);

-- name: FinishAttempt :execrows
UPDATE job_attempts
SET heartbeat_at = sqlc.arg('current_time'),
    outcome = sqlc.arg(outcome),
    sanitized_error = sqlc.narg(sanitized_error)::text,
    finished_at = sqlc.arg('current_time')
WHERE job_id = sqlc.arg(job_id)
  AND lease_generation = sqlc.arg(lease_generation);

-- name: LockJobForCancellation :one
SELECT id, status, attempt_count, lease_expires_at, lease_generation
FROM jobs
WHERE id = sqlc.arg(job_id)
FOR UPDATE;

-- name: RequestJobCancellation :execrows
UPDATE jobs
SET status = 'CANCEL_REQUESTED',
    updated_at = sqlc.arg('current_time')
WHERE id = sqlc.arg(job_id);

-- name: CancelJob :execrows
UPDATE jobs
SET status = 'CANCELLED',
    lease_owner = NULL,
    lease_expires_at = NULL,
    not_before = NULL,
    result = NULL,
    sanitized_error = NULL,
    updated_at = sqlc.arg('current_time'),
    finished_at = sqlc.arg('current_time')
WHERE id = sqlc.arg(job_id);

-- name: InsertJobEvent :exec
INSERT INTO job_events (
    job_id,
    attempt_number,
    event_kind,
    status,
    payload,
    created_at
)
SELECT
    id,
    sqlc.narg(attempt_number)::integer,
    sqlc.arg(event_kind),
    status,
    '{}'::jsonb,
    sqlc.arg('current_time')
FROM jobs
WHERE id = sqlc.arg(job_id);

-- name: AcquireEventSequenceLock :exec
SELECT pg_advisory_xact_lock(hashtextextended('ref0.event-log.sequence', 0));

-- name: InsertJobResourceEvent :exec
INSERT INTO event_log (
    event_type,
    resource_type,
    resource_id,
    snapshot,
    created_at
)
SELECT
    'job.' || lower(sqlc.arg(event_kind)::text),
    'job',
    id,
    jsonb_build_object(
        'id', id::text,
        'job_type', lower(job_type),
        'target_type', target_type,
        'target_id', target_id::text,
        'status', lower(status),
        'attempt_count', attempt_count,
        'max_attempts', max_attempts,
        'progress', progress,
        'lease_owner', lease_owner,
        'lease_expires_at', lease_expires_at,
        'lease_generation', lease_generation,
        'not_before', not_before,
        'result', result,
        'sanitized_error', sanitized_error,
        'created_at', created_at,
        'updated_at', updated_at,
        'started_at', started_at,
        'finished_at', finished_at
    ),
    sqlc.arg('current_time')
FROM jobs
WHERE id = sqlc.arg(job_id);

-- name: ListJobAttempts :many
SELECT *
FROM job_attempts
WHERE job_id = sqlc.arg(job_id)
ORDER BY attempt_number;

-- name: ListJobEvents :many
SELECT *
FROM job_events
WHERE job_id = sqlc.arg(job_id)
ORDER BY sequence;
