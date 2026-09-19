package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// PendingApprovalRepository implements chat.PendingApprovalRepository
// against pending_approval.
type PendingApprovalRepository struct {
	pool *pgxpool.Pool
}

func NewPendingApprovalRepository(pool *pgxpool.Pool) *PendingApprovalRepository {
	return &PendingApprovalRepository{pool: pool}
}

var _ chat.PendingApprovalRepository = (*PendingApprovalRepository)(nil)

func (r *PendingApprovalRepository) Create(ctx context.Context, p chat.PendingApproval) error {
	var eventID *uuid.UUID
	if p.EventID != nil {
		eventID = &p.EventID.Value
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO pending_approval(id, kind, chat_id, event_id, subject, requested_by, created_at, expires_at, self_confirmable)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		p.ID.Value, string(p.Kind), p.ChatID.Value, eventID, p.Subject,
		p.RequestedBy.Value, p.CreatedAt, p.ExpiresAt, p.SelfConfirmable)
	return err
}

func (r *PendingApprovalRepository) Find(ctx context.Context, id common.RequestID) (*chat.PendingApproval, error) {
	var p chat.PendingApproval
	var kind string
	var eventID *uuid.UUID
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT kind, chat_id, event_id, subject, requested_by, created_at, expires_at, self_confirmable
		   FROM pending_approval WHERE id = $1 AND resolved_at IS NULL`,
		id.Value).Scan(&kind, &p.ChatID.Value, &eventID, &p.Subject,
		&p.RequestedBy.Value, &p.CreatedAt, &p.ExpiresAt, &p.SelfConfirmable)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.ID = id
	p.Kind = chat.ApprovalKind(kind)
	if eventID != nil {
		p.EventID = &common.EventID{Value: *eventID}
	}
	return &p, nil
}

func (r *PendingApprovalRepository) Resolve(ctx context.Context, id common.RequestID) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE pending_approval SET resolved_at = now() WHERE id = $1 AND resolved_at IS NULL`, id.Value)
	return err
}
