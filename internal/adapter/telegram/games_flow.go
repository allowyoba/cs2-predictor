package telegram

import (
	"context"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// Turning a game on and off for a chat.
//
// Switching one ON is immediate: the chat starts seeing that game's
// tournaments, and anyone who disagrees can switch it back. Switching one
// OFF goes through the same two-manager approval an unsubscribe does, for a
// stronger version of the same reason: it silences every tournament of that
// game at once, and nothing in the chat afterwards says why the polls
// stopped. Either way the chat itself is told — a setting that changes what
// everybody sees should not change silently in a DM.

// setGameEnabled applies the change and announces it in the chat.
func (h *UpdateHandler) setGameEnabled(ctx context.Context, settings chat.Settings, code competition.GameCode,
	enabled bool, actor *User) (chat.Settings, error) {
	next := make([]competition.GameCode, 0, len(settings.EnabledGames)+1)
	for _, g := range settings.EnabledGames {
		if g != code {
			next = append(next, g)
		}
	}
	if enabled {
		next = append(next, code)
	}
	if err := h.Chats.SetEnabledGames(ctx, settings.ChatID, next); err != nil {
		return settings, err
	}
	settings.EnabledGames = next

	state := "off"
	messageKey := "games.disabled_announcement"
	if enabled {
		state = "on"
		messageKey = "games.enabled_announcement"
	}
	h.logAdminAction(ctx, settings.ChatID, actor, "games", string(code)+":"+state)

	// Best effort: the setting is already saved, and a failed announcement
	// must not make the caller think it wasn't.
	name := h.Texts.Get(gameLabelKey(code), settings.Locale)
	text := h.Texts.Get(messageKey, settings.Locale, bold(escapeHTML(name)))
	if actor != nil {
		text += "\n" + italic(escapeHTML(actor.DisplayName()))
	}
	if err := h.sendText(ctx, settings.ChatID, text, settings.DefaultTopicID); err != nil {
		loggerFrom(ctx, h.Log).Warn("game toggle announcement failed", "chatId", settings.ChatID.Value, "error", err)
	}
	// Turning on the first game is half of what a new chat needs; without
	// a tournament to follow it still produces nothing. Point at the next
	// step exactly then — not on later games, and not for a chat that is
	// already following something.
	if enabled && h.onboardingNeedsTournament(ctx, settings) {
		if err := h.sendOnboardingStep(ctx, settings, "onboarding.pick_tournament", "events:add", "events.add"); err != nil {
			return settings, err
		}
	}
	return settings, nil
}

// requestGameDisable opens a two-manager approval instead of switching the
// game off on the spot. Mirrors the unsubscribe request exactly, down to
// the self-confirmable case: if no other manager can be reached by DM,
// there is nobody to ask and the requester's own second tap stands in.
func (h *UpdateHandler) requestGameDisable(ctx context.Context, cb *CallbackQuery, target replyTarget,
	settings chat.Settings, code competition.GameCode) (bool, error) {
	actor := common.UserID{Value: cb.From.ID}
	others, err := h.Authorization.OtherManagers(ctx, settings.ChatID, actor)
	if err != nil {
		return false, err
	}
	reachable, err := h.Chats.FilterDMReachable(ctx, others)
	if err != nil {
		return false, err
	}

	now := h.Clock.Now()
	requestID := common.NewRequestID()
	pending := chat.PendingApproval{
		ID: requestID, Kind: chat.ApprovalDisableGame, ChatID: settings.ChatID,
		Subject: string(code), RequestedBy: actor,
		CreatedAt: now, ExpiresAt: now.Add(chat.PendingApprovalTTL),
		SelfConfirmable: len(reachable) == 0,
	}
	name := h.Texts.Get(gameLabelKey(code), settings.Locale)
	// The request and the DMs asking about it commit together, the same way
	// an unsubscribe request does: a confirmation button must never exist
	// for a request that was never stored.
	if err := h.inTx(ctx, func(ctx context.Context) error {
		if err := h.PendingApprovals.Create(ctx, pending); err != nil {
			return err
		}
		return h.enqueueConfirmationRequests(ctx, chat.ApprovalDisableGame, reachable, settings, name, requestID, cb.From.DisplayName())
	}); err != nil {
		return false, err
	}

	token := requestID.String()
	bodyKey := "games.disable_pending"
	confirmKey := "events.unsubscribe_confirm"
	if pending.SelfConfirmable {
		bodyKey = "games.disable_self"
		confirmKey = "events.unsubscribe_self_confirm"
	}
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{
		button(h.Texts.Get(confirmKey, settings.Locale), "unsubok:"+token),
		button(h.Texts.Get("events.unsubscribe_reject", settings.Locale), "unsubreject:"+token),
	}}}
	return true, h.respond(ctx, target, h.Texts.Get(bodyKey, settings.Locale, bold(escapeHTML(name))), &kb)
}

// applyApprovedGameDisable is the disable half of a confirmed approval —
// the counterpart of the unsubscribe branch in confirmUnsubscribe.
func (h *UpdateHandler) applyApprovedGameDisable(ctx context.Context, cb *CallbackQuery,
	pending chat.PendingApproval, settings chat.Settings) (bool, error) {
	code := competition.GameCode(pending.Subject)
	if _, err := h.setGameEnabled(ctx, settings, code, false, &cb.From); err != nil {
		return false, err
	}
	if err := h.PendingApprovals.Resolve(ctx, pending.ID); err != nil {
		loggerFrom(ctx, h.Log).Warn("pending game disable resolve failed", "requestId", pending.ID.Value, "error", err)
	}

	name := h.Texts.Get(gameLabelKey(code), settings.Locale)
	if err := h.toast(ctx, cb.ID, "✅ "+name); err != nil {
		return false, err
	}
	target := editTargetFromCallback(cb, common.ChatID{Value: cb.Message.Chat.ID})
	if err := h.respond(ctx, target, h.Texts.Get("games.disable_confirmed_by_you", settings.Locale, bold(escapeHTML(name))), nil); err != nil {
		return true, err
	}
	// The requester is waiting on a screen that would otherwise just go
	// stale; tell them who closed it.
	if actor := (common.UserID{Value: cb.From.ID}); actor != pending.RequestedBy {
		text := h.Texts.Get("games.disable_request_confirmed", settings.Locale,
			bold(escapeHTML(name)), escapeHTML(cb.From.DisplayName()))
		if err := h.sendText(ctx, common.ChatID{Value: pending.RequestedBy.Value}, text, nil); err != nil {
			loggerFrom(ctx, h.Log).Warn("game disable requester notice failed", "requester", pending.RequestedBy.Value, "error", err)
		}
	}
	return true, nil
}
