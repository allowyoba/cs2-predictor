package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/platform/common"
)

// ScheduledReportRepository is the persistent idempotency store for monthly
// and annual automatic Telegram digests. Claim is intentionally transaction-
// aware via executor(ctx, pool), so the marker and the outbox enqueue can be
// committed atomically by app.DigestScheduler.
type ScheduledReportRepository struct {
	pool *pgxpool.Pool
}

func NewScheduledReportRepository(pool *pgxpool.Pool) *ScheduledReportRepository {
	return &ScheduledReportRepository{pool: pool}
}

// Claimed is a cheap, read-only pre-check used to skip expensive report
// computation once a period is already claimed — see ScheduledReportStore's
// doc comment in internal/app/digest.go for why this doesn't need to be
// transactional the way Claim does.
func (r *ScheduledReportRepository) Claimed(ctx context.Context, chatID common.ChatID, reportType, periodKey string) (bool, error) {
	var exists bool
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM scheduled_report WHERE chat_id = $1 AND report_type = $2 AND period_key = $3)`,
		chatID.Value, reportType, periodKey).Scan(&exists)
	return exists, err
}

func (r *ScheduledReportRepository) Claim(ctx context.Context, chatID common.ChatID, reportType, periodKey string) (bool, error) {
	return claimOnce(ctx, executor(ctx, r.pool), `
        INSERT INTO scheduled_report(chat_id, report_type, period_key, created_at)
        VALUES ($1, $2, $3, now())
        ON CONFLICT (chat_id, report_type, period_key) DO NOTHING`,
		chatID.Value, reportType, periodKey)
}
