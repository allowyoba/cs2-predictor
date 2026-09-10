package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// InvitationRepository implements chat.ModeratorInvitationRepository
// against moderator_invitation. Permissions are stored as a single
// comma-joined column rather than a join table: an invitation is a small,
// short-lived, single-row object, not something ever queried by permission.
type InvitationRepository struct {
	pool *pgxpool.Pool
}

func NewInvitationRepository(pool *pgxpool.Pool) *InvitationRepository {
	return &InvitationRepository{pool: pool}
}

func encodePermissions(perms []chat.Permission) string {
	parts := make([]string, len(perms))
	for i, p := range perms {
		parts[i] = string(p)
	}
	return strings.Join(parts, ",")
}

func decodePermissions(raw string) []chat.Permission {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	perms := make([]string, len(parts))
	copy(perms, parts)
	return toPermissions(perms)
}

func (r *InvitationRepository) CreateInvitation(ctx context.Context, inv chat.ModeratorInvitation) error {
	// created_by references telegram_user; the creator (a Telegram admin
	// acting from a group they may never have DMed the bot from before)
	// isn't guaranteed to have a row there yet.
	if err := ensureUser(ctx, executor(ctx, r.pool), inv.CreatedBy); err != nil {
		return err
	}
	_, err := executor(ctx, r.pool).Exec(ctx,
		`INSERT INTO moderator_invitation(token, chat_id, permissions, created_by, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		inv.Token, inv.ChatID.Value, encodePermissions(inv.Permissions), inv.CreatedBy.Value, inv.CreatedAt, inv.ExpiresAt)
	return err
}

func (r *InvitationRepository) Invitation(ctx context.Context, token string) (*chat.ModeratorInvitation, error) {
	var inv chat.ModeratorInvitation
	var chatID, createdBy int64
	var permissions string
	var usedBy *int64
	row := executor(ctx, r.pool).QueryRow(ctx,
		`SELECT token, chat_id, permissions, created_by, created_at, expires_at, used_at, used_by, revoked_at
		   FROM moderator_invitation WHERE token = $1`, token)
	if err := row.Scan(&inv.Token, &chatID, &permissions, &createdBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt, &usedBy, &inv.RevokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	inv.ChatID = common.ChatID{Value: chatID}
	inv.CreatedBy = common.UserID{Value: createdBy}
	inv.Permissions = decodePermissions(permissions)
	if usedBy != nil {
		id := common.UserID{Value: *usedBy}
		inv.UsedBy = &id
	}
	return &inv, nil
}

// UseInvitation's WHERE clause is the compare-and-swap that makes accepting
// an invitation exactly-once under concurrent taps: only a row that is still
// unused and unrevoked matches, so a second accept (or a decline racing an
// accept) affects zero rows instead of overwriting the first acceptor.
func (r *InvitationRepository) UseInvitation(ctx context.Context, token string, usedBy common.UserID, usedAt time.Time) (bool, error) {
	// used_by references telegram_user too; the acceptor may be a brand
	// new user who has never interacted with the bot before tapping the
	// invitation link.
	if err := ensureUser(ctx, executor(ctx, r.pool), usedBy); err != nil {
		return false, err
	}
	tag, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE moderator_invitation SET used_at = $2, used_by = $3
		  WHERE token = $1 AND used_at IS NULL AND revoked_at IS NULL`,
		token, usedAt, usedBy.Value)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *InvitationRepository) RevokeInvitation(ctx context.Context, token string) error {
	_, err := executor(ctx, r.pool).Exec(ctx,
		`UPDATE moderator_invitation SET revoked_at = now() WHERE token = $1 AND used_at IS NULL AND revoked_at IS NULL`,
		token)
	return err
}
