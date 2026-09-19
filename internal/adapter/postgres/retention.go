package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/platform/common"
)

// RetentionRepository prunes the four tables that would otherwise grow
// without bound: the webhook dedup ledger (one row per Telegram update
// ever received), published outbox rows, resolved unsubscribe
// confirmations, and the admin change history. Nothing else in the
// schema accumulates unboundedly — everything else is keyed to a chat,
// event or match that has its own lifecycle.
type RetentionRepository struct {
	pool *pgxpool.Pool
}

func NewRetentionRepository(pool *pgxpool.Pool) *RetentionRepository {
	return &RetentionRepository{pool: pool}
}

var _ common.RetentionRepository = (*RetentionRepository)(nil)

// DeleteProcessedUpdatesBefore drops dedup rows old enough that Telegram
// will never retry the update they guard. Telegram gives up re-delivering a
// webhook long before this, so an entry older than the cutoff can no longer
// suppress anything.
func (r *RetentionRepository) DeleteProcessedUpdatesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`DELETE FROM processed_telegram_update WHERE processed_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeletePublishedOutboxBefore drops outbox rows that were successfully
// published before the cutoff. Unpublished rows are never touched, however
// old: those are still owed to somebody.
func (r *RetentionRepository) DeletePublishedOutboxBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`DELETE FROM outbox_event WHERE published_at IS NOT NULL AND published_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteResolvedUnsubscribesBefore drops approval requests that were
// already confirmed or rejected. Unresolved ones stay: they're read back
// (and rejected on expiry) by the domain's own Expired check, which is what
// makes a stale button fail closed rather than silently execute.
func (r *RetentionRepository) DeleteResolvedUnsubscribesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`DELETE FROM pending_approval WHERE resolved_at IS NOT NULL AND resolved_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteProcessedUpdatesExceeding keeps at most maxRows of the newest dedup
// rows — the row-count backstop under DeleteProcessedUpdatesBefore's TTL
// (see RetentionRepository's doc comment). Finding the maxRows-th newest
// row's timestamp and deleting everything strictly older reuses the same
// indexed-by-processed_at delete DeleteProcessedUpdatesBefore already does,
// rather than a separate deletion strategy; the OFFSET subquery costs one
// index-ordered scan capped at maxRows rows, not a scan of the table.
func (r *RetentionRepository) DeleteProcessedUpdatesExceeding(ctx context.Context, maxRows int) (int64, error) {
	if maxRows <= 0 {
		return 0, nil
	}
	tag, err := executor(ctx, r.pool).Exec(ctx, `
		DELETE FROM processed_telegram_update
		 WHERE processed_at < COALESCE(
		   (SELECT processed_at FROM processed_telegram_update ORDER BY processed_at DESC OFFSET ($1 - 1) LIMIT 1),
		   '-infinity'::timestamptz)`, maxRows)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeletePublishedOutboxExceeding is DeleteProcessedUpdatesExceeding's sibling
// for published outbox rows — unpublished rows are never touched, same as
// DeletePublishedOutboxBefore.
func (r *RetentionRepository) DeletePublishedOutboxExceeding(ctx context.Context, maxRows int) (int64, error) {
	if maxRows <= 0 {
		return 0, nil
	}
	tag, err := executor(ctx, r.pool).Exec(ctx, `
		DELETE FROM outbox_event
		 WHERE published_at IS NOT NULL
		   AND published_at < COALESCE(
		     (SELECT published_at FROM outbox_event WHERE published_at IS NOT NULL ORDER BY published_at DESC OFFSET ($1 - 1) LIMIT 1),
		     '-infinity'::timestamptz)`, maxRows)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteAdminActionsBefore trims the per-chat change history. Only the
// newest AdminActionHistorySize entries are ever displayed, so anything
// past the window is kept purely for a "what happened last month?"
// question, and eventually not even that.
func (r *RetentionRepository) DeleteAdminActionsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`DELETE FROM admin_action_log WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
