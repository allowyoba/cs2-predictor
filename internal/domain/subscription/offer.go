package subscription

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// EventOfferKind is what was decided for one (chat, tournament) pair: the
// chat was asked whether to add it, or the chat auto-subscribes to that game
// and was simply joined. Both close the question, so both are recorded — the
// point of the record is that the chat is never asked twice, not which of
// the two answers it got.
type EventOfferKind string

const (
	EventOffered        EventOfferKind = "OFFERED"
	EventAutoSubscribed EventOfferKind = "AUTO_SUBSCRIBED"
)

// EventOfferRepository remembers which tournaments a chat has already been
// told about. Separate from Repository (what a chat actually follows) because
// the two answer different questions: a chat that declined an offer follows
// nothing and must still never be asked again.
type EventOfferRepository interface {
	// RecordEventOffer closes the question for this pair and reports whether
	// it was still open. Two instances racing on the same pair both call
	// this; only one gets true, and only that one sends the message.
	RecordEventOffer(ctx context.Context, chatID common.ChatID, eventID common.EventID, kind EventOfferKind, at time.Time) (isNew bool, err error)
	// UndecidedEvents narrows candidates to the ones this chat has not been
	// told about yet — the whole run's question in one query rather than one
	// per tournament.
	UndecidedEvents(ctx context.Context, chatID common.ChatID, candidates []common.EventID) ([]common.EventID, error)
}
