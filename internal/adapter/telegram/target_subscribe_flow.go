package telegram

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// The team-follow flow: search a team by name, subscribe a chat to it, and
// manage (list/unsubscribe from) whatever it already follows. This is the
// active counterpart of screens_target_stats.go (which only ever reads
// subscriptions already made) and of target_cross_sell_flow.go (which only
// ever offers one reactively) — until this file, a chat's own admin had no
// way to start a subscription in the first place.
//
// Player subscriptions are deliberately not offered here: subscription.
// TargetPlayer exists in the domain and its persistence, but there is still
// no player/roster concept anywhere in this codebase to search or pick
// from (matches carry Teams only). Building a picker with nothing behind
// it would be a fake feature, so only the team half of TargetKind gets a
// menu entry; TargetPlayer stays reachable only via whatever created it
// historically (nothing does, today).

const maxTargetSearchResults = 20

// targetsMenu is the entry point reached from the events menu ("what this
// chat follows" sits next to "what this chat is subscribed to"): follow a
// new team, or see/undo what is already followed.
func (h *UpdateHandler) targetsMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	// The lists come first and the typing second: picking from what is
	// actually there asks less of the reader than knowing how a handle is
	// spelled, and this menu used to offer only the latter.
	rows := [][]InlineButton{
		{button(h.Texts.Get("targets.follow_team", settings.Locale), "targets:browse:0")},
		{button(h.Texts.Get("targets.follow_player", settings.Locale), "targets:pbrowse:0")},
		{button(h.Texts.Get("targets.mine", settings.Locale), "targets:mine")},
		{h.backButton(settings.Locale, "menu:events")},
	}
	text := bold(escapeHTML(h.Texts.Get("targets.title", settings.Locale))) + "\n\n" + h.Texts.Get("targets.explainer", settings.Locale)
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// requestTargetSearch prompts for a team name via ForceReply, the same
// pattern requestEventSearch uses (see its doc comment for why selective
// needs the mention).
func (h *UpdateHandler) requestTargetSearch(ctx context.Context, cb *CallbackQuery, settings chat.Settings) error {
	text := h.Texts.Get("targets.search_prompt", settings.Locale) + "\n" + mentionHTML(cb.From)
	payload := map[string]any{
		"chat_id":    cb.Message.Chat.ID,
		"text":       text,
		"parse_mode": "HTML",
		"reply_markup": map[string]any{
			"force_reply":             true,
			"selective":               true,
			"input_field_placeholder": "G2",
		},
	}
	if cb.Message != nil && cb.Message.MessageThreadID != nil {
		payload["message_thread_id"] = *cb.Message.MessageThreadID
	}
	_, err := h.Client.Call(ctx, "sendMessage", payload)
	return err
}

func isTargetSearchReply(msg *Message, locale common.LocaleCode, texts *Texts) bool {
	if msg == nil || msg.ReplyToMessage == nil || msg.ReplyToMessage.Text == nil {
		return false
	}
	return strings.TrimSpace(*msg.ReplyToMessage.Text) == strings.TrimSpace(stripHTML(texts.Get("targets.search_prompt", locale)))
}

// searchTargets mirrors searchEvents: replies into msg.Chat (not necessarily
// settings.ChatID — see searchEvents's doc comment), listing teams already
// followed as already-subscribed rather than hiding them, since "follow
// again" is a no-op worth showing rather than a result worth removing.
func (h *UpdateHandler) searchTargets(ctx context.Context, msg *Message, settings chat.Settings, rawQuery string) error {
	if msg.From == nil {
		return newValidationError("message.from is required")
	}
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: msg.From.ID}); err != nil {
		return err
	}
	replyChatID := common.ChatID{Value: msg.Chat.ID}
	query := strings.TrimSpace(rawQuery)
	if len([]rune(query)) < 2 {
		return h.sendText(ctx, replyChatID, h.Texts.Get("targets.search_hint", settings.Locale), msg.MessageThreadID)
	}
	if h.Catalog == nil {
		return newValidationError("catalog unavailable")
	}
	found, err := h.Catalog.SearchTeams(ctx, query, maxTargetSearchResults, settings.EnabledGames)
	if err != nil {
		return err
	}

	subscribed := map[string]bool{}
	if h.Targets != nil {
		subs, err := h.Targets.TargetSubscriptions(ctx, settings.ChatID)
		if err != nil {
			return err
		}
		for _, s := range subs {
			if s.Kind == subscription.TargetTeam && s.Active {
				subscribed[s.TargetID] = true
			}
		}
	}

	var rows [][]InlineButton
	for _, t := range found {
		label := truncate(t.Name, 48)
		if subscribed[t.ID.Value.String()] {
			label = "✅ " + label
		}
		rows = append(rows, []InlineButton{button(label, cbTargetSubscribe(t.ID))})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:targets")})

	header := bold(escapeHTML(query))
	body := header + "\n\n" + h.Texts.Get("targets.search_pick_prompt", settings.Locale)
	if len(found) == 0 {
		body = header + "\n\n" + h.Texts.Get("targets.search_empty", settings.Locale)
	}
	return h.sendTextWithKeyboard(ctx, replyChatID, body, InlineKeyboard{InlineKeyboard: rows}, msg.MessageThreadID)
}

func cbTargetSubscribe(teamID common.TeamID) string {
	return "targets:sub:" + compactUUID(teamID.Value)
}

func cbTargetUnsubscribe(teamID string) string {
	return "targets:unsub:" + teamID
}

// subscribeTarget creates (or reactivates) the chat's follow of one team.
// Unlike tournament subscribe, no polls are created here — a team
// subscription's only effect today is scoping targetTeamStats and feeding
// the cross-sell prompt (internal/app/target_cross_sell.go); it does not by
// itself add any tournament.
func (h *UpdateHandler) subscribeTarget(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, teamIDStr string) (bool, error) {
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
		return false, err
	}
	id, err := uuid.Parse(teamIDStr)
	if err != nil {
		return false, newValidationError("invalid team id")
	}
	if h.Targets == nil {
		return false, newValidationError("target subscriptions unavailable")
	}
	teamID := common.TeamID{Value: id}
	team, err := h.resolveTeamName(ctx, teamID)
	if err != nil {
		return false, err
	}
	sub := subscription.TargetSubscription{
		ChatID: settings.ChatID, Kind: subscription.TargetTeam, TargetID: teamID.Value.String(),
		TargetName: team, SubscribedAt: h.Clock.Now(), Active: true,
	}
	if _, err := h.Targets.SubscribeTarget(ctx, sub); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "target_subscribe", team)

	text := h.Texts.Get("targets.subscribed", settings.Locale, escapeHTML(team))
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("targets.follow_another", settings.Locale), "targets:search")},
		{button(h.Texts.Get("targets.mine", settings.Locale), "targets:mine")},
	}}
	return false, h.respond(ctx, target, text, &kb)
}

// resolveTeamName looks a team up by id for its display name: the callback
// only carries the id, so a fresh tap needs one lookup. Falls back to the id
// itself if the team is gone, rather than failing the subscription over a
// cosmetic name.
func (h *UpdateHandler) resolveTeamName(ctx context.Context, teamID common.TeamID) (string, error) {
	team, err := h.Catalog.FindTeam(ctx, teamID)
	if err != nil {
		return "", err
	}
	if team == nil {
		return teamID.Value.String(), nil
	}
	return team.Name, nil
}

// targetsMine lists the chat's active team follows with an unsubscribe
// button each — a follow-only screen with no way to see or undo it is
// incomplete.
func (h *UpdateHandler) targetsMine(ctx context.Context, target replyTarget, settings chat.Settings) error {
	if h.Targets == nil {
		return h.respond(ctx, target, h.Texts.Get("targets.empty", settings.Locale), &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "menu:targets")}}})
	}
	subs, err := h.Targets.TargetSubscriptions(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	// Teams and players under their own headings rather than one flat list:
	// they are followed for different reasons, and a bare list of names gives
	// no way to tell an organisation from a person.
	var rows [][]InlineButton
	for _, section := range []struct {
		kind     subscription.TargetKind
		heading  string
		callback func(string) string
	}{
		{subscription.TargetTeam, "targets.section_teams", cbTargetUnsubscribe},
		{subscription.TargetPlayer, "targets.section_players", cbPlayerUnsubscribe},
	} {
		var body [][]InlineButton
		for _, s := range subs {
			if s.Kind != section.kind || !s.Active {
				continue
			}
			name := s.TargetName
			if name == "" {
				name = s.TargetID
			}
			body = append(body, []InlineButton{
				button(truncate(name, 40), "noop"),
				button(h.Texts.Get("targets.unfollow", settings.Locale), section.callback(s.TargetID)),
			})
		}
		if len(body) == 0 {
			continue
		}
		rows = append(rows, []InlineButton{button(h.Texts.Get(section.heading, settings.Locale), "noop")})
		rows = append(rows, body...)
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:targets")})
	body := h.Texts.Get("targets.empty", settings.Locale)
	if len(rows) > 1 {
		body = bold(escapeHTML(h.Texts.Get("targets.mine", settings.Locale)))
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, body), &InlineKeyboard{InlineKeyboard: rows})
}

// unsubscribeTarget removes a chat's follow of a team. Unlike tournament
// unsubscribe, no cross-manager confirmation dance guards this — a team
// follow creates no polls and no per-user data to lose, so the same
// one-tap-reversible rule notify_prefs.go's switches follow applies here
// too, not the heavier removal-confirmation flow.
func (h *UpdateHandler) unsubscribeTarget(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, teamID string) (bool, error) {
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
		return false, err
	}
	if h.Targets == nil {
		return false, newValidationError("target subscriptions unavailable")
	}
	if err := h.Targets.UnsubscribeTarget(ctx, settings.ChatID, subscription.TargetTeam, teamID); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "target_unsubscribe", teamID)
	if err := h.toast(ctx, cb.ID, "✅ "+h.Texts.Get("targets.unfollowed", settings.Locale)); err != nil {
		return false, err
	}
	return false, h.targetsMine(ctx, target, settings)
}
