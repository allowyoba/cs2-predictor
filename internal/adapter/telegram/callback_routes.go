package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// This file holds routeCallback's routing table: what callback data a route
// matches, what access it requires, and what it does. Splitting match/guard/
// handle into data (rather than writing all three as one linear switch) is
// what makes an ungated route a decision instead of an omission — grep this
// file for "guard: nil" to see every callback that runs with no
// authorization check beyond routeCallback's own scope check, rather than
// trusting each of ~40 case bodies to have remembered its own.
//
// A route's handle sees the exact same cb/target/settings routeCallback
// already resolved, plus the full, untrimmed callback data — precisely what
// the old switch cases closed over, so every handler below is a direct,
// behavior-preserving lift of its old case body.

type callbackHandler func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (answered bool, err error)

type callbackGuard func(h *UpdateHandler, ctx context.Context, chatID common.ChatID, userID common.UserID) error

type callbackRoute struct {
	match  func(data string) bool
	guard  callbackGuard
	handle callbackHandler
}

func exact(want string) func(string) bool {
	return func(data string) bool { return data == want }
}

func prefixed(want string) func(string) bool {
	return func(data string) bool { return strings.HasPrefix(data, want) }
}

// --- guards ---
//
// Named wrappers around the three checks routes use, so the table reads as
// "guardManager" / "guardTelegramAdmin" / "guardPermission(X)" instead of a
// differently-indented inline call repeated at ~25 call sites.

func guardManager(h *UpdateHandler, ctx context.Context, chatID common.ChatID, userID common.UserID) error {
	return h.requireManager(ctx, chatID, userID)
}

func guardTelegramAdmin(h *UpdateHandler, ctx context.Context, chatID common.ChatID, userID common.UserID) error {
	return h.requireTelegramAdmin(ctx, chatID, userID)
}

func guardPermission(permission chat.Permission) callbackGuard {
	return func(h *UpdateHandler, ctx context.Context, chatID common.ChatID, userID common.UserID) error {
		return h.requirePermission(ctx, chatID, userID, permission)
	}
}

// simple adapts a plain "render this screen" renderer — the (ctx, target,
// settings) error shape most screens_*.go functions already have — into the
// full callbackHandler shape, so most simple() call sites below are a bare
// method expression such as simple((*UpdateHandler).statsMenu).
func simple(fn func(h *UpdateHandler, ctx context.Context, target replyTarget, settings chat.Settings) error) callbackHandler {
	return func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, fn(h, ctx, target, settings)
	}
}

// callbackRoutes is checked in order, first match wins — exactly like the
// switch it replaces. Built once at package init rather than per call.
var callbackRoutes = []callbackRoute{
	{match: exact("menu:main"), handle: routeMenuMain},
	{match: exact("menu:stats"), handle: simple((*UpdateHandler).statsMenu)},
	{match: exact("menu:events"), handle: simple((*UpdateHandler).eventMenu)},
	{match: exact("events:add"), guard: guardManager, handle: simple((*UpdateHandler).eventAddMenu)},
	{match: exact("events:search"), guard: guardManager, handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.requestEventSearch(ctx, cb, settings)
	}},
	{match: prefixed("events:browse:"), guard: guardManager, handle: routeEventsBrowse},
	{match: exact("events:cleanup"), guard: guardManager, handle: simple((*UpdateHandler).cleanupMenu)},
	{match: exact("events:cleanup:go"), guard: guardManager, handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return h.cleanupFinished(ctx, cb, target, settings)
	}},
	{match: exact("events:mine"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.subscribedEvents(ctx, target, settings, cb.Message.Chat.Type == "private")
	}},
	{match: prefixed("events:view:"), handle: routeEventsView},
	// guardManager mirrors the requireManager check routeMenuMain already
	// runs before it will render this same screen's "⚙️ Settings" button
	// for the DM panel — but that check only fired one hop up, in the
	// parent route. A callback naming "menu:settings" directly (a forged/
	// replayed callback_data reaching this route without going through
	// menu:main first — Telegram doesn't cryptographically bind
	// callback_data to the button actually tapped) skipped it entirely.
	// Every one of this screen's own child actions already requires at
	// least this bar (or the stricter guardPermission), so gating the
	// parent screen the same way costs no legitimate moderator anything.
	{match: exact("menu:settings"), guard: guardManager, handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.settingsView(ctx, target, settings, cb.Message.Chat.Type == "private")
	}},
	{match: exact("menu:upcoming"), handle: simple((*UpdateHandler).upcoming)},
	{match: exact("menu:rules"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "menu:main")}}}
		return false, h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("rules.text", settings.Locale)), kb)
	}},
	{match: exact("menu:help"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.helpView(ctx, target, settings.Locale, "menu:main")
	}},
	{match: prefixed("stats:p:"), handle: routeStatsPage},
	{match: exact("stats:all"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.renderLeaderboard(ctx, target, settings, scoring.AllTime(), "menu:stats", common.UserID{Value: cb.From.ID})
	}},
	{match: exact("stats:years"), handle: simple((*UpdateHandler).yearMenu)},
	{match: prefixed("stats:months:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		year, _ := strconv.Atoi(strings.TrimPrefix(data, "stats:months:"))
		return false, h.monthMenu(ctx, target, settings, year)
	}},
	{match: prefixed("stats:year:"), handle: routeStatsYear},
	{match: prefixed("stats:month:"), handle: routeStatsMonth},
	{match: prefixed("stats:event:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		id, err := uuid.Parse(strings.TrimPrefix(data, "stats:event:"))
		if err != nil {
			return false, newValidationError("invalid event id")
		}
		return false, h.renderLeaderboard(ctx, target, settings, scoring.ForEvent(common.EventID{Value: id}), "stats:events", common.UserID{Value: cb.From.ID})
	}},
	{match: prefixed("stats:mine:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		id, err := uuid.Parse(strings.TrimPrefix(data, "stats:mine:"))
		if err != nil {
			return false, newValidationError("invalid event id")
		}
		return false, h.personalStats(ctx, target, settings, cb.From, common.EventID{Value: id})
	}},
	{match: prefixed("stats:notif:event:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		id, err := uuid.Parse(strings.TrimPrefix(data, "stats:notif:event:"))
		if err != nil {
			return false, newValidationError("invalid event id")
		}
		notifTarget := sendTarget(settings.ChatID, cb.Message.MessageThreadID)
		return false, h.renderLeaderboard(ctx, notifTarget, settings, scoring.ForEvent(common.EventID{Value: id}), "stats:events", common.UserID{Value: cb.From.ID})
	}},
	{match: prefixed("stats:notif:mine:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		id, err := uuid.Parse(strings.TrimPrefix(data, "stats:notif:mine:"))
		if err != nil {
			return false, newValidationError("invalid event id")
		}
		notifTarget := sendTarget(settings.ChatID, cb.Message.MessageThreadID)
		return false, h.personalStats(ctx, notifTarget, settings, cb.From, common.EventID{Value: id})
	}},
	{match: exact("stats:events"), handle: simple((*UpdateHandler).eventStatsMenu)},
	{match: prefixed("stats:evbets:me:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		id, err := uuid.Parse(strings.TrimPrefix(data, "stats:evbets:me:"))
		if err != nil {
			return false, newValidationError("invalid event id")
		}
		eventID := common.EventID{Value: id}
		viewer := common.UserID{Value: cb.From.ID}
		return false, h.renderEventBets(ctx, target, settings, eventID, viewer, "", viewer, "stats:event:"+id.String())
	}},
	{match: prefixed("stats:evbets:u:"), handle: routeEventBetsForUser},
	{match: prefixed("stats:evpick:"), handle: routeEventParticipantPicker},
	{match: prefixed("stats:chart:"), handle: routeStatsChart},
	{match: prefixed("stats:rankchart:"), handle: routeStatsRankChart},
	// guardManager (not the stricter guardTelegramAdmin the moderators:*
	// mutation routes below use): seeing who the other moderators are is
	// harmless for an existing moderator to know, but was previously
	// reachable with no check at all — including via a forged/replayed
	// callback_data naming an arbitrary chat this caller doesn't belong to
	// — disclosing that chat's moderator roster (names, usernames, ids).
	{match: exact("settings:moderators"), guard: guardManager, handle: simple((*UpdateHandler).moderatorsView)},
	{match: exact("bulk:menu"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.bulkMenu(ctx, cb, target, settings)
	}},
	{match: prefixed("bulk:ask:"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.bulkConfirm(ctx, cb, target, settings, strings.TrimPrefix(data, "bulk:ask:"))
	}},
	{match: prefixed("bulk:do:"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.bulkApply(ctx, cb, target, settings, strings.TrimPrefix(data, "bulk:do:"))
	}},
	{match: exact("settings:timezone"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: simple((*UpdateHandler).timezoneView)},
	{match: prefixed("settings:tz:"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return h.setTimezone(ctx, cb, target, settings, strings.TrimPrefix(data, "settings:tz:"))
	}},
	{match: exact("settings:history"), guard: guardPermission(chat.PermissionViewStats), handle: simple((*UpdateHandler).historyView)},
	// Appointing/removing a moderator or editing their permissions is
	// Telegram-admin-only (see chat.Permission's doc comment) — a moderator
	// must never be able to manage other moderators.
	{match: exact("moderators:add"), guard: guardTelegramAdmin, handle: simple((*UpdateHandler).assignMenuView)},
	{match: prefixed("moderators:pick:"), guard: guardTelegramAdmin, handle: routeModeratorsPick},
	{match: prefixed("moderators:card:"), guard: guardTelegramAdmin, handle: routeModeratorsCard},
	{match: prefixed("moderators:permstart:"), guard: guardTelegramAdmin, handle: routeModeratorsPermStart},
	{match: prefixed("moderators:permpreset:"), guard: guardTelegramAdmin, handle: routeModeratorsPermPreset},
	{match: prefixed("moderators:permtoggle:"), guard: guardTelegramAdmin, handle: routeModeratorsPermToggle},
	{match: prefixed("moderators:permconfirm:"), guard: guardTelegramAdmin, handle: routeModeratorsPermConfirm},
	{match: prefixed("moderators:permapply:"), guard: guardTelegramAdmin, handle: routeModeratorsPermApply},
	{match: prefixed("moderators:invite:revoke:ask:"), guard: guardTelegramAdmin, handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return false, h.invitationRevokeAskView(ctx, target, settings, strings.TrimPrefix(data, "moderators:invite:revoke:ask:"))
	}},
	{match: prefixed("moderators:invite:revoke:do:"), guard: guardTelegramAdmin, handle: routeModeratorsInviteRevokeDo},
	{match: prefixed("moderators:remove:ask:"), guard: guardTelegramAdmin, handle: routeModeratorsRemoveAsk},
	{match: prefixed("moderators:remove:do:"), guard: guardTelegramAdmin, handle: routeModeratorsRemoveDo},
	{match: exact("settings:locale"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: routeSettingsLocale},
	{match: exact("settings:top_tier"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: routeSettingsTopTier},
	{match: exact("settings:auto_subscribe"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: routeSettingsAutoSubscribe},
	{match: exact("settings:stream_language"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: routeSettingsStreamLanguage},
	{match: exact("settings:games"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: simple((*UpdateHandler).gamesView)},
	{match: prefixed("settings:games:toggle:"), guard: guardPermission(chat.PermissionManageGroupSettings), handle: routeSettingsGamesToggle},
	{match: prefixed("subscribe:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return h.subscribe(ctx, cb, target, settings, strings.TrimPrefix(data, "subscribe:"))
	}},
	{match: prefixed("unsubscribe:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return h.unsubscribe(ctx, cb, target, settings, strings.TrimPrefix(data, "unsubscribe:"))
	}},
	{match: prefixed("unsubok:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		// Deliberately ignores the settings routed here — see
		// confirmUnsubscribe's doc comment.
		return h.confirmUnsubscribe(ctx, cb, strings.TrimPrefix(data, "unsubok:"))
	}},
	{match: prefixed("unsubreject:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return h.rejectUnsubscribe(ctx, cb, strings.TrimPrefix(data, "unsubreject:"))
	}},
	{match: prefixed("event-topic:"), handle: func(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
		return h.setEventTopic(ctx, cb, settings, strings.TrimPrefix(data, "event-topic:"))
	}},
}

// --- handlers with real parsing/branching, kept as named functions rather
// than inline closures purely for readability (same logic as their former
// case bodies, unchanged) ---

func routeMenuMain(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	if cb.Message.Chat.Type == "private" {
		if accessErr := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); accessErr != nil {
			if errors.Is(accessErr, chat.ErrAccessDenied) {
				return false, h.readOnlyGroupMenu(ctx, target, settings)
			}
			return false, accessErr
		}
	}
	return false, h.menu(ctx, target, settings, cb.Message.Chat.Type == "private")
}

func routeEventsBrowse(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	mode, page, err := parseEventBrowseCallback(data)
	if err != nil {
		return false, err
	}
	return false, h.renderEventBrowse(ctx, target, settings, mode == "top", page)
}

func routeEventsView(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	id, err := uuid.Parse(strings.TrimPrefix(data, "events:view:"))
	if err != nil {
		return false, newValidationError("invalid event id")
	}
	return false, h.eventDetails(ctx, target, settings, common.EventID{Value: id}, cb.Message.Chat.Type == "private")
}

// routeStatsPage dispatches "stats:p:<kind>:..." to the parser for that one
// leaderboard-period kind — split out of a single function (once one long
// nested switch) so each kind's parsing stays independently readable and
// under the complexity budget the rest of this file holds to.
func routeStatsPage(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	parts := strings.Split(data, ":")
	if len(parts) < 4 {
		return false, newValidationError("invalid leaderboard page callback")
	}
	viewer := common.UserID{Value: cb.From.ID}
	switch parts[2] {
	case "a":
		return routeStatsPageAllTime(h, ctx, target, settings, viewer, parts)
	case "y":
		return routeStatsPageYear(h, ctx, target, settings, viewer, parts)
	case "m":
		return routeStatsPageMonth(h, ctx, target, settings, viewer, parts)
	case "e":
		return routeStatsPageEvent(h, ctx, target, settings, viewer, parts)
	default:
		return false, newValidationError("unknown leaderboard period")
	}
}

func routeStatsPageAllTime(h *UpdateHandler, ctx context.Context, target replyTarget, settings chat.Settings, viewer common.UserID, parts []string) (bool, error) {
	if len(parts) != 4 {
		return false, newValidationError("invalid all-time leaderboard page callback")
	}
	page, parseErr := strconv.Atoi(parts[3])
	if parseErr != nil || page < 0 {
		return false, newValidationError("invalid leaderboard page")
	}
	return false, h.renderLeaderboard(ctx, target, settings, scoring.AllTime(), "menu:stats", viewer, page)
}

func routeStatsPageYear(h *UpdateHandler, ctx context.Context, target replyTarget, settings chat.Settings, viewer common.UserID, parts []string) (bool, error) {
	if len(parts) != 6 {
		return false, newValidationError("invalid yearly leaderboard page callback")
	}
	year, yearErr := strconv.Atoi(parts[3])
	page, pageErr := strconv.Atoi(parts[4])
	if yearErr != nil || pageErr != nil || page < 0 {
		return false, newValidationError("invalid yearly leaderboard page")
	}
	back := "menu:stats"
	if parts[5] == "y" {
		back = "stats:years"
	}
	return false, h.renderLeaderboard(ctx, target, settings, scoring.ForYear(year), back, viewer, page)
}

func routeStatsPageMonth(h *UpdateHandler, ctx context.Context, target replyTarget, settings chat.Settings, viewer common.UserID, parts []string) (bool, error) {
	if len(parts) != 6 || len(parts[3]) != 6 {
		return false, newValidationError("invalid monthly leaderboard page callback")
	}
	year, yearErr := strconv.Atoi(parts[3][:4])
	monthInt, monthErr := strconv.Atoi(parts[3][4:])
	page, pageErr := strconv.Atoi(parts[4])
	if yearErr != nil || monthErr != nil || monthInt < 1 || monthInt > 12 || pageErr != nil || page < 0 {
		return false, newValidationError("invalid monthly leaderboard page")
	}
	back := "menu:stats"
	if parts[5] == "m" {
		back = fmt.Sprintf("stats:months:%d", year)
	}
	return false, h.renderLeaderboard(ctx, target, settings, scoring.ForMonth(year, time.Month(monthInt)), back, viewer, page)
}

func routeStatsPageEvent(h *UpdateHandler, ctx context.Context, target replyTarget, settings chat.Settings, viewer common.UserID, parts []string) (bool, error) {
	if len(parts) != 5 {
		return false, newValidationError("invalid event leaderboard page callback")
	}
	id, idErr := uuid.Parse(parts[3])
	page, pageErr := strconv.Atoi(parts[4])
	if idErr != nil || pageErr != nil || page < 0 {
		return false, newValidationError("invalid event leaderboard page")
	}
	return false, h.renderLeaderboard(ctx, target, settings, scoring.ForEvent(common.EventID{Value: id}), "stats:events", viewer, page)
}

// routeEventBetsForUser handles "stats:evbets:u:<eventId>:<userId>" — an
// arbitrary participant picked off routeEventParticipantPicker's list, as
// opposed to "stats:evbets:me:<eventId>"'s always-the-viewer shortcut. The
// subject's display name for the title comes from the same event
// leaderboard the picker itself was built from, so a stale/forged userId
// that never actually appears in it just renders an empty screen rather
// than guessing a name.
func routeEventBetsForUser(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	parts := strings.Split(strings.TrimPrefix(data, "stats:evbets:u:"), ":")
	if len(parts) != 2 {
		return false, newValidationError("invalid participant bets callback")
	}
	id, idErr := uuid.Parse(parts[0])
	userVal, userErr := strconv.ParseInt(parts[1], 10, 64)
	if idErr != nil || userErr != nil {
		return false, newValidationError("invalid participant bets callback")
	}
	eventID := common.EventID{Value: id}
	subject := common.UserID{Value: userVal}
	viewer := common.UserID{Value: cb.From.ID}

	standings, err := h.Scoring.Leaderboard(ctx, settings.ChatID, scoring.ForEvent(eventID))
	if err != nil {
		return false, err
	}
	var subjectName string
	for _, s := range standings {
		if s.UserID == subject {
			subjectName = s.DisplayName
			break
		}
	}
	backData := eventParticipantPickerCallback(eventID, 0)
	return false, h.renderEventBets(ctx, target, settings, eventID, subject, subjectName, viewer, backData)
}

func routeEventParticipantPicker(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	parts := strings.Split(strings.TrimPrefix(data, "stats:evpick:"), ":")
	if len(parts) != 2 {
		return false, newValidationError("invalid participant picker callback")
	}
	id, idErr := uuid.Parse(parts[0])
	page, pageErr := strconv.Atoi(parts[1])
	if idErr != nil || pageErr != nil || page < 0 {
		return false, newValidationError("invalid participant picker callback")
	}
	eventID := common.EventID{Value: id}
	return false, h.renderEventParticipantPicker(ctx, target, settings, eventID, page, "stats:event:"+id.String())
}

// parseChartPeriod parses the "<kind>[:...]" tail both stats:chart: and
// stats:rankchart: share — the same period encoding leaderboardPageData
// already uses for pagination, minus the page number neither chart has a
// use for.
func parseChartPeriod(parts []string) (scoring.StatsPeriod, error) {
	if len(parts) == 0 {
		return scoring.StatsPeriod{}, newValidationError("invalid chart callback")
	}
	switch parts[0] {
	case "a":
		return scoring.AllTime(), nil
	case "y":
		if len(parts) != 2 {
			return scoring.StatsPeriod{}, newValidationError("invalid chart callback")
		}
		year, err := strconv.Atoi(parts[1])
		if err != nil {
			return scoring.StatsPeriod{}, newValidationError("invalid chart callback")
		}
		return scoring.ForYear(year), nil
	case "m":
		if len(parts) != 2 {
			return scoring.StatsPeriod{}, newValidationError("invalid chart callback")
		}
		year, month, err := parseYearMonth(parts[1])
		if err != nil {
			return scoring.StatsPeriod{}, newValidationError("invalid chart callback")
		}
		return scoring.ForMonth(year, month), nil
	case "e":
		if len(parts) != 2 {
			return scoring.StatsPeriod{}, newValidationError("invalid chart callback")
		}
		id, err := uuid.Parse(parts[1])
		if err != nil {
			return scoring.StatsPeriod{}, newValidationError("invalid chart callback")
		}
		return scoring.ForEvent(common.EventID{Value: id}), nil
	default:
		return scoring.StatsPeriod{}, newValidationError("invalid chart callback")
	}
}

// routeStatsChart renders/uploads a period's rating (cumulative points)
// chart. Whether it's drawn for everyone or just the caller follows the
// same personal-vs-group signal sendProgressionChart itself resolves.
func routeStatsChart(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	period, err := parseChartPeriod(strings.Split(strings.TrimPrefix(data, "stats:chart:"), ":"))
	if err != nil {
		return false, err
	}
	viewer := common.UserID{Value: cb.From.ID}
	return false, h.sendProgressionChart(ctx, target, settings, period, viewer)
}

// routeStatsRankChart is routeStatsChart's sibling for the leaderboard
// position (rank movement) chart.
func routeStatsRankChart(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	period, err := parseChartPeriod(strings.Split(strings.TrimPrefix(data, "stats:rankchart:"), ":"))
	if err != nil {
		return false, err
	}
	viewer := common.UserID{Value: cb.From.ID}
	return false, h.sendRankChart(ctx, target, settings, period, viewer)
}

func routeStatsYear(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 || len(parts) > 4 {
		return false, newValidationError("invalid stats year callback")
	}
	year, parseErr := strconv.Atoi(parts[2])
	if parseErr != nil {
		return false, newValidationError("invalid stats year")
	}
	back := "menu:stats"
	if len(parts) == 4 && parts[3] == "years" {
		back = "stats:years"
	}
	return false, h.renderLeaderboard(ctx, target, settings, scoring.ForYear(year), back, common.UserID{Value: cb.From.ID})
}

func routeStatsMonth(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 || len(parts) > 5 {
		return false, newValidationError("invalid stats month callback")
	}
	ym := parts[2]
	year, month, parseErr := parseYearMonth(ym)
	if parseErr != nil {
		return false, newValidationError("invalid month %q", ym)
	}
	back := "menu:stats"
	if len(parts) == 5 && parts[3] == "months" {
		backYear, backErr := strconv.Atoi(parts[4])
		if backErr != nil {
			return false, newValidationError("invalid stats month back year")
		}
		back = fmt.Sprintf("stats:months:%d", backYear)
	}
	return false, h.renderLeaderboard(ctx, target, settings, scoring.ForMonth(year, month), back, common.UserID{Value: cb.From.ID})
}

func routeModeratorsPick(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	page, parseErr := strconv.Atoi(strings.TrimPrefix(data, "moderators:pick:"))
	if parseErr != nil || page < 0 {
		return false, newValidationError("invalid participant page")
	}
	return false, h.participantPickView(ctx, target, settings, page)
}

func routeModeratorsCard(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	id, parseErr := strconv.ParseInt(strings.TrimPrefix(data, "moderators:card:"), 36, 64)
	if parseErr != nil {
		return false, newValidationError("invalid moderator id")
	}
	return false, h.moderatorCardView(ctx, target, settings, common.UserID{Value: id})
}

func routeModeratorsPermStart(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	purpose, subject, _, parseOk := parsePermWizardData(strings.TrimPrefix(data, "moderators:permstart:"))
	if !parseOk {
		return false, newValidationError("invalid permission wizard payload")
	}
	return false, h.permPresetView(ctx, target, settings, purpose, subject)
}

func routeModeratorsPermPreset(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	purpose, subject, rest, parseOk := parsePermWizardData(strings.TrimPrefix(data, "moderators:permpreset:"))
	if !parseOk || len(rest) != 1 {
		return false, newValidationError("invalid permission wizard payload")
	}
	var perms []chat.Permission
	switch rest[0] {
	case "full":
		perms = chat.PresetFullAccess()
	case "content":
		perms = chat.PresetContent()
	case "stats":
		perms = chat.PresetStatsOnly()
	case "manual":
		return false, h.permToggleView(ctx, target, settings, purpose, subject, 0)
	default:
		return false, newValidationError("invalid permission preset")
	}
	return false, h.permConfirmView(ctx, target, settings, purpose, subject, permMask(perms))
}

func routeModeratorsPermToggle(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	purpose, subject, rest, parseOk := parsePermWizardData(strings.TrimPrefix(data, "moderators:permtoggle:"))
	mask, maskOk := 0, false
	if len(rest) == 1 {
		mask, maskOk = parsePermMask(rest[0])
	}
	if !parseOk || !maskOk {
		return false, newValidationError("invalid permission wizard payload")
	}
	return false, h.permToggleView(ctx, target, settings, purpose, subject, mask)
}

func routeModeratorsPermConfirm(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	purpose, subject, rest, parseOk := parsePermWizardData(strings.TrimPrefix(data, "moderators:permconfirm:"))
	mask, maskOk := 0, false
	if len(rest) == 1 {
		mask, maskOk = parsePermMask(rest[0])
	}
	if !parseOk || !maskOk {
		return false, newValidationError("invalid permission wizard payload")
	}
	return false, h.permConfirmView(ctx, target, settings, purpose, subject, mask)
}

func routeModeratorsPermApply(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	purpose, subject, rest, parseOk := parsePermWizardData(strings.TrimPrefix(data, "moderators:permapply:"))
	mask, maskOk := 0, false
	if len(rest) == 1 {
		mask, maskOk = parsePermMask(rest[0])
	}
	if !parseOk || !maskOk {
		return false, newValidationError("invalid permission wizard payload")
	}
	perms := permsFromMask(mask)
	switch purpose {
	case permPurposeEditModerator:
		return h.applyEditPermissions(ctx, cb, target, settings, subject, perms)
	case permPurposeAssignNew:
		return h.applyAssignNew(ctx, cb, target, settings, subject, perms)
	default:
		return h.createInvitation(ctx, cb, target, settings, perms)
	}
}

func routeModeratorsInviteRevokeDo(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	if h.Invitations == nil {
		return false, newValidationError("invitations are not configured")
	}
	token := strings.TrimPrefix(data, "moderators:invite:revoke:do:")
	if err := h.Invitations.RevokeInvitation(ctx, token); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "invitation_revoked", "")
	if err := h.toast(ctx, cb.ID, h.Texts.Get("moderators.invite_revoked", settings.Locale)); err != nil {
		return false, err
	}
	return true, h.moderatorsView(ctx, target, settings)
}

func routeModeratorsRemoveAsk(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	id, parseErr := strconv.ParseInt(strings.TrimPrefix(data, "moderators:remove:ask:"), 36, 64)
	if parseErr != nil {
		return false, newValidationError("invalid moderator id")
	}
	removed := common.UserID{Value: id}
	name := h.moderatorName(ctx, settings.ChatID, removed)
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("moderators.remove_confirm", settings.Locale), cbModeratorRemoveDo(removed))},
		{h.backButton(settings.Locale, "settings:moderators")},
	}}
	return false, h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("moderators.remove_question", settings.Locale, bold(escapeHTML(name)))), kb)
}

func routeModeratorsRemoveDo(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	id, parseErr := strconv.ParseInt(strings.TrimPrefix(data, "moderators:remove:do:"), 36, 64)
	if parseErr != nil {
		return false, newValidationError("invalid moderator id")
	}
	removed := common.UserID{Value: id}
	name := h.moderatorName(ctx, settings.ChatID, removed)
	if err := h.Chats.RemoveModerator(ctx, settings.ChatID, removed); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "moderator_removed", name)
	if err := h.toast(ctx, cb.ID, h.Texts.Get("moderators.updated", settings.Locale)); err != nil {
		return false, err
	}
	return true, h.moderatorsView(ctx, target, settings)
}

func routeSettingsLocale(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	newLocale := common.LocaleEN
	if settings.Locale == common.LocaleEN {
		newLocale = common.LocaleRU
	}
	settings.Locale = newLocale
	if _, err := h.Chats.Save(ctx, settings); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "locale", newLocale.Tag())
	toastText := "✅ Язык: RU"
	if newLocale == common.LocaleEN {
		toastText = "✅ Language: EN"
	}
	if err := h.toast(ctx, cb.ID, toastText); err != nil {
		return false, err
	}
	return true, h.settingsView(ctx, target, settings, cb.Message.Chat.Type == "private")
}

func routeSettingsTopTier(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	settings.DefaultTopTierOnly = !settings.DefaultTopTierOnly
	if _, err := h.Chats.Save(ctx, settings); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "top_tier", tournamentMode(settings.DefaultTopTierOnly, settings.Locale))
	toastText := topTierToastText(h.Texts, settings)
	if err := h.toast(ctx, cb.ID, toastText); err != nil {
		return false, err
	}
	return true, h.settingsView(ctx, target, settings, cb.Message.Chat.Type == "private")
}

func routeSettingsAutoSubscribe(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	settings.AutoSubscribeTopTier = !settings.AutoSubscribeTopTier
	if _, err := h.Chats.Save(ctx, settings); err != nil {
		return false, err
	}
	state := "off"
	toastKey := "settings.auto_subscribe_off"
	if settings.AutoSubscribeTopTier {
		state, toastKey = "on", "settings.auto_subscribe_on"
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "auto_subscribe", state)
	if err := h.toast(ctx, cb.ID, "✅ "+h.Texts.Get(toastKey, settings.Locale)); err != nil {
		return false, err
	}
	return true, h.settingsView(ctx, target, settings, cb.Message.Chat.Type == "private")
}

// routeSettingsStreamLanguage cycles the broadcast language between the
// bot's own languages — with only two of them, a picker screen would be a
// pointless extra tap, same reasoning as routeSettingsLocale.
func routeSettingsStreamLanguage(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	next := common.LocaleEN
	if settings.StreamLocale() == common.LocaleEN {
		next = common.LocaleRU
	}
	settings.StreamLanguage = next
	if _, err := h.Chats.Save(ctx, settings); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "stream_language", next.Tag())
	toast := h.Texts.Get("settings.stream_language_label", settings.Locale, h.localeName(next, settings.Locale))
	if err := h.toast(ctx, cb.ID, "✅ "+toast); err != nil {
		return false, err
	}
	return true, h.settingsView(ctx, target, settings, cb.Message.Chat.Type == "private")
}

// routeSettingsGamesToggle flips one game on or off for this chat —
// SetEnabledGames replaces the whole set, so this always builds the full
// next set rather than issuing a single-row add/remove.
func routeSettingsGamesToggle(h *UpdateHandler, ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, data string) (bool, error) {
	code := competition.GameCode(strings.TrimPrefix(data, "settings:games:toggle:"))
	var supported bool
	for _, g := range competition.Games {
		if g == code {
			supported = true
			break
		}
	}
	if !supported {
		return false, newValidationError("unknown game code %q", code)
	}

	var next []competition.GameCode
	if settings.GameEnabled(code) {
		for _, g := range settings.EnabledGames {
			if g != code {
				next = append(next, g)
			}
		}
	} else {
		next = append(append([]competition.GameCode{}, settings.EnabledGames...), code)
	}
	if err := h.Chats.SetEnabledGames(ctx, settings.ChatID, next); err != nil {
		return false, err
	}
	settings.EnabledGames = next

	state := "off"
	if settings.GameEnabled(code) {
		state = "on"
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "games", string(code)+":"+state)
	return true, h.gamesView(ctx, target, settings)
}
