package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// AdminActionRepository stores the per-chat history of administrative
// changes surfaced by the "recent changes" screen.
type AdminActionRepository struct {
	pool *pgxpool.Pool
}

func NewAdminActionRepository(pool *pgxpool.Pool) *AdminActionRepository {
	return &AdminActionRepository{pool: pool}
}

var _ chat.AdminActionLog = (*AdminActionRepository)(nil)

// Record appends one action. created_at is left to the database default so
// history ordering comes from a single clock, not from whichever process
// happened to handle the update.
func (r *AdminActionRepository) Record(ctx context.Context, action chat.AdminAction) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO admin_action_log (chat_id, actor_id, actor_name, kind, detail)
		 VALUES ($1, $2, $3, $4, $5)`,
		action.ChatID.Value, action.ActorID.Value, action.ActorName, action.Kind, action.Detail)
	return err
}

func (r *AdminActionRepository) Recent(ctx context.Context, chatID common.ChatID, limit int) ([]chat.AdminAction, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT actor_id, actor_name, kind, detail, created_at
		   FROM admin_action_log
		  WHERE chat_id = $1
		  ORDER BY created_at DESC, id DESC
		  LIMIT $2`, chatID.Value, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actions []chat.AdminAction
	for rows.Next() {
		action := chat.AdminAction{ChatID: chatID}
		if err := rows.Scan(&action.ActorID.Value, &action.ActorName, &action.Kind, &action.Detail, &action.CreatedAt); err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}
