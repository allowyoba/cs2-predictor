package telegram

import (
	"context"
	"reflect"
	"strings"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// Copying one chat's configuration onto every other chat the same person
// manages. Someone running a handful of communities sets them up the same
// way; without this they walk the same three settings through every panel
// by hand, and the panels drift apart the moment they miss one.
//
// The three settings here are exactly the ones on the settings screen that
// are safe to copy: preferences with no per-chat identity. Subscriptions,
// moderators and topics deliberately stay out — those name things that only
// exist in one chat.

// bulkSetting names a copyable setting. Kind doubles as the callback
// discriminator, the i18n key suffix, and the admin-history kind, so adding
// one is a single entry plus two bundle lines.
type bulkSetting struct {
	kind  string
	apply func(source chat.Settings, target *chat.Settings)
	// value renders what is about to be copied, for the confirmation
	// screen and the history entry. Rendered in the SOURCE chat's locale:
	// it describes the source's setting, not the target's.
	value func(source chat.Settings) string
}

func bulkSettings() []bulkSetting {
	return []bulkSetting{
		{
			kind:  "locale",
			apply: func(s chat.Settings, t *chat.Settings) { t.Locale = s.Locale },
			value: func(s chat.Settings) string { return s.Locale.Tag() },
		},
		{
			kind:  "timezone",
			apply: func(s chat.Settings, t *chat.Settings) { t.Timezone = s.Timezone },
			value: func(s chat.Settings) string { return s.Timezone },
		},
		{
			kind:  "top_tier",
			apply: func(s chat.Settings, t *chat.Settings) { t.DefaultTopTierOnly = s.DefaultTopTierOnly },
			value: func(s chat.Settings) string { return tournamentMode(s.DefaultTopTierOnly, s.Locale) },
		},
	}
}

// bulkSettingKey reuses the change-history labels: they are already the
// plain nouns for these three settings in both bundles, and keeping one
// label per setting means the confirmation screen and the history entry it
// produces can never disagree.
func bulkSettingKey(kind string) string { return historyKindKey(kind) }

func findBulkSetting(kind string) (bulkSetting, bool) {
	for _, s := range bulkSettings() {
		if s.kind == kind {
			return s, true
		}
	}
	return bulkSetting{}, false
}

// bulkTargets lists the chats a copy would land in: every chat the user
// manages except the one being copied FROM. Rights are re-verified per
// chat at apply time, not here — this is only what to offer.
func (h *UpdateHandler) bulkTargets(ctx context.Context, userID common.UserID, source common.ChatID) ([]chat.Settings, error) {
	managed, err := h.Chats.ManagedChats(ctx, userID)
	if err != nil {
		return nil, err
	}
	targets := make([]chat.Settings, 0, len(managed))
	for _, c := range managed {
		if c.ChatID != source {
			targets = append(targets, c)
		}
	}
	return targets, nil
}

// bulkMenu offers one button per copyable setting, each labelled with the
// value that would be copied — the user should not have to go back and
// look up what they are about to spread.
func (h *UpdateHandler) bulkMenu(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings) error {
	userID := common.UserID{Value: cb.From.ID}
	targets, err := h.bulkTargets(ctx, userID, settings.ChatID)
	if err != nil {
		return err
	}
	back := []InlineButton{h.backButton(settings.Locale, "menu:settings")}
	if len(targets) == 0 {
		return h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("bulk.no_targets", settings.Locale)), &InlineKeyboard{InlineKeyboard: [][]InlineButton{back}})
	}

	rows := make([][]InlineButton, 0, len(bulkSettings())+1)
	for _, setting := range bulkSettings() {
		label := h.Texts.Get(bulkSettingKey(setting.kind), settings.Locale) + ": " + setting.value(settings)
		rows = append(rows, []InlineButton{button(label, "bulk:ask:"+setting.kind)})
	}
	rows = append(rows, back)
	text := h.Texts.Get("bulk.title", settings.Locale, bold(escapeHTML(settings.Title)), len(targets))
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// bulkConfirm names every chat that is about to change before anything
// does. A bulk write is the one place in this bot where a misread button
// costs more than one undo.
func (h *UpdateHandler) bulkConfirm(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, kind string) error {
	setting, ok := findBulkSetting(kind)
	if !ok {
		return newValidationError("unknown bulk setting %q", kind)
	}
	targets, err := h.bulkTargets(ctx, common.UserID{Value: cb.From.ID}, settings.ChatID)
	if err != nil {
		return err
	}
	back := []InlineButton{h.backButton(settings.Locale, "bulk:menu")}
	if len(targets) == 0 {
		return h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("bulk.no_targets", settings.Locale)), &InlineKeyboard{InlineKeyboard: [][]InlineButton{back}})
	}

	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, "• "+escapeHTML(truncate(t.Title, 40)))
	}
	text := h.Texts.Get("bulk.confirm", settings.Locale,
		bold(h.Texts.Get(bulkSettingKey(kind), settings.Locale)),
		bold(escapeHTML(setting.value(settings))),
		len(targets)) + "\n\n" + strings.Join(names, "\n")
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("bulk.apply", settings.Locale), "bulk:do:"+kind)},
		back,
	}}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &kb)
}

// bulkApply writes the setting into every managed chat, re-verifying
// rights per chat: the managed-chats index is a cache of past checks, and
// somebody may have been demoted in one of those chats since. A chat that
// refuses is skipped and counted, not fatal — the other chats still get
// their change, and the result screen says how many were skipped.
func (h *UpdateHandler) bulkApply(ctx context.Context, cb *CallbackQuery, screen replyTarget, settings chat.Settings, kind string) error {
	setting, ok := findBulkSetting(kind)
	if !ok {
		return newValidationError("unknown bulk setting %q", kind)
	}
	userID := common.UserID{Value: cb.From.ID}
	targets, err := h.bulkTargets(ctx, userID, settings.ChatID)
	if err != nil {
		return err
	}

	applied, skipped := 0, 0
	for _, target := range targets {
		if err := h.Authorization.RequireManager(ctx, target.ChatID, userID); err != nil {
			skipped++
			continue
		}
		updated := target
		setting.apply(settings, &updated)
		if reflect.DeepEqual(updated, target) {
			continue // already the same value; nothing to write or to log
		}
		if _, err := h.Chats.Save(ctx, updated); err != nil {
			loggerFrom(ctx, h.Log).Warn("bulk settings apply failed", "chatId", target.ChatID.Value, "kind", kind, "error", err)
			skipped++
			continue
		}
		applied++
		h.logAdminAction(ctx, target.ChatID, &cb.From, kind, setting.value(settings))
	}
	h.recordAdminAction("bulk_settings", kind)

	text := h.Texts.Get("bulk.applied", settings.Locale,
		bold(h.Texts.Get(bulkSettingKey(kind), settings.Locale)), applied)
	if skipped > 0 {
		text += "\n" + h.Texts.Get("bulk.skipped", settings.Locale, skipped)
	}
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "menu:settings")}}}
	return h.respond(ctx, screen, managedScreenContext(screen, settings, text), &kb)
}
