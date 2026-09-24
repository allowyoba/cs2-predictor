package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// TargetSubscriptionRepository implements subscription.TargetRepository
// against target_subscription, mirroring SubscriptionRepository's shape
// (upsert-on-subscribe, soft-delete-on-unsubscribe).
type TargetSubscriptionRepository struct {
	pool *pgxpool.Pool
}

func NewTargetSubscriptionRepository(pool *pgxpool.Pool) *TargetSubscriptionRepository {
	return &TargetSubscriptionRepository{pool: pool}
}

func (r *TargetSubscriptionRepository) SubscribeTarget(ctx context.Context, s subscription.TargetSubscription) (subscription.TargetSubscription, error) {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO target_subscription(chat_id, kind, target_id, target_name, subscribed_at, active)
		 VALUES ($1, $2, $3, $4, $5, true)
		 ON CONFLICT (chat_id, kind, target_id) DO UPDATE
		   SET subscribed_at = excluded.subscribed_at, active = true, target_name = excluded.target_name`,
		s.ChatID.Value, string(s.Kind), s.TargetID, s.TargetName, s.SubscribedAt)
	s.Active = true
	return s, err
}

func (r *TargetSubscriptionRepository) UnsubscribeTarget(ctx context.Context, chatID common.ChatID, kind subscription.TargetKind, targetID int64) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE target_subscription SET active = false WHERE chat_id = $1 AND kind = $2 AND target_id = $3`,
		chatID.Value, string(kind), targetID)
	return err
}

func (r *TargetSubscriptionRepository) TargetSubscriptions(ctx context.Context, chatID common.ChatID) ([]subscription.TargetSubscription, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT chat_id, kind, target_id, target_name, subscribed_at, active
		   FROM target_subscription WHERE chat_id = $1 AND active = true`, chatID.Value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subscription.TargetSubscription
	for rows.Next() {
		var s subscription.TargetSubscription
		var chatVal int64
		var kind string
		if err := rows.Scan(&chatVal, &kind, &s.TargetID, &s.TargetName, &s.SubscribedAt, &s.Active); err != nil {
			return nil, err
		}
		s.ChatID = common.ChatID{Value: chatVal}
		s.Kind = subscription.TargetKind(kind)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *TargetSubscriptionRepository) ChatsForTarget(ctx context.Context, kind subscription.TargetKind, targetID int64) ([]common.ChatID, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT chat_id FROM target_subscription WHERE kind = $1 AND target_id = $2 AND active = true`,
		string(kind), targetID)
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

// TargetCrossSellRepository implements subscription.CrossSellRepository
// against target_cross_sell_offer.
type TargetCrossSellRepository struct {
	pool *pgxpool.Pool
}

func NewTargetCrossSellRepository(pool *pgxpool.Pool) *TargetCrossSellRepository {
	return &TargetCrossSellRepository{pool: pool}
}

// RecordOffer relies on the primary key (chat_id, event_id) to dedupe: a
// second offer for the same tournament, from any target subscription, is
// silently absorbed rather than erroring, and isNew reports which happened.
func (r *TargetCrossSellRepository) RecordOffer(ctx context.Context, o subscription.CrossSellOffer) (bool, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO target_cross_sell_offer(chat_id, event_id, kind, target_id, offered_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (chat_id, event_id) DO NOTHING`,
		o.ChatID.Value, o.EventID.Value, string(o.Kind), o.TargetID, o.OfferedAt)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *TargetCrossSellRepository) MarkDismissed(ctx context.Context, chatID common.ChatID, eventID common.EventID) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE target_cross_sell_offer SET dismissed = true WHERE chat_id = $1 AND event_id = $2`,
		chatID.Value, eventID.Value)
	return err
}

func (r *TargetCrossSellRepository) MarkSubscribed(ctx context.Context, chatID common.ChatID, eventID common.EventID) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE target_cross_sell_offer SET subscribed = true WHERE chat_id = $1 AND event_id = $2`,
		chatID.Value, eventID.Value)
	return err
}
