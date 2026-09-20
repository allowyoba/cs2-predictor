package telegram

import (
	"context"
	"strings"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// The per-chat settings screens: preferences, moderators, and the record
// of who changed what.

func topTierToastText(texts *Texts, settings chat.Settings) string {
	return texts.Get("settings.top_tier_toast", settings.Locale, tournamentMode(settings.DefaultTopTierOnly, settings.Locale))
}

// localeName spells out `of` (the language being named) in `in` (the
// language the reader is using) — e.g. RU named in EN is "Russian".
func (h *UpdateHandler) localeName(of, in common.LocaleCode) string {
	return h.Texts.Get(ternary(of == common.LocaleEN, "locale.english", "locale.russian"), in)
}

func (h *UpdateHandler) settingsView(ctx context.Context, target replyTarget, settings chat.Settings, dmContext bool) error {
	text := bold(escapeHTML(h.Texts.Get("menu.settings", settings.Locale)))
	languageLabel := h.Texts.Get("settings.language_label", settings.Locale, h.localeName(settings.Locale, settings.Locale))
	timezoneLabel := h.Texts.Get("settings.timezone_label", settings.Locale, settings.Timezone)
	tournamentMode := h.Texts.Get("settings.tournaments_all", settings.Locale)
	if settings.DefaultTopTierOnly {
		tournamentMode = h.Texts.Get("settings.tournaments_top", settings.Locale)
	}
	topTierLabel := h.Texts.Get("settings.tournaments_label", settings.Locale, tournamentMode)
	autoSubscribeLabel := h.Texts.Get("settings.auto_subscribe_label", settings.Locale, h.autoSubscribeState(settings))
	streamLanguageLabel := h.Texts.Get("settings.stream_language_label", settings.Locale, h.localeName(settings.StreamLocale(), settings.Locale))
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(languageLabel, "settings:locale")},
		{button(timezoneLabel, "settings:timezone")},
		{button(streamLanguageLabel, "settings:stream_language")},
		{button(h.Texts.Get("settings.notifications", settings.Locale), "settings:notify")},
		{button(h.quietHoursLabel(settings), "settings:quiet")},
		{button(h.logoSourceLabel(settings), "settings:logos")},
		{button(h.flagSourceLabel(settings), "settings:flags")},
		{button(topTierLabel, "settings:top_tier")},
		{button(autoSubscribeLabel, "settings:auto_subscribe")},
		{button(h.Texts.Get("settings.games", settings.Locale), "settings:games")},
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
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &kb)
}

// gameLabelKey maps a GameCode to its i18n key — the only place that
// mapping is spelled out, so gamesView and any future per-game copy agree.
func gameLabelKey(code competition.GameCode) string {
	switch code {
	case competition.GameCS2:
		return "game.cs2"
	case competition.GameDota2:
		return "game.dota2"
	default:
		return "game.unknown"
	}
}

// gameShortLabelKey is gameLabelKey's compact counterpart, for the places
// the game is named inline next to something else ("🏆 CS Major · CS2")
// rather than standing alone as a heading.
func gameShortLabelKey(code competition.GameCode) string {
	return gameLabelKey(code) + "_short"
}

// autoSubscribeState summarises the setting for the menu row: "on"/"off"
// while the chat follows a single game — naming it there would only repeat
// what the row above already says — and the names of the games it is on for
// once there are several, which is the whole point of splitting it.
func (h *UpdateHandler) autoSubscribeState(settings chat.Settings) string {
	on := make([]string, 0, len(settings.EnabledGames))
	for _, code := range settings.EnabledGames {
		if settings.AutoSubscribesTo(code) {
			on = append(on, h.Texts.Get(gameShortLabelKey(code), settings.Locale))
		}
	}
	if len(on) == 0 {
		return h.Texts.Get("settings.auto_subscribe_off", settings.Locale)
	}
	if len(settings.EnabledGames) < 2 {
		return h.Texts.Get("settings.auto_subscribe_on", settings.Locale)
	}
	return strings.Join(on, ", ")
}

// autoSubscribeView is the per-game screen, shown only once a chat follows
// more than one game: with a single game there is nothing to split, and the
// menu row toggles it in place instead of spending a tap on a screen with
// one button on it.
func (h *UpdateHandler) autoSubscribeView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	rows := make([][]InlineButton, 0, len(settings.EnabledGames)+1)
	for _, code := range settings.EnabledGames {
		mark := "⬜️ "
		if settings.AutoSubscribesTo(code) {
			mark = "✅ "
		}
		rows = append(rows, []InlineButton{button(mark+h.Texts.Get(gameLabelKey(code), settings.Locale), "settings:auto_subscribe:"+string(code))})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:settings")})
	text := bold(h.Texts.Get("settings.auto_subscribe_title", settings.Locale)) + "\n" +
		h.Texts.Get("settings.auto_subscribe_hint", settings.Locale)
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// logoSourceLabel is the settings-menu row for which crests Counter-Strike
// teams are shown with.
func (h *UpdateHandler) logoSourceLabel(settings chat.Settings) string {
	state := h.Texts.Get("settings.logos_provider", settings.Locale)
	if settings.PreferHLTVLogos {
		state = h.Texts.Get("settings.logos_hltv", settings.Locale)
	}
	return h.Texts.Get("settings.logos_label", settings.Locale, state)
}

// flagSourceLabel is the same row for the country flag beside a team name.
// A separate switch from the crest one: PandaScore's country is often the
// organisation's registration, HLTV's is the roster's, and a chat may well
// want the provider's picture with HLTV's flag or the other way round.
func (h *UpdateHandler) flagSourceLabel(settings chat.Settings) string {
	state := h.Texts.Get("settings.logos_provider", settings.Locale)
	if settings.PreferHLTVFlags {
		state = h.Texts.Get("settings.logos_hltv", settings.Locale)
	}
	return h.Texts.Get("settings.flags_label", settings.Locale, state)
}

// gamesView lists every supported game as a toggle — a chat starts with
// none enabled (see chat_enabled_game's migration), so this is also the
// only place a chat ever turns one on for the first time.
func (h *UpdateHandler) gamesView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	rows := make([][]InlineButton, 0, len(competition.Games)+1)
	for _, code := range competition.Games {
		mark := "⬜️ "
		if settings.GameEnabled(code) {
			mark = "✅ "
		}
		label := mark + h.Texts.Get(gameLabelKey(code), settings.Locale)
		rows = append(rows, []InlineButton{button(label, "settings:games:toggle:"+string(code))})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:settings")})
	text := bold(h.Texts.Get("settings.games_title", settings.Locale))
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
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
	"top_tier", "auto_subscribe", "stream_language", "notify", "quiet_hours", "logo_source", "flag_source", "games", "event_topic", "moderator_added", "moderator_removed",
	// stream_announce is retired as a call site — the switch moved into
	// the notifications screen — but rows written under it are still in
	// the history, and a label they no longer have would read as a bug.
	"stream_announce",
	"moderator_permissions_changed", "invitation_created", "invitation_revoked", "invitation_accepted",
}

func historyKindKey(kind string) string { return "history.kind." + kind }

// historyView answers "who changed this, and when?" — the question that
// became unanswerable once administration moved out of the group and into
// each manager's private chat with the bot.
func (h *UpdateHandler) historyView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	back := InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "menu:settings")}}}
	if h.AdminActions == nil {
		// Distinct from "no history yet" (below): this deployment has no
		// AdminActionLog wired at all, which is a configuration gap, not a
		// chat that simply hasn't had any admin activity.
		return newValidationError("admin action history is not configured")
	}
	actions, err := h.AdminActions.Recent(ctx, settings.ChatID, chat.AdminActionHistorySize)
	if err != nil {
		return err
	}
	if len(actions) == 0 {
		return h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("history.empty", settings.Locale)), &back)
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
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &back)
}

// actorLabel names who acted, falling back to the numeric id for someone
// whose display name was never captured (an anonymous group admin, say).
func actorLabel(action chat.AdminAction) string {
	if name := strings.TrimSpace(action.ActorName); name != "" {
		return name
	}
	return action.ActorID.String()
}

// moderatorsView, the moderator card, the permission wizard, participant
// picking and invitation screens live in screens_moderators.go.
