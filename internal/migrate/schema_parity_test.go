package migrate_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/cyr1en/ref0/db/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const (
	baselineDatabaseURL = "TEST_DATABASE_URL"
)

type schemaCatalog struct {
	Tables      []tableDefinition
	Columns     []columnDefinition
	Constraints []constraintDefinition
	Indexes     []indexDefinition
	Sequences   []sequenceDefinition
}

type applicationCatalogManifest struct {
	Tables      []tableDefinition      `json:"tables"`
	Columns     []columnDefinition     `json:"columns"`
	Indexes     []indexDefinition      `json:"indexes"`
	Checks      []constraintDefinition `json:"checks"`
	ForeignKeys []constraintDefinition `json:"foreign_keys"`
}

type tableDefinition struct {
	Name         string
	Kind         string
	AccessMethod string
}

type columnDefinition struct {
	Table     string
	Position  int
	Name      string
	Type      string
	NotNull   bool
	Default   string
	Generated string
	Identity  string
	Collation string
}

type constraintDefinition struct {
	Table        string
	Name         string
	Type         string
	Deferrable   bool
	Deferred     bool
	Validated    bool
	UpdateAction string
	DeleteAction string
	MatchType    string
	Definition   string
}

type indexDefinition struct {
	Table      string
	Name       string
	Method     string
	Unique     bool
	Primary    bool
	Valid      bool
	Predicate  string
	Definition string
}

type sequenceDefinition struct {
	Name           string
	Type           string
	Start          int64
	Increment      int64
	Minimum        int64
	Maximum        int64
	Cache          int64
	Cycle          bool
	OwnerTable     string
	OwnerColumn    string
	DependencyType string
}

func TestGooseBaselineCatalogInvariants(t *testing.T) {
	databaseURL := os.Getenv(baselineDatabaseURL)
	if databaseURL == "" {
		t.Skipf("set %s to a disposable PostgreSQL database", baselineDatabaseURL)
	}

	ctx := context.Background()
	target := openDatabase(t, databaseURL)

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("configure Goose: %v", err)
	}
	if err := goose.UpContext(ctx, target, "."); err != nil {
		t.Fatalf("apply Goose baseline: %v", err)
	}
	version, err := goose.GetDBVersionContext(ctx, target)
	if err != nil {
		t.Fatalf("read Goose version: %v", err)
	}
	if version != 1 {
		t.Fatalf("Goose version = %d, want 1", version)
	}

	catalog, err := readSchemaCatalog(ctx, target)
	if err != nil {
		t.Fatalf("read Goose baseline catalog: %v", err)
	}
	assertCatalogInvariants(t, "Goose baseline", catalog)
	assertExactApplicationCatalog(t, catalog)
}

func assertExactApplicationCatalog(t *testing.T, catalog schemaCatalog) {
	t.Helper()
	manifest := applicationCatalogManifest{
		Tables: catalog.Tables, Columns: catalog.Columns, Indexes: catalog.Indexes,
		Checks: slices.DeleteFunc(slices.Clone(catalog.Constraints), func(value constraintDefinition) bool {
			return value.Type != "c"
		}),
		ForeignKeys: slices.DeleteFunc(slices.Clone(catalog.Constraints), func(value constraintDefinition) bool {
			return value.Type != "f"
		}),
	}
	path := applicationCatalogManifestPath(t)
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("REF0_UPDATE_SCHEMA_MANIFEST") == "1" {
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read exact application catalog manifest: %v", err)
	}
	var expected applicationCatalogManifest
	if err = json.Unmarshal(committed, &expected); err != nil {
		t.Fatalf("decode exact application catalog manifest: %v", err)
	}
	if !reflect.DeepEqual(manifest, expected) {
		t.Fatalf("application catalog differs from %s; regenerate with REF0_UPDATE_SCHEMA_MANIFEST=1 TEST_DATABASE_URL=<fresh-postgres-url> go test ./internal/migrate -run TestGooseBaselineCatalogInvariants", path)
	}
}

func applicationCatalogManifestPath(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve schema parity source path")
	}
	return filepath.Join(filepath.Dir(source), "testdata", "application_catalog.json")
}

func openDatabase(t *testing.T, databaseURL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("connect to PostgreSQL database: %v", err)
	}
	return db
}

func readSchemaCatalog(ctx context.Context, db *sql.DB) (schemaCatalog, error) {
	var catalog schemaCatalog
	var err error
	if catalog.Tables, err = readTables(ctx, db); err != nil {
		return schemaCatalog{}, err
	}
	if catalog.Columns, err = readColumns(ctx, db); err != nil {
		return schemaCatalog{}, err
	}
	if catalog.Constraints, err = readConstraints(ctx, db); err != nil {
		return schemaCatalog{}, err
	}
	if catalog.Indexes, err = readIndexes(ctx, db); err != nil {
		return schemaCatalog{}, err
	}
	if catalog.Sequences, err = readSequences(ctx, db); err != nil {
		return schemaCatalog{}, err
	}
	return catalog, nil
}

func readTables(ctx context.Context, db *sql.DB) ([]tableDefinition, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT relation.relname,
		       CASE relation.relkind WHEN 'p' THEN 'partitioned' ELSE 'table' END,
		       COALESCE(access_method.amname, '')
		FROM pg_catalog.pg_class AS relation
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		LEFT JOIN pg_catalog.pg_am AS access_method ON access_method.oid = relation.relam
		WHERE namespace.nspname = 'public'
		  AND relation.relkind IN ('r', 'p')
		  AND relation.relname != 'goose_db_version'
		ORDER BY relation.relname
	`)
	if err != nil {
		return nil, fmt.Errorf("query tables: %w", err)
	}
	defer rows.Close()
	var definitions []tableDefinition
	for rows.Next() {
		var definition tableDefinition
		if err := rows.Scan(&definition.Name, &definition.Kind, &definition.AccessMethod); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, rows.Err()
}

func readColumns(ctx context.Context, db *sql.DB) ([]columnDefinition, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT relation.relname,
		       attribute.attnum,
		       attribute.attname,
		       pg_catalog.format_type(attribute.atttypid, attribute.atttypmod),
		       attribute.attnotnull,
		       COALESCE(pg_catalog.pg_get_expr(default_value.adbin, default_value.adrelid, false), ''),
		       attribute.attgenerated::text,
		       attribute.attidentity::text,
		       COALESCE(column_collation.collname, '')
		FROM pg_catalog.pg_attribute AS attribute
		JOIN pg_catalog.pg_class AS relation ON relation.oid = attribute.attrelid
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		LEFT JOIN pg_catalog.pg_attrdef AS default_value
		       ON default_value.adrelid = attribute.attrelid
		      AND default_value.adnum = attribute.attnum
		LEFT JOIN pg_catalog.pg_collation AS column_collation ON column_collation.oid = attribute.attcollation
		WHERE namespace.nspname = 'public'
		  AND relation.relkind IN ('r', 'p')
		  AND relation.relname != 'goose_db_version'
		  AND attribute.attnum > 0
		  AND NOT attribute.attisdropped
		ORDER BY relation.relname, attribute.attnum
	`)
	if err != nil {
		return nil, fmt.Errorf("query columns: %w", err)
	}
	defer rows.Close()
	var definitions []columnDefinition
	for rows.Next() {
		var definition columnDefinition
		if err := rows.Scan(
			&definition.Table,
			&definition.Position,
			&definition.Name,
			&definition.Type,
			&definition.NotNull,
			&definition.Default,
			&definition.Generated,
			&definition.Identity,
			&definition.Collation,
		); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, rows.Err()
}

func readConstraints(ctx context.Context, db *sql.DB) ([]constraintDefinition, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT relation.relname,
		       constraint_value.conname,
		       constraint_value.contype::text,
		       constraint_value.condeferrable,
		       constraint_value.condeferred,
		       constraint_value.convalidated,
		       CASE constraint_value.contype WHEN 'f' THEN
		           CASE constraint_value.confupdtype
		               WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
		               WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL'
		               WHEN 'd' THEN 'SET DEFAULT' END
		           ELSE '' END,
		       CASE constraint_value.contype WHEN 'f' THEN
		           CASE constraint_value.confdeltype
		               WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
		               WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL'
		               WHEN 'd' THEN 'SET DEFAULT' END
		           ELSE '' END,
		       CASE constraint_value.contype WHEN 'f' THEN
		           CASE constraint_value.confmatchtype
		               WHEN 'f' THEN 'FULL' WHEN 'p' THEN 'PARTIAL' WHEN 's' THEN 'SIMPLE' END
		           ELSE '' END,
		       pg_catalog.pg_get_constraintdef(constraint_value.oid, false)
		FROM pg_catalog.pg_constraint AS constraint_value
		JOIN pg_catalog.pg_class AS relation ON relation.oid = constraint_value.conrelid
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = 'public'
		  AND relation.relname != 'goose_db_version'
		ORDER BY relation.relname, constraint_value.conname
	`)
	if err != nil {
		return nil, fmt.Errorf("query constraints: %w", err)
	}
	defer rows.Close()
	var definitions []constraintDefinition
	for rows.Next() {
		var definition constraintDefinition
		if err := rows.Scan(
			&definition.Table,
			&definition.Name,
			&definition.Type,
			&definition.Deferrable,
			&definition.Deferred,
			&definition.Validated,
			&definition.UpdateAction,
			&definition.DeleteAction,
			&definition.MatchType,
			&definition.Definition,
		); err != nil {
			return nil, fmt.Errorf("scan constraint: %w", err)
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := canonicalizeCheckConstraints(ctx, db, definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

// pg_dump's check expressions are semantically stable but not always textually
// idempotent. Reparse both sides once so PostgreSQL compares like with like.
func canonicalizeCheckConstraints(ctx context.Context, db *sql.DB, definitions []constraintDefinition) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin check canonicalization: %w", err)
	}
	defer tx.Rollback()

	const temporaryTable = "ref0_catalog_check_table"
	const temporaryConstraint = "ref0_catalog_check"
	currentTable := ""
	for index := range definitions {
		definition := &definitions[index]
		if definition.Type != "c" {
			continue
		}
		if definition.Table != currentTable {
			if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS pg_temp."+temporaryTable); err != nil {
				return fmt.Errorf("drop check scratch table: %w", err)
			}
			statement := fmt.Sprintf(
				"CREATE TEMPORARY TABLE %s (LIKE public.%s) ON COMMIT DROP",
				temporaryTable,
				quoteIdentifier(definition.Table),
			)
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("create check scratch table for %s: %w", definition.Table, err)
			}
			currentTable = definition.Table
		}

		statement := fmt.Sprintf(
			"ALTER TABLE pg_temp.%s ADD CONSTRAINT %s %s",
			temporaryTable,
			temporaryConstraint,
			definition.Definition,
		)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("reparse check %s.%s: %w", definition.Table, definition.Name, err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT pg_catalog.pg_get_constraintdef(constraint_value.oid, false)
			FROM pg_catalog.pg_constraint AS constraint_value
			WHERE constraint_value.conrelid = 'pg_temp.ref0_catalog_check_table'::regclass
			  AND constraint_value.conname = 'ref0_catalog_check'
		`).Scan(&definition.Definition); err != nil {
			return fmt.Errorf("read canonical check %s.%s: %w", definition.Table, definition.Name, err)
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE pg_temp."+temporaryTable+" DROP CONSTRAINT "+temporaryConstraint); err != nil {
			return fmt.Errorf("drop canonical check %s.%s: %w", definition.Table, definition.Name, err)
		}
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func readIndexes(ctx context.Context, db *sql.DB) ([]indexDefinition, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT relation.relname,
		       index_relation.relname,
		       access_method.amname,
		       index_value.indisunique,
		       index_value.indisprimary,
		       index_value.indisvalid,
		       COALESCE(pg_catalog.pg_get_expr(index_value.indpred, index_value.indrelid, false), ''),
		       pg_catalog.pg_get_indexdef(index_relation.oid, 0, false)
		FROM pg_catalog.pg_index AS index_value
		JOIN pg_catalog.pg_class AS relation ON relation.oid = index_value.indrelid
		JOIN pg_catalog.pg_class AS index_relation ON index_relation.oid = index_value.indexrelid
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		JOIN pg_catalog.pg_am AS access_method ON access_method.oid = index_relation.relam
		WHERE namespace.nspname = 'public'
		  AND relation.relname != 'goose_db_version'
		ORDER BY relation.relname, index_relation.relname
	`)
	if err != nil {
		return nil, fmt.Errorf("query indexes: %w", err)
	}
	defer rows.Close()
	var definitions []indexDefinition
	for rows.Next() {
		var definition indexDefinition
		if err := rows.Scan(
			&definition.Table,
			&definition.Name,
			&definition.Method,
			&definition.Unique,
			&definition.Primary,
			&definition.Valid,
			&definition.Predicate,
			&definition.Definition,
		); err != nil {
			return nil, fmt.Errorf("scan index: %w", err)
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := canonicalizeIndexPredicates(ctx, db, definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

func canonicalizeIndexPredicates(ctx context.Context, db *sql.DB, definitions []indexDefinition) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin index canonicalization: %w", err)
	}
	defer tx.Rollback()

	const temporaryTable = "ref0_catalog_index_table"
	const temporaryConstraint = "ref0_catalog_predicate"
	currentTable := ""
	for index := range definitions {
		definition := &definitions[index]
		if definition.Predicate == "" {
			continue
		}
		suffix := " WHERE " + definition.Predicate
		if !strings.HasSuffix(definition.Definition, suffix) {
			return fmt.Errorf("index %s.%s definition does not end in its predicate", definition.Table, definition.Name)
		}
		definition.Definition = strings.TrimSuffix(definition.Definition, suffix)

		if definition.Table != currentTable {
			if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS pg_temp."+temporaryTable); err != nil {
				return fmt.Errorf("drop index scratch table: %w", err)
			}
			statement := fmt.Sprintf(
				"CREATE TEMPORARY TABLE %s (LIKE public.%s) ON COMMIT DROP",
				temporaryTable,
				quoteIdentifier(definition.Table),
			)
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("create index scratch table for %s: %w", definition.Table, err)
			}
			currentTable = definition.Table
		}

		statement := fmt.Sprintf(
			"ALTER TABLE pg_temp.%s ADD CONSTRAINT %s CHECK (%s)",
			temporaryTable,
			temporaryConstraint,
			definition.Predicate,
		)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("reparse predicate %s.%s: %w", definition.Table, definition.Name, err)
		}
		var checkDefinition string
		if err := tx.QueryRowContext(ctx, `
			SELECT pg_catalog.pg_get_constraintdef(constraint_value.oid, false)
			FROM pg_catalog.pg_constraint AS constraint_value
			WHERE constraint_value.conrelid = 'pg_temp.ref0_catalog_index_table'::regclass
			  AND constraint_value.conname = 'ref0_catalog_predicate'
		`).Scan(&checkDefinition); err != nil {
			return fmt.Errorf("read canonical predicate %s.%s: %w", definition.Table, definition.Name, err)
		}
		if !strings.HasPrefix(checkDefinition, "CHECK (") || !strings.HasSuffix(checkDefinition, ")") {
			return fmt.Errorf("canonical predicate %s.%s has unexpected form %q", definition.Table, definition.Name, checkDefinition)
		}
		definition.Predicate = strings.TrimSuffix(strings.TrimPrefix(checkDefinition, "CHECK ("), ")")
		if _, err := tx.ExecContext(ctx, "ALTER TABLE pg_temp."+temporaryTable+" DROP CONSTRAINT "+temporaryConstraint); err != nil {
			return fmt.Errorf("drop canonical predicate %s.%s: %w", definition.Table, definition.Name, err)
		}
	}
	return nil
}

func readSequences(ctx context.Context, db *sql.DB) ([]sequenceDefinition, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT sequence_relation.relname,
		       pg_catalog.format_type(sequence_value.seqtypid, NULL),
		       sequence_value.seqstart,
		       sequence_value.seqincrement,
		       sequence_value.seqmin,
		       sequence_value.seqmax,
		       sequence_value.seqcache,
		       sequence_value.seqcycle,
		       COALESCE(owner_relation.relname, ''),
		       COALESCE(owner_attribute.attname, ''),
		       COALESCE(dependency.deptype::text, '')
		FROM pg_catalog.pg_class AS sequence_relation
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = sequence_relation.relnamespace
		JOIN pg_catalog.pg_sequence AS sequence_value ON sequence_value.seqrelid = sequence_relation.oid
		LEFT JOIN pg_catalog.pg_depend AS dependency
		       ON dependency.classid = 'pg_catalog.pg_class'::pg_catalog.regclass
		      AND dependency.objid = sequence_relation.oid
		      AND dependency.objsubid = 0
		      AND dependency.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass
		      AND dependency.deptype IN ('a', 'i')
		LEFT JOIN pg_catalog.pg_class AS owner_relation ON owner_relation.oid = dependency.refobjid
		LEFT JOIN pg_catalog.pg_attribute AS owner_attribute
		       ON owner_attribute.attrelid = dependency.refobjid
		      AND owner_attribute.attnum = dependency.refobjsubid
		WHERE namespace.nspname = 'public'
		  AND sequence_relation.relkind = 'S'
		  AND COALESCE(owner_relation.relname, '') != 'goose_db_version'
		ORDER BY sequence_relation.relname
	`)
	if err != nil {
		return nil, fmt.Errorf("query sequences: %w", err)
	}
	defer rows.Close()
	var definitions []sequenceDefinition
	for rows.Next() {
		var definition sequenceDefinition
		if err := rows.Scan(
			&definition.Name,
			&definition.Type,
			&definition.Start,
			&definition.Increment,
			&definition.Minimum,
			&definition.Maximum,
			&definition.Cache,
			&definition.Cycle,
			&definition.OwnerTable,
			&definition.OwnerColumn,
			&definition.DependencyType,
		); err != nil {
			return nil, fmt.Errorf("scan sequence: %w", err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, rows.Err()
}

func assertCatalogInvariants(t *testing.T, source string, catalog schemaCatalog) {
	t.Helper()
	if len(catalog.Tables) != 52 {
		t.Errorf("%s table count = %d, want 52", source, len(catalog.Tables))
	}
	if len(catalog.Columns) != 586 {
		t.Errorf("%s column count = %d, want 586", source, len(catalog.Columns))
	}
	if len(catalog.Indexes) != 135 {
		t.Errorf("%s index count = %d, want 135", source, len(catalog.Indexes))
	}
	for _, legacy := range []string{"conversation_messages", "conversations", "query_runs"} {
		if slices.ContainsFunc(catalog.Tables, func(table tableDefinition) bool { return table.Name == legacy }) {
			t.Errorf("%s retains legacy table %s", source, legacy)
		}
	}
	for _, expected := range []struct{ table, name string }{
		{table: "documentation_run_models", name: "ck_documentation_run_models_role_valid"},
		{table: "model_assignments", name: "ck_model_assignments_role_valid"},
	} {
		constraint, ok := findConstraint(catalog.Constraints, expected.table, expected.name)
		if !ok || !strings.Contains(constraint.Definition, "DOCUMENTATION_PLANNER") ||
			!strings.Contains(constraint.Definition, "DOCUMENTATION_WRITER") || strings.Contains(constraint.Definition, "ANSWER") {
			t.Errorf("%s knowledge-base model role constraint differs: %#v", source, constraint)
		}
	}

	assertSearchInvariant(t, source, catalog, "claims", "statement", "ix_claims_search")
	assertSearchInvariant(t, source, catalog, "wiki_pages", "title", "ix_wiki_pages_search")
	assertAgentInvariants(t, source, catalog)
	assertDiscordInvariants(t, source, catalog)
	assertJobInvariants(t, source, catalog)
	assertDeferredForeignKeys(t, source, catalog)
	assertSequenceInvariants(t, source, catalog)
}

func assertDiscordInvariants(t *testing.T, source string, catalog schemaCatalog) {
	t.Helper()
	digest, ok := findColumn(catalog.Columns, "discord_channels", "audience_overwrite_sha256")
	if !ok || digest.Type != "bytea" || !digest.NotNull || !strings.Contains(digest.Default, "4f53cda18c2baa0c") {
		t.Errorf("%s Discord audience digest differs: %#v", source, digest)
	}
	for _, expected := range []struct {
		name, column, predicate string
	}{
		{name: "uq_channel_binding_triggers_enabled_route", column: "connection_id, server_id, listen_channel_id, trigger_type", predicate: "enabled"},
		{name: "uq_discord_connections_application_id", column: "application_id", predicate: "application_id IS NOT NULL"},
		{name: "uq_discord_connections_bot_user_id", column: "bot_user_id", predicate: "bot_user_id IS NOT NULL"},
	} {
		table := "discord_connections"
		if expected.name == "uq_channel_binding_triggers_enabled_route" {
			table = "channel_binding_triggers"
		}
		index, ok := findIndex(catalog.Indexes, table, expected.name)
		if !ok || !index.Unique || index.Method != "btree" ||
			!strings.Contains(index.Definition, "("+expected.column+")") ||
			!strings.Contains(index.Predicate, expected.predicate) {
			t.Errorf("%s Discord unique index %s differs: %#v", source, expected.name, index)
		}
	}
	wikiScopeIndex, ok := findIndex(catalog.Indexes, "agent_run_knowledge_bases", "ix_agent_run_knowledge_bases_wiki")
	if !ok || wikiScopeIndex.Method != "btree" || !strings.Contains(
		wikiScopeIndex.Definition, "(wiki_version_id, knowledge_base_id, run_id)",
	) {
		t.Errorf("%s Agent run wiki scope index differs: %#v", source, wikiScopeIndex)
	}
}

func assertAgentInvariants(t *testing.T, source string, catalog schemaCatalog) {
	t.Helper()
	requiredChecks := map[string]struct {
		table     string
		fragments []string
	}{
		"ck_agents_agent_key_valid": {
			table: "agents", fragments: []string{"[a-z0-9-]", "{0,62}"},
		},
		"ck_agent_versions_mode_tool_limit_valid": {
			table: "agent_versions", fragments: []string{"SINGLE_PASS", "max_tool_calls = 0", "TOOL_CALLING"},
		},
		"ck_agent_version_knowledge_bases_position_valid": {
			table: "agent_version_knowledge_bases", fragments: []string{"\"position\" >= 0", "\"position\" < 32"},
		},
		"ck_chat_access_tokens_token_digest_length": {
			table: "chat_access_tokens", fragments: []string{"octet_length(token_digest) = 32"},
		},
		"ck_agent_runs_tool_and_citation_json_valid": {
			table: "agent_runs", fragments: []string{"jsonb_typeof(tool_calls)", "jsonb_typeof(citations)", "262144"},
		},
		"ck_agent_run_knowledge_bases_source_scope_digest_length": {
			table: "agent_run_knowledge_bases", fragments: []string{"octet_length(source_scope_digest) = 32"},
		},
		"ck_agent_runs_captured_credential_valid": {
			table: "agent_runs", fragments: []string{"captured_credential_id IS NULL", "captured_credential_version > 0"},
		},
		"ck_agent_run_scope_reservations_expiry_valid": {
			table: "agent_run_scope_reservations", fragments: []string{"expires_at > created_at"},
		},
	}
	for name, expected := range requiredChecks {
		constraint, ok := findConstraint(catalog.Constraints, expected.table, name)
		if !ok {
			t.Errorf("%s missing Agent constraint %s", source, name)
			continue
		}
		if constraint.Type != "c" || !constraint.Validated {
			t.Errorf("%s Agent constraint %s is not a validated check: %#v", source, name, constraint)
		}
		for _, fragment := range expected.fragments {
			if !strings.Contains(constraint.Definition, fragment) {
				t.Errorf("%s Agent constraint %s does not contain %q: %s", source, name, fragment, constraint.Definition)
			}
		}
	}

	requiredUnique := []struct {
		table, name string
	}{
		{table: "agents", name: "uq_agents_agent_key"},
		{table: "agent_versions", name: "uq_agent_versions_agent_version_number"},
		{table: "agent_version_knowledge_bases", name: "uq_agent_version_knowledge_bases_version_knowledge_base"},
		{table: "chat_access_tokens", name: "uq_chat_access_tokens_token_digest"},
		{table: "agent_run_knowledge_bases", name: "uq_agent_run_knowledge_bases_run_knowledge_base"},
	}
	for _, expected := range requiredUnique {
		constraint, ok := findConstraint(catalog.Constraints, expected.table, expected.name)
		if !ok || constraint.Type != "u" || !constraint.Validated {
			t.Errorf("%s Agent unique constraint %s.%s differs: %#v", source, expected.table, expected.name, constraint)
		}
	}
	credentialID, ok := findColumn(catalog.Columns, "agent_runs", "captured_credential_id")
	if !ok || credentialID.Type != "uuid" || credentialID.NotNull {
		t.Errorf("%s Agent run credential identity differs: %#v", source, credentialID)
	}
	for _, expected := range []struct {
		name, definition, predicate string
	}{
		{name: "ix_agent_runs_completed", definition: "(completed_at, id)"},
		{name: "ix_agent_runs_failed_created", definition: "(created_at DESC, id)", predicate: "outcome"},
	} {
		index, ok := findIndex(catalog.Indexes, "agent_runs", expected.name)
		if !ok || index.Method != "btree" || !strings.Contains(index.Definition, expected.definition) ||
			expected.predicate != "" && !strings.Contains(index.Predicate, expected.predicate) {
			t.Errorf("%s Agent run index %s differs: %#v", source, expected.name, index)
		}
	}
}

func assertSearchInvariant(t *testing.T, source string, catalog schemaCatalog, table, expressionPart, indexName string) {
	t.Helper()
	column, ok := findColumn(catalog.Columns, table, "search_vector")
	if !ok {
		t.Errorf("%s missing %s.search_vector", source, table)
		return
	}
	if column.Type != "tsvector" || column.Generated != "s" || !strings.Contains(column.Default, "to_tsvector('simple'::regconfig") || !strings.Contains(column.Default, expressionPart) {
		t.Errorf("%s %s.search_vector is not the required stored language-neutral search expression: %#v", source, table, column)
	}
	index, ok := findIndex(catalog.Indexes, table, indexName)
	if !ok {
		t.Errorf("%s missing %s", source, indexName)
		return
	}
	if index.Method != "gin" || index.Unique || index.Predicate != "" || !strings.Contains(index.Definition, "(search_vector)") {
		t.Errorf("%s %s is not the required unfiltered GIN search index: %#v", source, indexName, index)
	}
}

func assertJobInvariants(t *testing.T, source string, catalog schemaCatalog) {
	t.Helper()
	requiredChecks := map[string][]string{
		"ck_jobs_attempts_valid":                {"attempt_count >= 0", "attempt_count <= max_attempts"},
		"ck_jobs_job_type_valid":                {"PROBE_MODEL", "APPLY_RETENTION"},
		"ck_jobs_lease_generation_nonnegative":  {"lease_generation >= 0"},
		"ck_jobs_lease_state_valid":             {"CANCEL_REQUESTED", "lease_owner IS NOT NULL", "lease_expires_at IS NULL"},
		"ck_jobs_payload_object":                {"jsonb_typeof(payload)"},
		"ck_jobs_progress_valid":                {"progress >= 0", "progress <= 100"},
		"ck_jobs_result_object":                 {"jsonb_typeof(result)"},
		"ck_jobs_retry_wait_not_before_present": {"RETRY_WAIT", "not_before IS NOT NULL"},
		"ck_jobs_status_valid":                  {"PENDING", "LEASED", "CANCEL_REQUESTED", "CANCELLED"},
	}
	for name, fragments := range requiredChecks {
		constraint, ok := findConstraint(catalog.Constraints, "jobs", name)
		if !ok {
			t.Errorf("%s missing jobs constraint %s", source, name)
			continue
		}
		if constraint.Type != "c" || !constraint.Validated {
			t.Errorf("%s jobs constraint %s is not a validated check: %#v", source, name, constraint)
		}
		for _, fragment := range fragments {
			if !strings.Contains(constraint.Definition, fragment) {
				t.Errorf("%s jobs constraint %s does not contain %q: %s", source, name, fragment, constraint.Definition)
			}
		}
	}

	claim, ok := findIndex(catalog.Indexes, "jobs", "ix_jobs_claim")
	if !ok || claim.Method != "btree" || claim.Unique || !strings.Contains(claim.Definition, "(status, not_before, created_at)") {
		t.Errorf("%s jobs claim index differs: %#v", source, claim)
	}
	active, ok := findIndex(catalog.Indexes, "jobs", "uq_jobs_active_operation_key")
	if !ok || active.Method != "btree" || !active.Unique || !strings.Contains(active.Definition, "(operation_key)") {
		t.Errorf("%s active-operation index differs: %#v", source, active)
	} else {
		for _, status := range []string{"PENDING", "LEASED", "RETRY_WAIT", "CANCEL_REQUESTED"} {
			if !strings.Contains(active.Predicate, status) {
				t.Errorf("%s active-operation predicate omits %s: %s", source, status, active.Predicate)
			}
		}
	}

	generation, ok := findColumn(catalog.Columns, "jobs", "lease_generation")
	if !ok || generation.Type != "bigint" || !generation.NotNull || generation.Default != "0" {
		t.Errorf("%s jobs.lease_generation differs: %#v", source, generation)
	}
}

func assertDeferredForeignKeys(t *testing.T, source string, catalog schemaCatalog) {
	t.Helper()
	want := map[string][2]string{
		"fk_agent_versions_agent_id_agents":                   {"NO ACTION", "CASCADE"},
		"fk_agents_current_version":                           {"NO ACTION", "NO ACTION"},
		"fk_channel_binding_triggers_route_state":             {"CASCADE", "CASCADE"},
		"fk_discord_conversations_agent_version":              {"NO ACTION", "RESTRICT"},
		"fk_discord_conversations_binding_agent":              {"NO ACTION", "CASCADE"},
		"fk_model_profile_versions_profile_id_model_profiles": {"NO ACTION", "CASCADE"},
		"fk_model_profiles_current_version":                   {"NO ACTION", "NO ACTION"},
		"fk_sources_current_revision":                         {"NO ACTION", "NO ACTION"},
	}
	var names []string
	for _, constraint := range catalog.Constraints {
		if constraint.Type != "f" || !constraint.Deferrable || !constraint.Deferred {
			continue
		}
		names = append(names, constraint.Name)
		actions, ok := want[constraint.Name]
		if !ok {
			t.Errorf("%s has unexpected initially-deferred foreign key %s", source, constraint.Name)
			continue
		}
		if constraint.UpdateAction != actions[0] || constraint.DeleteAction != actions[1] || constraint.MatchType != "SIMPLE" {
			t.Errorf("%s deferred foreign key %s actions differ: %#v", source, constraint.Name, constraint)
		}
	}
	slices.Sort(names)
	wantNames := []string{
		"fk_agent_versions_agent_id_agents",
		"fk_agents_current_version",
		"fk_channel_binding_triggers_route_state",
		"fk_discord_conversations_agent_version",
		"fk_discord_conversations_binding_agent",
		"fk_model_profile_versions_profile_id_model_profiles",
		"fk_model_profiles_current_version",
		"fk_sources_current_revision",
	}
	slices.Sort(wantNames)
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("%s initially-deferred foreign keys = %v, want %v", source, names, wantNames)
	}
}

func assertSequenceInvariants(t *testing.T, source string, catalog schemaCatalog) {
	t.Helper()
	want := map[string][2]string{
		"event_log_sequence_seq":  {"event_log", "sequence"},
		"job_events_sequence_seq": {"job_events", "sequence"},
	}
	if len(catalog.Sequences) != len(want) {
		t.Errorf("%s sequence count = %d, want %d", source, len(catalog.Sequences), len(want))
	}
	for _, sequence := range catalog.Sequences {
		owner, ok := want[sequence.Name]
		if !ok {
			t.Errorf("%s has unexpected sequence %s", source, sequence.Name)
			continue
		}
		if sequence.Type != "bigint" || sequence.Start != 1 || sequence.Increment != 1 || sequence.Cache != 1 || sequence.Cycle || sequence.OwnerTable != owner[0] || sequence.OwnerColumn != owner[1] || sequence.DependencyType != "i" {
			t.Errorf("%s sequence %s differs: %#v", source, sequence.Name, sequence)
		}
		column, ok := findColumn(catalog.Columns, owner[0], owner[1])
		if !ok || column.Identity != "d" {
			t.Errorf("%s identity column %s.%s differs: %#v", source, owner[0], owner[1], column)
		}
	}
}

func findColumn(columns []columnDefinition, table, name string) (columnDefinition, bool) {
	for _, column := range columns {
		if column.Table == table && column.Name == name {
			return column, true
		}
	}
	return columnDefinition{}, false
}

func findConstraint(constraints []constraintDefinition, table, name string) (constraintDefinition, bool) {
	for _, constraint := range constraints {
		if constraint.Table == table && constraint.Name == name {
			return constraint, true
		}
	}
	return constraintDefinition{}, false
}

func findIndex(indexes []indexDefinition, table, name string) (indexDefinition, bool) {
	for _, index := range indexes {
		if index.Table == table && index.Name == name {
			return index, true
		}
	}
	return indexDefinition{}, false
}
