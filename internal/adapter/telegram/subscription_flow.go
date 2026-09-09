package telegram

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// Adding and removing a chat's tournament subscriptions, including the
// two-manager confirmation dance that guards a removal.

// --- subscribe / unsubscribe / event-topic ---

func (h *UpdateHandler) subscribe(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, idStr string) (bool, error) {
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
		return false, err
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return false, newValidationError("invalid event id")
	}
	eventID := common.EventID{Value: id}
	event, err := h.Catalog.FindEvent(ctx, eventID)
	if err != nil {
		return false, err
	}
	if event == nil {
		return false, nil
	}
	if !event.IsSubscribable(h.Clock.Now()) {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("events.finished", settings.Locale))
	}
	current, err := h.Subscriptions.Subscriptions(ctx, settings.ChatID)
	if err != nil {
		return false, err
	}
	for _, existing := range current {
		if existing.EventID == eventID {
			return true, h.toast(ctx, cb.ID, h.Texts.Get("events.already_added", settings.Locale))
		}
	}

	sub := subscription.EventSubscription{ChatID: settings.ChatID, EventID: eventID, SubscribedAt: h.Clock.Now(), Active: true}
	if _, err := h.Subscriptions.Subscribe(ctx, sub); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "subscribe", event.Name)

	topic, err := h.Chats.EventTopic(ctx, settings.ChatID, eventID)
	if err != nil {
		return false, err
	}
	if topic == nil {
		topic = settings.DefaultTopicID
	}
	if topic == nil && cb.Message != nil {
		topic = cb.Message.MessageThreadID
	}

	matches, err := h.Catalog.FindUnstartedMatches(ctx, eventID)
	if err != nil {
		return false, err
	}
	sent := 0
	for _, m := range matches {
		if _, err := h.Predictions.Create(ctx, m, settings.ChatID, topic); err != nil {
			loggerFrom(ctx, h.Log).Warn("poll creation failed", "chatId", settings.ChatID.Value, "matchId", m.ID.Value, "error", err)
			continue
		}
		sent++
	}

	text := h.Texts.Get("events.subscribed", settings.Locale, escapeHTML(event.Name))
	if sent > 0 {
		text += "\n" + h.Texts.Get("events.polls_created", settings.Locale, sent)
	} else {
		text += "\n" + h.Texts.Get("events.polls_later", settings.Locale)
	}
	// Adding tournaments comes in runs. "Add another" returns to a list
	// that already excludes what was just added, instead of leaving the
	// person to navigate back from the main menu for each one.
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("events.add_another", settings.Locale), "events:add")},
		{button(h.Texts.Get("events.mine", settings.Locale), "events:mine")},
	}}
	return false, h.respond(ctx, target, text, &kb)
}

// unsubscribe starts a removal request, reached only from a DM (see
// subscribedEvents's dmContext gating), and fans it out as a DM
// confirmation request to every other manager of the chat (see
// enqueueConfirmationRequests). When there's no other manager, or none of
// them are reachable by DM yet (Telegram refuses a bot's first message to
// a user who hasn't started a chat with it), the requester's own
// confirmation is accepted instead of blocking the action on a safeguard
// nobody can actually satisfy.
func (h *UpdateHandler) unsubscribe(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, idStr string) (bool, error) {
	actor := common.UserID{Value: cb.From.ID}
	if err := h.requireManager(ctx, settings.ChatID, actor); err != nil {
		return false, err
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return false, newValidationError("invalid event id")
	}
	eventID := common.EventID{Value: id}
	event, err := h.Catalog.FindEvent(ctx, eventID)
	if err != nil {
		return false, err
	}
	name := idStr
	if event != nil {
		name = event.Name
	}

	others, err := h.Authorization.OtherManagers(ctx, settings.ChatID, actor)
	if err != nil {
		return false, err
	}
	// Only managers the bot may actually DM can be asked: Telegram forbids a
	// bot's first message to a user who has never started a chat with it, so
	// counting on someone unreachable would leave the request permanently
	// unconfirmable.
	reachable, err := h.Chats.FilterDMReachable(ctx, others)
	if err != nil {
		return false, err
	}

	now := h.Clock.Now()
	requestID := common.NewRequestID()
	pending := chat.PendingUnsubscribe{
		ID: requestID, ChatID: settings.ChatID, EventID: eventID, RequestedBy: actor,
		CreatedAt: now, ExpiresAt: now.Add(chat.PendingUnsubscribeTTL),
		SelfConfirmable: len(reachable) == 0,
	}
	// The request and the messages asking about it commit together: a
	// confirmation button must never exist for a request that was never
	// stored, nor a stored request nobody was ever asked about.
	if err := h.inTx(ctx, func(ctx context.Context) error {
		if err := h.PendingUnsubscribes.Create(ctx, pending); err != nil {
			return err
		}
		return h.enqueueConfirmationRequests(ctx, reachable, settings, name, requestID, cb.From.DisplayName())
	}); err != nil {
		return false, err
	}

	token := requestID.String()
	var text string
	var kb InlineKeyboard
	if pending.SelfConfirmable {
		explanation := h.Texts.Get("events.unsubscribe_self", settings.Locale)
		if len(others) > 0 {
			explanation = h.Texts.Get("events.unsubscribe_unreachable", settings.Locale)
		}
		text = h.Texts.Get("events.unsubscribe_request", settings.Locale, bold(escapeHTML(name)), explanation)
		kb = InlineKeyboard{InlineKeyboard: [][]InlineButton{
			{button(h.Texts.Get("events.unsubscribe_confirm", settings.Locale), "unsubok:"+token)},
			{h.backButton(settings.Locale, "events:mine")},
		}}
	} else {
		text = h.Texts.Get("events.unsubscribe_pending", settings.Locale, bold(escapeHTML(name)))
		// Withdrawing is the requester's own reject: it resolves the request
		// so the buttons already sitting in other managers' DMs stop being
		// live decisions, instead of leaving them to expire in a day.
		kb = InlineKeyboard{InlineKeyboard: [][]InlineButton{
			{button(h.Texts.Get("events.unsubscribe_withdraw", settings.Locale), "unsubreject:"+token)},
			{h.backButton(settings.Locale, "events:mine")},
		}}
	}
	return false, h.respond(ctx, target, text, &kb)
}

// enqueueConfirmationRequests hands one confirmation request per manager to
// the outbox. Delivery (and the "this person turned out to be unreachable
// after all" bookkeeping) belongs to UnsubscribeConfirmationPublisher; all
// this does is record who should be asked, inside the caller's transaction.
func (h *UpdateHandler) enqueueConfirmationRequests(ctx context.Context, recipients []common.UserID, settings chat.Settings, eventName string, requestID common.RequestID, requester string) error {
	if h.Outbox == nil {
		return nil
	}
	for _, userID := range recipients {
		payload, err := json.Marshal(common.UnsubscribeConfirmationNotification{
			UserID: userID.Value, RequestID: requestID.String(),
			ChatTitle: settings.Title, EventName: eventName,
			Requester: requester, Locale: string(settings.Locale),
		})
		if err != nil {
			return err
		}
		if _, err := h.Outbox.Enqueue(ctx, "chat", settings.ChatID.String(), "telegram.unsubscribe-confirmation", string(payload)); err != nil {
			return err
		}
	}
	return nil
}

// confirmUnsubscribe and rejectUnsubscribe are reached from ANY manager's
// own DM — the requester's self-confirm button, or another manager's
// fanned-out confirm/reject buttons — so, unlike every other callback
// handler in this file, they deliberately do NOT use the settings routed to
// them by their caller (routeCallback derives it from the tapping user's
// own context, which for a fanned-out confirmation is irrelevant — the
// chat being acted on is whichever one the pending request itself names).
// Both resolve settings from pending.ChatID directly instead.

// eventsExit closes out a resolved unsubscribe request: a way back to the
// subscription list rather than leaving the person in a private chat
// with nothing to tap.
func (h *UpdateHandler) eventsExit(locale common.LocaleCode) *InlineKeyboard {
	return &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("events.mine", locale), "events:mine")},
	}}
}

func (h *UpdateHandler) confirmUnsubscribe(ctx context.Context, cb *CallbackQuery, requestIDStr string) (bool, error) {
	actor := common.UserID{Value: cb.From.ID}
	requestID, err := common.ParseRequestID(requestIDStr)
	if err != nil {
		return false, newValidationError("invalid unsubscribe request id")
	}
	pending, err := h.PendingUnsubscribes.Find(ctx, requestID)
	if err != nil {
		return false, err
	}
	if pending == nil || pending.Expired(h.Clock.Now()) {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("events.unsubscribe_expired", common.LocaleRU))
	}
	settings, err := h.Chats.Find(ctx, pending.ChatID)
	if err != nil {
		return false, err
	}
	if settings == nil {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("events.unsubscribe_expired", common.LocaleRU))
	}
	if err := h.requireManager(ctx, pending.ChatID, actor); err != nil {
		return false, err
	}
	if !pending.SelfConfirmable && actor == pending.RequestedBy {
		h.recordAdminAction("unsubscribe", "needs_second")
		return true, h.toast(ctx, cb.ID, h.Texts.Get("events.unsubscribe_need_second", settings.Locale))
	}

	if err := h.Subscriptions.Unsubscribe(ctx, pending.ChatID, pending.EventID); err != nil {
		return false, err
	}
	h.recordAdminAction("unsubscribe", "confirmed")
	h.logAdminAction(ctx, pending.ChatID, &cb.From, "unsubscribe", h.eventNameOrID(ctx, pending.EventID))
	if err := h.PendingUnsubscribes.Resolve(ctx, requestID); err != nil {
		loggerFrom(ctx, h.Log).Warn("pending unsubscribe resolve failed", "requestId", requestID.Value, "error", err)
	}
	name := h.eventNameOrID(ctx, pending.EventID)

	if err := h.toast(ctx, cb.ID, "✅ "+h.Texts.Get("events.unsubscribe", settings.Locale)); err != nil {
		return false, err
	}
	target := editTargetFromCallback(cb, common.ChatID{Value: cb.Message.Chat.ID})
	if err := h.respond(ctx, target, h.Texts.Get("events.unsubscribe_confirmed_by_you", settings.Locale, bold(escapeHTML(name))), h.eventsExit(settings.Locale)); err != nil {
		return true, err
	}
	// One minimal group-side notice for the completed removal, and — when
	// someone other than the requester did the confirming — a heads-up back
	// to their own DM so their "waiting" screen doesn't just go stale.
	if err := h.sendText(ctx, pending.ChatID, h.Texts.Get("events.unsubscribed_announcement", settings.Locale, bold(escapeHTML(name))), nil); err != nil {
		loggerFrom(ctx, h.Log).Warn("unsubscribe group announcement failed", "chatId", pending.ChatID.Value, "error", err)
	}
	if actor != pending.RequestedBy {
		text := h.Texts.Get("events.unsubscribe_request_confirmed", settings.Locale, bold(escapeHTML(name)), escapeHTML(cb.From.DisplayName()))
		if err := h.sendText(ctx, common.ChatID{Value: pending.RequestedBy.Value}, text, nil); err != nil {
			loggerFrom(ctx, h.Log).Warn("unsubscribe requester notice failed", "requester", pending.RequestedBy.Value, "error", err)
		}
	}
	return true, nil
}

func (h *UpdateHandler) rejectUnsubscribe(ctx context.Context, cb *CallbackQuery, requestIDStr string) (bool, error) {
	actor := common.UserID{Value: cb.From.ID}
	requestID, err := common.ParseRequestID(requestIDStr)
	if err != nil {
		return false, newValidationError("invalid unsubscribe request id")
	}
	pending, err := h.PendingUnsubscribes.Find(ctx, requestID)
	if err != nil {
		return false, err
	}
	if pending == nil || pending.Expired(h.Clock.Now()) {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("events.unsubscribe_expired", common.LocaleRU))
	}
	settings, err := h.Chats.Find(ctx, pending.ChatID)
	if err != nil {
		return false, err
	}
	if settings == nil {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("events.unsubscribe_expired", common.LocaleRU))
	}
	if err := h.requireManager(ctx, pending.ChatID, actor); err != nil {
		return false, err
	}

	if err := h.PendingUnsubscribes.Resolve(ctx, requestID); err != nil {
		loggerFrom(ctx, h.Log).Warn("pending unsubscribe resolve failed", "requestId", requestID.Value, "error", err)
	}
	// The requester resolving their own request is a withdrawal, not a
	// rejection: same effect on the record, different thing to say about it.
	withdrawn := actor == pending.RequestedBy
	action, toastKey, screenKey := "rejected", "events.unsubscribe_rejected_toast", "events.unsubscribe_rejected_by_you"
	if withdrawn {
		action, toastKey, screenKey = "withdrawn", "events.unsubscribe_withdrawn_toast", "events.unsubscribe_withdrawn_by_you"
	}
	h.recordAdminAction("unsubscribe", action)
	name := h.eventNameOrID(ctx, pending.EventID)

	if err := h.toast(ctx, cb.ID, "🚫 "+h.Texts.Get(toastKey, settings.Locale)); err != nil {
		return false, err
	}
	target := editTargetFromCallback(cb, common.ChatID{Value: cb.Message.Chat.ID})
	if err := h.respond(ctx, target, h.Texts.Get(screenKey, settings.Locale, bold(escapeHTML(name))), h.eventsExit(settings.Locale)); err != nil {
		return true, err
	}
	if withdrawn {
		h.notifyWithdrawn(ctx, pending, settings, name)
		return true, nil
	}
	text := h.Texts.Get("events.unsubscribe_request_rejected", settings.Locale, bold(escapeHTML(name)), escapeHTML(cb.From.DisplayName()))
	if err := h.sendText(ctx, common.ChatID{Value: pending.RequestedBy.Value}, text, nil); err != nil {
		loggerFrom(ctx, h.Log).Warn("unsubscribe requester notice failed", "requester", pending.RequestedBy.Value, "error", err)
	}
	return true, nil
}

// notifyWithdrawn tells the managers who were asked to decide that there is
// no longer anything to decide, so the confirm/reject buttons sitting in
// their DMs stop reading as live. Best effort throughout: the request is
// already resolved, and a manager who misses this notice only ever sees the
// "expired" toast if they tap anyway.
func (h *UpdateHandler) notifyWithdrawn(ctx context.Context, pending *chat.PendingUnsubscribe, settings *chat.Settings, eventName string) {
	others, err := h.Authorization.OtherManagers(ctx, pending.ChatID, pending.RequestedBy)
	if err != nil {
		loggerFrom(ctx, h.Log).Warn("withdrawal notice recipients failed", "chatId", pending.ChatID.Value, "error", err)
		return
	}
	reachable, err := h.Chats.FilterDMReachable(ctx, others)
	if err != nil {
		loggerFrom(ctx, h.Log).Warn("withdrawal notice reachability failed", "chatId", pending.ChatID.Value, "error", err)
		return
	}
	text := h.Texts.Get("events.unsubscribe_request_withdrawn", settings.Locale, bold(escapeHTML(eventName)), escapeHTML(settings.Title))
	for _, userID := range reachable {
		if err := h.sendText(ctx, common.ChatID(userID), text, nil); err != nil {
			loggerFrom(ctx, h.Log).Warn("withdrawal notice failed", "userId", userID.Value, "error", err)
		}
	}
}

func (h *UpdateHandler) eventNameOrID(ctx context.Context, eventID common.EventID) string {
	event, err := h.Catalog.FindEvent(ctx, eventID)
	if err != nil || event == nil {
		return eventID.Value.String()
	}
	return event.Name
}

func (h *UpdateHandler) setEventTopic(ctx context.Context, cb *CallbackQuery, settings chat.Settings, idStr string) (bool, error) {
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
		return false, err
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return false, newValidationError("invalid event id")
	}
	if cb.Message == nil || cb.Message.MessageThreadID == nil {
		return false, newValidationError("use inside a topic")
	}
	topicID := *cb.Message.MessageThreadID
	if err := h.Chats.SaveEventTopic(ctx, chat.EventTopic{ChatID: settings.ChatID, EventID: common.EventID{Value: id}, TopicID: topicID}); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "event_topic", h.eventNameOrID(ctx, common.EventID{Value: id}))
	return true, h.toast(ctx, cb.ID, "✅ "+h.Texts.Get("events.topic", settings.Locale))
}
