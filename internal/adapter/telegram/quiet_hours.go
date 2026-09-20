package telegram

import (
	"context"
	"encoding/json"
	"strconv"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// Quiet hours: the window a chat asks not to be posted in.
//
// This wraps the proactive, non-urgent publishers — tournament
// announcements, the day-before nudge, digests, match and tournament
// recaps. A held message is rescheduled, never dropped: a chat that asked
// for silence overnight still wants its announcement, at breakfast rather
// than at three in the morning.
//
// Deliberately not wrapped: the polls themselves (a poll that arrives
// after its match has started is worthless, not late), pre-close
// reminders, anything a person just asked for, and the operational alerts
// — an administrator being told the bot is broken is exactly the message
// that must not wait for morning.

// quietHoursPublisher defers its inner publisher's messages while the
// target chat is inside its window.
type quietHoursPublisher struct {
	inner common.OutboxPublisher
	chats chat.Repository
	clock common.Clock
}

// WithQuietHours wraps a publisher so its messages respect the target
// chat's window.
func WithQuietHours(inner common.OutboxPublisher, chats chat.Repository, clock common.Clock) common.OutboxPublisher {
	return &quietHoursPublisher{inner: inner, chats: chats, clock: clock}
}

func (p *quietHoursPublisher) Supports(eventType string) bool { return p.inner.Supports(eventType) }

func (p *quietHoursPublisher) Publish(ctx context.Context, message common.OutboxMessage) error {
	// Every chat-targeted payload carries its chat id under the same name;
	// nothing else about the payload matters here, so only that is read.
	var envelope struct {
		ChatID int64 `json:"chatId"`
	}
	if err := json.Unmarshal([]byte(message.Payload), &envelope); err != nil || envelope.ChatID == 0 {
		// Undecodable or not addressed to a chat: not this decorator's
		// business. The inner publisher will report a real problem better
		// than a guess here would.
		return p.inner.Publish(ctx, message)
	}
	settings, err := p.chats.Find(ctx, common.ChatID{Value: envelope.ChatID})
	if err != nil || settings == nil {
		return p.inner.Publish(ctx, message)
	}
	if until, quiet := settings.QuietUntil(p.clock.Now()); quiet {
		return common.Deferred(until, "quiet hours in chat "+strconv.FormatInt(envelope.ChatID, 10))
	}
	return p.inner.Publish(ctx, message)
}

// --- the screen ---

// quietPresets are the windows offered on the screen. Presets rather than
// two free-form time pickers: a chat setting nobody can mis-enter beats a
// flexible one behind four taps, and every window here is the same answer
// to the same question — "not overnight".
var quietPresets = []struct{ from, to int }{
	{22 * 60, 8 * 60},
	{23 * 60, 8 * 60},
	{0, 9 * 60},
	{1 * 60, 7 * 60},
}

func cbQuietPreset(index int) string { return "settings:quiet:" + strconv.Itoa(index) }

// formatQuietWindow renders a window the way the buttons show it.
func formatQuietWindow(from, to int) string {
	render := func(m int) string {
		return pad2(m/60) + ":" + pad2(m%60)
	}
	return render(from) + "–" + render(to)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// quietHoursLabel is the settings-menu row: the window, or "off".
func (h *UpdateHandler) quietHoursLabel(settings chat.Settings) string {
	state := h.Texts.Get("settings.quiet_off", settings.Locale)
	if settings.QuietHoursSet() {
		state = formatQuietWindow(*settings.QuietFromMinute, *settings.QuietToMinute)
	}
	return h.Texts.Get("settings.quiet_label", settings.Locale, state)
}

func (h *UpdateHandler) quietHoursView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	rows := make([][]InlineButton, 0, len(quietPresets)+2)
	for i, preset := range quietPresets {
		label := formatQuietWindow(preset.from, preset.to)
		if settings.QuietHoursSet() && *settings.QuietFromMinute == preset.from && *settings.QuietToMinute == preset.to {
			label = "✓ " + label
		}
		rows = append(rows, []InlineButton{button(label, cbQuietPreset(i))})
	}
	offLabel := h.Texts.Get("settings.quiet_off", settings.Locale)
	if !settings.QuietHoursSet() {
		offLabel = "✓ " + offLabel
	}
	rows = append(rows,
		[]InlineButton{button(offLabel, cbQuietPreset(-1))},
		[]InlineButton{h.backButton(settings.Locale, "menu:settings")},
	)
	text := bold(h.Texts.Get("settings.quiet_title", settings.Locale)) + "\n\n" +
		h.Texts.Get("settings.quiet_hint", settings.Locale, code(escapeHTML(settings.Timezone)))
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// setQuietHours applies one preset, or clears the window entirely.
func (h *UpdateHandler) setQuietHours(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, raw string) (bool, error) {
	index, err := strconv.Atoi(raw)
	if err != nil || index < -1 || index >= len(quietPresets) {
		return false, newValidationError("invalid quiet hours choice %q", raw)
	}
	var state string
	if index < 0 {
		settings.QuietFromMinute, settings.QuietToMinute = nil, nil
		state = "off"
	} else {
		from, to := quietPresets[index].from, quietPresets[index].to
		settings.QuietFromMinute, settings.QuietToMinute = &from, &to
		state = formatQuietWindow(from, to)
	}
	if _, err := h.Chats.Save(ctx, settings); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "quiet_hours", state)
	if err := h.toast(ctx, cb.ID, "✅ "+state); err != nil {
		return false, err
	}
	return true, h.quietHoursView(ctx, target, settings)
}
