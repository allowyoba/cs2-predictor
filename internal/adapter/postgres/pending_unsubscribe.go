package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// PendingUnsubscribeRepository implements chat.PendingUnsubscribeRepository
// against pending_unsubscribe.
type PendingUnsubscribeRepository struct {
	pool *pgxpool.Pool
}

func NewPendingUnsubscribeRepository(pool *pgxpool.Pool) *PendingUnsubscribeRepository {
	return &PendingUnsubscribeRepository{pool: pool}
}

var _ chat.PendingUnsubscribeRepository = (*PendingUnsubscribeRepository)(nil)

func (r *PendingUnsubscribeRepository) Create(ctx context.Context, p chat.PendingUnsubscribe) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO pending_unsubscribe(id, chat_id, event_id, requested_by, created_at, expires_at, self_confirmable)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		p.ID.Value, p.ChatID.Value, p.EventID.Value, p.RequestedBy.Value, p.CreatedAt, p.ExpiresAt, p.SelfConfirmable)
	return err
}

func (r *PendingUnsubscribeRepository) Find(ctx context.Context, id common.RequestID) (*chat.PendingUnsubscribe, error) {
	var p chat.PendingUnsubscribe
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT chat_id, event_id, requested_by, created_at, expires_at, self_confirmable
		   FROM pending_unsubscribe WHERE id = $1 AND resolved_at IS NULL`,
		id.Value).Scan(&p.ChatID.Value, &p.EventID.Value, &p.RequestedBy.Value, &p.CreatedAt, &p.ExpiresAt, &p.SelfConfirmable)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.ID = id
	return &p, nil
}

func (r *PendingUnsubscribeRepository) Resolve(ctx context.Context, id common.RequestID) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE pending_unsubscribe SET resolved_at = now() WHERE id = $1 AND resolved_at IS NULL`, id.Value)
	return err
}
