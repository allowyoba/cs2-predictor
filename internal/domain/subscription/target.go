package subscription

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// TargetKind distinguishes a team subscription from a player subscription.
// Both follow the same shape (a chat, a target, an active flag) so they
// share one table and one repository rather than two near-identical ones.
type TargetKind string

const (
	TargetTeam   TargetKind = "TEAM"
	TargetPlayer TargetKind = "PLAYER"
)

// TargetSubscription is a chat's subscription to a team or player, across
// whatever tournaments that team/player plays in — unlike EventSubscription
// it is not scoped to one event. TargetID is a common.TeamID.Value for
// TargetTeam and a player's external/provider id for TargetPlayer (there is
// no domain Player entity yet — see PlayerRef).
type TargetSubscription struct {
	ChatID       common.ChatID
	Kind         TargetKind
	TargetID     int64
	TargetName   string
	SubscribedAt time.Time
	Active       bool
}

// TargetRepository is the persistence port for team/player subscriptions,
// separate from Repository (tournament subscriptions) since the two are
// independent concerns that only meet in the scoping logic below.
type TargetRepository interface {
	SubscribeTarget(ctx context.Context, s TargetSubscription) (TargetSubscription, error)
	UnsubscribeTarget(ctx context.Context, chatID common.ChatID, kind TargetKind, targetID int64) error
	TargetSubscriptions(ctx context.Context, chatID common.ChatID) ([]TargetSubscription, error)
	// ChatsForTarget returns chats actively subscribed to the given
	// team/player — the counterpart of Repository.SubscribedChats, used to
	// fan out team/player-scoped notifications and cross-sell prompts.
	ChatsForTarget(ctx context.Context, kind TargetKind, targetID int64) ([]common.ChatID, error)
}

// CrossSellOffer records that a chat has already been offered a one-tap
// tournament-subscribe prompt for a given tournament, discovered through a
// given team/player subscription — so the offer fires once per
// (chat, tournament, target) rather than every time new matches sync in.
type CrossSellOffer struct {
	ChatID       common.ChatID
	EventID      common.EventID
	Kind         TargetKind
	TargetID     int64
	OfferedAt    time.Time
	Dismissed    bool
	Subscribed   bool
}

// CrossSellRepository tracks which cross-sell offers have already been
// made, so the notification path (which reuses notification_prefs.go's
// switchboard for the on/off gate) can dedupe.
type CrossSellRepository interface {
	// RecordOffer inserts the offer if one doesn't already exist for this
	// (chat, event, kind, target) and reports whether it is new — the
	// caller only sends the Telegram prompt when it is.
	RecordOffer(ctx context.Context, o CrossSellOffer) (isNew bool, err error)
	MarkDismissed(ctx context.Context, chatID common.ChatID, eventID common.EventID) error
	MarkSubscribed(ctx context.Context, chatID common.ChatID, eventID common.EventID) error
}
