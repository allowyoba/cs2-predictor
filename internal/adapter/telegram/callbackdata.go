package telegram

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"cs2predictor/internal/platform/common"
)

// callbackDataLimit is Telegram's hard cap on callback_data (64 bytes).
// Exceeding it is rejected by the Bot API at send time, for that one
// button, so every payload this package produces is checked against it at
// construction rather than discovered in production.
const callbackDataLimit = 64

// Callback payloads carry an optional single-character scope prefix,
// separated from the payload by "|" (a byte no payload itself uses):
//
//	c<base36 chat id>|<payload>   the action targets that specific chat
//	u<base36 user id>|<payload>   only that user may use this keyboard
//
// Chat scoping is what lets one person keep admin panels for several
// different chats open in the same DM at once: each button carries the
// chat it belongs to, so a panel scrolled back to still acts on its own
// chat rather than on whatever was opened most recently. User scoping
// makes a group-chat menu personal — Telegram has no per-viewer message
// visibility, so the next best thing is a keyboard that only answers to
// the person who opened it (see scopeKeyboard).
const (
	scopeSeparator  = "|"
	scopeChatPrefix = "c"
	scopeUserPrefix = "u"
)

// callbackScope is the decoded prefix: at most one of the two is set.
type callbackScope struct {
	chatID *common.ChatID
	userID *common.UserID
}

func chatScope(chatID common.ChatID) callbackScope { return callbackScope{chatID: &chatID} }
func userScope(userID common.UserID) callbackScope { return callbackScope{userID: &userID} }

func (s callbackScope) empty() bool { return s.chatID == nil && s.userID == nil }

// prefix renders the scope's wire form, including the separator ("" when
// the scope is empty).
func (s callbackScope) prefix() string {
	switch {
	case s.chatID != nil:
		return scopeChatPrefix + strconv.FormatInt(s.chatID.Value, 36) + scopeSeparator
	case s.userID != nil:
		return scopeUserPrefix + strconv.FormatInt(s.userID.Value, 36) + scopeSeparator
	default:
		return ""
	}
}

// splitCallbackScope peels the scope prefix off data, returning the scope
// (empty if there was none) and the remaining payload. Unscoped data, from
// a screen that doesn't need scoping or from a keyboard already sitting
// in someone's chat history, passes through untouched.
func splitCallbackScope(data string) (callbackScope, string) {
	head, payload, found := strings.Cut(data, scopeSeparator)
	if !found || head == "" {
		return callbackScope{}, data
	}
	kind, raw := head[:1], head[1:]
	id, err := strconv.ParseInt(raw, 36, 64)
	if err != nil {
		return callbackScope{}, data
	}
	switch kind {
	case scopeChatPrefix:
		return chatScope(common.ChatID{Value: id}), payload
	case scopeUserPrefix:
		return userScope(common.UserID{Value: id}), payload
	default:
		return callbackScope{}, data
	}
}

// applyToKeyboard rewrites every callback button in kb to carry this
// scope. URL buttons and already-scoped payloads are left alone, and a
// button whose scoped form would exceed Telegram's 64-byte budget keeps
// its unscoped payload (degrading to the pre-scoping behavior for that one
// button) rather than being silently rejected by the API at send time.
//
// Doing this once at the send boundary — rather than at each of the ~40
// places a button is built — is what keeps the shared group/DM renderers
// identical in both contexts.
func (s callbackScope) applyToKeyboard(kb *InlineKeyboard) (dropped int) {
	if kb == nil || s.empty() {
		return 0
	}
	prefix := s.prefix()
	for _, row := range kb.InlineKeyboard {
		for i := range row {
			btn := &row[i]
			if btn.CallbackData == nil {
				continue
			}
			if _, _, alreadyScoped := strings.Cut(*btn.CallbackData, scopeSeparator); alreadyScoped {
				continue
			}
			scoped := prefix + *btn.CallbackData
			if len(scoped) > callbackDataLimit {
				dropped++
				continue
			}
			btn.CallbackData = &scoped
		}
	}
	return dropped
}

// --- context plumbing ---

type callbackScopeKey struct{}

// withCallbackScope stamps the scope every keyboard rendered downstream
// should carry. Set once per dispatched update (see handleCallback,
// handlePrivateCallback and handleMessage), so renderers stay unaware of
// it entirely.
func withCallbackScope(ctx context.Context, scope callbackScope) context.Context {
	return context.WithValue(ctx, callbackScopeKey{}, scope)
}

func callbackScopeFrom(ctx context.Context) callbackScope {
	scope, _ := ctx.Value(callbackScopeKey{}).(callbackScope)
	return scope
}

// --- payload builders ---
//
// Ids go on the wire in their compact 32-character hex form rather than
// the 36-character dashed one: uuid.Parse accepts both, so decoding stays
// backward compatible with buttons already sent, while new buttons gain
// the four bytes that let a scope prefix fit inside the 64-byte budget.

func compactUUID(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")
}

func cbSubscribe(eventID common.EventID) string   { return "subscribe:" + compactUUID(eventID.Value) }
func cbEventView(eventID common.EventID) string   { return "events:view:" + compactUUID(eventID.Value) }
func cbUnsubscribe(eventID common.EventID) string { return "unsubscribe:" + compactUUID(eventID.Value) }
func cbEventTopic(eventID common.EventID) string  { return "event-topic:" + compactUUID(eventID.Value) }
func cbStatsEvent(eventID common.EventID) string  { return "stats:event:" + compactUUID(eventID.Value) }
func cbStatsMine(eventID common.EventID) string   { return "stats:mine:" + compactUUID(eventID.Value) }

func cbModeratorRemove(userID common.UserID) string {
	return "moderators:remove:ask:" + strconv.FormatInt(userID.Value, 36)
}

func cbModeratorRemoveDo(userID common.UserID) string {
	return "moderators:remove:do:" + strconv.FormatInt(userID.Value, 36)
}

func cbManageOpen(chatID common.ChatID) string {
	return "manage:open:" + strconv.FormatInt(chatID.Value, 36)
}
