package chat

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// AdminAction is one administrative change made to a chat's configuration.
// Administration happens in private messages (see openManagedChat), so a
// co-manager doesn't witness a change happening in the group. This record
// is how they find out who turned something off.
//
// It's a flat, append-only log, not a diff engine. Kind names the surface
// that changed, Detail is the human-readable result, and adding a new
// admin surface never needs a schema change.
type AdminAction struct {
	ChatID    common.ChatID
	ActorID   common.UserID
	ActorName string
	// Kind is a stable machine-readable slug ("subscribe", "locale",
	// "moderator"), used as the i18n key suffix for the label.
	// Detail is already-formatted display text (an event name, a
	// timezone), stored as-is since the thing it names may be gone by
	// the time it's read.
	Kind      string
	Detail    string
	CreatedAt time.Time
}

// AdminActionHistorySize bounds the "recent changes" screen: one screenful
// answering "what just changed", not an audit archive.
const AdminActionHistorySize = 10

// AdminActionLog persists administrative changes per chat. Recording is
// best-effort at every call site: losing a log line must never fail the
// change it describes.
type AdminActionLog interface {
	Record(ctx context.Context, action AdminAction) error
	// Recent returns the newest actions first, at most limit of them.
	Recent(ctx context.Context, chatID common.ChatID, limit int) ([]AdminAction, error)
}
