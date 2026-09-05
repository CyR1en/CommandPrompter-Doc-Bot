package verification

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestDatabaseDoesNotContainPlaintextSentinel is an opt-in acceptance check
// used after submitting a write-only credential through the production API.
// It searches every textual, JSON, and bytea column without printing the
// submitted value when containment fails.
func TestDatabaseDoesNotContainPlaintextSentinel(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	sentinel := os.Getenv("REF0_SECRET_SCAN_SENTINEL")
	if databaseURL == "" || sentinel == "" {
		t.Skip("TEST_DATABASE_URL and REF0_SECRET_SCAN_SENTINEL are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal("connect to acceptance database")
	}
	defer connection.Close(ctx)

	rows, err := connection.Query(ctx, `
		SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND data_type IN ('character varying', 'character', 'text', 'json', 'jsonb', 'bytea')
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatal("enumerate searchable database columns")
	}
	type column struct{ table, name, dataType string }
	var columns []column
	for rows.Next() {
		var item column
		if err := rows.Scan(&item.table, &item.name, &item.dataType); err != nil {
			rows.Close()
			t.Fatal("read searchable database column")
		}
		columns = append(columns, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal("enumerate searchable database columns")
	}
	rows.Close()
	if len(columns) == 0 {
		t.Fatal("acceptance database has no searchable public columns")
	}

	for _, item := range columns {
		table := pgx.Identifier{item.table}.Sanitize()
		name := pgx.Identifier{item.name}.Sanitize()
		predicate := fmt.Sprintf("position($1 in COALESCE(%s::text, '')) > 0", name)
		if item.dataType == "bytea" {
			predicate = fmt.Sprintf("position(convert_to($1, 'UTF8') in COALESCE(%s, ''::bytea)) > 0", name)
		}
		var found bool
		query := fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM %s WHERE %s)", table, predicate)
		if err := connection.QueryRow(ctx, query, sentinel).Scan(&found); err != nil {
			t.Fatalf("scan database column %s.%s", item.table, item.name)
		}
		if found {
			t.Fatalf("plaintext secret found in database column %s.%s", item.table, item.name)
		}
	}
}
