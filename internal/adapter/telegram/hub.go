package telegram

import (
	"context"

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
func (h *UpdateHandler) modeHub(ctx context.Context, target replyTarget, locale common.LocaleCode, managesAnyChat, isOperator bool) error {
	var rows [][]InlineButton
	rows = append(rows, []InlineButton{button(h.Texts.Get("hub.personal", locale), "hub:personal")})
	if managesAnyChat {
		rows = append(rows, []InlineButton{button(h.Texts.Get("hub.manage", locale), "hub:manage")})
	}
	if isOperator {
		rows = append(rows, []InlineButton{button(h.Texts.Get("hub.system", locale), "hub:system")})
	}
	return h.respond(ctx, target, bold(h.Texts.Get("hub.title", locale)), &InlineKeyboard{InlineKeyboard: rows})
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
			[]InlineButton{button(h.Texts.Get("hub.team_match_operators", locale), "hub:team_match_operators")},
		)
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "hub:root")})
	return h.respond(ctx, target, bold(h.Texts.Get("hub.system_title", locale)), &InlineKeyboard{InlineKeyboard: rows})
}
