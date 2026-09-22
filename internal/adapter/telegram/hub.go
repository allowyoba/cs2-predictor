package telegram

import (
	"context"
	"fmt"

	"cs2predictor/internal/platform/common"
)

// The mode-switcher hub: a bare /start or /menu in a DM used to always
// land on the personal-stats dashboard, with group management one tap
// away (manage:chats) and the root/operator-only surfaces
// (/team_matches, /provider_status, /team_match_admin) reachable only by
// already knowing their exact command — no discoverable entry point at
// all. startLanding gives every role its own explicit, single point of
// entry instead: someone with no admin or operator rights anywhere still
// goes straight to their personal dashboard (a hub offering two dead-end
// options nobody can use would just be friction), but the moment either
// role applies, /start asks which panel to open rather than guessing.

// startLanding is what /start and /menu resolve to in a DM with no active
// managed-chat session — see handlePrivateMessage.
func (h *UpdateHandler) startLanding(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	chats, err := h.Chats.ManagedChats(ctx, userID)
	if err != nil {
		return err
	}
	managesAnyChat := len(chats) > 0
	isOperator := h.isTeamMatchOperator(ctx, userID)

	if !managesAnyChat && !isOperator {
		return h.privateStatsMenu(ctx, target, userID, locale)
	}
	return h.modeHub(ctx, target, locale, managesAnyChat, isOperator)
}

// modeHub renders the actual switcher. Each row is a full panel — personal
// dashboard, group management, or (operators only) system tools — never a
// mix of the three on one screen.
//
// Settings and Help sit here too, below the panels: for anyone who sees
// this screen at all, it IS their root, so this is where "root level"
// means — the personal dashboard below no longer repeats them (see
// privateStatsMenu).
func (h *UpdateHandler) modeHub(ctx context.Context, target replyTarget, locale common.LocaleCode, managesAnyChat, isOperator bool) error {
	var rows [][]InlineButton
	rows = append(rows, []InlineButton{button(h.Texts.Get("hub.personal", locale), "hub:personal")})
	if managesAnyChat {
		rows = append(rows, []InlineButton{button(h.Texts.Get("hub.manage", locale), "hub:manage")})
	}
	if isOperator {
		rows = append(rows, []InlineButton{button(h.Texts.Get("hub.system", locale), "hub:system")})
	}
	rows = append(rows,
		[]InlineButton{button(h.Texts.Get("private.settings", locale), "pstats:settings")},
		[]InlineButton{button(h.Texts.Get("menu.help", locale), "pstats:help")},
	)
	return h.respond(ctx, target, bold(h.Texts.Get("hub.title", locale)), &InlineKeyboard{InlineKeyboard: rows})
}

// crestSourceLabel is the operator row for which team crests this
// deployment shows.
//
// A deployment-wide answer rather than a personal or a per-chat one: which
// pictures the bot uses is a decision about the product, and the same team
// has to look the same to everybody who sees it.
func (h *UpdateHandler) crestSourceLabel(ctx context.Context, locale common.LocaleCode) string {
	state := h.Texts.Get("settings.logos_provider", locale)
	if h.crestSourceIsHLTV(ctx) {
		state = h.Texts.Get("settings.logos_hltv", locale)
	}
	return h.Texts.Get("settings.logos_label", locale, state)
}

// crestSourceIsHLTV reads the stored choice; anything unreadable is the
// default, because a settings row that errors is worse than one that shows
// what the app is actually rendering.
func (h *UpdateHandler) crestSourceIsHLTV(ctx context.Context) bool {
	store, ok := h.Chats.(common.BotSettings)
	if !ok {
		return false
	}
	value, err := store.BotSetting(ctx, common.CrestSourceKey)
	if err != nil {
		h.Log.Warn("crest source lookup failed", "error", err)
		return false
	}
	return value == common.CrestSourceHLTV
}

// toggleCrestSource flips it for the whole deployment.
func (h *UpdateHandler) toggleCrestSource(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	if !h.isRootTeamMatchOperator(userID) {
		return h.refuse(ctx, target, locale, "hub:system")
	}
	store, ok := h.Chats.(common.BotSettings)
	if !ok {
		return fmt.Errorf("chat repository does not support deployment settings")
	}
	next := common.CrestSourceHLTV
	if h.crestSourceIsHLTV(ctx) {
		next = "provider"
	}
	if err := store.SetBotSetting(ctx, common.CrestSourceKey, next, userID); err != nil {
		return err
	}
	return h.systemToolsMenu(ctx, target, userID, locale)
}

// systemToolsMenu is the root/delegated-operator panel: a discoverable
// home for surfaces that used to be command-only. Denies anyone else the
// same way every other operator-only screen in this package does.
func (h *UpdateHandler) systemToolsMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	if !h.isTeamMatchOperator(ctx, userID) {
		return h.refuse(ctx, target, locale, "hub:root")
	}
	rows := [][]InlineButton{
		{button(h.Texts.Get("hub.team_matches", locale), "team_matches:list:0")},
	}
	if h.isRootTeamMatchOperator(userID) {
		rows = append(rows,
			[]InlineButton{button(h.Texts.Get("hub.provider_status", locale), "hub:provider_status")},
			[]InlineButton{button(h.Texts.Get("hub.outbox", locale), "hub:outbox")},
			[]InlineButton{button(h.Texts.Get("notify.alert.title", locale), "hub:alerts")},
			[]InlineButton{button(h.crestSourceLabel(ctx, locale), "hub:crest_source")},
			[]InlineButton{button(h.Texts.Get("hub.team_match_operators", locale), "hub:team_match_operators")},
		)
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "hub:root")})
	return h.respond(ctx, target, bold(h.Texts.Get("hub.system_title", locale)), &InlineKeyboard{InlineKeyboard: rows})
}
