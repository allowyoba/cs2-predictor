package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// outboxPartitionName is the shared "outbox_event_YYYYMMDD" naming scheme —
// DropOutboxPartitionIfEmpty identifies a partition by name alone, not by
// its declared range.
func outboxPartitionName(day time.Time) string {
	return "outbox_event_" + day.Format("20060102")
}

// EnsureOutboxPartition creates the day's partition if missing. Idempotent.
func (r *RetentionRepository) EnsureOutboxPartition(ctx context.Context, day time.Time) error {
	from := day.Format("2006-01-02")
	to := day.AddDate(0, 0, 1).Format("2006-01-02")
	sql := `CREATE TABLE IF NOT EXISTS ` + pgx.Identifier{outboxPartitionName(day)}.Sanitize() +
		` PARTITION OF outbox_event FOR VALUES FROM ('` + from + `') TO ('` + to + `')`
	_, err := r.pool.Exec(ctx, sql)
	return err
}

// DropOutboxPartitionIfEmpty drops the day's partition only if it holds no
// rows. Reports whether it dropped anything; a missing partition isn't an error.
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

	if _, err := r.pool.Exec(ctx, `DROP TABLE `+ident); err != nil {
		return false, err
	}
	return true, nil
}
