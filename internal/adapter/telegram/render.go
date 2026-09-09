package telegram

import (
	"context"
	"errors"
	"strings"

	"cs2predictor/internal/platform/common"
)

// escapeHTML makes s safe to interpolate into an HTML parse_mode message.
// Telegram's HTML mode only requires escaping these three characters (unlike
// full HTML) — anything else is rendered literally. Every piece of
// user-controlled text reaching a message (team/event names, chat titles,
// display names) MUST go through this before interpolation, since an
// unescaped '<' or '>' would either break the message's formatting or make
// Telegram reject the send outright as an unclosed/unknown tag.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// bold/italic/code wrap already-escaped text in Telegram's HTML tags — kept
// as tiny named helpers rather than inline fmt.Sprintf calls so renderers
// read as "bold(x)" instead of a wall of "<b>%s</b>".
func bold(s string) string   { return "<b>" + s + "</b>" }
func italic(s string) string { return "<i>" + s + "</i>" }
func code(s string) string   { return "<code>" + s + "</code>" }

// replyTarget is where a rendered view goes: sendTarget posts a brand-new
// message (a /command has nothing to edit yet); editTarget rewrites an
// existing message in place. Editing in place is what stops inline-keyboard
// navigation (menu -> submenu -> submenu) from leaving a trail of one new
// message per click — the same "screen" just updates as you navigate,
// matching how a well-behaved Telegram bot behaves.
type replyTarget struct {
	chatID    common.ChatID
	messageID *int64 // set => edit this message; nil => send a new one
	topicID   *int64 // only consulted when sending new (messageID == nil)
}

func sendTarget(chatID common.ChatID, topicID *int64) replyTarget {
	return replyTarget{chatID: chatID, topicID: topicID}
}

// editTargetFromCallback builds a target that edits the message a callback
// query's button was attached to — always in cb.Message.Chat, the chat that
// message actually lives in, which is NOT necessarily the chat the tapped
// action logically operates on: a DM admin session dispatches group-chat
// callback data (settings:*, subscribe:*, ...) from inside a private chat,
// so the message being edited lives in the DM even though settings.ChatID
// is the managed group. fallbackChatID is used only in the defensive case
// where the callback somehow carries no message at all (handleCallback
// already returns early in that case, so this path isn't expected to run).
func editTargetFromCallback(cb *CallbackQuery, fallbackChatID common.ChatID) replyTarget {
	if cb == nil || cb.Message == nil {
		return sendTarget(fallbackChatID, nil)
	}
	id := cb.Message.MessageID
	return replyTarget{chatID: common.ChatID{Value: cb.Message.Chat.ID}, messageID: &id, topicID: cb.Message.MessageThreadID}
}

// respond renders text (already HTML-escaped/formatted by the caller) and
// an optional keyboard to target. Editing that fails because the message
// simply can't be edited anymore (deleted, older than 48h, ...) falls back
// to sending a new message rather than surfacing an error the user can't
// act on; editing that fails because the content is byte-identical to what
// is already there (a double-tap on the same button) is treated as success.
func (h *UpdateHandler) respond(ctx context.Context, target replyTarget, text string, keyboard *InlineKeyboard) error {
	if target.messageID == nil {
		return h.send(ctx, target.chatID, text, keyboard, target.topicID)
	}
	h.scopeKeyboard(ctx, keyboard)

	payload := map[string]any{
		"chat_id": target.chatID.Value, "message_id": *target.messageID,
		"text": text, "parse_mode": "HTML",
	}
	if keyboard != nil {
		payload["reply_markup"] = *keyboard
	}
	_, err := h.Client.Call(ctx, "editMessageText", payload)
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.IsNotModified() {
			return nil
		}
		if apiErr.IsEditUnavailable() {
			return h.send(ctx, target.chatID, text, keyboard, target.topicID)
		}
	}
	return err
}

// scopeKeyboard stamps the ambient callback scope (see callbackdata.go)
// onto every button about to go out. Applied here at the send boundary,
// once, so no individual screen renderer has to know whether it's being
// drawn into a group or into someone's DM admin panel. Mutates in place:
// every caller builds its keyboard immediately before sending it.
func (h *UpdateHandler) scopeKeyboard(ctx context.Context, keyboard *InlineKeyboard) {
	scope := callbackScopeFrom(ctx)
	if dropped := scope.applyToKeyboard(keyboard); dropped > 0 {
		loggerFrom(ctx, h.Log).Warn("callback data too long to scope, button left unscoped", "buttons", dropped)
	}
}

// send posts text/keyboard as a brand-new sendMessage.
func (h *UpdateHandler) send(ctx context.Context, chatID common.ChatID, text string, keyboard *InlineKeyboard, topicID *int64) error {
	h.scopeKeyboard(ctx, keyboard)
	payload := map[string]any{"chat_id": chatID.Value, "text": text, "parse_mode": "HTML"}
	if keyboard != nil {
		payload["reply_markup"] = *keyboard
	}
	if topicID != nil {
		payload["message_thread_id"] = *topicID
	}
	_, err := h.Client.Call(ctx, "sendMessage", payload)
	return err
}

// toast answers a callback query with a small popup notification (Telegram
// shows `text` briefly over the chat) instead of posting a new chat
// message — the native, low-friction way to confirm a lightweight action (a
// settings toggle, an unsubscribe) without cluttering the chat history.
func (h *UpdateHandler) toast(ctx context.Context, callbackID, text string) error {
	_, err := h.Client.Call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID, "text": text,
	})
	return err
}

// alert is toast's modal sibling: Telegram shows it as a dialog the user
// must dismiss, and — importantly for group chats — only to the person who
// tapped. It's the one genuinely private surface a bot has inside a group,
// so it carries anything meant for one member's eyes, such as the
// "this menu belongs to someone else" refusal (see scopeKeyboard).
func (h *UpdateHandler) alert(ctx context.Context, callbackID, text string) error {
	_, err := h.Client.Call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackID, "text": text, "show_alert": true,
	})
	return err
}

// backButton is the "go back up one level" button appended to every
// submenu; data is the parent menu's own routeCallback data string. The
// label comes from the bundle like every other button label, so it stays
// in one place and tests can assert on the key rather than the rendered
// text.
func (h *UpdateHandler) backButton(locale common.LocaleCode, data string) InlineButton {
	return button(h.Texts.Get("nav.back", locale), data)
}
