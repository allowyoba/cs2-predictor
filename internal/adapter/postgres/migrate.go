package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strconv"
	"strings"

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

// MigrateDownTo rolls the schema back to a given version, used by the
// rollback test (and available to an operator who has to undo a release by
// hand). Every migration in this repository writes a Down section, but
// until something actually runs them they are prose: a rollback that has
// never been executed is discovered to be broken at the worst possible
// moment, in the middle of an incident.
func MigrateDownTo(ctx context.Context, dsn string, version int64) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.DownToContext(ctx, db, migrationsDir, version); err != nil {
		return fmt.Errorf("roll migrations back: %w", err)
	}
	return nil
}

// LastIrreversibleVersion is the newest migration that declares no Down
// section — the floor a rollback can reach.
//
// A handful of early migrations (and the outbox partitioning one) are
// deliberately forward-only: undoing them would mean dropping the whole
// schema or rebuilding a partitioned table from an unpartitioned one, and
// nobody is doing either during an incident. Everything above this line is
// expected to roll back cleanly, and the rollback test holds it to that.
func LastIrreversibleVersion() (int64, error) {
	entries, err := migrationsFS.ReadDir(migrationsDir)
	if err != nil {
		return 0, err
	}
	var floor int64
	for _, entry := range entries {
		body, err := migrationsFS.ReadFile(migrationsDir + "/" + entry.Name())
		if err != nil {
			return 0, err
		}
		if hasDownStatements(string(body)) {
			continue
		}
		version, err := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q has no version prefix: %w", entry.Name(), err)
		}
		if version > floor {
			floor = version
		}
	}
	return floor, nil
}

// hasDownStatements reports whether a migration's Down section contains
// anything but comments and blank lines.
func hasDownStatements(body string) bool {
	_, down, found := strings.Cut(body, "-- +goose Down")
	if !found {
		return false
	}
	for _, line := range strings.Split(down, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return true
		}
	}
	return false
}
