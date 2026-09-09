package telegram

import (
	"context"
	"strings"
	"unicode/utf8"

	"cs2predictor/internal/platform/common"
)

// The name a leaderboard shows for someone is, by default, whatever their
// Telegram profile says — refreshed on every vote (see
// PredictionRepository.SaveVote), so it drifts if they rename themselves
// on Telegram, and it's not always the name they'd pick for a public
// scoreboard. This lets them set one directly, from their own DM, that
// every result list (leaderboards, digests, year-end nominations) prefers
// over the Telegram-sourced name — see the COALESCE in scoring.go's
// queries. It is deliberately global rather than per-chat: the same
// person is the same person on every leaderboard they appear on.

// nicknameMaxRunes bounds what someone can set. Generous enough for a
// real name in most scripts, short enough to never need truncating in a
// "name — points" row (see leaderboardRows and the publishers that render
// standings).
const nicknameMaxRunes = 40

func cbRenameAsk() string   { return "pstats:rename:ask" }
func cbRenameReset() string { return "pstats:rename:reset" }

// renameMenu shows the current choice (or the fact that none has been
// made yet) and offers to change or clear it.
func (h *UpdateHandler) renameMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	nickname, err := h.Chats.Nickname(ctx, userID)
	if err != nil {
		return err
	}
	rows := [][]InlineButton{{button(h.Texts.Get("dm.rename_change", locale), cbRenameAsk())}}
	text := h.Texts.Get("dm.rename_unset", locale)
	if nickname != nil {
		text = h.Texts.Get("dm.rename_current", locale, bold(escapeHTML(*nickname)))
		rows = append(rows, []InlineButton{button(h.Texts.Get("dm.rename_reset", locale), cbRenameReset())})
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "pstats:menu")})
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

// requestNickname sends the ForceReply prompt that isRenameReply later
// recognizes — the same pattern requestEventSearch uses for "reply with a
// tournament name".
func (h *UpdateHandler) requestNickname(ctx context.Context, chatID common.ChatID, locale common.LocaleCode) error {
	_, err := h.Client.Call(ctx, "sendMessage", map[string]any{
		"chat_id":    chatID.Value,
		"text":       h.Texts.Get("dm.rename_prompt", locale),
		"parse_mode": "HTML",
		"reply_markup": map[string]any{
			"force_reply":             true,
			"selective":               true,
			"input_field_placeholder": h.Texts.Get("dm.rename_placeholder", locale),
		},
	})
	return err
}

// isRenameReply reports whether msg is a reply to that prompt. Checked
// against the user's own resolved locale (see resolveUserLocale) rather
// than a chat's, since renaming has no chat context at all — unlike the
// events-search reply, it must work for every DM user, not only someone
// with an active managed-chat session.
func isRenameReply(msg *Message, locale common.LocaleCode, texts *Texts) bool {
	if msg == nil || msg.ReplyToMessage == nil || msg.ReplyToMessage.Text == nil {
		return false
	}
	return strings.TrimSpace(*msg.ReplyToMessage.Text) == strings.TrimSpace(stripHTML(texts.Get("dm.rename_prompt", locale)))
}

// sanitizeNickname collapses internal whitespace (a pasted multi-line
// reply must not break a one-line "name — points" leaderboard row) and
// bounds the length. Emptiness after cleaning is rejected rather than
// treated as a reset — resetting has its own explicit button so it can
// never happen by an accidental blank reply.
func sanitizeNickname(raw string) (string, error) {
	cleaned := strings.Join(strings.Fields(raw), " ")
	if cleaned == "" {
		return "", newValidationError("nickname must not be empty")
	}
	if utf8.RuneCountInString(cleaned) > nicknameMaxRunes {
		return "", newValidationError("nickname must be at most %d characters", nicknameMaxRunes)
	}
	return cleaned, nil
}

// applyNicknameReply validates and saves a ForceReply answer, then
// confirms in a fresh message — this is reached from free text, not a
// callback, so there is no existing message to edit in place.
func (h *UpdateHandler) applyNicknameReply(ctx context.Context, msg *Message, userID common.UserID, locale common.LocaleCode, raw string) error {
	nickname, err := sanitizeNickname(raw)
	if err != nil {
		return err
	}
	if err := h.Chats.SetNickname(ctx, userID, nickname); err != nil {
		return err
	}
	chatID := common.ChatID{Value: msg.Chat.ID}
	text := "✅ " + h.Texts.Get("dm.rename_saved", locale, bold(escapeHTML(nickname)))
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "pstats:menu")}}}
	return h.respond(ctx, sendTarget(chatID, nil), text, &kb)
}
