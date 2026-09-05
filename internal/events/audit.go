package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type AuditEvent struct {
	ActorType  string
	ActorID    *[16]byte
	Action     string
	TargetType string
	TargetID   *[16]byte
	RequestID  [16]byte
	Details    map[string]any
}

// AppendAudit writes a sanitized audit object through the caller's transaction.
func AppendAudit(ctx context.Context, tx pgx.Tx, event AuditEvent) error {
	if tx == nil ||
		!validLabel(event.ActorType, 32) ||
		!validLabel(event.Action, 128) ||
		!validLabel(event.TargetType, 64) ||
		event.Details == nil {
		return ErrInvalidEvent
	}
	encoded, err := json.Marshal(event.Details)
	if err != nil {
		return ErrInvalidEvent
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized map[string]any
	if err = decoder.Decode(&normalized); err != nil || normalized == nil {
		return ErrInvalidEvent
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO audit_events (
			actor_type, actor_id, action, target_type, target_id,
			request_id, details, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, clock_timestamp())
	`,
		event.ActorType,
		nullableUUID(event.ActorID),
		event.Action,
		event.TargetType,
		nullableUUID(event.TargetID),
		pgtype.UUID{Bytes: event.RequestID, Valid: true},
		string(encoded),
	); err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

func nullableUUID(value *[16]byte) pgtype.UUID {
	if value == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *value, Valid: true}
}
