package telegram

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// dismissCrossSell handles the "✕" button on a target cross-sell offer
// (see internal/app/target_cross_sell.go): marks the offer dismissed so
// nothing re-offers it, and clears the prompt in place. The subscribe half
// of this same offer deliberately reuses the plain "subscribe:<eventID>"
// route (h.subscribe) rather than a bespoke one — it is the exact same
// action a chat takes from the events list.
func (h *UpdateHandler) dismissCrossSell(ctx context.Context, target replyTarget, settings chat.Settings, idStr string) (bool, error) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return false, newValidationError("invalid event id")
	}
	eventID := common.EventID{Value: id}
	if h.CrossSell != nil {
		if err := h.CrossSell.MarkDismissed(ctx, settings.ChatID, eventID); err != nil {
			return false, err
		}
	}
	return true, h.respond(ctx, target, h.Texts.Get("crosssell.dismissed", settings.Locale), nil)
}

// crossSellDismissPrefix is the callback data prefix for dismissCrossSell,
// shared with the publisher that renders the offer's button.
const crossSellDismissPrefix = "crosssell:dismiss:"

func trimCrossSellDismiss(data string) string {
	return strings.TrimPrefix(data, crossSellDismissPrefix)
}
