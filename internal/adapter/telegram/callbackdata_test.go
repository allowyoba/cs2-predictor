package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

func TestCallbackScope_RoundTrips(t *testing.T) {
	chatID := common.ChatID{Value: -1001234567890}
	userID := common.UserID{Value: 42}

	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{button("x", "settings:locale")}}}
	chatScope(chatID).applyToKeyboard(&kb)
	gotScope, payload := splitCallbackScope(*kb.InlineKeyboard[0][0].CallbackData)
	if gotScope.chatID == nil || *gotScope.chatID != chatID || payload != "settings:locale" {
		t.Fatalf("chat scope round trip = %+v, %q", gotScope, payload)
	}

	kb2 := InlineKeyboard{InlineKeyboard: [][]InlineButton{{button("x", "menu:events")}}}
	userScope(userID).applyToKeyboard(&kb2)
	gotScope2, payload2 := splitCallbackScope(*kb2.InlineKeyboard[0][0].CallbackData)
	if gotScope2.userID == nil || *gotScope2.userID != userID || payload2 != "menu:events" {
		t.Fatalf("user scope round trip = %+v, %q", gotScope2, payload2)
	}
}

// Buttons sent before scoping existed carry no prefix and must keep working
// exactly as they did — chat history outlives a deploy.
func TestCallbackScope_UnscopedDataPassesThroughUnchanged(t *testing.T) {
	for _, data := range []string{"menu:main", "stats:month:2026-01", "unsubok:abc", ""} {
		scope, payload := splitCallbackScope(data)
		if !scope.empty() || payload != data {
			t.Fatalf("splitCallbackScope(%q) = %+v, %q — want untouched", data, scope, payload)
		}
	}
}

func TestCallbackScope_NeverExceedsTelegramBudget(t *testing.T) {
	// The worst realistic case: a scoped action against a full-length
	// supergroup id carrying a compact event uuid.
	chatID := common.ChatID{Value: -1001234567890}
	eventID := common.NewEventID()
	for _, payload := range []string{
		cbSubscribe(eventID), cbUnsubscribe(eventID), cbEventTopic(eventID),
		cbStatsEvent(eventID), cbStatsMine(eventID), cbManageOpen(chatID), cbEventView(eventID),
	} {
		kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{button("x", payload)}}}
		if dropped := chatScope(chatID).applyToKeyboard(&kb); dropped != 0 {
			t.Fatalf("payload %q could not be scoped within %d bytes", payload, callbackDataLimit)
		}
		if got := len(*kb.InlineKeyboard[0][0].CallbackData); got > callbackDataLimit {
			t.Fatalf("scoped %q is %d bytes, over the %d-byte limit", payload, got, callbackDataLimit)
		}
	}
}

// An over-budget payload keeps its unscoped form rather than being silently
// rejected by Telegram at send time.
func TestCallbackScope_OverBudgetButtonKeepsUnscopedPayload(t *testing.T) {
	long := "x" + strings.Repeat("y", callbackDataLimit-1)
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{button("x", long)}}}
	if dropped := chatScope(common.ChatID{Value: -1001234567890}).applyToKeyboard(&kb); dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if *kb.InlineKeyboard[0][0].CallbackData != long {
		t.Fatal("an over-budget button must keep its original payload")
	}
}

func TestCallbackScope_LeavesURLButtonsAlone(t *testing.T) {
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{urlButton("open", "https://t.me/bot?start=admin_1")}}}
	chatScope(common.ChatID{Value: -1}).applyToKeyboard(&kb)
	if kb.InlineKeyboard[0][0].CallbackData != nil {
		t.Fatal("a URL button must not gain callback data")
	}
}

// --- the point of all this: several chats managed at once ---

// Two admin panels for two different chats can sit in the same DM, and each
// keeps acting on its own chat — including the one opened first, which
// before scoping would have been repointed at whatever was opened last.
func TestDM_PanelsForDifferentChatsStayIndependent(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})

	chatA := common.ChatID{Value: -1}
	chatB := common.ChatID{Value: -2}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatA, Title: "Chat A", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatB, Title: "Chat B", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	userID := common.UserID{Value: 42}

	// Open A, then B — the DM session now points at B.
	if err := handler.openManagedChat(context.Background(), sendTarget(common.ChatID{Value: 42}, nil), userID, common.LocaleRU, chatA); err != nil {
		t.Fatal(err)
	}
	if err := handler.openManagedChat(context.Background(), sendTarget(common.ChatID{Value: 42}, nil), userID, common.LocaleRU, chatB); err != nil {
		t.Fatal(err)
	}
	if chats.dmSessions[42] != chatB.Value {
		t.Fatalf("session = %d, want chat B (%d)", chats.dmSessions[42], chatB.Value)
	}

	// Now use a button from chat A's older panel: it carries A's stamp, so it
	// must toggle A's locale, not B's.
	data := chatScope(chatA).prefix() + "settings:locale"
	cb := &CallbackQuery{
		ID: "cb1", From: User{ID: 42, FirstName: "Admin"},
		Message: &Message{MessageID: 1, Chat: Chat{ID: 42, Type: "private"}}, Data: &data,
	}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	settingsA, _ := chats.Find(context.Background(), chatA)
	settingsB, _ := chats.Find(context.Background(), chatB)
	if settingsA.Locale != common.LocaleEN {
		t.Fatalf("chat A locale = %v, want EN — the stamped panel must act on its own chat", settingsA.Locale)
	}
	if settingsB.Locale != common.LocaleRU {
		t.Fatalf("chat B locale = %v, want RU — the most recently opened chat must be untouched", settingsB.Locale)
	}
	if chats.dmSessions[42] != chatA.Value {
		t.Fatalf("session = %d, want it to follow the panel just used (%d)", chats.dmSessions[42], chatA.Value)
	}
	_ = calls
}

// --- personal group menus ---

func TestGroupMenu_OnlyItsOwnerCanUseIt(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	// Owner opens the menu.
	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: strPtr("/menu")}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	var scoped string
	for _, c := range *calls {
		kb, ok := c["reply_markup"].(map[string]any)
		if !ok {
			continue
		}
		for _, row := range kb["inline_keyboard"].([]any) {
			for _, btn := range row.([]any) {
				if cd, ok := btn.(map[string]any)["callback_data"].(string); ok && strings.HasSuffix(cd, "menu:stats") {
					scoped = cd
				}
			}
		}
	}
	if scoped == "" {
		t.Fatal("no scoped stats button rendered")
	}
	if gotScope, _ := splitCallbackScope(scoped); gotScope.userID == nil || gotScope.userID.Value != 1 {
		t.Fatalf("menu button is not bound to its opener: %q", scoped)
	}

	// A different member taps it: refused with a private alert, nothing else.
	before := len(*calls)
	cb := &CallbackQuery{
		ID: "cb1", From: User{ID: 999, FirstName: "Someone"},
		Message: &Message{MessageID: 1, Chat: Chat{ID: -1, Type: "group"}}, Data: &scoped,
	}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	for _, c := range (*calls)[before:] {
		if c["__method"] != "answerCallbackQuery" {
			t.Fatalf("a non-owner tap must only answer privately, got a %v call", c["__method"])
		}
		if c["show_alert"] != true {
			t.Fatal("the refusal must be an alert (private to the tapper), not a toast")
		}
	}

	// The owner's own tap still works.
	before = len(*calls)
	cbOwner := &CallbackQuery{
		ID: "cb2", From: User{ID: 1, FirstName: "Owner"},
		Message: &Message{MessageID: 1, Chat: Chat{ID: -1, Type: "group"}}, Data: &scoped,
	}
	if err := handler.handleCallback(context.Background(), cbOwner); err != nil {
		t.Fatal(err)
	}
	rendered := false
	for _, c := range (*calls)[before:] {
		if c["__method"] == "editMessageText" || c["__method"] == "sendMessage" {
			rendered = true
		}
	}
	if !rendered {
		t.Fatal("the owner's own tap should render the screen")
	}
}

// An anonymous admin / channel post has no sender to bind to; the menu is
// then unowned and usable by anyone, as it was before ownership existed.
func TestGroupMenu_UnownedWhenMessageHasNoSender(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, Text: strPtr("/menu")} // From is nil
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if len(cds) == 0 {
		t.Fatal("expected a menu keyboard even without a sender")
	}
	for _, c := range *calls {
		kb, ok := c["reply_markup"].(map[string]any)
		if !ok {
			continue
		}
		for _, row := range kb["inline_keyboard"].([]any) {
			for _, btn := range row.([]any) {
				if cd, ok := btn.(map[string]any)["callback_data"].(string); ok {
					if scope, _ := splitCallbackScope(cd); !scope.empty() {
						t.Fatalf("a senderless menu must not be owner-bound, got %q", cd)
					}
				}
			}
		}
	}
}
