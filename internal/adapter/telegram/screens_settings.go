package telegram

import (
	"context"
	"strings"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// The per-chat settings screens: preferences, moderators, and the record
// of who changed what.

func topTierToastText(settings chat.Settings) string {
	label := ternary(settings.Locale == common.LocaleRU, "Турниры: ", "Tournaments: ")
	return "✅ " + label + tournamentMode(settings.DefaultTopTierOnly, settings.Locale)
}

func (h *UpdateHandler) settingsView(ctx context.Context, target replyTarget, settings chat.Settings, dmContext bool) error {
	text := bold(escapeHTML(h.Texts.Get("menu.settings", settings.Locale)))
	language := ternary(settings.Locale == common.LocaleRU, "Русский", "English")
	languageLabel := h.Texts.Get("settings.language_label", settings.Locale, language)
	timezoneLabel := h.Texts.Get("settings.timezone_label", settings.Locale, settings.Timezone)
	tournamentMode := h.Texts.Get("settings.tournaments_all", settings.Locale)
	if settings.DefaultTopTierOnly {
		tournamentMode = h.Texts.Get("settings.tournaments_top", settings.Locale)
	}
	topTierLabel := h.Texts.Get("settings.tournaments_label", settings.Locale, tournamentMode)
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(languageLabel, "settings:locale")},
		{button(timezoneLabel, "settings:timezone")},
		{button(topTierLabel, "settings:top_tier")},
		{button(h.Texts.Get("settings.moderators", settings.Locale), "settings:moderators")},
		{button(h.Texts.Get("settings.history", settings.Locale), "settings:history")},
	}}
	// Copying settings between chats only makes sense from the DM panel,
	// where "the chats you manage" is the frame the user is already in; in
	// a group it would silently reach into chats nobody in this room can
	// see.
	if dmContext {
		kb.InlineKeyboard = append(kb.InlineKeyboard, []InlineButton{button(h.Texts.Get("settings.apply_all", settings.Locale), "bulk:menu")})
	}
	kb.InlineKeyboard = append(kb.InlineKeyboard, []InlineButton{h.backButton(settings.Locale, "menu:main")})
	return h.respond(ctx, target, text, &kb)
}

// moderatorName resolves a moderator's stored display name so the history
// records who was removed rather than a bare numeric id. Falls back to the
// id when the row is already gone or unreadable — a best-effort label for a
// best-effort log.
func (h *UpdateHandler) moderatorName(ctx context.Context, chatID common.ChatID, userID common.UserID) string {
	mods, err := h.Chats.ListModerators(ctx, chatID)
	if err != nil {
		return userID.String()
	}
	for _, mod := range mods {
		if mod.UserID == userID && strings.TrimSpace(mod.DisplayName) != "" {
			return mod.DisplayName
		}
	}
	return userID.String()
}

// adminActionKinds enumerates every chat.AdminAction.Kind this handler
// writes. It exists so the i18n parity test can check that each one has a
// label in both bundles — historyKindKey composes its key at runtime, which
// the bundle scanner cannot see. Adding a logAdminAction call site without
// adding its kind here fails that test.
var adminActionKinds = []string{
	"subscribe", "unsubscribe", "locale", "timezone",
	"top_tier", "event_topic", "moderator_added", "moderator_removed",
}

func historyKindKey(kind string) string { return "history.kind." + kind }

// historyView answers "who changed this, and when?" — the question that
// became unanswerable once administration moved out of the group and into
// each manager's private chat with the bot.
func (h *UpdateHandler) historyView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	back := InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "menu:settings")}}}
	if h.AdminActions == nil {
		return h.respond(ctx, target, h.Texts.Get("history.empty", settings.Locale), &back)
	}
	actions, err := h.AdminActions.Recent(ctx, settings.ChatID, chat.AdminActionHistorySize)
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		return h.respond(ctx, target, h.Texts.Get("history.empty", settings.Locale), &back)
	}
	loc := chatZone(settings)
	lines := make([]string, 0, len(actions))
	for _, action := range actions {
		line := "• " + code(action.CreatedAt.In(loc).Format("02.01 15:04")) + " " + h.Texts.Get(historyKindKey(action.Kind), settings.Locale)
		if strings.TrimSpace(action.Detail) != "" {
			line += " — " + bold(escapeHTML(truncate(action.Detail, 40)))
		}
		lines = append(lines, line+"\n  <i>"+escapeHTML(actorLabel(action))+"</i>")
	}
	text := bold(h.Texts.Get("history.title", settings.Locale)) + "\n\n" + strings.Join(lines, "\n")
	return h.respond(ctx, target, text, &back)
}

// actorLabel names who acted, falling back to the numeric id for someone
// whose display name was never captured (an anonymous group admin, say).
func actorLabel(action chat.AdminAction) string {
	if name := strings.TrimSpace(action.ActorName); name != "" {
		return name
	}
	return action.ActorID.String()
}

func (h *UpdateHandler) moderatorsView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	mods, err := h.Chats.ListModerators(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	text := h.Texts.Get("moderators.title", settings.Locale)
	var rows [][]InlineButton
	if len(mods) == 0 {
		text += "\n\n" + h.Texts.Get("moderators.empty", settings.Locale)
	} else {
		var lines []string
		for _, mod := range mods {
			name := strings.TrimSpace(mod.DisplayName)
			if name == "" {
				name = "Telegram user " + mod.UserID.String()
			}
			handle := ""
			if mod.Username != "" {
				handle = " @" + escapeHTML(mod.Username)
			}
			lines = append(lines, "• "+bold(escapeHTML(name))+handle)
			rows = append(rows, []InlineButton{button(h.Texts.Get("moderators.remove", settings.Locale)+" · "+truncate(name, 28), cbModeratorRemove(mod.UserID))})
		}
		text += "\n\n" + strings.Join(lines, "\n")
	}
	rows = append(rows, []InlineButton{button(h.Texts.Get("moderators.add", settings.Locale), "moderators:add")})
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:settings")})
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}
