package knowledgebases

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type databaseRow struct {
	id                pgtype.UUID
	name              string
	nameKey           string
	access            string
	lifecycle         string
	instructions      string
	language          string
	publishedWikiID   pgtype.UUID
	archivedAt        pgtype.Timestamptz
	deleteRequestedAt pgtype.Timestamptz
	purgeAfter        pgtype.Timestamptz
	deletedAt         pgtype.Timestamptz
	createdAt         pgtype.Timestamptz
	updatedAt         pgtype.Timestamptz
	version           int32
}

type rowScanner interface {
	Scan(...any) error
}

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanDatabaseRow(scanner rowScanner) (databaseRow, error) {
	var row databaseRow
	err := scanner.Scan(
		&row.id, &row.name, &row.nameKey, &row.access, &row.lifecycle,
		&row.instructions, &row.language, &row.publishedWikiID,
		&row.archivedAt, &row.deleteRequestedAt, &row.purgeAfter,
		&row.deletedAt, &row.createdAt, &row.updatedAt, &row.version,
	)
	if err != nil {
		return databaseRow{}, err
	}
	if !row.id.Valid || !row.createdAt.Valid || !row.updatedAt.Valid ||
		row.version <= 0 || !validAccess(Access(row.access)) || !validLifecycle(Lifecycle(row.lifecycle)) {
		return databaseRow{}, errors.New("stored knowledge base is invalid")
	}
	return row, nil
}

func (row databaseRow) value() KnowledgeBase {
	value := KnowledgeBase{
		ID: ID(row.id.Bytes), Name: row.name, Access: Access(row.access),
		Lifecycle: Lifecycle(row.lifecycle), Instructions: row.instructions,
		Language: row.language, CreatedAt: row.createdAt.Time,
		UpdatedAt: row.updatedAt.Time, Version: row.version,
	}
	value.PublishedWikiID = uuidPointer(row.publishedWikiID)
	value.ArchivedAt = timePointer(row.archivedAt)
	value.DeleteRequestedAt = timePointer(row.deleteRequestedAt)
	value.PurgeAfter = timePointer(row.purgeAfter)
	value.DeletedAt = timePointer(row.deletedAt)
	return value
}

func readRow(
	ctx context.Context,
	database rowQueryer,
	id ID,
	includeDeleted bool,
	lock bool,
) (databaseRow, error) {
	query := `
		SELECT id, name, name_key, access_policy, lifecycle, instructions,
		       language, published_wiki_id, archived_at, delete_requested_at,
		       purge_after, deleted_at, created_at, updated_at, version
		FROM knowledge_bases
		WHERE id = $1
	`
	if !includeDeleted {
		query += " AND deleted_at IS NULL"
	}
	if lock {
		query += " FOR UPDATE"
	}
	row, err := scanDatabaseRow(database.QueryRow(ctx, query, pgUUID(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return databaseRow{}, ErrNotFound
	}
	return row, err
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
	value KnowledgeBase,
	actor *auth.OperatorID,
	action string,
	eventType string,
) error {
	snapshot := snapshot(value)
	requestID, err := newID()
	if err != nil {
		return err
	}
	targetID := [16]byte(value.ID)
	actorType := "system"
	var actorID *[16]byte
	if actor != nil {
		converted := [16]byte(*actor)
		actorID = &converted
		actorType = "operator"
	}
	if err = events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: actorType, ActorID: actorID, Action: action,
		TargetType: "knowledge_base", TargetID: &targetID,
		RequestID: [16]byte(requestID), Details: snapshot,
	}); err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{
		Type: eventType, ResourceType: "knowledge_base",
		ResourceID: targetID, Snapshot: snapshot,
	})
}

func snapshot(value KnowledgeBase) map[string]any {
	return map[string]any{
		"id":                  value.ID.String(),
		"name":                value.Name,
		"access":              strings.ToLower(string(value.Access)),
		"lifecycle":           strings.ToLower(string(value.Lifecycle)),
		"instructions":        value.Instructions,
		"language":            value.Language,
		"published_wiki_id":   uuidString(value.PublishedWikiID),
		"archived_at":         timeString(value.ArchivedAt),
		"delete_requested_at": timeString(value.DeleteRequestedAt),
		"purge_after":         timeString(value.PurgeAfter),
		"deleted_at":          timeString(value.DeletedAt),
		"created_at":          pythonISO(value.CreatedAt),
		"updated_at":          pythonISO(value.UpdatedAt),
		"version":             value.Version,
	}
}

func uuidString(value *[16]byte) any {
	if value == nil {
		return nil
	}
	return ID(*value).String()
}

func timeString(value *time.Time) any {
	if value == nil {
		return nil
	}
	return pythonISO(*value)
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

func uuidPointer(value pgtype.UUID) *[16]byte {
	if !value.Valid {
		return nil
	}
	id := value.Bytes
	return &id
}

func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func isUniqueViolation(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "23505"
}
