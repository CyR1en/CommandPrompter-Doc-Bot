package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/database"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Run applies or inspects the embedded schema baseline.
func Run(ctx context.Context, args []string) error {
	action := "up"
	if len(args) > 1 {
		return errors.New("usage: ref0 migrate [up|down|status|version]")
	}
	if len(args) == 1 {
		action = args[0]
	}

	databaseURL, err := database.URLFromEnvironment()
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect database: %w", err)
	}

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("configure migrations: %w", err)
	}

	switch action {
	case "up":
		err = goose.UpContext(ctx, db, ".")
	case "down":
		err = goose.DownContext(ctx, db, ".")
	case "status":
		err = goose.StatusContext(ctx, db, ".")
	case "version":
		var version int64
		version, err = goose.GetDBVersionContext(ctx, db)
		if err == nil {
			fmt.Println(version)
		}
	default:
		return fmt.Errorf("unknown migrate action %q", action)
	}
	if err != nil {
		return fmt.Errorf("migrate %s: %w", action, err)
	}
	return nil
}
