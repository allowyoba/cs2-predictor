package telegram

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// Team and player subscriptions: the same subscribe/list/remove shape as
// tournaments, plus the chat-level stats cut for each target.

// followSearchLimit bounds each kind of search result.
const followSearchLimit = 8

// maxPlayerToken keeps a player's callback within Telegram's 64 bytes
// alongside the longest prefix and a callback scope.
const maxPlayerToken = 20

// leaderboard is the one way this adapter reads a chat board, so tournament
// scoping for team/player followers cannot be skipped by a screen.
func (h *UpdateHandler) leaderboard(ctx context.Context, chatID common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	narrowed, err := h.Scopes.Narrow(ctx, chatID, period)
	if err != nil {
		return nil, err
	}
	return h.Scoring.Leaderboard(ctx, chatID, narrowed)
}

// targetToken is a team/player target's compact callback form.
func targetToken(t subscription.Target) (string, bool) {
	switch t.Kind {
	case subscription.TargetTeam:
		return "t" + compactUUID(t.TeamID.Value), true
	case subscription.TargetPlayer:
		if t.Player == "" || len(t.Player) > maxPlayerToken || strings.ContainsAny(t.Player, ":|") {
			return "", false
		}
		return "p" + t.Player, true
	}
	return "", false
}

func parseTargetToken(token string) (subscription.Target, error) {
	switch {
	case strings.HasPrefix(token, "t"):
		id, err := uuid.Parse(token[1:])
		if err != nil {
			return subscription.Target{}, newValidationError("invalid team id")
		}
		return subscription.TeamTarget(common.TeamID{Value: id}, ""), nil
	case strings.HasPrefix(token, "p") && len(token) > 1:
		return subscription.PlayerTarget(token[1:]), nil
	}
	return subscription.Target{}, newValidationError("invalid follow target")
}

// followTargets lists the chat's team/player targets, labelled.
func (h *UpdateHandler) followTargets(ctx context.Context, chatID common.ChatID) ([]subscription.Target, error) {
	if h.Follows == nil {
		return nil, nil
	}
	all, err := h.Follows.Targets.Targets(ctx, chatID)
	if err != nil {
		return nil, err
	}
	var out []subscription.Target
	for _, t := range all {
		if t.Kind != subscription.TargetTournament {
			out = append(out, t)
		}
	}
	return out, nil
}

// labelled fills a target's label from the chat's own list when the
// callback carried only its id.
func (h *UpdateHandler) labelled(ctx context.Context, chatID common.ChatID, t subscription.Target) subscription.Target {
	if t.Label != "" && t.Kind == subscription.TargetPlayer {
		return t
	}
	targets, err := h.followTargets(ctx, chatID)
	if err == nil {
		for _, existing := range targets {
			if existing.Key() == t.Key() {
				return existing
			}
		}
	}
	return t
}

func (h *UpdateHandler) targetLabel(t subscription.Target, locale common.LocaleCode) string {
	icon := h.Texts.Get("follows.kind_team", locale)
	if t.Kind == subscription.TargetPlayer {
		icon = h.Texts.Get("follows.kind_player", locale)
	}
	label := t.Label
	if label == "" {
		label = t.Player
	}
	return icon + " " + label
}

// followsScreen lists the chat's teams and players. Removal is
// administration, so DM only — like the tournament list.
func (h *UpdateHandler) followsScreen(ctx context.Context, target replyTarget, settings chat.Settings, dmContext bool) error {
	targets, err := h.followTargets(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	var rows [][]InlineButton
	var lines []string
	for _, t := range targets {
		lines = append(lines, "• "+escapeHTML(h.targetLabel(t, settings.Locale)))
		token, ok := targetToken(t)
		if dmContext && ok {
			rows = append(rows, []InlineButton{button("✖ "+h.targetLabel(t, settings.Locale), "unfollow:"+token)})
		}
	}
	body := bold(escapeHTML(h.Texts.Get("follows.title", settings.Locale))) + "\n"
	if len(lines) == 0 {
		body += h.Texts.Get("follows.empty", settings.Locale)
	} else {
		body += strings.Join(lines, "\n")
	}
	body += "\n\n" + italic(h.Texts.Get("follows.hint", settings.Locale))
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:events")})
	return h.respond(ctx, target, managedScreenContext(target, settings, body), &InlineKeyboard{InlineKeyboard: rows})
}

// searchFollowTargets answers "/follow <name>" from a manager's DM session.
func (h *UpdateHandler) searchFollowTargets(ctx context.Context, msg *Message, settings chat.Settings, query string) error {
	if msg.From == nil {
		return newValidationError("message.from is required")
	}
	if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: msg.From.ID}); err != nil {
		return err
	}
	replyChatID := common.ChatID{Value: msg.Chat.ID}
	if h.Follows == nil {
		return h.sendText(ctx, replyChatID, h.Texts.Get("follows.unavailable", settings.Locale), msg.MessageThreadID)
	}
	if len([]rune(query)) < 2 {
		return h.sendText(ctx, replyChatID, h.Texts.Get("follows.search_hint", settings.Locale), msg.MessageThreadID)
	}
	found, err := h.Follows.Targets.SearchTargets(ctx, query, followSearchLimit)
	if err != nil {
		return err
	}
	var rows [][]InlineButton
	for _, t := range found {
		if token, ok := targetToken(t); ok {
			rows = append(rows, []InlineButton{button(h.targetLabel(t, settings.Locale), "follow:"+token)})
		}
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "follows:mine")})
	body := bold(escapeHTML(query)) + "\n\n" + h.Texts.Get("follows.search_pick", settings.Locale)
	if len(rows) == 1 {
		body = bold(escapeHTML(query)) + "\n\n" + h.Texts.Get("follows.search_empty", settings.Locale)
	}
	return h.respond(ctx, sendTarget(replyChatID, msg.MessageThreadID), body, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) follow(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, token string) (bool, error) {
	if h.Follows == nil {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("follows.unavailable", settings.Locale))
	}
	t, err := parseTargetToken(token)
	if err != nil {
		return false, err
	}
	topic := settings.DefaultTopicID
	if topic == nil && cb.Message != nil {
		topic = cb.Message.MessageThreadID
	}
	opened, err := h.Follows.Follow(ctx, settings, t, topic)
	if err != nil {
		return false, err
	}
	t = h.labelled(ctx, settings.ChatID, t)
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "follow", h.targetLabel(t, settings.Locale))
	text := h.Texts.Get("follows.added", settings.Locale, escapeHTML(h.targetLabel(t, settings.Locale)))
	if opened > 0 {
		text += "\n" + h.Texts.Get("events.polls_created", settings.Locale, opened)
	} else {
		text += "\n" + h.Texts.Get("events.polls_later", settings.Locale)
	}
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("follows.mine", settings.Locale), "follows:mine")},
	}}
	return false, h.respond(ctx, target, text, &kb)
}

func (h *UpdateHandler) unfollow(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, token string) (bool, error) {
	if h.Follows == nil {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("follows.unavailable", settings.Locale))
	}
	t, err := parseTargetToken(token)
	if err != nil {
		return false, err
	}
	t = h.labelled(ctx, settings.ChatID, t)
	if err := h.Follows.Targets.UnsubscribeTarget(ctx, settings.ChatID, t); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "unfollow", h.targetLabel(t, settings.Locale))
	if err := h.toast(ctx, cb.ID, "✅ "+h.Texts.Get("follows.removed", settings.Locale)); err != nil {
		return true, err
	}
	return true, h.followsScreen(ctx, target, settings, true)
}

// statsFollowsMenu offers one board per followed team/player.
func (h *UpdateHandler) statsFollowsMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	targets, err := h.followTargets(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	var rows [][]InlineButton
	for _, t := range targets {
		if token, ok := targetToken(t); ok {
			rows = append(rows, []InlineButton{button(h.targetLabel(t, settings.Locale), "stats:follow:"+token)})
		}
	}
	text := h.Texts.Get("stats.by_follow", settings.Locale)
	if len(rows) == 0 {
		text += "\n\n" + h.Texts.Get("follows.empty", settings.Locale)
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:stats")})
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// followLeaderboardSize caps the cut; the full board has its own screen.
const followLeaderboardSize = 15

// statsFollowBoard is the chat's all-time board over one target's matches.
func (h *UpdateHandler) statsFollowBoard(ctx context.Context, target replyTarget, settings chat.Settings, token string, viewer common.UserID) error {
	t, err := parseTargetToken(token)
	if err != nil {
		return err
	}
	t = h.labelled(ctx, settings.ChatID, t)
	teams, err := h.Scopes.TargetTeams(ctx, t)
	if err != nil {
		return err
	}
	standings, err := h.Scoring.Leaderboard(ctx, settings.ChatID, scoring.AllTime().ForTeams(teams))
	if err != nil {
		return err
	}
	if len(standings) > followLeaderboardSize {
		standings = standings[:followLeaderboardSize]
	}
	body := h.Texts.Get("stats.empty", settings.Locale)
	if len(standings) > 0 {
		body = leaderboardRows(standings, viewer)
	}
	text := h.Texts.Get("stats.follow_title", settings.Locale, escapeHTML(h.targetLabel(t, settings.Locale))) + "\n\n" + body
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "stats:follows")}}}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), kb)
}

// allCallbackRoutes puts the follow routes first: "stats:follow:" must win
// over any broader "stats:" route.
var allCallbackRoutes = append(append([]callbackRoute{}, followRoutes...), callbackRoutes...)

var followRoutes = []callbackRoute{
	{match: exact("follows:mine"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.followsScreen(ctx, target, settings, cb.Message != nil && cb.Message.Chat.Type == "private")
	}},
	{match: prefixed("follow:"), guard: guardManager, handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return h.follow(ctx, cb, target, settings, strings.TrimPrefix(data, "follow:"))
	}},
	{match: prefixed("unfollow:"), guard: guardManager, handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return h.unfollow(ctx, cb, target, settings, strings.TrimPrefix(data, "unfollow:"))
	}},
	{match: exact("stats:follows"), handle: simple((*UpdateHandler).statsFollowsMenu)},
	{match: prefixed("stats:follow:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.statsFollowBoard(ctx, target, settings, strings.TrimPrefix(data, "stats:follow:"), common.UserID{Value: cb.From.ID})
	}},
}
