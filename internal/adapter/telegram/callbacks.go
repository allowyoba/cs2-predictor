package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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
// Navigation between screens the bot itself renders (menus, submenus,
// period pickers, the leaderboard itself) edits the tapped message in place
// via editTargetFromCallback — one evolving panel instead of a new message
// per click, the same in a group as in a DM. The one deliberate exception
// is the stats:notif: family: those buttons live on a standalone
// match-result notification, not on a screen this handler owns, and
// editing one in place would silently overwrite that historical result
// with a leaderboard. They always send a new message instead.
//
// The actual match/guard/handle table lives in callback_routes.go.
func (h *UpdateHandler) routeCallback(ctx context.Context, cb *CallbackQuery, settings chat.Settings, data string) (answered bool, err error) {
	target := editTargetFromCallback(cb, settings.ChatID)
	if data == "noop" {
		return false, nil
	}
	actor := common.UserID{Value: cb.From.ID}
	for _, route := range callbackRoutes {
		if !route.match(data) {
			continue
		}
		if route.guard != nil {
			if guardErr := route.guard(h, ctx, settings.ChatID, actor); guardErr != nil {
				return false, guardErr
			}
		}
		return route.handle(h, ctx, cb, target, settings, data)
	}
	return true, h.toast(ctx, cb.ID, h.Texts.Get("callback.expired", settings.Locale))
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
	case data == "pstats:help":
		err = h.helpView(ctx, target, locale, "pstats:menu")
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
	case strings.HasPrefix(data, "invite:accept:"):
		err = h.acceptInvitation(ctx, cb, target, userID, locale, strings.TrimPrefix(data, "invite:accept:"))
	case strings.HasPrefix(data, "invite:decline:"):
		err = h.respond(ctx, target, h.Texts.Get("invite.declined", locale), nil)
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
