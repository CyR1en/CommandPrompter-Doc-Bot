package credentials

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const idempotencyTTL = 24 * time.Hour

type Service struct {
	pool  *pgxpool.Pool
	vault *security.CredentialVault
}

func NewService(pool *pgxpool.Pool, vault *security.CredentialVault) (*Service, error) {
	if pool == nil || vault == nil {
		return nil, errors.New("credential service dependencies are incomplete")
	}
	return &Service{pool: pool, vault: vault}, nil
}

func (service *Service) List(ctx context.Context) ([]Metadata, error) {
	rows, err := service.pool.Query(ctx, `
		SELECT id, kind, label, masked_value, secret_version, key_id,
		       created_at, rotated_at
		FROM credentials
		WHERE deleted_at IS NULL
		ORDER BY created_at, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Metadata, 0)
	for rows.Next() {
		value, scanErr := scanMetadata(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func (service *Service) Get(ctx context.Context, id ID) (Metadata, error) {
	return readMetadata(ctx, service.pool, id, false)
}

func (service *Service) Create(
	ctx context.Context,
	command CreateCommand,
	actor auth.OperatorID,
	requestKey string,
) (Metadata, error) {
	if err := ValidateCreate(command); err != nil {
		return Metadata{}, err
	}
	digests, err := service.vault.KeyedDigests(
		[]byte("credential.create"),
		[]byte(command.Kind),
		[]byte(command.Label),
		[]byte(command.Secret.Reveal()),
	)
	if err != nil {
		return Metadata{}, err
	}
	request, err := digestRequest(actor, requestKey, "credential.create", digests)
	if err != nil {
		return Metadata{}, err
	}
	return service.execute(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		now, clockErr := databaseClock(ctx, tx)
		if clockErr != nil {
			return idempotency.Result{}, clockErr
		}
		id, idErr := newID()
		if idErr != nil {
			return idempotency.Result{}, idErr
		}
		envelope, encryptErr := service.vault.Encrypt(
			security.CredentialID(id), command.Kind, 1, command.Secret,
		)
		if encryptErr != nil {
			return idempotency.Result{}, encryptErr
		}
		if _, insertErr := tx.Exec(ctx, `
			INSERT INTO credentials (
				id, kind, label, masked_value, key_id, nonce, ciphertext,
				secret_version, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8)
		`, pgUUID(id), string(command.Kind), command.Label, MaskedValue,
			envelope.KeyID(), envelope.Nonce(), envelope.Ciphertext(), now,
		); insertErr != nil {
			return idempotency.Result{}, insertErr
		}
		metadata, readErr := readMetadata(ctx, tx, id, false)
		if readErr != nil {
			return idempotency.Result{}, readErr
		}
		if recordErr := recordChange(ctx, tx, metadata, &actor, "credential.create", "credential.created"); recordErr != nil {
			return idempotency.Result{}, recordErr
		}
		return idempotency.Result{Type: "credential:1", ID: [16]byte(id)}, nil
	})
}

func (service *Service) Rotate(
	ctx context.Context,
	command RotateCommand,
	actor auth.OperatorID,
	requestKey string,
) (Metadata, error) {
	if command.Secret == nil {
		return Metadata{}, security.ErrInvalidSecret
	}
	digests, err := service.vault.KeyedDigests(
		[]byte("credential.rotate"),
		command.CredentialID[:],
		[]byte(command.Secret.Reveal()),
	)
	if err != nil {
		return Metadata{}, err
	}
	request, err := digestRequest(actor, requestKey, "credential.rotate", digests)
	if err != nil {
		return Metadata{}, err
	}
	return service.execute(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		var storedKind string
		var oldVersion int32
		err := tx.QueryRow(ctx, `
			SELECT kind, secret_version
			FROM credentials
			WHERE id = $1 AND deleted_at IS NULL
			FOR UPDATE
		`, pgUUID(command.CredentialID)).Scan(&storedKind, &oldVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return idempotency.Result{}, ErrNotFound
		}
		if err != nil {
			return idempotency.Result{}, err
		}
		kind := Kind(storedKind)
		if err = security.ValidateCredentialSecret(kind, command.Secret); err != nil {
			return idempotency.Result{}, err
		}
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return idempotency.Result{}, err
		}
		newVersion := oldVersion + 1
		envelope, err := service.vault.Encrypt(
			security.CredentialID(command.CredentialID), kind, int64(newVersion), command.Secret,
		)
		if err != nil {
			return idempotency.Result{}, err
		}
		if _, err = tx.Exec(ctx, `
			UPDATE credentials
			SET key_id = $2, nonce = $3, ciphertext = $4,
			    secret_version = $5, rotated_at = $6
			WHERE id = $1
		`, pgUUID(command.CredentialID), envelope.KeyID(), envelope.Nonce(),
			envelope.Ciphertext(), newVersion, now,
		); err != nil {
			return idempotency.Result{}, err
		}
		attemptID, err := newID()
		if err != nil {
			return idempotency.Result{}, err
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO credential_rotation_attempts (
				id, credential_id, old_secret_version, new_secret_version,
				new_key_id, status, actor_operator_id, started_at, finished_at
			) VALUES ($1, $2, $3, $4, $5, 'SUCCEEDED', $6, $7, $7)
		`, pgUUID(attemptID), pgUUID(command.CredentialID), oldVersion,
			newVersion, envelope.KeyID(), pgUUID(ID(actor)), now,
		); err != nil {
			return idempotency.Result{}, err
		}
		metadata, err := readMetadata(ctx, tx, command.CredentialID, false)
		if err != nil {
			return idempotency.Result{}, err
		}
		if err = recordChange(ctx, tx, metadata, &actor, "credential.rotate", "credential.rotated"); err != nil {
			return idempotency.Result{}, err
		}
		return idempotency.Result{
			Type: fmt.Sprintf("credential:%d", newVersion),
			ID:   [16]byte(command.CredentialID),
		}, nil
	})
}

func (service *Service) execute(
	ctx context.Context,
	request idempotency.Request,
	operation idempotency.Operation,
) (Metadata, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Metadata{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := idempotency.Execute(ctx, tx, request, operation)
	if err != nil {
		return Metadata{}, err
	}
	metadata, err := metadataForResult(ctx, tx, result)
	if err != nil {
		return Metadata{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func metadataForResult(ctx context.Context, database rowQueryer, result idempotency.Result) (Metadata, error) {
	resourceType, versionText, found := strings.Cut(result.Type, ":")
	if !found || resourceType != "credential" {
		return Metadata{}, idempotency.ErrConflict
	}
	parsed, err := strconv.ParseInt(versionText, 10, 32)
	expected := int32(parsed)
	if err != nil || expected <= 0 || strconv.FormatInt(parsed, 10) != versionText {
		return Metadata{}, idempotency.ErrConflict
	}
	metadata, err := readMetadata(ctx, database, ID(result.ID), false)
	if errors.Is(err, ErrNotFound) {
		return Metadata{}, idempotency.ErrConflict
	}
	if err != nil {
		return Metadata{}, err
	}
	if metadata.SecretVersion != expected {
		return Metadata{}, idempotency.ErrConflict
	}
	return metadata, nil
}

func digestRequest(actor auth.OperatorID, key, operation string, values [][]byte) (idempotency.Request, error) {
	if len(values) == 0 {
		return idempotency.Request{}, errors.New("credential digester returned no values")
	}
	request := idempotency.Request{
		Scope:     "operator:" + actor.String(),
		Key:       key,
		Operation: operation,
		TTL:       idempotencyTTL,
	}
	copy(request.Digest[:], values[0])
	request.AcceptedDigests = make([]idempotency.Digest, 0, len(values)-1)
	for _, value := range values[1:] {
		if len(value) != len(request.Digest) {
			return idempotency.Request{}, errors.New("credential digest is invalid")
		}
		var digest idempotency.Digest
		copy(digest[:], value)
		request.AcceptedDigests = append(request.AcceptedDigests, digest)
	}
	if len(values[0]) != len(request.Digest) {
		return idempotency.Request{}, errors.New("credential digest is invalid")
	}
	return request, nil
}

type rowScanner interface {
	Scan(...any) error
}

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readMetadata(ctx context.Context, database rowQueryer, id ID, includeDeleted bool) (Metadata, error) {
	query := `
		SELECT id, kind, label, masked_value, secret_version, key_id,
		       created_at, rotated_at
		FROM credentials
		WHERE id = $1 AND deleted_at IS NULL
	`
	if includeDeleted {
		query = `
			SELECT id, kind, label, masked_value, secret_version, key_id,
			       created_at, rotated_at
			FROM credentials WHERE id = $1
		`
	}
	metadata, err := scanMetadata(database.QueryRow(ctx, query, pgUUID(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Metadata{}, ErrNotFound
	}
	return metadata, err
}

func scanMetadata(row rowScanner) (Metadata, error) {
	var id pgtype.UUID
	var kind, label, maskedValue, keyID string
	var secretVersion int32
	var createdAt pgtype.Timestamptz
	var rotatedAt pgtype.Timestamptz
	if err := row.Scan(&id, &kind, &label, &maskedValue, &secretVersion, &keyID, &createdAt, &rotatedAt); err != nil {
		return Metadata{}, err
	}
	if !id.Valid || !createdAt.Valid {
		return Metadata{}, errors.New("stored credential metadata is invalid")
	}
	value := Metadata{
		ID: ID(id.Bytes), Kind: Kind(kind), Label: label, MaskedValue: maskedValue,
		SecretVersion: secretVersion, KeyID: keyID, CreatedAt: createdAt.Time,
	}
	if rotatedAt.Valid {
		rotated := rotatedAt.Time
		value.RotatedAt = &rotated
	}
	if err := validateMetadata(value); err != nil {
		return Metadata{}, err
	}
	return value, nil
}

func databaseClock(ctx context.Context, database rowQueryer) (time.Time, error) {
	var value pgtype.Timestamptz
	if err := database.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&value); err != nil || !value.Valid {
		if err == nil {
			err = errors.New("database clock did not return a timestamp")
		}
		return time.Time{}, err
	}
	return value.Time, nil
}

func recordChange(
	ctx context.Context,
	tx pgx.Tx,
	metadata Metadata,
	actor *auth.OperatorID,
	action string,
	eventType string,
) error {
	snapshot := metadataSnapshot(metadata)
	requestID, err := newID()
	if err != nil {
		return err
	}
	targetID := [16]byte(metadata.ID)
	var actorID *[16]byte
	actorType := "system"
	if actor != nil {
		value := [16]byte(*actor)
		actorID = &value
		actorType = "operator"
	}
	if err = events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: actorType, ActorID: actorID, Action: action,
		TargetType: "credential", TargetID: &targetID,
		RequestID: [16]byte(requestID), Details: snapshot,
	}); err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{
		Type: eventType, ResourceType: "credential",
		ResourceID: targetID, Snapshot: snapshot,
	})
}

func metadataSnapshot(value Metadata) map[string]any {
	var rotatedAt any
	if value.RotatedAt != nil {
		rotatedAt = pythonISO(*value.RotatedAt)
	}
	return map[string]any{
		"id": value.ID.String(), "kind": strings.ToLower(string(value.Kind)),
		"label": value.Label, "masked_value": value.MaskedValue,
		"secret_version": value.SecretVersion, "key_id": value.KeyID,
		"created_at": pythonISO(value.CreatedAt), "rotated_at": rotatedAt,
	}
}

func pythonISO(value time.Time) string {
	base := value.Format("2006-01-02T15:04:05")
	if microseconds := value.Nanosecond() / 1000; microseconds != 0 {
		base += fmt.Sprintf(".%06d", microseconds)
	}
	_, offset := value.Zone()
	sign := '+'
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	return fmt.Sprintf("%s%c%02d:%02d", base, sign, offset/3600, offset%3600/60)
}

func pgUUID(id ID) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte(id), Valid: true}
}

func newID() (ID, error) {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		return ID{}, err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}
