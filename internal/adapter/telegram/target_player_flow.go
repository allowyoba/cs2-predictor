package telegram

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// The player half of the follow flow, mirroring target_subscribe_flow.go's
// team half: search a nickname, follow the person behind it, and undo it from
// the same list the teams are on.
//
// This used to be deliberately absent, because there was no player anywhere
// in the codebase to search — matches carry teams, and the only player data
// was HLTV's roster nicknames, which identify nobody. There is a player
// entity now (app.RosterSync builds it from the match provider's rosters and
// pairs it with HLTV's nicknames), so the picker has something real behind it.

// requestPlayerSearch prompts for a nickname, the same ForceReply pattern the
// team and tournament searches use.
func (h *UpdateHandler) requestPlayerSearch(ctx context.Context, cb *CallbackQuery, settings chat.Settings) error {
	text := h.Texts.Get("targets.search_player_prompt", settings.Locale) + "\n" + mentionHTML(cb.From)
	payload := map[string]any{
		"chat_id":    cb.Message.Chat.ID,
		"text":       text,
		"parse_mode": "HTML",
		"reply_markup": map[string]any{
			"force_reply":             true,
			"selective":               true,
			"input_field_placeholder": "s1mple",
		},
	}
	if cb.Message != nil && cb.Message.MessageThreadID != nil {
		payload["message_thread_id"] = *cb.Message.MessageThreadID
	}
	_, err := h.Client.Call(ctx, "sendMessage", payload)
	return err
}

func isPlayerSearchReply(msg *Message, locale common.LocaleCode, texts *Texts) bool {
	if msg == nil || msg.ReplyToMessage == nil || msg.ReplyToMessage.Text == nil {
		return false
	}
	return strings.TrimSpace(*msg.ReplyToMessage.Text) == strings.TrimSpace(stripHTML(texts.Get("targets.search_player_prompt", locale)))
}

// searchPlayers mirrors searchTargets, including replying into msg.Chat
// rather than settings.ChatID — see searchEvents's doc comment for why.
func (h *UpdateHandler) searchPlayers(ctx context.Context, msg *Message, settings chat.Settings, rawQuery string) error {
	if msg.From == nil {
		return newValidationError("message.from is required")
	}
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: msg.From.ID}); err != nil {
		return err
	}
	replyChatID := common.ChatID{Value: msg.Chat.ID}
	query := strings.TrimSpace(rawQuery)
	if len([]rune(query)) < 2 {
		return h.sendText(ctx, replyChatID, h.Texts.Get("targets.search_player_hint", settings.Locale), msg.MessageThreadID)
	}
	if h.Rosters == nil {
		return newValidationError("player catalogue unavailable")
	}
	found, err := h.Rosters.SearchPlayers(ctx, query, maxTargetSearchResults, settings.EnabledGames)
	if err != nil {
		return err
	}

	subscribed, err := h.followedTargetIDs(ctx, settings.ChatID, subscription.TargetPlayer)
	if err != nil {
		return err
	}

	var rows [][]InlineButton
	for _, p := range found {
		label := truncate(playerLabel(p), 48)
		if subscribed[p.ID.Value.String()] {
			label = "✅ " + label
		}
		rows = append(rows, []InlineButton{button(label, cbPlayerSubscribe(p.ID))})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:targets")})

	body := bold(escapeHTML(query)) + "\n\n" + h.Texts.Get("targets.search_player_pick_prompt", settings.Locale)
	if len(found) == 0 {
		body = bold(escapeHTML(query)) + "\n\n" + h.Texts.Get("targets.search_player_empty", settings.Locale)
	}
	return h.sendTextWithKeyboard(ctx, replyChatID, body, InlineKeyboard{InlineKeyboard: rows}, msg.MessageThreadID)
}

// playerLabel is the nickname, with the real name in brackets when it is
// known — two people share a handle often enough that the nickname alone is
// not always enough to pick between them.
func playerLabel(p competition.Player) string {
	if p.FullName == "" {
		return p.Nickname
	}
	return p.Nickname + " (" + p.FullName + ")"
}

// followedTargetIDs is the set of target ids this chat already follows of one
// kind, so a search result can be shown as already-followed rather than
// hidden — "follow again" is a no-op worth showing, not a result worth
// removing.
func (h *UpdateHandler) followedTargetIDs(ctx context.Context, chatID common.ChatID, kind subscription.TargetKind) (map[string]bool, error) {
	out := map[string]bool{}
	if h.Targets == nil {
		return out, nil
	}
	subs, err := h.Targets.TargetSubscriptions(ctx, chatID)
	if err != nil {
		return nil, err
	}
	for _, s := range subs {
		if s.Kind == kind && s.Active {
			out[s.TargetID] = true
		}
	}
	return out, nil
}

func cbPlayerSubscribe(id common.PlayerID) string {
	return "targets:psub:" + compactUUID(id.Value)
}

func cbPlayerUnsubscribe(id string) string {
	return "targets:punsub:" + id
}

// subscribePlayer creates (or reactivates) the chat's follow of one player.
// Like the team half it creates no polls: a follow scopes statistics and
// feeds the cross-sell offer, it does not by itself add a tournament.
func (h *UpdateHandler) subscribePlayer(ctx context.Context, cb *CallbackQuery, target replyTarget,
	settings chat.Settings, playerIDStr string) (bool, error) {
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
		return false, err
	}
	id, err := uuid.Parse(playerIDStr)
	if err != nil {
		return false, newValidationError("invalid player id")
	}
	if h.Targets == nil || h.Rosters == nil {
		return false, newValidationError("player subscriptions unavailable")
	}
	playerID := common.PlayerID{Value: id}
	name := playerID.Value.String()
	if player, err := h.Rosters.FindPlayer(ctx, playerID); err != nil {
		return false, err
	} else if player != nil {
		name = player.Nickname
	}

	sub := subscription.TargetSubscription{
		ChatID: settings.ChatID, Kind: subscription.TargetPlayer, TargetID: playerID.Value.String(),
		TargetName: name, SubscribedAt: h.Clock.Now(), Active: true,
	}
	if _, err := h.Targets.SubscribeTarget(ctx, sub); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "player_subscribe", name)

	text := h.Texts.Get("targets.player_subscribed", settings.Locale, escapeHTML(name))
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("targets.follow_another_player", settings.Locale), "targets:psearch")},
		{button(h.Texts.Get("targets.mine", settings.Locale), "targets:mine")},
	}}
	return false, h.respond(ctx, target, text, &kb)
}

// unsubscribePlayer removes a chat's follow of a player — one tap, no
// second-manager confirmation, for the reason unsubscribeTarget gives.
func (h *UpdateHandler) unsubscribePlayer(ctx context.Context, cb *CallbackQuery, target replyTarget,
	settings chat.Settings, playerID string) (bool, error) {
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
		return false, err
	}
	if h.Targets == nil {
		return false, newValidationError("target subscriptions unavailable")
	}
	if err := h.Targets.UnsubscribeTarget(ctx, settings.ChatID, subscription.TargetPlayer, playerID); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "player_unsubscribe", playerID)
	if err := h.toast(ctx, cb.ID, "✅ "+h.Texts.Get("targets.unfollowed", settings.Locale)); err != nil {
		return false, err
	}
	return false, h.targetsMine(ctx, target, settings)
}
