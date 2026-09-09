package telegram

import (
	"context"
	"strings"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// subscribedEventsPageSize bounds the "my tournaments" picker so the
// navigation and cleanup action remain reachable without excessive scroll.
const subscribedEventsPageSize = 10

// splitByLiveness separates subscriptions whose tournament can still
// produce a poll from those whose cannot. A subscription whose event is no
// longer in the catalog at all counts as finished: nothing will ever come
// of it either, and leaving it unclassified would make it unclearable.
func splitByLiveness(subs []subscription.EventSubscription, byID map[common.EventID]competition.Event, now time.Time) (live, finished []subscription.EventSubscription) {
	for _, s := range subs {
		event, ok := byID[s.EventID]
		if ok && event.IsSubscribable(now) {
			live = append(live, s)
			continue
		}
		finished = append(finished, s)
	}
	return live, finished
}

// finishedSubscriptions re-derives the finished set at action time rather
// than trusting a count encoded in a button: the list may have been
// rendered days ago, and a tournament that has finished since must be
// included rather than silently left behind.
func (h *UpdateHandler) finishedSubscriptions(ctx context.Context, chatID common.ChatID) ([]subscription.EventSubscription, map[common.EventID]competition.Event, error) {
	subs, err := h.Subscriptions.Subscriptions(ctx, chatID)
	if err != nil {
		return nil, nil, err
	}
	byID, err := h.eventsByID(ctx, subs)
	if err != nil {
		return nil, nil, err
	}
	_, finished := splitByLiveness(subs, byID, h.Clock.Now())
	return finished, byID, nil
}

// cleanupMenu confirms the bulk removal of finished tournaments, naming
// every one of them.
//
// Unlike the per-event unsubscribe, this deliberately does NOT go through
// the second-manager confirmation flow. That safeguard exists so one
// manager cannot silently stop a running tournament's polls; a finished
// tournament has no poll left to stop, so the safeguard would only be
// friction — and friction is exactly why these pile up in the first place.
func (h *UpdateHandler) cleanupMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	finished, byID, err := h.finishedSubscriptions(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	back := []InlineButton{h.backButton(settings.Locale, "events:mine")}
	if len(finished) == 0 {
		return h.respond(ctx, target, h.Texts.Get("events.cleanup_none", settings.Locale), &InlineKeyboard{InlineKeyboard: [][]InlineButton{back}})
	}

	names := make([]string, 0, len(finished))
	for _, s := range finished {
		name := s.EventID.Value.String()
		if event, ok := byID[s.EventID]; ok {
			name = event.Name
		}
		names = append(names, "• "+escapeHTML(truncate(name, 40)))
	}
	text := h.Texts.Get("events.cleanup_confirm", settings.Locale, len(finished)) + "\n\n" + strings.Join(names, "\n")
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("events.cleanup_apply", settings.Locale), "events:cleanup:go")},
		back,
	}}
	return h.respond(ctx, target, text, &kb)
}

func (h *UpdateHandler) cleanupFinished(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings) (bool, error) {
	finished, byID, err := h.finishedSubscriptions(ctx, settings.ChatID)
	if err != nil {
		return false, err
	}
	if len(finished) == 0 {
		return false, h.subscribedEvents(ctx, target, settings, true)
	}

	removed := 0
	for _, s := range finished {
		if err := h.Subscriptions.Unsubscribe(ctx, settings.ChatID, s.EventID); err != nil {
			loggerFrom(ctx, h.Log).Warn("finished subscription cleanup failed", "chatId", settings.ChatID.Value, "eventId", s.EventID.Value, "error", err)
			continue
		}
		removed++
		name := s.EventID.Value.String()
		if event, ok := byID[s.EventID]; ok {
			name = event.Name
		}
		h.logAdminAction(ctx, settings.ChatID, &cb.From, "unsubscribe", name)
	}
	h.recordAdminAction("cleanup_finished", "applied")

	if err := h.toast(ctx, cb.ID, h.Texts.Get("events.cleanup_done", settings.Locale, removed)); err != nil {
		return false, err
	}
	return true, h.subscribedEvents(ctx, target, settings, true)
}
