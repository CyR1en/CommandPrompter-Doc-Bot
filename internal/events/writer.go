package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const eventSequenceLock = "ref0.event-log.sequence"

type ResourceEvent struct {
	Type         string
	ResourceType string
	ResourceID   [16]byte
	Snapshot     map[string]any
}

// Append writes an event through the caller's transaction. The advisory lock
// makes sequence order equal commit order, while rollback removes the event and
// releases the lock with the rest of the transaction.
func Append(ctx context.Context, tx pgx.Tx, event ResourceEvent) error {
	if tx == nil || !validLabel(event.Type, 128) || !validLabel(event.ResourceType, 64) || event.Snapshot == nil {
		return ErrInvalidEvent
	}
	encoded, err := json.Marshal(event.Snapshot)
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
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, eventSequenceLock); err != nil {
		return fmt.Errorf("acquire event sequence lock: %w", err)
	}
	resourceID := pgtype.UUID{Bytes: event.ResourceID, Valid: true}
	if _, err = tx.Exec(ctx, `
		INSERT INTO event_log (
			event_type, resource_type, resource_id, snapshot, created_at
		) VALUES ($1, $2, $3, $4::jsonb, clock_timestamp())
	`, event.Type, event.ResourceType, resourceID, string(encoded)); err != nil {
		return fmt.Errorf("append resource event: %w", err)
	}
	return nil
}

func validLabel(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= maximum &&
		!strings.ContainsAny(value, "\x00\r\n")
}
