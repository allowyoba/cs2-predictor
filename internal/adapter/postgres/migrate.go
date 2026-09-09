package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver goose needs
	"github.com/pressly/goose/v3"
)

//go:embed all:migrations
var migrationsFS embed.FS

const migrationsDir = "migrations"

// Migrate applies every embedded migration exactly once, via goose. Goose
// needs a database/sql connection, so this opens a short-lived stdlib
// connection over dsn and closes it once migrations are applied — dsn must
// be a plain connection-level DSN (no pool_* parameters, which pgxpool
// understands but Postgres itself rejects as an unknown startup parameter).
// The app's actual runtime traffic stays entirely on a separate pgxpool.
func Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
