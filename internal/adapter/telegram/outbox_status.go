package telegram

import (
	"context"
	"strings"

	"cs2predictor/internal/platform/common"
)

// The undelivered-messages panel: what the transactional outbox gave up on,
// and the one button that puts it back in the queue.
//
// A message that exhausts its retries stops being selected for delivery and
// is never mentioned again — the chat that should have received a poll, a
// recap or a moderator invitation simply never does. This screen is the
// only place that state is visible to a human, and replaying is a
// deliberate act rather than something the dispatcher does on its own: the
// usual cause is a failure that has to be fixed first (a chat the bot was
// removed from, a topic that no longer exists), and an automatic retry
// loop would just burn the budget again.

func (h *UpdateHandler) outboxStatusView(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	back := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "hub:system")}}}
	if !h.isRootTeamMatchOperator(userID) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), back)
	}
	if h.DeadLetters == nil {
		return h.respond(ctx, target, h.Texts.Get("outbox.unavailable", locale), back)
	}
	groups, err := h.DeadLetters.DeadLetters(ctx)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return h.respond(ctx, target, bold(h.Texts.Get("outbox.title", locale))+"\n\n"+
			h.Texts.Get("outbox.empty", locale), back)
	}

	total := 0
	lines := make([]string, 0, len(groups))
	for _, g := range groups {
		total += g.Count
		line := "• " + code(escapeHTML(g.EventType)) + " — " + bold(itoa(g.Count))
		if g.LastError != "" {
			line += "\n  " + italic(escapeHTML(truncate(g.LastError, 120)))
		}
		lines = append(lines, line)
	}
	text := bold(h.Texts.Get("outbox.title", locale)) + "\n\n" +
		h.Texts.Get("outbox.summary", locale, total) + "\n\n" + strings.Join(lines, "\n")
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("outbox.replay", locale), "hub:outbox:replay")},
		{h.backButton(locale, "hub:system")},
	}}
	return h.respond(ctx, target, text, kb)
}

// replayDeadLetters releases every dead letter back to the dispatcher and
// re-renders the panel, which is then either empty or — if delivery fails
// again — populated with the same messages a few minutes later.
func (h *UpdateHandler) replayDeadLetters(ctx context.Context, cb *CallbackQuery, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	if !h.isRootTeamMatchOperator(userID) {
		return h.refuse(ctx, target, locale, "hub:system")
	}
	if h.DeadLetters == nil {
		back := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "hub:system")}}}
		return h.respond(ctx, target, h.Texts.Get("outbox.unavailable", locale), back)
	}
	released, err := h.DeadLetters.ReplayDeadLetters(ctx)
	if err != nil {
		return err
	}
	if err := h.toast(ctx, cb.ID, "✅ "+h.Texts.Get("outbox.replayed", locale, released)); err != nil {
		return err
	}
	return h.outboxStatusView(ctx, target, userID, locale)
}

// itoa keeps this file's one number-to-text conversion local, matching the
// rest of the package's habit of not reaching for strconv in view code.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
