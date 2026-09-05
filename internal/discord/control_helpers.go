package discord

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const discordIdempotencyTTL = 24 * time.Hour

type discordRowScanner interface {
	Scan(...any) error
}

type discordRowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (store *Store) idempotencyRequest(
	actor ActorID,
	key, operation string,
	payload map[string]any,
) (idempotency.Request, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return idempotency.Request{}, errors.New("Discord request payload is invalid")
	}
	digests, err := store.vault.KeyedDigests([]byte(operation), raw)
	if err != nil {
		return idempotency.Request{}, err
	}
	if len(digests) == 0 || len(digests[0]) != 32 {
		return idempotency.Request{}, errors.New("Discord request digest is invalid")
	}
	request := idempotency.Request{
		Scope: "operator:" + actor.String(), Key: key, Operation: operation,
		TTL: discordIdempotencyTTL,
	}
	copy(request.Digest[:], digests[0])
	for _, rawDigest := range digests[1:] {
		if len(rawDigest) != 32 {
			return idempotency.Request{}, errors.New("Discord request digest is invalid")
		}
		var digest idempotency.Digest
		copy(digest[:], rawDigest)
		request.AcceptedDigests = append(request.AcceptedDigests, digest)
	}
	return request, nil
}

func discordClock(ctx context.Context, database discordRowQueryer) (time.Time, error) {
	var value pgtype.Timestamptz
	if err := database.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&value); err != nil || !value.Valid {
		if err == nil {
			err = errors.New("database clock did not return a timestamp")
		}
		return time.Time{}, err
	}
	return value.Time, nil
}

func newDiscordUUID() ([16]byte, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return [16]byte{}, err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return value, nil
}

func pgDiscordUUID(value [16]byte) pgtype.UUID {
	return pgtype.UUID{Bytes: value, Valid: true}
}

func recordDiscordChange(
	ctx context.Context,
	tx pgx.Tx,
	actor *ActorID,
	action, eventType, resourceType string,
	resourceID [16]byte,
	snapshot map[string]any,
) error {
	requestID, err := newDiscordUUID()
	if err != nil {
		return err
	}
	var actorID *[16]byte
	actorType := "system"
	if actor != nil {
		converted := [16]byte(*actor)
		actorID = &converted
		actorType = "operator"
	}
	targetID := resourceID
	if err = events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: actorType, ActorID: actorID, Action: action,
		TargetType: resourceType, TargetID: &targetID,
		RequestID: requestID, Details: snapshot,
	}); err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{
		Type: eventType, ResourceType: resourceType, ResourceID: resourceID,
		Snapshot: snapshot,
	})
}

func discordIntegrityConflict(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) &&
		(databaseError.Code == "23503" || databaseError.Code == "23505")
}

func credentialVersion(
	ctx context.Context,
	tx pgx.Tx,
	id [16]byte,
) (int32, error) {
	var kind string
	var version int32
	var deleted pgtype.Timestamptz
	err := tx.QueryRow(ctx, `
		SELECT kind, secret_version, deleted_at
		FROM credentials WHERE id=$1 FOR UPDATE
	`, pgDiscordUUID(id)).Scan(&kind, &version, &deleted)
	if errors.Is(err, pgx.ErrNoRows) || err == nil &&
		(kind != string(security.CredentialDiscordBotToken) || deleted.Valid || version <= 0) {
		return 0, ErrConflict
	}
	return version, err
}
