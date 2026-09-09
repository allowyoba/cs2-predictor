package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UpdateDeduplicator implements common.UpdateDeduplicator via
// processed_telegram_update's unique constraint + INSERT ... ON CONFLICT DO
// NOTHING.
type UpdateDeduplicator struct {
	pool *pgxpool.Pool
}

func NewUpdateDeduplicator(pool *pgxpool.Pool) *UpdateDeduplicator {
	return &UpdateDeduplicator{pool: pool}
}

func (d *UpdateDeduplicator) Claim(ctx context.Context, updateID int64) (bool, error) {
	return claimOnce(ctx, executor(ctx, d.pool),
		`INSERT INTO processed_telegram_update(update_id) VALUES ($1) ON CONFLICT DO NOTHING`, updateID)
}

func (d *UpdateDeduplicator) Release(ctx context.Context, updateID int64) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM processed_telegram_update WHERE update_id = $1`, updateID)
	return err
}
