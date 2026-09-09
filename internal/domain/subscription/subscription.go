// Package subscription tracks which chats are subscribed to which
// tournament events — pure data/port, no business logic.
package subscription

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

type EventSubscription struct {
	ChatID       common.ChatID
	EventID      common.EventID
	SubscribedAt time.Time
	Active       bool
}

// Repository is the persistence port. Unsubscribe soft-deletes (sets
// active=false on the existing row rather than deleting it) to preserve
// subscription history — the Postgres adapter must replicate that, not a
// hard delete.
type Repository interface {
	Subscribe(ctx context.Context, sub EventSubscription) (EventSubscription, error)
	Unsubscribe(ctx context.Context, chatID common.ChatID, eventID common.EventID) error
	SubscribedChats(ctx context.Context, eventID common.EventID) ([]common.ChatID, error)
	ActiveEventIDs(ctx context.Context) ([]common.EventID, error)
	Subscriptions(ctx context.Context, chatID common.ChatID) ([]EventSubscription, error)
}
