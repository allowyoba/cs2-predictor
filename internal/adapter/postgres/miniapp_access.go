package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// Mini App access lives on the chat repository: it is about a person's
// standing with the bot, which is what everything else on this repository
// is about too.

func (r *ChatRepository) MiniAppAccess(ctx context.Context, userID common.UserID) (*chat.MiniAppAccess, error) {
	var access chat.MiniAppAccess
	var decidedBy *int64
	err := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT user_id, status, requested_at, decided_at, decided_by
		   FROM miniapp_access WHERE user_id = $1`, userID.Value).
		Scan(&access.UserID.Value, &access.Status, &access.RequestedAt, &access.DecidedAt, &decidedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if decidedBy != nil {
		access.DecidedBy = &common.UserID{Value: *decidedBy}
	}
	return &access, nil
}

// RequestMiniAppAccess is idempotent while a request is pending: tapping
// again must not push somebody back down the queue, and must not reopen a
// decision that was already made.
func (r *ChatRepository) RequestMiniAppAccess(ctx context.Context, userID common.UserID, at time.Time) error {
	if err := r.ensureUser(ctx, userID); err != nil {
		return err
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO miniapp_access(user_id, status, requested_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO NOTHING`,
		userID.Value, string(chat.MiniAppPending), at)
	return err
}

func (r *ChatRepository) DecideMiniAppAccess(ctx context.Context, userID common.UserID,
	status chat.MiniAppStatus, decidedBy common.UserID, at time.Time) error {
	if err := r.ensureUser(ctx, userID); err != nil {
		return err
	}
	if err := r.ensureUser(ctx, decidedBy); err != nil {
		return err
	}
	// A decision can also be made for somebody who never asked — an
	// operator granting access ahead of time — so this inserts as well as
	// updates.
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO miniapp_access(user_id, status, requested_at, decided_at, decided_by)
		 VALUES ($1, $2, $4, $4, $3)
		 ON CONFLICT (user_id) DO UPDATE
		   SET status = excluded.status, decided_at = excluded.decided_at, decided_by = excluded.decided_by`,
		userID.Value, string(status), decidedBy.Value, at)
	return err
}

func (r *ChatRepository) PendingMiniAppRequests(ctx context.Context, limit int) ([]chat.MiniAppAccess, error) {
	rows, err := executor(ctx, r.pool).Query(ctx,
		`SELECT user_id, status, requested_at, decided_at, decided_by
		   FROM miniapp_access WHERE status = $1
		  ORDER BY requested_at ASC LIMIT $2`, string(chat.MiniAppPending), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chat.MiniAppAccess
	for rows.Next() {
		var access chat.MiniAppAccess
		var decidedBy *int64
		if err := rows.Scan(&access.UserID.Value, &access.Status, &access.RequestedAt, &access.DecidedAt, &decidedBy); err != nil {
			return nil, err
		}
		if decidedBy != nil {
			access.DecidedBy = &common.UserID{Value: *decidedBy}
		}
		out = append(out, access)
	}
	return out, rows.Err()
}
