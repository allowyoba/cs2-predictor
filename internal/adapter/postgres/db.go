// Package postgres implements every domain persistence port against a
// single Postgres database (see migrations/ and each repository file's doc
// comment for the specific queries used).
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbtx is satisfied by both *pgxpool.Pool and pgx.Tx, letting every
// repository method run either against the pool directly or inside an
// active transaction pulled from ctx via WithTx — without repository
// constructors needing to know which.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

// WithTx returns a context carrying tx, so repository calls made with the
// returned context run inside it instead of auto-committing individually.
// Used to give application services (result settlement, event completion)
// atomicity between a domain write and Outbox.Enqueue.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

func executor(ctx context.Context, pool *pgxpool.Pool) dbtx {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// claimOnce runs an idempotent "claim" INSERT ... ON CONFLICT DO NOTHING and
// reports whether THIS call was the one that actually inserted the row —
// the shared implementation behind every exactly-once claim in this package
// (Telegram update dedup, scheduled digest markers, ...), so each caller
// only needs to supply its own table/conflict-target SQL and args.
func claimOnce(ctx context.Context, ex dbtx, sql string, args ...any) (bool, error) {
	tag, err := ex.Exec(ctx, sql, args...)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RunInTx begins a transaction, runs fn with a context that routes every
// repository call in fn to that transaction, and commits iff fn returns nil
// (rolling back otherwise).
func RunInTx(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	txCtx := WithTx(ctx, tx)
	if err := fn(txCtx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}
