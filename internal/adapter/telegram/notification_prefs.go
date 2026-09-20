package telegram

import (
	"context"
	"fmt"

	"cs2predictor/internal/platform/common"
)

// Every notification screen in the bot is this file. Three audiences —
// a chat's managers, a person in their own DM, an operator of the bot
// itself — but one rule: nothing is on until somebody turns it on here,
// and everything turned on here is reversible in one tap from the same
// screen. A person who regrets a switch must not have to go looking for
// where they flipped it.

// switchboard resolves the preference store. Like the other personal ports
// it is an assertion, since the same postgres repository implements every
// chat port; a deployment wired without it gets an error screen rather
// than a panic.
func switchboardOf(chats any) (common.NotifySwitchboard, error) {
	repo, ok := chats.(common.NotifySwitchboard)
	if !ok {
		return nil, fmt.Errorf("chat repository does not support notification preferences")
	}
	return repo, nil
}

func (h *UpdateHandler) switchboard() (common.NotifySwitchboard, error) {
	return switchboardOf(h.Chats)
}

// switchRow renders one switch as a button that toggles it.
func (h *UpdateHandler) switchRow(locale common.LocaleCode, labelKey, callback string, on bool) []InlineButton {
	stateKey := "notify.disabled"
	if on {
		stateKey = "notify.enabled"
	}
	return []InlineButton{button(h.Texts.Get(labelKey, locale)+": "+h.Texts.Get(stateKey, locale), callback)}
}

// --- a person's own DM ------------------------------------------------

// personalLabelKey names one personal notification for the screen.
func personalLabelKey(kind common.NotificationKind) string {
	switch kind {
	case common.NotifyResultRecaps:
		return "notify.recaps"
	case common.NotifyEventRecaps:
		return "notify.event_recaps"
	default:
		return "notify.reminders"
	}
}

func (h *UpdateHandler) notificationsMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	repo, err := h.switchboard()
	if err != nil {
		return err
	}
	prefs, err := repo.NotifySettings(ctx, common.ScopeUser, userID.Value)
	if err != nil {
		return err
	}

	rows := make([][]InlineButton, 0, len(common.PersonalNotificationKinds)+1)
	for _, kind := range common.PersonalNotificationKinds {
		rows = append(rows, h.switchRow(locale, personalLabelKey(kind), "notify:toggle:"+string(kind), prefs[string(kind)]))
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "pstats:settings")})

	text := bold(h.Texts.Get("notify.title", locale)) + "\n\n" + h.Texts.Get("notify.explainer", locale)
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) toggleNotification(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, raw string) error {
	kind := common.NotificationKind(raw)
	if !common.KnownNotificationKind(kind) {
		return newValidationError("unknown notification kind %q", raw)
	}
	repo, err := h.switchboard()
	if err != nil {
		return err
	}
	on, err := repo.NotifyEnabled(ctx, common.ScopeUser, userID.Value, raw)
	if err != nil {
		return err
	}
	if err := repo.SetNotifyEnabled(ctx, common.ScopeUser, userID.Value, raw, !on); err != nil {
		return err
	}
	return h.notificationsMenu(ctx, target, userID, locale)
}

// --- what the bot posts into a chat -----------------------------------

func chatNotifyLabelKey(kind common.ChatNotificationKind) string {
	switch kind {
	case common.ChatNotifyNewEvents:
		return "notify.chat.new_events"
	case common.ChatNotifyEventEve:
		return "notify.chat.event_eve"
	case common.ChatNotifyEventFinished:
		return "notify.chat.event_finished"
	case common.ChatNotifyDigests:
		return "notify.chat.digests"
	default:
		return "notify.chat.streams"
	}
}

func (h *UpdateHandler) chatNotificationsMenu(ctx context.Context, target replyTarget, chatID common.ChatID, locale common.LocaleCode) error {
	repo, err := h.switchboard()
	if err != nil {
		return err
	}
	prefs, err := repo.NotifySettings(ctx, common.ScopeChat, chatID.Value)
	if err != nil {
		return err
	}

	rows := make([][]InlineButton, 0, len(common.ChatNotificationKinds)+1)
	for _, kind := range common.ChatNotificationKinds {
		rows = append(rows, h.switchRow(locale, chatNotifyLabelKey(kind), "settings:notify:"+string(kind), prefs[string(kind)]))
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "menu:settings")})

	text := bold(h.Texts.Get("notify.chat.title", locale)) + "\n\n" + h.Texts.Get("notify.chat.explainer", locale)
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) toggleChatNotification(ctx context.Context, target replyTarget, chatID common.ChatID, locale common.LocaleCode, raw string) error {
	kind := common.ChatNotificationKind(raw)
	if !common.KnownChatNotification(kind) {
		return newValidationError("unknown chat notification kind %q", raw)
	}
	repo, err := h.switchboard()
	if err != nil {
		return err
	}
	on, err := repo.NotifyEnabled(ctx, common.ScopeChat, chatID.Value, raw)
	if err != nil {
		return err
	}
	if err := repo.SetNotifyEnabled(ctx, common.ScopeChat, chatID.Value, raw, !on); err != nil {
		return err
	}
	return h.chatNotificationsMenu(ctx, target, chatID, locale)
}

// --- the bot's own operators ------------------------------------------

func alertLabelKey(kind common.AdminAlertKind) string {
	switch kind {
	case common.AdminAlertRelease:
		return "notify.alert.release"
	case common.AdminAlertProviderDown:
		return "notify.alert.providers"
	case common.AdminAlertWebhookBroken:
		return "notify.alert.webhook"
	case common.AdminAlertDeadLetters:
		return "notify.alert.dead_letters"
	default:
		return "notify.alert.host"
	}
}

func (h *UpdateHandler) alertsMenu(ctx context.Context, target replyTarget, chatID common.ChatID, locale common.LocaleCode) error {
	repo, err := h.switchboard()
	if err != nil {
		return err
	}
	prefs, err := repo.NotifySettings(ctx, common.ScopeOperator, chatID.Value)
	if err != nil {
		return err
	}

	rows := make([][]InlineButton, 0, len(common.AdminAlertKinds)+1)
	for _, kind := range common.AdminAlertKinds {
		rows = append(rows, h.switchRow(locale, alertLabelKey(kind), "alerts:toggle:"+string(kind), prefs[string(kind)]))
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "hub:system")})

	text := bold(h.Texts.Get("notify.alert.title", locale)) + "\n\n" + h.Texts.Get("notify.alert.explainer", locale)
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) toggleAlert(ctx context.Context, target replyTarget, chatID common.ChatID, locale common.LocaleCode, raw string) error {
	kind := common.AdminAlertKind(raw)
	if !common.KnownAdminAlert(kind) {
		return newValidationError("unknown alert kind %q", raw)
	}
	repo, err := h.switchboard()
	if err != nil {
		return err
	}
	on, err := repo.NotifyEnabled(ctx, common.ScopeOperator, chatID.Value, raw)
	if err != nil {
		return err
	}
	if err := repo.SetNotifyEnabled(ctx, common.ScopeOperator, chatID.Value, raw, !on); err != nil {
		return err
	}
	return h.alertsMenu(ctx, target, chatID, locale)
}
