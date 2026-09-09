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
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// Callback routing: which button was tapped, whose it was, and which
// chat's settings the screen behind it should be rendered against. The
// screens themselves live in screens_*.go and subscription_flow.go —
// everything here is dispatch, so a new screen is a new case plus a new
// function in the file that owns its area.

func (h *UpdateHandler) handleCallback(ctx context.Context, cb *CallbackQuery) error {
	if cb.Message == nil {
		return h.answer(ctx, cb.ID)
	}
	if cb.Message.Chat.Type == "private" {
		return h.handlePrivateCallback(ctx, cb)
	}
	settings, err := h.Chats.Find(ctx, common.ChatID{Value: cb.Message.Chat.ID})
	if err != nil {
		return err
	}
	if settings == nil {
		return h.answer(ctx, cb.ID)
	}

	data := ""
	if cb.Data != nil {
		data = *cb.Data
	}

	// A group menu belongs to whoever opened it: anyone else tapping it gets
	// a private alert instead, and the menu they were looking at is left
	// exactly as its owner left it.
	scope, data := splitCallbackScope(data)
	actor := common.UserID{Value: cb.From.ID}
	if scope.userID != nil && *scope.userID != actor {
		return h.alert(ctx, cb.ID, h.Texts.Get("menu.not_yours", settings.Locale))
	}
	ctx = withCallbackScope(ctx, scope)

	answered, cbErr := h.routeCallback(ctx, cb, *settings, data)
	finalErr := h.handleCommandError(ctx, *settings, nil, data, cbErr)
	// A branch that already answered via a toast (h.toast) must not be
	// answered again — Telegram accepts exactly one answerCallbackQuery per
	// callback query and rejects a second attempt.
	if !answered {
		if answerErr := h.answer(ctx, cb.ID); answerErr != nil && finalErr == nil {
			loggerFrom(ctx, h.Log).Warn("answerCallbackQuery failed", "error", answerErr)
		}
	}
	return finalErr
}

// routeCallback dispatches on the button's callback data and reports
// whether it already answered the callback query itself (via a toast),
// so handleCallback doesn't try to answer it a second time.
//
// Navigation between screens the bot itself renders (menus, submenus) edits
// the tapped message in place via editTargetFromCallback — one evolving
// message instead of a new one per click. Leaf "answer" screens reachable
// from BOTH a menu AND a standalone notification message (the
// stats:all/year/month/event/mine family, populated by
// MatchResultPublisher's buttons too) deliberately still send a new
// message: editing them in place would silently overwrite a match-result
// notification with a leaderboard, destroying that historical message.
// statsResultTarget preserves the public group experience while making a
// managed group's DM panel behave like a single mini-app. A stats request
// tapped in a group posts a new public leaderboard; the same request tapped
// in DM edits only that private panel and can never leak the result back to
// the managed group.
func statsResultTarget(cb *CallbackQuery, settings chat.Settings) replyTarget {
	if cb != nil && cb.Message != nil && cb.Message.Chat.Type == "private" {
		return editTargetFromCallback(cb, settings.ChatID)
	}
	if cb != nil && cb.Message != nil {
		return sendTarget(settings.ChatID, cb.Message.MessageThreadID)
	}
	return sendTarget(settings.ChatID, nil)
}

func (h *UpdateHandler) routeCallback(ctx context.Context, cb *CallbackQuery, settings chat.Settings, data string) (answered bool, err error) {
	target := editTargetFromCallback(cb, settings.ChatID)
	switch {
	case data == "noop":
		return false, nil
	case data == "menu:main":
		if cb.Message.Chat.Type == "private" {
			if accessErr := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); accessErr != nil {
				if errors.Is(accessErr, chat.ErrAccessDenied) {
					return false, h.readOnlyGroupMenu(ctx, target, settings)
				}
				return false, accessErr
			}
		}
		return false, h.menu(ctx, target, settings, cb.Message.Chat.Type == "private")
	case data == "menu:stats":
		return false, h.statsMenu(ctx, target, settings)
	case data == "menu:events":
		return false, h.eventMenu(ctx, target, settings)
	case data == "events:add":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.eventAddMenu(ctx, target, settings)
	case data == "events:search":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.requestEventSearch(ctx, cb, settings)
	case strings.HasPrefix(data, "events:browse:"):
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		mode, page, err := parseEventBrowseCallback(data)
		if err != nil {
			return false, err
		}
		return false, h.renderEventBrowse(ctx, target, settings, mode == "top", page)
	case data == "events:cleanup":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.cleanupMenu(ctx, target, settings)
	case data == "events:cleanup:go":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return h.cleanupFinished(ctx, cb, target, settings)
	case data == "events:mine":
		return false, h.subscribedEvents(ctx, target, settings, cb.Message.Chat.Type == "private")
	case strings.HasPrefix(data, "events:view:"):
		id, err := uuid.Parse(strings.TrimPrefix(data, "events:view:"))
		if err != nil {
			return false, newValidationError("invalid event id")
		}
		return false, h.eventDetails(ctx, target, settings, common.EventID{Value: id}, cb.Message.Chat.Type == "private")
	case data == "menu:settings":
		return false, h.settingsView(ctx, target, settings, cb.Message.Chat.Type == "private")
	case data == "menu:upcoming":
		return false, h.upcoming(ctx, target, settings)
	case data == "menu:rules":
		return false, h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("rules.text", settings.Locale)), &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "menu:main")}}})
	case strings.HasPrefix(data, "stats:p:"):
		parts := strings.Split(data, ":")
		if len(parts) < 4 {
			return false, newValidationError("invalid leaderboard page callback")
		}
		viewer := common.UserID{Value: cb.From.ID}
		switch parts[2] {
		case "a":
			if len(parts) != 4 {
				return false, newValidationError("invalid all-time leaderboard page callback")
			}
			page, parseErr := strconv.Atoi(parts[3])
			if parseErr != nil || page < 0 {
				return false, newValidationError("invalid leaderboard page")
			}
			return false, h.renderLeaderboard(ctx, statsResultTarget(cb, settings), settings, scoring.AllTime(), "menu:stats", viewer, page)
		case "y":
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
			return false, h.renderLeaderboard(ctx, statsResultTarget(cb, settings), settings, scoring.ForYear(year), back, viewer, page)
		case "m":
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
			return false, h.renderLeaderboard(ctx, statsResultTarget(cb, settings), settings, scoring.ForMonth(year, time.Month(monthInt)), back, viewer, page)
		case "e":
			if len(parts) != 5 {
				return false, newValidationError("invalid event leaderboard page callback")
			}
			id, idErr := uuid.Parse(parts[3])
			page, pageErr := strconv.Atoi(parts[4])
			if idErr != nil || pageErr != nil || page < 0 {
				return false, newValidationError("invalid event leaderboard page")
			}
			return false, h.renderLeaderboard(ctx, statsResultTarget(cb, settings), settings, scoring.ForEvent(common.EventID{Value: id}), "stats:events", viewer, page)
		default:
			return false, newValidationError("unknown leaderboard period")
		}
	case data == "stats:all":
		return false, h.renderLeaderboard(ctx, statsResultTarget(cb, settings), settings, scoring.AllTime(), "menu:stats", common.UserID{Value: cb.From.ID})
	case data == "stats:years":
		return false, h.yearMenu(ctx, target, settings)
	case strings.HasPrefix(data, "stats:months:"):
		year, _ := strconv.Atoi(strings.TrimPrefix(data, "stats:months:"))
		return false, h.monthMenu(ctx, target, settings, year)
	case strings.HasPrefix(data, "stats:year:"):
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
		return false, h.renderLeaderboard(ctx, statsResultTarget(cb, settings), settings, scoring.ForYear(year), back, common.UserID{Value: cb.From.ID})
	case strings.HasPrefix(data, "stats:month:"):
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
		return false, h.renderLeaderboard(ctx, statsResultTarget(cb, settings), settings, scoring.ForMonth(year, month), back, common.UserID{Value: cb.From.ID})
	case strings.HasPrefix(data, "stats:event:"):
		id, err := uuid.Parse(strings.TrimPrefix(data, "stats:event:"))
		if err != nil {
			return false, newValidationError("invalid event id")
		}
		return false, h.renderLeaderboard(ctx, statsResultTarget(cb, settings), settings, scoring.ForEvent(common.EventID{Value: id}), "stats:events", common.UserID{Value: cb.From.ID})
	case strings.HasPrefix(data, "stats:mine:"):
		id, err := uuid.Parse(strings.TrimPrefix(data, "stats:mine:"))
		if err != nil {
			return false, newValidationError("invalid event id")
		}
		return false, h.personalStats(ctx, statsResultTarget(cb, settings), settings, cb.From, common.EventID{Value: id})
	case data == "stats:events":
		return false, h.eventStatsMenu(ctx, target, settings)
	case data == "settings:moderators":
		return false, h.moderatorsView(ctx, target, settings)
	case data == "bulk:menu":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.bulkMenu(ctx, cb, target, settings)
	case strings.HasPrefix(data, "bulk:ask:"):
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.bulkConfirm(ctx, cb, target, settings, strings.TrimPrefix(data, "bulk:ask:"))
	case strings.HasPrefix(data, "bulk:do:"):
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.bulkApply(ctx, cb, target, settings, strings.TrimPrefix(data, "bulk:do:"))
	case data == "settings:timezone":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.timezoneView(ctx, target, settings)
	case strings.HasPrefix(data, "settings:tz:"):
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return h.setTimezone(ctx, cb, target, settings, strings.TrimPrefix(data, "settings:tz:"))
	case data == "settings:history":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.historyView(ctx, target, settings)
	case data == "moderators:add":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		return false, h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("moderators.add_help", settings.Locale)), &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "settings:moderators")}}})
	case strings.HasPrefix(data, "moderators:remove:ask:"):
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
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
	case strings.HasPrefix(data, "moderators:remove:do:"):
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
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
	case data == "settings:locale":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
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
	case data == "settings:top_tier":
		if err := h.requireManager(ctx, settings.ChatID, common.UserID{Value: cb.From.ID}); err != nil {
			return false, err
		}
		settings.DefaultTopTierOnly = !settings.DefaultTopTierOnly
		if _, err := h.Chats.Save(ctx, settings); err != nil {
			return false, err
		}
		h.logAdminAction(ctx, settings.ChatID, &cb.From, "top_tier", tournamentMode(settings.DefaultTopTierOnly, settings.Locale))
		toastText := topTierToastText(settings)
		if err := h.toast(ctx, cb.ID, toastText); err != nil {
			return false, err
		}
		return true, h.settingsView(ctx, target, settings, cb.Message.Chat.Type == "private")
	case strings.HasPrefix(data, "subscribe:"):
		return h.subscribe(ctx, cb, target, settings, strings.TrimPrefix(data, "subscribe:"))
	case strings.HasPrefix(data, "unsubscribe:"):
		return h.unsubscribe(ctx, cb, target, settings, strings.TrimPrefix(data, "unsubscribe:"))
	case strings.HasPrefix(data, "unsubok:"):
		// Deliberately ignores the settings routed here — see
		// confirmUnsubscribe's doc comment.
		return h.confirmUnsubscribe(ctx, cb, strings.TrimPrefix(data, "unsubok:"))
	case strings.HasPrefix(data, "unsubreject:"):
		return h.rejectUnsubscribe(ctx, cb, strings.TrimPrefix(data, "unsubreject:"))
	case strings.HasPrefix(data, "event-topic:"):
		return h.setEventTopic(ctx, cb, settings, strings.TrimPrefix(data, "event-topic:"))
	default:
		return true, h.toast(ctx, cb.ID, h.Texts.Get("callback.expired", settings.Locale))
	}
}

func (h *UpdateHandler) personalScoring() (scoring.PersonalRepository, error) {
	personal, ok := h.Scoring.(scoring.PersonalRepository)
	if !ok {
		return nil, fmt.Errorf("scoring repository does not support personal statistics")
	}
	return personal, nil
}

func (h *UpdateHandler) handlePrivateCallback(ctx context.Context, cb *CallbackQuery) error {
	data := ""
	if cb.Data != nil {
		data = *cb.Data
	}
	userID := common.UserID{Value: cb.From.ID}
	locale := h.userLocale(ctx, userID)
	target := editTargetFromCallback(cb, common.ChatID{Value: cb.Message.Chat.ID})

	// A chat-scoped button names the chat it acts on, so panels for several
	// different chats can sit in this DM at once and each keeps working —
	// including after the user has opened another one. Keyboards rendered
	// from here inherit the same scope (see scopeKeyboard), so the whole
	// panel stays bound to its chat as the user navigates it.
	scope, data := splitCallbackScope(data)
	ctx = withCallbackScope(ctx, scope)
	h.markDMReachable(ctx, userID)

	var err error
	var answered bool
	switch {
	case data == "noop":
		err = nil
	case data == "pstats:menu":
		err = h.privateStatsMenu(ctx, target, userID, locale)
	case data == "pstats:all":
		err = h.renderPrivateStats(ctx, target, userID, locale, scoring.AllTime())
	case data == "pstats:years":
		err = h.privateYearMenu(ctx, target, userID, locale)
	case strings.HasPrefix(data, "pstats:months:"):
		year, parseErr := strconv.Atoi(strings.TrimPrefix(data, "pstats:months:"))
		if parseErr != nil {
			err = newValidationError("invalid personal stats year")
		} else {
			err = h.privateMonthMenu(ctx, target, userID, locale, year)
		}
	case strings.HasPrefix(data, "pstats:year:"):
		year, parseErr := strconv.Atoi(strings.TrimPrefix(data, "pstats:year:"))
		if parseErr != nil {
			err = newValidationError("invalid personal stats year")
		} else {
			err = h.renderPrivateStats(ctx, target, userID, locale, scoring.ForYear(year))
		}
	case strings.HasPrefix(data, "pstats:month:"):
		ym := strings.TrimPrefix(data, "pstats:month:")
		year, month, parseErr := parseYearMonth(ym)
		if parseErr != nil {
			err = newValidationError("invalid personal stats month")
		} else {
			err = h.renderPrivateStats(ctx, target, userID, locale, scoring.ForMonth(year, month))
		}
	case strings.HasPrefix(data, "pstats:chats:"):
		page, parseErr := strconv.Atoi(strings.TrimPrefix(data, "pstats:chats:"))
		if parseErr != nil || page < 0 {
			err = newValidationError("invalid personal chats page")
		} else {
			err = h.privateChatsMenu(ctx, target, userID, locale, page)
		}
	case strings.HasPrefix(data, "pstats:chat:"):
		parts := strings.Split(data, ":")
		if len(parts) != 4 {
			err = newValidationError("invalid personal chat callback")
			break
		}
		chatID, parseErr := strconv.ParseInt(parts[2], 10, 64)
		if parseErr != nil {
			err = newValidationError("invalid personal chat id")
			break
		}
		page, parseErr := strconv.Atoi(parts[3])
		if parseErr != nil || page < 0 {
			err = newValidationError("invalid personal chats page")
			break
		}
		err = h.renderPrivateChatStats(ctx, target, userID, common.ChatID{Value: chatID}, locale, page)
	case data == "pstats:locale":
		next := common.LocaleEN
		if locale == common.LocaleEN {
			next = common.LocaleRU
		}
		if setErr := h.Chats.SetUserLocale(ctx, userID, next); setErr != nil {
			err = setErr
			break
		}
		err = h.privateStatsMenu(ctx, target, userID, next)
	case data == "notify:menu":
		err = h.notificationsMenu(ctx, target, userID, locale)
	case strings.HasPrefix(data, "notify:toggle:"):
		err = h.toggleNotification(ctx, target, userID, locale, strings.TrimPrefix(data, "notify:toggle:"))
	case data == "pstats:insights":
		err = h.renderPersonalInsights(ctx, target, userID, locale)
	case data == "pstats:rename":
		err = h.renameMenu(ctx, target, userID, locale)
	case data == cbRenameAsk():
		err = h.requestNickname(ctx, common.ChatID{Value: cb.Message.Chat.ID}, locale)
	case data == cbRenameReset():
		if resetErr := h.Chats.SetNickname(ctx, userID, ""); resetErr != nil {
			err = resetErr
			break
		}
		if toastErr := h.toast(ctx, cb.ID, "✅ "+h.Texts.Get("dm.rename_was_reset", locale)); toastErr != nil {
			err = toastErr
			break
		}
		answered = true
		err = h.renameMenu(ctx, target, userID, locale)
	case data == "manage:chats":
		err = h.managedChatsMenu(ctx, target, userID, locale)
	case strings.HasPrefix(data, "manage:open:"):
		id, parseErr := strconv.ParseInt(strings.TrimPrefix(data, "manage:open:"), 36, 64)
		if parseErr != nil {
			err = newValidationError("invalid managed chat id")
		} else {
			err = h.openManagedChat(ctx, target, userID, locale, common.ChatID{Value: id})
		}
	case strings.HasPrefix(data, "unsubok:"):
		// Handled directly rather than via tryDelegateToManagedChat: the
		// tapping user might be a different manager entirely, fanned out a
		// confirmation request for a chat their OWN DM session (if any)
		// isn't even pointed at — see confirmUnsubscribe's doc comment.
		answered, err = h.confirmUnsubscribe(ctx, cb, strings.TrimPrefix(data, "unsubok:"))
	case strings.HasPrefix(data, "unsubreject:"):
		answered, err = h.rejectUnsubscribe(ctx, cb, strings.TrimPrefix(data, "unsubreject:"))
	default:
		delegated, delegatedAnswered, delegateErr := h.tryDelegateToManagedChat(ctx, cb, userID, data)
		if delegated {
			answered, err = delegatedAnswered, delegateErr
		} else {
			err = h.privateStatsMenu(ctx, target, userID, locale)
		}
	}

	if answered {
		return err
	}
	answerErr := h.answer(ctx, cb.ID)
	if err != nil {
		return err
	}
	return answerErr
}

// tryDelegateToManagedChat handles a button tap using the group-chat admin
// panel's own callback vocabulary (menu:events, settings:locale,
// subscribe:<id>, ...) reached from within a DM: it looks up which managed
// chat this user's DM session (chat.Repository.DMSession) currently points
// at and, if one is set, re-dispatches through the exact same
// routeCallback used for the group rendering path. Every
// mutating leaf inside routeCallback already re-verifies RequireManager
// against settings.ChatID (here, the managed chat) using cb.From.ID (the
// tapping user, correct regardless of which chat cb.Message physically
// lives in), so no extra authorization check is needed here — establishing
// the session (openManagedChat) is where that live check happens.
// delegated is false when there's no active session, so the caller falls
// back to its own default handling instead.
func (h *UpdateHandler) tryDelegateToManagedChat(ctx context.Context, cb *CallbackQuery, userID common.UserID, data string) (delegated, answered bool, err error) {
	settings, ok, err := h.targetManagedChat(ctx, userID)
	if err != nil || !ok {
		return false, false, err
	}
	answered, err = h.routeCallback(ctx, cb, settings, data)
	return true, answered, err
}

// targetManagedChat resolves which chat a DM action applies to, preferring
// the chat stamped into the button itself over the user's stored session:
// the stamp is what the panel on screen was rendered for, whereas the
// session is only ever "the last panel opened". Preferring the stamp is
// what makes several chats' panels usable side by side, and it's why an
// older panel scrolled back to still acts on its own chat.
//
// Using the stamp also re-points the session at that chat, so DM text
// commands (/events, /timezone) follow whichever panel was touched last.
func (h *UpdateHandler) targetManagedChat(ctx context.Context, userID common.UserID) (chat.Settings, bool, error) {
	scope := callbackScopeFrom(ctx)
	if scope.chatID == nil {
		return h.currentManagedChat(ctx, userID)
	}
	settings, err := h.Chats.Find(ctx, *scope.chatID)
	if err != nil {
		return chat.Settings{}, false, err
	}
	if settings == nil {
		return h.currentManagedChat(ctx, userID)
	}
	if err := h.Chats.SetDMSession(ctx, userID, *scope.chatID); err != nil {
		loggerFrom(ctx, h.Log).Warn("dm session refresh failed", "userId", userID.Value, "chatId", scope.chatID.Value, "error", err)
	}
	return *settings, true, nil
}

// currentManagedChat resolves the chat a user's DM session
// (chat.Repository.DMSession) currently points at, if any and still
// present. ok=false covers every "nothing to delegate to" case uniformly
// — no session set, or the session's chat has since vanished — so
// callers don't need to distinguish them.
func (h *UpdateHandler) currentManagedChat(ctx context.Context, userID common.UserID) (chat.Settings, bool, error) {
	chatID, err := h.Chats.DMSession(ctx, userID)
	if err != nil {
		return chat.Settings{}, false, err
	}
	if chatID == nil {
		return chat.Settings{}, false, nil
	}
	settings, err := h.Chats.Find(ctx, *chatID)
	if err != nil {
		return chat.Settings{}, false, err
	}
	if settings == nil {
		return chat.Settings{}, false, nil
	}
	return *settings, true, nil
}

func (h *UpdateHandler) answer(ctx context.Context, callbackID string) error {
	_, err := h.Client.Call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID})
	return err
}
