package telegram

import (
	"context"
	"strings"
	"time"

	"cs2predictor/internal/domain/feedback"
	"cs2predictor/internal/platform/common"
)

// The Ideas screen: the one place somebody using this bot can say
// something back to the people who run it.
//
// It lives in a private chat only. The same message in a group would be
// read by a room full of people who did not ask for it, and the rate
// limits below are per person — which only means anything where a person
// is the one speaking.
//
// Everything about how much and how often is decided by feedback.Policy;
// this file is only the conversation around it. The limits are stated in
// the prompt itself rather than discovered by hitting them, which is the
// difference between a form and a trap.

// suggestionMenu explains the channel and offers the one action.
func (h *UpdateHandler) suggestionMenu(ctx context.Context, target replyTarget, locale common.LocaleCode) error {
	if h.Feedback == nil {
		return h.respond(ctx, target, h.Texts.Get("idea.unavailable", locale),
			&InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "pstats:settings")}}})
	}
	policy := h.Feedback.Policy
	text := bold(h.Texts.Get("idea.title", locale)) + "\n\n" +
		h.Texts.Get("idea.explainer", locale) + "\n\n" +
		italic(h.Texts.Get("idea.limits", locale, policy.MaxRunes, policy.Quota, int(policy.QuotaWindow.Hours())))
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("idea.write", locale), "idea:write")},
		{h.backButton(locale, "pstats:settings")},
	}}
	return h.respond(ctx, target, text, &kb)
}

// requestSuggestion sends the ForceReply prompt isSuggestionReply
// recognizes — the same pattern the rename and event-search prompts use.
// The size limit is in the prompt: somebody typing a long idea should know
// the ceiling before they write to it, not after.
func (h *UpdateHandler) requestSuggestion(ctx context.Context, chatID common.ChatID, locale common.LocaleCode) error {
	maxRunes := feedback.DefaultPolicy().MaxRunes
	if h.Feedback != nil {
		maxRunes = h.Feedback.Policy.MaxRunes
	}
	_, err := h.Client.Call(ctx, "sendMessage", map[string]any{
		"chat_id":    chatID.Value,
		"text":       h.Texts.Get("idea.prompt", locale, maxRunes),
		"parse_mode": "HTML",
		"reply_markup": map[string]any{
			"force_reply":             true,
			"selective":               true,
			"input_field_placeholder": h.Texts.Get("idea.placeholder", locale),
		},
	})
	return err
}

// isSuggestionReply reports whether msg answers that prompt. Matched on
// the prompt's own text, like every other ForceReply flow here, so an
// unrelated message never gets mistaken for an idea.
func isSuggestionReply(msg *Message, locale common.LocaleCode, texts *Texts, maxRunes int) bool {
	if msg == nil || msg.ReplyToMessage == nil || msg.ReplyToMessage.Text == nil {
		return false
	}
	return strings.TrimSpace(*msg.ReplyToMessage.Text) ==
		strings.TrimSpace(stripHTML(texts.Get("idea.prompt", locale, maxRunes)))
}

// applySuggestionReply submits the reply and says what happened to it.
//
// Every refusal names what to do next — shorten it, wait this long, it is
// already here — except the flood case past its threshold, which is
// answered exactly once and then not at all: a reply per message is the
// one thing a flood is actually extracting from this bot.
func (h *UpdateHandler) applySuggestionReply(ctx context.Context, msg *Message, user *User, locale common.LocaleCode, raw string) error {
	if h.Feedback == nil {
		return h.respond(ctx, sendTarget(common.ChatID{Value: msg.Chat.ID}, nil), h.Texts.Get("idea.unavailable", locale), nil)
	}
	if user == nil {
		return nil // a channel post or an anonymous sender: nobody to answer
	}
	userID := common.UserID{Value: user.ID}
	username := ""
	if user.Username != nil {
		username = *user.Username
	}
	decision, err := h.Feedback.Submit(ctx, userID, user.DisplayName(), username, raw)
	if err != nil {
		return err
	}
	if decision.Silent {
		return nil
	}

	chatID := common.ChatID{Value: msg.Chat.ID}
	back := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "pstats:settings")}}}
	text := h.suggestionOutcomeText(decision, locale)
	return h.respond(ctx, sendTarget(chatID, nil), text, back)
}

// suggestionOutcomeText renders one decision for the person who made it.
func (h *UpdateHandler) suggestionOutcomeText(decision feedback.Decision, locale common.LocaleCode) string {
	maxRunes := h.Feedback.Policy.MaxRunes
	switch decision.Outcome {
	case feedback.OutcomeAccepted:
		return "✅ " + h.Texts.Get("idea.accepted", locale)
	case feedback.OutcomeEmpty:
		return h.Texts.Get("idea.empty", locale)
	case feedback.OutcomeTooLong:
		return h.Texts.Get("idea.too_long", locale, maxRunes)
	case feedback.OutcomeDuplicate:
		return h.Texts.Get("idea.duplicate", locale)
	case feedback.OutcomeTooSoon:
		return h.Texts.Get("idea.too_soon", locale, formatRetryAfter(h.Texts, locale, decision.RetryAfter))
	case feedback.OutcomeQuotaReached:
		return h.Texts.Get("idea.quota", locale, h.Feedback.Policy.Quota, formatRetryAfter(h.Texts, locale, decision.RetryAfter))
	default:
		return h.Texts.Get("idea.flood", locale)
	}
}

// formatRetryAfter renders a wait the way a person would say it: whole
// minutes while it is minutes, whole hours once it is longer, and never
// "0 minutes".
func formatRetryAfter(texts *Texts, locale common.LocaleCode, d time.Duration) string {
	if d < time.Minute {
		return texts.Get("idea.wait_minutes", locale, 1)
	}
	if d < time.Hour {
		return texts.Get("idea.wait_minutes", locale, int(d.Round(time.Minute).Minutes()))
	}
	hours := int(d.Round(time.Hour).Hours())
	if hours < 1 {
		hours = 1
	}
	return texts.Get("idea.wait_hours", locale, hours)
}
