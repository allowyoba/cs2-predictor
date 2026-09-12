package telegram

import (
	"context"
	"fmt"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// The bot's private notifications are strictly opt-in, and this screen is
// the only place they can be turned on. Nothing enables itself as a side
// effect of using the bot, and every switch is reversible in one tap from
// the same screen — a person who regrets turning something on must not
// have to go looking for where.

// notificationPrefs resolves the preference store. Like the other personal
// ports it is an assertion, since the same postgres repository implements
// every chat port; a deployment wired without it gets an error screen
// rather than a panic.
func (h *UpdateHandler) notificationPrefs() (chat.NotificationPrefsRepository, error) {
	repo, ok := h.Chats.(chat.NotificationPrefsRepository)
	if !ok {
		return nil, fmt.Errorf("chat repository does not support notification preferences")
	}
	return repo, nil
}

// notificationKinds is the rendered order, paired with its label key. A
// slice rather than a map so the two switches never swap places between
// views.
var notificationKinds = []struct {
	kind common.NotificationKind
	key  string
}{
	{common.NotifyResultRecaps, "notify.recaps"},
	{common.NotifyPollReminders, "notify.reminders"},
}

func (h *UpdateHandler) notificationsMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	repo, err := h.notificationPrefs()
	if err != nil {
		return err
	}
	prefs, err := repo.NotificationPrefs(ctx, userID)
	if err != nil {
		return err
	}

	rows := make([][]InlineButton, 0, len(notificationKinds)+1)
	for _, entry := range notificationKinds {
		stateKey := "notify.disabled"
		if prefEnabled(prefs, entry.kind) {
			stateKey = "notify.enabled"
		}
		label := h.Texts.Get(entry.key, locale) + ": " + h.Texts.Get(stateKey, locale)
		rows = append(rows, []InlineButton{button(label, "notify:toggle:"+string(entry.kind))})
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "pstats:settings")})

	text := bold(h.Texts.Get("notify.title", locale)) + "\n\n" + h.Texts.Get("notify.explainer", locale)
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) toggleNotification(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, raw string) error {
	kind := common.NotificationKind(raw)
	if !knownNotificationKind(kind) {
		return newValidationError("unknown notification kind %q", raw)
	}
	repo, err := h.notificationPrefs()
	if err != nil {
		return err
	}
	prefs, err := repo.NotificationPrefs(ctx, userID)
	if err != nil {
		return err
	}
	if err := repo.SetNotificationPref(ctx, userID, kind, !prefEnabled(prefs, kind)); err != nil {
		return err
	}
	return h.notificationsMenu(ctx, target, userID, locale)
}

func prefEnabled(prefs chat.NotificationPrefs, kind common.NotificationKind) bool {
	switch kind {
	case common.NotifyResultRecaps:
		return prefs.ResultRecaps
	case common.NotifyPollReminders:
		return prefs.PollReminders
	default:
		return false
	}
}

func knownNotificationKind(kind common.NotificationKind) bool {
	for _, entry := range notificationKinds {
		if entry.kind == kind {
			return true
		}
	}
	return false
}
