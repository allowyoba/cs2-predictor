package chat

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// PendingUnsubscribe is a requested-but-not-yet-confirmed removal of an
// event subscription, awaiting approval from another manager of the chat.
// The request appears only in the requester's own private chat with the
// bot, fanned out from there to every other manager who can approve it
// (see PendingUnsubscribeRepository and the fan-out logic that creates
// one).
type PendingUnsubscribe struct {
	ID          common.RequestID
	ChatID      common.ChatID
	EventID     common.EventID
	RequestedBy common.UserID
	CreatedAt   time.Time
	ExpiresAt   time.Time
	// SelfConfirmable is true when the requester's own confirmation is
	// enough: either there was no other manager to ask, or every other
	// manager turned out to be unreachable by DM (Telegram refuses a
	// bot's first message to a user who has never started a chat with
	// it). If false, at least one other manager was actually notified,
	// and the requester confirming their own request must be rejected.
	SelfConfirmable bool
}

// PendingUnsubscribeTTL bounds how long a pending confirmation stays
// valid: long enough for another manager to notice a DM, short enough
// that a stale request can't execute a removal much later without
// anyone expecting it.
const PendingUnsubscribeTTL = 24 * time.Hour

// Expired reports whether the request is too old to act on. Checked at
// resolution time, so a request nobody got to in time fails closed and
// must be re-requested rather than silently executing much later.
func (p PendingUnsubscribe) Expired(now time.Time) bool {
	return !now.Before(p.ExpiresAt)
}

// PendingUnsubscribeRepository persists pending unsubscribe requests.
type PendingUnsubscribeRepository interface {
	Create(ctx context.Context, p PendingUnsubscribe) error
	// Find returns (nil, nil) for an unknown or already-resolved id.
	Find(ctx context.Context, id common.RequestID) (*PendingUnsubscribe, error)
	// Resolve marks the request resolved so a second tap on the same
	// button (by the same or a different manager) is a no-op rather than a
	// double action. Idempotent: resolving an already-resolved or unknown
	// id is not an error.
	Resolve(ctx context.Context, id common.RequestID) error
}
