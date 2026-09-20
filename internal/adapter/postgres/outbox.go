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

// Defer pushes a message's next attempt out to a given time without
// touching its attempt count — see common.Outbox.Defer for why the two are
// different things.
func (o *Outbox) Defer(ctx context.Context, id uuid.UUID, occurredAt time.Time, until time.Time) error {
	_, err := executor(ctx, o.pool).Exec(ctx,
		`UPDATE outbox_event SET next_attempt_at = $3 WHERE id = $1 AND occurred_at = $2`,
		id, occurredAt, until)
	return err
}

// DeadLetters and ReplayDeadLetters are the operator's view of the
// messages Pending has given up on: attempts at the ceiling, never
// published, and invisible to every other query here.
func (o *Outbox) DeadLetters(ctx context.Context) ([]common.DeadLetterGroup, error) {
	rows, err := executor(ctx, o.pool).Query(ctx,
		`SELECT event_type, count(*), min(occurred_at), max(occurred_at),
		        coalesce((array_agg(last_error ORDER BY occurred_at DESC))[1], '')
		   FROM outbox_event
		  WHERE published_at IS NULL AND attempts >= $1
		  GROUP BY event_type
		  ORDER BY count(*) DESC, event_type`, common.OutboxMaxAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.DeadLetterGroup
	for rows.Next() {
		var g common.DeadLetterGroup
		if err := rows.Scan(&g.EventType, &g.Count, &g.Oldest, &g.Newest, &g.LastError); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReplayDeadLetters resets the attempt count so the ordinary dispatcher
// picks these up again on its next run. next_attempt_at goes back to now
// rather than staying at whatever the last backoff computed, so a replay
// asked for by a human happens immediately.
func (o *Outbox) ReplayDeadLetters(ctx context.Context) (int, error) {
	tag, err := executor(ctx, o.pool).Exec(ctx,
		`UPDATE outbox_event SET attempts = 0, next_attempt_at = now()
		  WHERE published_at IS NULL AND attempts >= $1`, common.OutboxMaxAttempts)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
