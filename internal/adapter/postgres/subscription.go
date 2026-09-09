package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// SubscriptionRepository implements subscription.Repository against
// event_subscription. Unsubscribe soft-deletes (active=false on the
// existing row) rather than deleting it, preserving subscription history.
type SubscriptionRepository struct {
	pool *pgxpool.Pool
}

func NewSubscriptionRepository(pool *pgxpool.Pool) *SubscriptionRepository {
	return &SubscriptionRepository{pool: pool}
}

// Subscribe is a full upsert — re-subscribing overwrites subscribed_at too.
func (r *SubscriptionRepository) Subscribe(ctx context.Context, s subscription.EventSubscription) (subscription.EventSubscription, error) {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO event_subscription(chat_id, event_id, subscribed_at, active) VALUES ($1, $2, $3, true)
		 ON CONFLICT (chat_id, event_id) DO UPDATE SET subscribed_at = excluded.subscribed_at, active = true`,
		s.ChatID.Value, s.EventID.Value, s.SubscribedAt)
	s.Active = true
	return s, err
}

// Unsubscribe is a no-op if the row doesn't exist — no error, nothing to
// soft-delete.
func (r *SubscriptionRepository) Unsubscribe(ctx context.Context, chatID common.ChatID, eventID common.EventID) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE event_subscription SET active = false WHERE chat_id = $1 AND event_id = $2`, chatID.Value, eventID.Value)
	return err
}

func (r *SubscriptionRepository) SubscribedChats(ctx context.Context, eventID common.EventID) ([]common.ChatID, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT chat_id FROM event_subscription WHERE event_id = $1 AND active = true`, eventID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.ChatID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, common.ChatID{Value: id})
	}
	return out, rows.Err()
}

// ActiveEventIDs deliberately does NOT filter by event status: a match sync
// still needs to run for an event that has just flipped to FINISHED, so its
// last match's terminal status/score gets persisted (settlement and
// EventCompletionService depend on it) before the event stops being fetched
// via subscription.Repository.Subscriptions, which does apply that filter.
func (r *SubscriptionRepository) ActiveEventIDs(ctx context.Context) ([]common.EventID, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT DISTINCT event_id FROM event_subscription WHERE active = true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []common.EventID
	for rows.Next() {
		var id [16]byte
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, common.EventID{Value: id})
	}
	return out, rows.Err()
}

func (r *SubscriptionRepository) Subscriptions(ctx context.Context, chatID common.ChatID) ([]subscription.EventSubscription, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT s.chat_id, s.event_id, s.subscribed_at, s.active
		   FROM event_subscription s
		   JOIN tournament_event e ON e.id = s.event_id
		  WHERE s.chat_id = $1
		    AND s.active = true
		    AND e.status IN ('UPCOMING', 'RUNNING')
		    AND (e.ends_at IS NULL OR e.ends_at > now())
		  ORDER BY e.starts_at ASC NULLS LAST, e.name ASC`, chatID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subscription.EventSubscription
	for rows.Next() {
		var s subscription.EventSubscription
		var chatVal int64
		var eventVal [16]byte
		if err := rows.Scan(&chatVal, &eventVal, &s.SubscribedAt, &s.Active); err != nil {
			return nil, err
		}
		s.ChatID = common.ChatID{Value: chatVal}
		s.EventID = common.EventID{Value: eventVal}
		out = append(out, s)
	}
	return out, rows.Err()
}
