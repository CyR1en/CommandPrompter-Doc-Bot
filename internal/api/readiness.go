package api

import (
	"context"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

const schemaVersion int64 = 1

type readinessResult struct {
	database      bool
	migrations    bool
	dataDirectory bool
	masterKey     bool
}

type readinessChecker interface {
	Check(context.Context) readinessResult
}

type readinessProbe struct {
	pool      *pgxpool.Pool
	dataDir   string
	masterKey bool
}

func (probe readinessProbe) Check(ctx context.Context) readinessResult {
	databaseContext, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()

	database := probe.database(databaseContext)
	migrations := database && probe.migrations(databaseContext)
	return readinessResult{
		database:      database,
		migrations:    migrations,
		dataDirectory: writableDirectory(probe.dataDir),
		masterKey:     probe.masterKey,
	}
}

func (probe readinessProbe) database(ctx context.Context) bool {
	var value int
	return probe.pool.QueryRow(ctx, "SELECT 1").Scan(&value) == nil && value == 1
}

func (probe readinessProbe) migrations(ctx context.Context) bool {
	var version int64
	err := probe.pool.QueryRow(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (version_id) version_id, is_applied
			FROM goose_db_version
			ORDER BY version_id, id DESC
		)
		SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0)
		FROM latest
	`).Scan(&version)
	return err == nil && version == schemaVersion
}

func writableDirectory(directory string) bool {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return false
	}
	file, err := os.CreateTemp(directory, ".readiness-")
	if err != nil {
		return false
	}
	name := file.Name()
	ok := true
	if _, err = file.Write([]byte("ready")); err != nil {
		ok = false
	}
	if err = file.Sync(); err != nil {
		ok = false
	}
	if err = file.Close(); err != nil {
		ok = false
	}
	if err = os.Remove(name); err != nil {
		ok = false
	}
	return ok
}
