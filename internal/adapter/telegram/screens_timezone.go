package telegram

import (
	"context"
	"strconv"
	"time"

	"cs2predictor/internal/domain/chat"
)

// The settings screen shows the chat's timezone, but for a long time the
// only way to change it was to know and type "/timezone Area/City" — a
// setting you can see and not touch. This is the picker: the handful of
// zones this bot's audience actually uses, one tap each, with the typed
// command still there for everything else.

// commonTimezones is ordered west to east, so the list reads like a map
// rather than an arbitrary ranking. The callback carries an index into
// this slice rather than the name itself: an IANA zone can be 30+
// characters, which does not fit the 64-byte callback budget once the
// chat scope is stamped on.
var commonTimezones = []string{
	"UTC",
	"Europe/London",
	"Europe/Berlin",
	"Europe/Kyiv",
	"Europe/Moscow",
	"Asia/Yekaterinburg",
	"Asia/Almaty",
	"America/New_York",
}

func cbTimezone(index int) string { return "settings:tz:" + strconv.Itoa(index) }

func (h *UpdateHandler) timezoneView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	rows := make([][]InlineButton, 0, len(commonTimezones)/2+2)
	for i := 0; i < len(commonTimezones); i += 2 {
		row := []InlineButton{button(timezoneLabel(commonTimezones[i], settings.Timezone), cbTimezone(i))}
		if i+1 < len(commonTimezones) {
			row = append(row, button(timezoneLabel(commonTimezones[i+1], settings.Timezone), cbTimezone(i+1)))
		}
		rows = append(rows, row)
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:settings")})

	text := bold(h.Texts.Get("settings.timezone", settings.Locale)) + "\n\n" +
		h.Texts.Get("settings.timezone_current", settings.Locale, code(escapeHTML(settings.Timezone))) + "\n\n" +
		h.Texts.Get("settings.timezone_hint", settings.Locale)
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

// timezoneLabel marks the zone currently in force, so the picker shows the
// state as well as the choices.
func timezoneLabel(zone, current string) string {
	if zone == current {
		return "• " + zone
	}
	return zone
}

func (h *UpdateHandler) setTimezone(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, raw string) (bool, error) {
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 || index >= len(commonTimezones) {
		return false, newValidationError("invalid timezone choice %q", raw)
	}
	zone := commonTimezones[index]
	// Validated against the tzdata the process actually has, so a zone
	// missing from a slim container fails here rather than silently
	// rendering every time in UTC.
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("settings.timezone_unknown", settings.Locale, zone))
	}
	if settings.Timezone == loc.String() {
		return true, h.toast(ctx, cb.ID, h.Texts.Get("settings.timezone_current", settings.Locale, loc.String()))
	}
	settings.Timezone = loc.String()
	if _, err := h.Chats.Save(ctx, settings); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "timezone", loc.String())
	if err := h.toast(ctx, cb.ID, "✅ "+loc.String()); err != nil {
		return false, err
	}
	return true, h.timezoneView(ctx, target, settings)
}
