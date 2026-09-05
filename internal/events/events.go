// Package events reads and encodes the committed resource-event stream.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const MaxReadLimit = 500

var (
	ErrInvalidCursor = errors.New("event cursor is invalid")
	ErrCursorPruned  = errors.New("event cursor has been pruned")
	ErrInvalidRead   = errors.New("event read is out of bounds")
	ErrInvalidEvent  = errors.New("committed event is invalid")
)

// Event is one transactionally committed event_log record.
type Event struct {
	Sequence int64
	Type     string
	Snapshot json.RawMessage
}

// CursorWindow describes the durable resume range. Retention advances
// PrunedThrough after deleting events; Tail is the newest committed sequence.
type CursorWindow struct {
	Tail          int64
	PrunedThrough int64
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Reader performs bounded, short-transaction reads from event_log.
type Reader struct {
	database queryer
}

func NewReader(database queryer) (*Reader, error) {
	if database == nil {
		return nil, errors.New("event reader database is required")
	}
	return &Reader{database: database}, nil
}

func (reader *Reader) ReadAfter(ctx context.Context, cursor int64, limit int) ([]Event, error) {
	if cursor < 0 || limit < 1 || limit > MaxReadLimit {
		return nil, ErrInvalidRead
	}
	rows, err := reader.database.Query(ctx, `
		SELECT state.pruned_through,batch.sequence,batch.event_type,batch.snapshot
		FROM event_stream_state state
		LEFT JOIN LATERAL (
			SELECT sequence,event_type,snapshot FROM event_log
			WHERE sequence > $1 ORDER BY sequence LIMIT $2
		) batch ON $1 >= state.pruned_through
		WHERE state.id=1
		ORDER BY batch.sequence NULLS LAST
	`, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("read committed events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0, limit)
	stateRead := false
	for rows.Next() {
		var (
			prunedThrough int64
			sequence      pgtype.Int8
			eventType     pgtype.Text
			snapshot      []byte
		)
		if err = rows.Scan(&prunedThrough, &sequence, &eventType, &snapshot); err != nil {
			return nil, fmt.Errorf("scan committed event: %w", err)
		}
		stateRead = true
		if cursor < prunedThrough {
			return nil, ErrCursorPruned
		}
		if sequence.Valid {
			events = append(events, Event{Sequence: sequence.Int64, Type: eventType.String, Snapshot: snapshot})
		}
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate committed events: %w", err)
	}
	if !stateRead {
		return nil, errors.New("event cursor state is unavailable")
	}
	return events, nil
}

func (reader *Reader) Window(ctx context.Context) (CursorWindow, error) {
	var window CursorWindow
	if err := reader.database.QueryRow(ctx, `
		SELECT
			GREATEST(COALESCE((SELECT MAX(sequence) FROM event_log), 0), pruned_through),
			pruned_through
		FROM event_stream_state
		WHERE id = 1
	`).Scan(&window.Tail, &window.PrunedThrough); err != nil {
		return CursorWindow{}, fmt.Errorf("read event cursor window: %w", err)
	}
	return window, nil
}

// ParseCursor applies one canonical signed-bigint cursor contract. Native
// EventSource reconnects add Last-Event-ID while retaining the original URL,
// so the header deliberately takes precedence over a stale after parameter.
func ParseCursor(after, lastEventID *string) (int64, error) {
	raw := after
	if lastEventID != nil {
		raw = lastEventID
	}
	if raw == nil {
		return 0, nil
	}
	value, err := strconv.ParseInt(*raw, 10, 64)
	if err != nil || value < 0 || strconv.FormatInt(value, 10) != *raw {
		return 0, ErrInvalidCursor
	}
	return value, nil
}

// Message returns a complete deterministic SSE record. Snapshot keys are sorted
// and non-ASCII runes use JSON escapes so every transport observes identical
// bytes.
func Message(event Event) ([]byte, error) {
	if event.Sequence < 0 || event.Type == "" || strings.ContainsAny(event.Type, "\r\n") {
		return nil, ErrInvalidEvent
	}
	var snapshot map[string]any
	decoder := json.NewDecoder(bytes.NewReader(event.Snapshot))
	decoder.UseNumber()
	if err := decoder.Decode(&snapshot); err != nil || snapshot == nil {
		return nil, ErrInvalidEvent
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidEvent
	}
	var encodedBuffer bytes.Buffer
	encoder := json.NewEncoder(&encodedBuffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(snapshot); err != nil {
		return nil, ErrInvalidEvent
	}
	encoded := bytes.TrimSuffix(encodedBuffer.Bytes(), []byte{'\n'})
	encoded = escapeNonASCIIJSON(encoded)

	message := make([]byte, 0, len(encoded)+len(event.Type)+64)
	message = append(message, "id: "...)
	message = strconv.AppendInt(message, event.Sequence, 10)
	message = append(message, "\nevent: "...)
	message = append(message, event.Type...)
	message = append(message, "\ndata: "...)
	message = append(message, encoded...)
	message = append(message, '\n', '\n')
	return message, nil
}

func escapeNonASCIIJSON(encoded []byte) []byte {
	result := make([]byte, 0, len(encoded))
	for len(encoded) > 0 {
		runeValue, width := utf8.DecodeRune(encoded)
		if runeValue < utf8.RuneSelf {
			result = append(result, byte(runeValue))
			encoded = encoded[width:]
			continue
		}
		if runeValue <= 0xffff {
			result = appendUnicodeEscape(result, uint16(runeValue))
		} else {
			value := runeValue - 0x10000
			result = appendUnicodeEscape(result, uint16(0xd800+(value>>10)))
			result = appendUnicodeEscape(result, uint16(0xdc00+(value&0x3ff)))
		}
		encoded = encoded[width:]
	}
	return result
}

func appendUnicodeEscape(destination []byte, value uint16) []byte {
	const hex = "0123456789abcdef"
	return append(destination,
		'\\', 'u',
		hex[value>>12],
		hex[value>>8&0xf],
		hex[value>>4&0xf],
		hex[value&0xf],
	)
}
