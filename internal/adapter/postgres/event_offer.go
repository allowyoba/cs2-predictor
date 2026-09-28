package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// EventOfferRepository implements subscription.EventOfferRepository against
// event_offer.
type EventOfferRepository struct {
	pool *pgxpool.Pool
}

func NewEventOfferRepository(pool *pgxpool.Pool) *EventOfferRepository {
	return &EventOfferRepository{pool: pool}
}

// RecordEventOffer leans on the primary key for the claim: the insert either
// wins the pair or is absorbed, and RowsAffected says which — the same
// cheap atomic claim TargetCrossSellRepository.RecordOffer makes.
func (r *EventOfferRepository) RecordEventOffer(ctx context.Context, chatID common.ChatID, eventID common.EventID,
	kind subscription.EventOfferKind, at time.Time) (bool, error) {
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO event_offer(chat_id, event_id, kind, decided_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (chat_id, event_id) DO NOTHING`,
		chatID.Value, eventID.Value, string(kind), at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *EventOfferRepository) UndecidedEvents(ctx context.Context, chatID common.ChatID,
	candidates []common.EventID) ([]common.EventID, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	ids := make([][16]byte, len(candidates))
	for i, id := range candidates {
		ids[i] = id.Value
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT c.id FROM unnest($2::uuid[]) AS c(id)
		  WHERE NOT EXISTS (
		        SELECT 1 FROM event_offer o WHERE o.chat_id = $1 AND o.event_id = c.id)`,
		chatID.Value, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Keyed rather than appended in row order: the caller's own ordering is
	// what decides which tournaments make this run's cap, and Postgres is
	// under no obligation to preserve the array's order here.
	open := map[common.EventID]bool{}
	for rows.Next() {
		var id [16]byte
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		open[common.EventID{Value: id}] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]common.EventID, 0, len(open))
	for _, id := range candidates {
		if open[id] {
			out = append(out, id)
		}
	}
	return out, nil
}
