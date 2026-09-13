package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/platform/common"
)

// Outbox implements common.Outbox against outbox_event:
//   - Enqueue is a plain INSERT (call it inside RunInTx alongside the domain
//     write it announces, via the same ctx, for exactly-once-enqueue).
//   - Pending selects rows not yet published, under common.OutboxMaxAttempts
//     attempts, whose next_attempt_at has arrived, ordered by occurred_at.
//   - Failed backs off next_attempt_at by min(300, 2^attempts) seconds,
//     computed from the attempts value BEFORE increment (Postgres evaluates
//     every SET clause's RHS against the pre-update row).
//   - last_error is truncated to 4000 chars, matching error.take(4000).
//   - Published/Failed key on (id, occurred_at): outbox_event is partitioned
//     by occurred_at (migration 0031), so occurred_at prunes the UPDATE to
//     one partition instead of scanning all of them.
type Outbox struct {
	pool *pgxpool.Pool
}

func NewOutbox(pool *pgxpool.Pool) *Outbox {
	return &Outbox{pool: pool}
}

func (o *Outbox) Enqueue(ctx context.Context, aggregateType, aggregateID, eventType, payload string) (uuid.UUID, error) {
	id := uuid.New()
	_, err := executor(ctx, o.pool).Exec(ctx,
		`INSERT INTO outbox_event(id, aggregate_type, aggregate_id, event_type, payload, occurred_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, now())`,
		id, aggregateType, aggregateID, eventType, payload)
	return id, err
}

func (o *Outbox) Pending(ctx context.Context, limit int) ([]common.OutboxMessage, error) {
	rows, err := executor(ctx, o.pool).Query(ctx,
		`SELECT id, event_type, payload::text, occurred_at, attempts FROM outbox_event
		 WHERE published_at IS NULL AND attempts < $1 AND next_attempt_at <= now()
		 ORDER BY occurred_at LIMIT $2`, common.OutboxMaxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []common.OutboxMessage
	for rows.Next() {
		var m common.OutboxMessage
		if err := rows.Scan(&m.ID, &m.Type, &m.Payload, &m.OccurredAt, &m.Attempts); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (o *Outbox) Published(ctx context.Context, id uuid.UUID, occurredAt time.Time) error {
	_, err := executor(ctx, o.pool).Exec(ctx,
		`UPDATE outbox_event SET published_at = now() WHERE id = $1 AND occurred_at = $2`, id, occurredAt)
	return err
}

func (o *Outbox) Failed(ctx context.Context, id uuid.UUID, occurredAt time.Time, errText string) error {
	if len(errText) > 4000 {
		errText = errText[:4000]
	}
	_, err := executor(ctx, o.pool).Exec(ctx,
		`UPDATE outbox_event SET attempts = attempts + 1, last_error = $3,
		 next_attempt_at = now() + make_interval(secs => least(300, power(2, attempts)::int))
		 WHERE id = $1 AND occurred_at = $2`, id, occurredAt, errText)
	return err
}
