package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// partitionLockTimeout bounds how long partition maintenance is willing to
// wait for the ACCESS EXCLUSIVE lock it needs on outbox_event.
//
// This is not a micro-optimization, it is the whole point. Both statements
// below take a lock on the parent table, and in PostgreSQL a *pending*
// ACCESS EXCLUSIVE request queues ahead of every ordinary reader that
// arrives after it. So one blocked DROP does not merely stall itself: it
// stalls the outbox dispatcher, the sync jobs and the digests behind it,
// for as long as whatever it is waiting on runs. A real incident — the
// pre-deploy pg_dump holds ACCESS SHARE on every table, and a sweep landing
// during it took the whole application down for its entire job timeout:
//
//	WARN failed to drop outbox partition date=2026-08-08 error="context deadline exceeded"
//	ERROR outbox dispatch failed error="context deadline exceeded"
//
// Two seconds is far longer than an uncontended lock needs, and dropping
// yesterday's empty partition is optional work that loses nothing by
// waiting for the next sweep.
const partitionLockTimeout = "2s"

// outboxPartitionName is the shared "outbox_event_YYYYMMDD" naming scheme —
// DropOutboxPartitionIfEmpty identifies a partition by name alone, not by
// its declared range.
func outboxPartitionName(day time.Time) string {
	return "outbox_event_" + day.Format("20060102")
}

// isLockTimeout reports whether err is PostgreSQL's lock_not_available
// (55P03) — the statement gave up waiting rather than failing on its own
// merits.
func isLockTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// EnsureOutboxPartition creates the day's partition if missing. Idempotent.
// A lock timeout is returned as an error: unlike a drop, a partition that
// never gets created eventually costs writes, so the caller should see it
// (and will retry on the next sweep).
func (r *RetentionRepository) EnsureOutboxPartition(ctx context.Context, day time.Time) error {
	from := day.Format("2006-01-02")
	to := day.AddDate(0, 0, 1).Format("2006-01-02")
	sql := `CREATE TABLE IF NOT EXISTS ` + pgx.Identifier{outboxPartitionName(day)}.Sanitize() +
		` PARTITION OF outbox_event FOR VALUES FROM ('` + from + `') TO ('` + to + `')`
	return r.withLockTimeout(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql)
		return err
	})
}

// DropOutboxPartitionIfEmpty drops the day's partition only if it holds no
// rows. Reports whether it dropped anything; a missing partition, a
// non-empty one, and one whose lock is currently held elsewhere are all
// ordinary "not today" outcomes rather than errors.
func (r *RetentionRepository) DropOutboxPartitionIfEmpty(ctx context.Context, day time.Time) (bool, error) {
	name := outboxPartitionName(day)
	ident := pgx.Identifier{name}.Sanitize()

	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}

	var empty bool
	if err := r.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM `+ident+` LIMIT 1)`).Scan(&empty); err != nil {
		return false, err
	}
	if !empty {
		return false, nil
	}

	err := r.withLockTimeout(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DROP TABLE `+ident)
		return err
	})
	if isLockTimeout(err) {
		return false, nil // somebody is reading it right now; next sweep will do it
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// withLockTimeout runs fn in a transaction that will not queue for a lock
// longer than partitionLockTimeout. SET LOCAL rather than a session-level
// SET, so the bound cannot leak onto the next user of this pooled
// connection.
func (r *RetentionRepository) withLockTimeout(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '`+partitionLockTimeout+`'`); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
