package chat

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// ApprovalKind names what a pending request would do once approved. The
// values are persisted, so they must stay stable.
type ApprovalKind string

const (
	// ApprovalUnsubscribe removes a tournament subscription.
	ApprovalUnsubscribe ApprovalKind = "unsubscribe"
	// ApprovalDisableGame switches a whole game off for the chat. Guarded
	// for the same reason unsubscribing is, only more so: one tap silences
	// every tournament of that game at once, and the polls simply stop
	// appearing — nothing about the chat says why.
	ApprovalDisableGame ApprovalKind = "disable_game"
)

// PendingApproval is a requested-but-not-yet-confirmed action awaiting
// approval from another manager of the chat. The request appears in the
// requester's own private chat with the bot and is fanned out from there to
// every other manager who can approve it (see PendingApprovalRepository and
// the fan-out logic that creates one).
//
// One type for every guarded action rather than one per action: the whole
// mechanism — the request, the DMs, the TTL, the "not by the same person"
// rule — is identical regardless of what is being approved, and only the
// sentence shown to the approver differs.
type PendingApproval struct {
	ID     common.RequestID
	Kind   ApprovalKind
	ChatID common.ChatID
	// EventID is set for ApprovalUnsubscribe and nil otherwise.
	EventID *common.EventID
	// Subject carries what the action is about when it is not an event —
	// the game code for ApprovalDisableGame.
	Subject     string
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

// PendingApprovalTTL bounds how long a pending confirmation stays valid:
// long enough for another manager to notice a DM, short enough that a stale
// request can't execute much later without anyone expecting it.
const PendingApprovalTTL = 24 * time.Hour

// Expired reports whether the request is too old to act on. Checked at
// resolution time, so a request nobody got to in time fails closed and
// must be re-requested rather than silently executing much later.
func (p PendingApproval) Expired(now time.Time) bool {
	return !now.Before(p.ExpiresAt)
}

// PendingApprovalRepository persists pending approval requests.
type PendingApprovalRepository interface {
	Create(ctx context.Context, p PendingApproval) error
	// Find returns (nil, nil) for an unknown or already-resolved id.
	Find(ctx context.Context, id common.RequestID) (*PendingApproval, error)
	// Resolve marks the request resolved so a second tap on the same
	// button (by the same or a different manager) is a no-op rather than a
	// double action. Idempotent: resolving an already-resolved or unknown
	// id is not an error.
	Resolve(ctx context.Context, id common.RequestID) error
}
