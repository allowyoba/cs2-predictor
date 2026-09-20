//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	pg "cs2predictor/internal/adapter/postgres"
)

// Every migration in this repository writes a Down section, and until this
// test existed not one of them had ever been executed. A rollback nobody
// has run is prose: it is discovered to be broken in the middle of an
// incident, which is the one moment there is no time to fix it.
//
// The cycle here is up → all the way down → up again, which catches the
// three things that actually go wrong: a Down that does not parse, a Down
// that leaves an object behind and makes the second Up collide with it,
// and a Down that drops something a later migration still depends on.
func TestMigrations_RollBackAndReapplyCleanly(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("cs2predictor"),
		tcpostgres.WithUsername("cs2predictor"),
		tcpostgres.WithPassword("cs2predictor"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	if err := pg.Migrate(ctx, dsn); err != nil {
		t.Fatalf("first up: %v", err)
	}
	applied := migrationVersion(t, ctx, dsn)
	if applied == 0 {
		t.Fatal("expected the schema to be migrated before rolling it back")
	}

	// Down to the floor: a few early migrations are deliberately
	// forward-only (see pg.LastIrreversibleVersion), and everything above
	// them is expected to undo itself.
	floor, err := pg.LastIrreversibleVersion()
	if err != nil {
		t.Fatal(err)
	}
	if floor >= applied {
		t.Fatalf("nothing reversible to test: floor %d, applied %d", floor, applied)
	}
	if err := pg.MigrateDownTo(ctx, dsn, floor); err != nil {
		t.Fatalf("rolling back to %d: %v", floor, err)
	}
	if got := migrationVersion(t, ctx, dsn); got != floor {
		t.Fatalf("schema version after rollback = %d, want %d", got, floor)
	}

	if err := pg.Migrate(ctx, dsn); err != nil {
		t.Fatalf("re-applying after a rollback: %v", err)
	}
	if got := migrationVersion(t, ctx, dsn); got != applied {
		t.Fatalf("schema version after re-applying = %d, want %d", got, applied)
	}
}

// migrationVersion reads goose's own record of where the schema is.
func migrationVersion(t *testing.T, ctx context.Context, dsn string) int64 {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var version int64
	err = pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&version)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}
