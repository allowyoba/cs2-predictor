package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// Screens that only exist in a private conversation with the bot: the
// personal statistics family, the chat picker, and the deep links that
// hand a group's admin panel off to a DM.

// managedChatsMenu lists every chat the user has passed a manager/admin
// check in, most-recently-confirmed first — tapping one live-reverifies
// and opens its admin panel (openManagedChat). Entries can be stale (the
// user may have since lost the rights that put them here); a stale one
// simply fails openManagedChat's live recheck with a clear denial rather
// than silently doing nothing.
func (h *UpdateHandler) managedChatsMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, backData string) error {
	chats, err := h.Chats.ManagedChats(ctx, userID)
	if err != nil {
		return err
	}
	back := []InlineButton{h.backButton(locale, backData)}
	if len(chats) == 0 {
		return h.respond(ctx, target, h.Texts.Get("dm.no_managed_chats", locale), &InlineKeyboard{InlineKeyboard: [][]InlineButton{back}})
	}
	rows := make([][]InlineButton, 0, len(chats)+1)
	for _, c := range chats {
		rows = append(rows, []InlineButton{button(truncate(c.Title, 40), cbManageOpen(c.ChatID))})
	}
	rows = append(rows, back)
	return h.respond(ctx, target, h.Texts.Get("dm.managed_chats_title", locale), &InlineKeyboard{InlineKeyboard: rows})
}

// openManagedChat live-reverifies the caller still manages targetChatID,
// points their DM session at it, and renders its admin home — the shared
// landing step for both the "/start admin_<id>" deep link and picking a
// chat from managedChatsMenu.
func (h *UpdateHandler) openManagedChat(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, targetChatID common.ChatID) error {
	denied := func() error {
		kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "manage:chats")}}}
		return h.respond(ctx, target, h.Texts.Get("dm.admin_denied", locale), &kb)
	}
	if err := h.requireManager(ctx, targetChatID, userID); err != nil {
		if errors.Is(err, chat.ErrAccessDenied) {
			return denied()
		}
		return err
	}
	settings, err := h.Chats.Find(ctx, targetChatID)
	if err != nil {
		return err
	}
	if settings == nil {
		return denied()
	}
	if err := h.Chats.SetDMSession(ctx, userID, targetChatID); err != nil {
		loggerFrom(ctx, h.Log).Warn("dm session save failed", "userId", userID.Value, "chatId", targetChatID.Value, "error", err)
	}
	// Every button on this panel — and on every screen reachable from it —
	// carries the chat it belongs to, so opening another chat's panel later
	// doesn't repoint this one.
	return h.menu(withCallbackScope(ctx, chatScope(targetChatID)), target, *settings, true)
}

const privateChatsPageSize = 8

// privateStatsMenu renders the root of a person's own statistics in a
// private chat: the periods they have data for, plus the buttons for
// managing chats, notifications, language and their display name. The
// locale is the user's own, independent of any group chat's setting, and
// the language button re-renders this screen with the other one.
// hasHubAccess mirrors startLanding's own condition for showing the
// mode-switcher hub at all: a plain voter with no managed chat and no
// operator rights has nowhere the hub would send them back to, so no back
// button is added (the previous, correct behavior for the common case).
// Anyone who could have reached this screen via hub:personal must be able
// to get back to it — this is exactly the dead end reported in production:
// an operator opening the hub's personal-stats panel had no way back to the
// hub at all.
func (h *UpdateHandler) hasHubAccess(ctx context.Context, userID common.UserID) (bool, error) {
	chats, err := h.Chats.ManagedChats(ctx, userID)
	if err != nil {
		return false, err
	}
	return len(chats) > 0 || h.isTeamMatchOperator(ctx, userID), nil
}

func (h *UpdateHandler) privateStatsMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	personal, err := h.personalScoring()
	if err != nil {
		return err
	}
	hubAccess, err := h.hasHubAccess(ctx, userID)
	if err != nil {
		return err
	}
	var backRow []InlineButton
	if hubAccess {
		backRow = []InlineButton{h.backButton(locale, "hub:root")}
	}

	available, err := personal.AvailableUserMonths(ctx, userID)
	if err != nil {
		return err
	}
	// Grouped by function rather than a flat dump: stats/analytics first
	// (this screen's primary purpose), then the person's own activity, then
	// personal preferences tucked behind one "Settings" entry point
	// (notifications/language/rename all move to privateSettingsMenu), then
	// help/back last — standard progressive disclosure instead of listing
	// every leaf action at the top level.
	//
	// Group management is deliberately absent: it is a mode of its own and
	// lives on the hub (modeHub's "hub:manage"), which the back row below
	// returns to. Repeating it here offered it to everyone, including the
	// majority who manage nothing and would only reach an empty list.
	if len(available) == 0 {
		rows := [][]InlineButton{
			{button(h.Texts.Get("private.bets", locale), "pstats:bets:0")},
			{button(h.Texts.Get("private.settings", locale), "pstats:settings")},
			{button(h.Texts.Get("menu.help", locale), "pstats:help")},
		}
		if backRow != nil {
			rows = append(rows, backRow)
		}
		return h.respond(ctx, target, h.Texts.Get("private.stats_empty", locale), &InlineKeyboard{InlineKeyboard: rows})
	}

	latest := available[0]
	latestMonthLabel := fmt.Sprintf("%s %d", shortMonthName(latest.Month, locale), latest.Year)
	rows := [][]InlineButton{
		{button(latestMonthLabel, fmt.Sprintf("pstats:month:%04d-%02d", latest.Year, latest.Month)), button(strconv.Itoa(latest.Year), fmt.Sprintf("pstats:year:%d", latest.Year))},
		{button(h.Texts.Get("stats.all_time", locale), "pstats:all"), button(h.Texts.Get("insights.title", locale), "pstats:insights")},
		{button(h.Texts.Get("private.chats", locale), "pstats:chats:0"), button(h.Texts.Get("stats.other_period", locale), "pstats:years")},
		{button(h.Texts.Get("private.bets", locale), "pstats:bets:0")},
		{button(h.Texts.Get("private.settings", locale), "pstats:settings")},
		{button(h.Texts.Get("menu.help", locale), "pstats:help")},
	}
	// The Mini App opens on top of this dashboard rather than replacing
	// it: the bot stays the place predictions are made, and the app is
	// where the history behind them is read.
	if h.MiniAppURL != "" {
		rows = append(rows, []InlineButton{webAppButton(h.Texts.Get("private.miniapp", locale), h.MiniAppURL)})
	}
	if backRow != nil {
		rows = append(rows, backRow)
	}
	return h.respond(ctx, target, h.Texts.Get("private.stats_choose", locale), &InlineKeyboard{InlineKeyboard: rows})
}

// privateSettingsMenu groups the person's own preferences — notifications,
// language, display name — behind one entry point from privateStatsMenu,
// instead of each living as its own top-level button alongside unrelated
// stats/navigation actions.
func (h *UpdateHandler) privateSettingsMenu(ctx context.Context, target replyTarget, locale common.LocaleCode) error {
	rows := [][]InlineButton{
		{button(h.Texts.Get("notify.title", locale), "notify:menu"), button(h.Texts.Get("dm.language", locale), "pstats:locale")},
		{button(h.Texts.Get("dm.timezone", locale), "pstats:timezone")},
		{button(h.Texts.Get("dm.rename", locale), "pstats:rename")},
		{button(h.Texts.Get("idea.title", locale), "idea:menu")},
		{h.backButton(locale, "pstats:menu")},
	}
	return h.respond(ctx, target, h.Texts.Get("private.settings_title", locale), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) privateYearMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	personal, err := h.personalScoring()
	if err != nil {
		return err
	}
	available, err := personal.AvailableUserMonths(ctx, userID)
	if err != nil {
		return err
	}
	seen := map[int]bool{}
	var rows [][]InlineButton
	for _, period := range available {
		if seen[period.Year] {
			continue
		}
		seen[period.Year] = true
		rows = append(rows, []InlineButton{
			button(strconv.Itoa(period.Year), fmt.Sprintf("pstats:year:%d", period.Year)),
			button(h.Texts.Get("stats.months_button", locale), fmt.Sprintf("pstats:months:%d", period.Year)),
		})
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "pstats:menu")})
	text := bold(escapeHTML(h.Texts.Get("stats.year", locale)))
	if len(seen) == 0 {
		text += "\n\n" + h.Texts.Get("stats.empty", locale)
	}
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) privateMonthMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, year int) error {
	personal, err := h.personalScoring()
	if err != nil {
		return err
	}
	available, err := personal.AvailableUserMonths(ctx, userID)
	if err != nil {
		return err
	}
	var rows [][]InlineButton
	var row []InlineButton
	for _, period := range available {
		if period.Year != year {
			continue
		}
		row = append(row, button(shortMonthName(period.Month, locale), fmt.Sprintf("pstats:month:%04d-%02d", year, period.Month)))
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "pstats:years")})
	text := bold(escapeHTML(fmt.Sprintf("%s · %d", h.Texts.Get("stats.month", locale), year)))
	if len(rows) == 1 {
		text += "\n\n" + h.Texts.Get("stats.empty", locale)
	}
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func privatePeriodName(texts *Texts, locale common.LocaleCode, period scoring.StatsPeriod) string {
	switch period.Kind {
	case scoring.PeriodYear:
		return texts.Get("stats.period_year", locale, period.Year)
	case scoring.PeriodMonth:
		return texts.Get("stats.period_month", locale, fmt.Sprintf("%s %d", shortMonthName(period.Month, locale), period.Year))
	default:
		return texts.Get("stats.period_all", locale)
	}
}

func (h *UpdateHandler) renderPrivateStats(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, period scoring.StatsPeriod) error {
	personal, err := h.personalScoring()
	if err != nil {
		return err
	}
	stats, err := personal.UserStats(ctx, userID, period)
	if err != nil {
		return err
	}
	periodName := privatePeriodName(h.Texts, locale, period)
	text := h.Texts.Get("private.stats_title", locale, escapeHTML(periodName))
	if stats == nil || stats.Predictions == 0 {
		text += "\n\n" + h.Texts.Get("stats.empty", locale)
	} else {
		text += "\n\n" + h.Texts.Get("private.points", locale, stats.Points)
		text += "\n" + h.Texts.Get("private.activity", locale, stats.Predictions, stats.Tournaments)
		text += "\n" + h.Texts.Get("private.accuracy", locale, stats.AccuracyPercent())
	}
	rows := [][]InlineButton{{h.backButton(locale, "pstats:menu")}}
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) privateChatsMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, page int) error {
	personal, err := h.personalScoring()
	if err != nil {
		return err
	}
	chats, err := personal.UserChatStats(ctx, userID)
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		rows := [][]InlineButton{{h.backButton(locale, "pstats:menu")}}
		return h.respond(ctx, target, h.Texts.Get("private.chats_title", locale)+"\n\n"+h.Texts.Get("stats.empty", locale), &InlineKeyboard{InlineKeyboard: rows})
	}

	maxPage := (len(chats) - 1) / privateChatsPageSize
	if page > maxPage {
		page = maxPage
	}
	start := page * privateChatsPageSize
	end := start + privateChatsPageSize
	if end > len(chats) {
		end = len(chats)
	}

	var rows [][]InlineButton
	for _, chatStats := range chats[start:end] {
		label := truncate(chatStats.ChatTitle, 40)
		rows = append(rows, []InlineButton{button(label, fmt.Sprintf("pstats:chat:%d:%d", chatStats.ChatID.Value, page))})
	}
	totalPages := maxPage + 1
	if nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return fmt.Sprintf("pstats:chats:%d", p)
	}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "pstats:menu")})
	return h.respond(ctx, target, h.Texts.Get("private.chats_title", locale), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) renderPrivateChatStats(ctx context.Context, target replyTarget, userID common.UserID, chatID common.ChatID, locale common.LocaleCode, page int) error {
	personal, err := h.personalScoring()
	if err != nil {
		return err
	}
	chats, err := personal.UserChatStats(ctx, userID)
	if err != nil {
		return err
	}
	var selected *scoring.UserChatStanding
	for i := range chats {
		if chats[i].ChatID == chatID {
			selected = &chats[i]
			break
		}
	}
	if selected == nil {
		return h.privateChatsMenu(ctx, target, userID, locale, page)
	}

	text := h.Texts.Get("private.chat_title", locale, escapeHTML(selected.ChatTitle), escapeHTML(h.Texts.Get("stats.period_all", locale)))
	text += "\n\n" + h.Texts.Get("private.points", locale, selected.Points)
	text += "\n" + h.Texts.Get("private.activity", locale, selected.Predictions, selected.Tournaments)
	text += "\n" + h.Texts.Get("private.accuracy", locale, selected.AccuracyPercent())
	rows := [][]InlineButton{
		{button(h.Texts.Get("private.chat_bets", locale), betsCallback(&chatID, page, 0))},
		{h.backButton(locale, fmt.Sprintf("pstats:chats:%d", page))},
	}
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) statsDeepLink(chatID common.ChatID) (string, bool) {
	if h.BotUsername == "" {
		return "", false
	}
	return fmt.Sprintf("https://t.me/%s?start=stats_%s", h.BotUsername, strconv.FormatInt(chatID.Value, 36)), true
}

// dmDeepLink builds a t.me/<bot>?start=admin_<chatId> deep link, or
// ok=false if the bot's own username hasn't been resolved yet (see
// UpdateHandler.BotUsername).
func (h *UpdateHandler) dmDeepLink(chatID common.ChatID) (string, bool) {
	if h.BotUsername == "" {
		return "", false
	}
	return fmt.Sprintf("https://t.me/%s?start=admin_%s", h.BotUsername, strconv.FormatInt(chatID.Value, 36)), true
}

// tryRedirectToDMAdmin replies with a short "manage this in DM" pointer
// instead of performing an admin action directly in the group — the
// group-side landing point for every admin command that has a DM
// equivalent today (/events, /timezone). handled=false means BotUsername
// hasn't been resolved (shouldn't happen outside tests/a getMe outage);
// callers fall back to performing the action in-group rather than leaving
// the user with no way to act at all.
func (h *UpdateHandler) tryRedirectToDMAdmin(ctx context.Context, settings chat.Settings, topicID *int64, bodyKey string) (handled bool, err error) {
	link, ok := h.dmDeepLink(settings.ChatID)
	if !ok {
		return false, nil
	}
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{urlButton(h.Texts.Get("menu.manage_in_dm", settings.Locale), link)}}}
	return true, h.sendTextWithKeyboard(ctx, settings.ChatID, h.Texts.Get(bodyKey, settings.Locale), kb, topicID)
}
