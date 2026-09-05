package knowledgebases

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const idempotencyTTL = 24 * time.Hour

type ArtifactPurger interface {
	Purge(context.Context, ID, []ID) error
}

type Service struct {
	pool           *pgxpool.Pool
	vault          *security.CredentialVault
	deleteGrace    time.Duration
	artifactPurger ArtifactPurger
	jobs           *jobs.Store
}

func NewService(
	pool *pgxpool.Pool,
	vault *security.CredentialVault,
	deleteGrace time.Duration,
	artifactPurger ArtifactPurger,
) (*Service, error) {
	if pool == nil || vault == nil {
		return nil, errors.New("knowledge base service dependencies are incomplete")
	}
	if deleteGrace <= 0 {
		return nil, errors.New("delete_grace must be positive")
	}
	return &Service{
		pool: pool, vault: vault, deleteGrace: deleteGrace,
		artifactPurger: artifactPurger, jobs: jobs.NewStore(pool, nil),
	}, nil
}

func (service *Service) List(ctx context.Context) ([]KnowledgeBase, error) {
	rows, err := service.pool.Query(ctx, `
		SELECT id, name, name_key, access_policy, lifecycle, instructions,
		       language, published_wiki_id, archived_at, delete_requested_at,
		       purge_after, deleted_at, created_at, updated_at, version
		FROM knowledge_bases
		WHERE deleted_at IS NULL
		ORDER BY created_at, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]KnowledgeBase, 0)
	for rows.Next() {
		row, scanErr := scanDatabaseRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, row.value())
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func (service *Service) Get(ctx context.Context, id ID) (KnowledgeBase, error) {
	row, err := readRow(ctx, service.pool, id, false, false)
	if err != nil {
		return KnowledgeBase{}, err
	}
	return row.value(), nil
}

func (service *Service) Create(
	ctx context.Context,
	command CreateCommand,
	actor auth.OperatorID,
	requestKey string,
) (KnowledgeBase, error) {
	if err := ValidateCreate(command); err != nil {
		return KnowledgeBase{}, err
	}
	request, err := service.request(actor, requestKey, "knowledge_base.create",
		[]byte("knowledge_base.create"),
		[]byte(command.Name.Display),
		[]byte(command.Access),
		[]byte(command.Instructions),
		[]byte(command.Language),
	)
	if err != nil {
		return KnowledgeBase{}, err
	}
	value, err := service.executeMetadata(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		id, err := newID()
		if err != nil {
			return idempotency.Result{}, err
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO knowledge_bases (
				id, name, name_key, access_policy, lifecycle, instructions,
				language, created_at, updated_at, version
			) VALUES ($1, $2, $3, $4, 'ACTIVE', $5, $6, $7, $7, 1)
		`, pgUUID(id), command.Name.Display, command.Name.Key,
			string(command.Access), command.Instructions, command.Language, now,
		); err != nil {
			return idempotency.Result{}, err
		}
		row, err := readRow(ctx, tx, id, false, false)
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = recordChange(ctx, tx, row.value(), &actor, "knowledge_base.create", "knowledge_base.created"); err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{Type: "knowledge_base:1", ID: [16]byte(id)}, nil
	})
	if isUniqueViolation(err) {
		return KnowledgeBase{}, conflict("knowledge base name already exists")
	}
	return value, err
}

func (service *Service) Update(
	ctx context.Context,
	command UpdateCommand,
	actor auth.OperatorID,
	requestKey string,
) (KnowledgeBase, error) {
	if err := ValidateUpdate(command); err != nil {
		return KnowledgeBase{}, err
	}
	request, err := service.request(actor, requestKey, "knowledge_base.update",
		[]byte("knowledge_base.update"), command.KnowledgeBaseID[:],
		versionBytes(command.ExpectedVersion),
		optionalName(command.Name), optionalAccess(command.Access),
		optionalString(command.Instructions), optionalString(command.Language),
		optionalLifecycle(command.Lifecycle),
	)
	if err != nil {
		return KnowledgeBase{}, err
	}
	value, err := service.executeMetadata(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		row, err := readRow(ctx, tx, command.KnowledgeBaseID, false, true)
		if err != nil {
			return idempotency.Result{}, err
		}
		if row.version != command.ExpectedVersion {
			return idempotency.Result{}, conflict("knowledge base version is stale")
		}
		current := Lifecycle(row.lifecycle)
		if current != Active && current != Archived {
			return idempotency.Result{}, transition("knowledge base cannot be updated")
		}
		target := current
		if command.Lifecycle != nil {
			target = *command.Lifecycle
		}
		if _, err = Transition(current, target); err != nil {
			return idempotency.Result{}, err
		}
		if command.Access != nil && *command.Access == Public && Access(row.access) != Public {
			var privateSourceExists bool
			if err = tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM sources
					WHERE knowledge_base_id = $1
					  AND privacy = 'PRIVATE' AND lifecycle <> 'REMOVED'
				)
			`, pgUUID(command.KnowledgeBaseID)).Scan(&privateSourceExists); err != nil {
				return idempotency.Result{}, err
			}
			if privateSourceExists {
				return idempotency.Result{}, conflict("private sources require a restricted knowledge base")
			}
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		name, nameKey := row.name, row.nameKey
		if command.Name != nil {
			name, nameKey = command.Name.Display, command.Name.Key
		}
		access := Access(row.access)
		if command.Access != nil {
			access = *command.Access
		}
		instructions := row.instructions
		if command.Instructions != nil {
			instructions = *command.Instructions
		}
		language := row.language
		if command.Language != nil {
			language = *command.Language
		}
		archivedAt := timePointer(row.archivedAt)
		if target == Active {
			archivedAt = nil
		} else if current == Active {
			archivedAt = &now
		}
		version := row.version + 1
		if _, err = tx.Exec(ctx, `
			UPDATE knowledge_bases
			SET name = $2, name_key = $3, access_policy = $4,
			    lifecycle = $5, instructions = $6, language = $7,
			    archived_at = $8, updated_at = $9, version = $10
			WHERE id = $1
		`, pgUUID(command.KnowledgeBaseID), name, nameKey, string(access),
			string(target), instructions, language, archivedAt, now, version,
		); err != nil {
			return idempotency.Result{}, err
		}
		updated, err := readRow(ctx, tx, command.KnowledgeBaseID, false, false)
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = recordChange(ctx, tx, updated.value(), &actor, "knowledge_base.update", "knowledge_base.updated"); err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{
			Type: fmt.Sprintf("knowledge_base:%d", version),
			ID:   [16]byte(command.KnowledgeBaseID),
		}, nil
	})
	if isUniqueViolation(err) {
		return KnowledgeBase{}, conflict("knowledge base name already exists")
	}
	return value, err
}

func (service *Service) RequestDelete(
	ctx context.Context,
	command DeleteCommand,
	actor auth.OperatorID,
	requestKey string,
) (Deletion, error) {
	if err := ValidateDelete(command); err != nil {
		return Deletion{}, err
	}
	request, err := service.request(actor, requestKey, "knowledge_base.delete",
		[]byte("knowledge_base.delete"), command.KnowledgeBaseID[:],
		versionBytes(command.ExpectedVersion), []byte(command.ConfirmationName),
	)
	if err != nil {
		return Deletion{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Deletion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		row, err := readRow(ctx, tx, command.KnowledgeBaseID, false, true)
		if err != nil {
			return idempotency.Result{}, err
		}
		if row.version != command.ExpectedVersion {
			return idempotency.Result{}, conflict("knowledge base version is stale")
		}
		if _, err = Transition(Lifecycle(row.lifecycle), PendingDelete); err != nil {
			return idempotency.Result{}, err
		}
		if command.ConfirmationName != row.name {
			return idempotency.Result{}, confirmation()
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		purgeAfter := now.Add(service.deleteGrace)
		version := row.version + 1
		if _, err = tx.Exec(ctx, `
			UPDATE knowledge_bases
			SET lifecycle = 'PENDING_DELETE', delete_requested_at = $2,
			    purge_after = $3, updated_at = $2, version = $4
			WHERE id = $1
		`, pgUUID(command.KnowledgeBaseID), now, purgeAfter, version); err != nil {
			return idempotency.Result{}, err
		}
		jobID, err := service.jobs.EnqueueTxAt(ctx, tx, jobs.Command{
			Type: jobs.PurgeKnowledgeBase, TargetType: "knowledge_base",
			TargetID: jobs.UUID(command.KnowledgeBaseID), Payload: map[string]any{},
			OperationKey: purgeOperationKey(command.KnowledgeBaseID),
			NotBefore:    &purgeAfter,
		}, now)
		if err != nil {
			return idempotency.Result{}, err
		}
		updated, err := readRow(ctx, tx, command.KnowledgeBaseID, false, false)
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = recordChange(ctx, tx, updated.value(), &actor, "knowledge_base.delete_requested", "knowledge_base.pending_delete"); err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{
			Type: fmt.Sprintf("knowledge_base_delete:%d", version),
			ID:   [16]byte(jobID),
		}, nil
	})
	if err != nil {
		return Deletion{}, err
	}
	deletion, err := deletionForResult(ctx, tx, command.KnowledgeBaseID, result)
	if err != nil {
		return Deletion{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Deletion{}, err
	}
	return deletion, nil
}

func (service *Service) Restore(
	ctx context.Context,
	command RestoreCommand,
	actor auth.OperatorID,
	requestKey string,
) (KnowledgeBase, error) {
	if err := ValidateRestore(command); err != nil {
		return KnowledgeBase{}, err
	}
	request, err := service.request(actor, requestKey, "knowledge_base.restore",
		[]byte("knowledge_base.restore"), command.KnowledgeBaseID[:],
		versionBytes(command.ExpectedVersion),
	)
	if err != nil {
		return KnowledgeBase{}, err
	}
	return service.executeMetadata(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		row, err := readRow(ctx, tx, command.KnowledgeBaseID, false, true)
		if err != nil {
			return idempotency.Result{}, err
		}
		if row.version != command.ExpectedVersion {
			return idempotency.Result{}, conflict("knowledge base version is stale")
		}
		if Lifecycle(row.lifecycle) != PendingDelete {
			return idempotency.Result{}, transition("knowledge base is not pending deletion")
		}
		var purgeStarted bool
		if err = tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM artifact_deletion_intents
				WHERE kind='KNOWLEDGE_BASE' AND resource_id=$1
			)
		`, pgUUID(command.KnowledgeBaseID)).Scan(&purgeStarted); err != nil {
			return idempotency.Result{}, err
		}
		if purgeStarted {
			return idempotency.Result{}, conflict("knowledge base purge has started")
		}
		var jobID pgtype.UUID
		var jobStatus string
		err = tx.QueryRow(ctx, `
			SELECT id, status FROM jobs
			WHERE operation_key = $1
			ORDER BY created_at DESC, id DESC
			LIMIT 1
			FOR UPDATE
		`, purgeOperationKey(command.KnowledgeBaseID)).Scan(&jobID, &jobStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return idempotency.Result{}, conflict("purge job is unavailable")
		}
		if err != nil {
			return idempotency.Result{}, err
		}
		if !jobID.Valid {
			return idempotency.Result{}, conflict("purge job is unavailable")
		}
		status := jobs.Status(jobStatus)
		if status != jobs.Cancelled && status != jobs.Failed {
			status, err = service.jobs.RequestCancelTx(ctx, tx, jobs.JobID(jobID.Bytes))
			if err != nil || status != jobs.Cancelled {
				if err != nil && !errors.Is(err, jobs.ErrJobConflict) {
					return idempotency.Result{}, err
				}
				return idempotency.Result{}, conflict("active purge job cannot be cancelled")
			}
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		if !row.purgeAfter.Valid || !now.Before(row.purgeAfter.Time) {
			return idempotency.Result{}, conflict("knowledge base restore period ended")
		}
		archivedAt := timePointer(row.archivedAt)
		target := RestoreLifecycle(archivedAt)
		if _, err = Transition(PendingDelete, target); err != nil {
			return idempotency.Result{}, err
		}
		if target == Active {
			archivedAt = nil
		}
		version := row.version + 1
		if _, err = tx.Exec(ctx, `
			UPDATE knowledge_bases
			SET lifecycle = $2, archived_at = $3, delete_requested_at = NULL,
			    purge_after = NULL, updated_at = $4, version = $5
			WHERE id = $1
		`, pgUUID(command.KnowledgeBaseID), string(target), archivedAt, now, version); err != nil {
			return idempotency.Result{}, err
		}
		updated, err := readRow(ctx, tx, command.KnowledgeBaseID, false, false)
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = recordChange(ctx, tx, updated.value(), &actor, "knowledge_base.restore", "knowledge_base.restored"); err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{
			Type: fmt.Sprintf("knowledge_base:%d", version),
			ID:   [16]byte(command.KnowledgeBaseID),
		}, nil
	})
}

func (service *Service) Purge(
	ctx context.Context,
	id ID,
	permit jobs.Permit,
) (KnowledgeBase, error) {
	request, err := service.purgeRequest(id, permit)
	if err != nil {
		return KnowledgeBase{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return KnowledgeBase{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := readRow(ctx, tx, id, true, true)
	if err != nil {
		return KnowledgeBase{}, err
	}
	lifecycle := Lifecycle(row.lifecycle)
	if lifecycle != PendingDelete && lifecycle != Deleted {
		return KnowledgeBase{}, transition("knowledge base is not pending deletion")
	}
	if err = service.assertPurgePermit(ctx, tx, id, permit); err != nil {
		return KnowledgeBase{}, err
	}
	if lifecycle == Deleted {
		result, executeErr := idempotency.Execute(ctx, tx, request, func(context.Context, pgx.Tx) (idempotency.Result, error) {
			return idempotency.Result{
				Type: fmt.Sprintf("knowledge_base:%d", row.version), ID: [16]byte(id),
			}, nil
		})
		if executeErr != nil {
			return KnowledgeBase{}, executeErr
		}
		value, metadataErr := metadataForResult(ctx, tx, result)
		if metadataErr != nil {
			return KnowledgeBase{}, metadataErr
		}
		if err = tx.Commit(ctx); err != nil {
			return KnowledgeBase{}, err
		}
		return value, nil
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return KnowledgeBase{}, err
	}
	if !row.purgeAfter.Valid || now.Before(row.purgeAfter.Time) {
		return KnowledgeBase{}, purgeNotReady()
	}
	sourceIDs, err := readSourceIDs(ctx, tx, id)
	if err != nil {
		return KnowledgeBase{}, err
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO artifact_deletion_intents(kind,resource_id,owner_id,scope_id)
		VALUES('KNOWLEDGE_BASE',$1,$1,$1)
		ON CONFLICT(kind,resource_id) DO NOTHING
	`, pgUUID(id)); err != nil {
		return KnowledgeBase{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return KnowledgeBase{}, err
	}
	if service.artifactPurger != nil {
		if err = service.artifactPurger.Purge(ctx, id, sourceIDs); err != nil {
			return KnowledgeBase{}, err
		}
	}
	return service.finalizePurge(ctx, id, permit, request)
}

func (service *Service) assertPurgePermit(ctx context.Context, tx pgx.Tx, id ID, permit jobs.Permit) error {
	if err := service.jobs.AssertPermit(ctx, tx, permit); err != nil {
		return err
	}
	var jobType, targetType, operationKey string
	var targetID pgtype.UUID
	if err := tx.QueryRow(ctx, `
		SELECT job_type, target_type, target_id, operation_key
		FROM jobs WHERE id = $1
	`, pgtype.UUID{Bytes: [16]byte(permit.JobID), Valid: true}).Scan(
		&jobType, &targetType, &targetID, &operationKey,
	); err != nil {
		return err
	}
	if !targetID.Valid || jobs.Type(jobType) != jobs.PurgeKnowledgeBase ||
		targetType != "knowledge_base" || targetID.Bytes != [16]byte(id) ||
		operationKey != purgeOperationKey(id) {
		return conflict("purge permit target is invalid")
	}
	return nil
}

func (service *Service) finalizePurge(
	ctx context.Context,
	id ID,
	permit jobs.Permit,
	request idempotency.Request,
) (KnowledgeBase, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return KnowledgeBase{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := readRow(ctx, tx, id, true, true)
	if err != nil {
		return KnowledgeBase{}, err
	}
	lifecycle := Lifecycle(row.lifecycle)
	if lifecycle != PendingDelete && lifecycle != Deleted {
		return KnowledgeBase{}, transition("knowledge base is not pending deletion")
	}
	if err = service.assertPurgePermit(ctx, tx, id, permit); err != nil {
		return KnowledgeBase{}, err
	}
	result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		if lifecycle == Deleted {
			return idempotency.Result{Type: fmt.Sprintf("knowledge_base:%d", row.version), ID: [16]byte(id)}, nil
		}
		var intentExists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM artifact_deletion_intents
				WHERE kind='KNOWLEDGE_BASE' AND resource_id=$1 AND owner_id=$1 AND scope_id=$1
			)
		`, pgUUID(id)).Scan(&intentExists); err != nil {
			return idempotency.Result{}, err
		}
		if !intentExists {
			return idempotency.Result{}, conflict("purge deletion intent is unavailable")
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		version := row.version + 1
		if _, err = tx.Exec(ctx, `
			UPDATE knowledge_bases
			SET lifecycle='DELETED',deleted_at=$2,updated_at=$2,version=$3
			WHERE id=$1
		`, pgUUID(id), now, version); err != nil {
			return idempotency.Result{}, err
		}
		updated, err := readRow(ctx, tx, id, true, false)
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = recordChange(ctx, tx, updated.value(), nil, "knowledge_base.purge", "knowledge_base.deleted"); err != nil {
			return idempotency.Result{}, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM artifact_deletion_intents WHERE scope_id=$1`, pgUUID(id)); err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{Type: fmt.Sprintf("knowledge_base:%d", version), ID: [16]byte(id)}, nil
	})
	if err != nil {
		return KnowledgeBase{}, err
	}
	value, err := metadataForResult(ctx, tx, result)
	if err != nil {
		return KnowledgeBase{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return KnowledgeBase{}, err
	}
	return value, nil
}

func (service *Service) executeMetadata(
	ctx context.Context,
	request idempotency.Request,
	operation idempotency.Operation,
) (KnowledgeBase, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return KnowledgeBase{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := idempotency.Execute(ctx, tx, request, operation)
	if err != nil {
		return KnowledgeBase{}, err
	}
	value, err := metadataForResult(ctx, tx, result)
	if err != nil {
		return KnowledgeBase{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return KnowledgeBase{}, err
	}
	return value, nil
}

func metadataForResult(ctx context.Context, database rowQueryer, result idempotency.Result) (KnowledgeBase, error) {
	version, ok := resultVersion(result.Type, "knowledge_base")
	if !ok {
		return KnowledgeBase{}, idempotency.ErrConflict
	}
	row, err := readRow(ctx, database, ID(result.ID), true, false)
	if errors.Is(err, ErrNotFound) {
		return KnowledgeBase{}, idempotency.ErrConflict
	}
	if err != nil {
		return KnowledgeBase{}, err
	}
	if row.version != version {
		return KnowledgeBase{}, idempotency.ErrConflict
	}
	return row.value(), nil
}

func deletionForResult(
	ctx context.Context,
	database rowQueryer,
	id ID,
	result idempotency.Result,
) (Deletion, error) {
	version, ok := resultVersion(result.Type, "knowledge_base_delete")
	if !ok {
		return Deletion{}, idempotency.ErrConflict
	}
	row, err := readRow(ctx, database, id, true, false)
	if err != nil || row.version != version {
		if err == nil || errors.Is(err, ErrNotFound) {
			return Deletion{}, idempotency.ErrConflict
		}
		return Deletion{}, err
	}
	var operationKey string
	err = database.QueryRow(ctx, `SELECT operation_key FROM jobs WHERE id = $1`,
		pgtype.UUID{Bytes: result.ID, Valid: true},
	).Scan(&operationKey)
	if err != nil || operationKey != purgeOperationKey(id) {
		if err == nil || errors.Is(err, pgx.ErrNoRows) {
			return Deletion{}, idempotency.ErrConflict
		}
		return Deletion{}, err
	}
	return Deletion{KnowledgeBase: row.value(), PurgeJobID: jobs.JobID(result.ID)}, nil
}

func resultVersion(value, expectedType string) (int32, bool) {
	resourceType, versionText, found := strings.Cut(value, ":")
	if !found || resourceType != expectedType {
		return 0, false
	}
	parsed, err := strconv.ParseInt(versionText, 10, 32)
	return int32(parsed), err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == versionText
}

func (service *Service) request(
	actor auth.OperatorID,
	key string,
	operation string,
	parts ...[]byte,
) (idempotency.Request, error) {
	digests, err := service.vault.KeyedDigests(parts...)
	if err != nil {
		return idempotency.Request{}, err
	}
	return makeRequest("operator:"+actor.String(), key, operation, digests)
}

func (service *Service) purgeRequest(id ID, permit jobs.Permit) (idempotency.Request, error) {
	digests, err := service.vault.KeyedDigests(
		[]byte("knowledge_base.purge"), id[:], permit.JobID[:],
	)
	if err != nil {
		return idempotency.Request{}, err
	}
	return makeRequest("job:"+permit.JobID.String(), "purge-knowledge-base", "knowledge_base.purge", digests)
}

func makeRequest(scope, key, operation string, values [][]byte) (idempotency.Request, error) {
	if len(values) == 0 || len(values[0]) != 32 {
		return idempotency.Request{}, errors.New("knowledge base digest is invalid")
	}
	request := idempotency.Request{Scope: scope, Key: key, Operation: operation, TTL: idempotencyTTL}
	copy(request.Digest[:], values[0])
	for _, value := range values[1:] {
		if len(value) != 32 {
			return idempotency.Request{}, errors.New("knowledge base digest is invalid")
		}
		var digest idempotency.Digest
		copy(digest[:], value)
		request.AcceptedDigests = append(request.AcceptedDigests, digest)
	}
	return request, nil
}

func readSourceIDs(ctx context.Context, tx pgx.Tx, id ID) ([]ID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM sources WHERE knowledge_base_id = $1`, pgUUID(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]ID, 0)
	for rows.Next() {
		var sourceID pgtype.UUID
		if err = rows.Scan(&sourceID); err != nil || !sourceID.Valid {
			if err == nil {
				err = errors.New("stored source ID is invalid")
			}
			return nil, err
		}
		values = append(values, ID(sourceID.Bytes))
	}
	return values, rows.Err()
}

func versionBytes(value int32) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, uint64(value))
	return encoded
}

func optionalString(value *string) []byte {
	if value == nil {
		return []byte{0}
	}
	return append([]byte{1}, []byte(*value)...)
}

func optionalName(value *Name) []byte {
	if value == nil {
		return []byte{0}
	}
	return append([]byte{1}, []byte(value.Display)...)
}

func optionalAccess(value *Access) []byte {
	if value == nil {
		return []byte{0}
	}
	return append([]byte{1}, []byte(*value)...)
}

func optionalLifecycle(value *Lifecycle) []byte {
	if value == nil {
		return []byte{0}
	}
	return append([]byte{1}, []byte(*value)...)
}

func purgeOperationKey(id ID) string { return "purge-knowledge-base:" + id.String() }
