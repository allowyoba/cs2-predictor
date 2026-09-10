package chat

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// ModeratorInvitationTTL is how long a one-time invitation link stays
// acceptable after creation.
const ModeratorInvitationTTL = 72 * time.Hour

// ModeratorInvitation is a one-time, revocable deep link that hands a chosen
// set of permissions to whoever accepts it — the "🔗 Создать приглашение"
// assignment method, for a person the admin can't (or doesn't want to)
// pick from a reply or a participant list.
type ModeratorInvitation struct {
	Token       string
	ChatID      common.ChatID
	Permissions []Permission
	CreatedBy   common.UserID
	CreatedAt   time.Time
	ExpiresAt   time.Time
	UsedAt      *time.Time
	UsedBy      *common.UserID
	RevokedAt   *time.Time
}

// Status classifies an invitation for display and for accept-time
// validation — computed rather than stored, so "expired" needs no
// background job to become true.
type InvitationStatus string

const (
	InvitationPending InvitationStatus = "pending"
	InvitationUsed    InvitationStatus = "used"
	InvitationRevoked InvitationStatus = "revoked"
	InvitationExpired InvitationStatus = "expired"
)

func (inv ModeratorInvitation) Status(now time.Time) InvitationStatus {
	switch {
	case inv.RevokedAt != nil:
		return InvitationRevoked
	case inv.UsedAt != nil:
		return InvitationUsed
	case now.After(inv.ExpiresAt):
		return InvitationExpired
	default:
		return InvitationPending
	}
}

// ModeratorInvitationRepository persists moderator invitation links. Kept
// separate from Repository so the (small) set of callers that deal with
// invitations don't force every chat.Repository implementation — including
// test fakes — to grow methods it never uses.
type ModeratorInvitationRepository interface {
	CreateInvitation(ctx context.Context, invitation ModeratorInvitation) error
	Invitation(ctx context.Context, token string) (*ModeratorInvitation, error)
	// UseInvitation atomically marks the invitation used by usedBy at usedAt,
	// only if it is still pending — the one-time-use guarantee. ok is false
	// (with no error) if the invitation was already used, revoked, or
	// doesn't exist, so callers can't double-spend it under a race.
	UseInvitation(ctx context.Context, token string, usedBy common.UserID, usedAt time.Time) (ok bool, err error)
	RevokeInvitation(ctx context.Context, token string) error
}
