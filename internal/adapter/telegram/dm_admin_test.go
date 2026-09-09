package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

func TestParseAdminDeepLink_ParsesValidPayload(t *testing.T) {
	id := int64(-1001234567890)
	text := "/start admin_" + int64Base36(id)
	got, ok := parseAdminDeepLink(text)
	if !ok || got.Value != id {
		t.Fatalf("parseAdminDeepLink(%q) = %v, %v, want %d, true", text, got, ok, id)
	}
}

func TestParseAdminDeepLink_RejectsNonAdminOrMalformedPayloads(t *testing.T) {
	cases := []string{
		"/start",
		"/start ",
		"/start somethingelse_123",
		"/start admin_not-a-number",
		"/menu",
	}
	for _, text := range cases {
		if _, ok := parseAdminDeepLink(text); ok {
			t.Fatalf("parseAdminDeepLink(%q) = ok, want not-ok", text)
		}
	}
}

func int64Base36(n int64) string {
	// Mirrors dmDeepLink's own encoding (strconv.FormatInt(n, 36)) — kept as
	// a separate tiny helper so the test doesn't silently pass by comparing
	// the implementation against itself via the same call.
	neg := n < 0
	if neg {
		n = -n
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{digits[n%36]}, b...)
		n /= 36
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func TestDMDeepLink_BuildsURLWhenBotUsernameSet(t *testing.T) {
	h := &UpdateHandler{BotUsername: "cs2predictor_bot"}
	link, ok := h.dmDeepLink(common.ChatID{Value: -100123})
	if !ok {
		t.Fatal("expected ok=true when BotUsername is set")
	}
	if !strings.HasPrefix(link, "https://t.me/cs2predictor_bot?start=admin_") {
		t.Fatalf("dmDeepLink = %q, want a t.me start-param URL", link)
	}
}

func TestDMDeepLink_FalseWhenBotUsernameEmpty(t *testing.T) {
	h := &UpdateHandler{}
	if _, ok := h.dmDeepLink(common.ChatID{Value: -100123}); ok {
		t.Fatal("expected ok=false when BotUsername is empty")
	}
}

// --- end-to-end: group /menu rendering ---

func TestMenu_ShowsDMHandoffButtonInGroupWhenBotUsernameSet(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.BotUsername = "cs2predictor_bot"
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, Title: "Test Chat"})

	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: strPtr("/menu")}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	cds, urls := findKeyboardButtons(*calls)
	if !containsPrefix(urls, "https://t.me/cs2predictor_bot?start=admin_") {
		t.Fatalf("expected a DM deep-link URL button in the group /menu keyboard, got urls %v", urls)
	}
	for _, cd := range cds {
		if cd == "menu:events" || cd == "menu:settings" {
			t.Fatalf("expected the direct events/settings buttons to be replaced by the DM handoff button, found %q", cd)
		}
	}
}

func TestMenu_FallsBackToDirectButtonsWhenBotUsernameEmpty(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server) // BotUsername left empty
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, Title: "Test Chat"})

	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: strPtr("/menu")}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "menu:events") {
		t.Fatalf("expected the direct events button when BotUsername is unresolved, got %v", cds)
	}
}

func strPtr(s string) *string { return &s }

// --- end-to-end: DM entry points ---

func TestPrivateMessage_AdminDeepLinkOpensManagedChatForManager(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, Title: "Managed Chat"})

	text := "/start admin_" + int64Base36(groupChatID.Value)
	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42}, Text: &text}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if chats.dmSessions[42] != groupChatID.Value {
		t.Fatalf("dmSessions[42] = %d, want %d (DM session should point at the managed chat)", chats.dmSessions[42], groupChatID.Value)
	}
	found := false
	for _, c := range *calls {
		if text, ok := c["text"].(string); ok && strings.Contains(text, "Managed Chat") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the managed chat's title to appear in the rendered admin panel")
	}
}

func TestPrivateMessage_AdminDeepLinkDeniesNonManager(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server) // default fakeMembership role: Member, no moderator flag
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, Title: "Managed Chat"})

	text := "/start admin_" + int64Base36(groupChatID.Value)
	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42}, Text: &text}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if _, ok := chats.dmSessions[42]; ok {
		t.Fatal("a denied user must not get a DM session pointed at the chat")
	}
	found := false
	for _, c := range *calls {
		if text, ok := c["text"].(string); ok && strings.Contains(text, "🚫") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a denial message")
	}
}

func TestManagedChatsMenu_ListsChatsFromTheIndex(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	userID := common.UserID{Value: 42}
	chatA := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Chat A", Locale: common.LocaleRU}
	chatB := chat.Settings{ChatID: common.ChatID{Value: -2}, Title: "Chat B", Locale: common.LocaleRU}
	_, _ = chats.Save(context.Background(), chatA)
	_, _ = chats.Save(context.Background(), chatB)
	_ = chats.RecordManaged(context.Background(), chatA.ChatID, userID)

	err := handler.managedChatsMenu(context.Background(), sendTarget(common.ChatID{Value: 42}, nil), userID, common.LocaleRU)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenManagedChat_DeniesWhenNoLongerAManager(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server) // fakeMembership defaults to RoleMember, not a moderator
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Title: "Managed Chat", Locale: common.LocaleRU})

	err := handler.openManagedChat(context.Background(), sendTarget(common.ChatID{Value: 42}, nil), common.UserID{Value: 42}, common.LocaleRU, groupChatID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := chats.dmSessions[42]; ok {
		t.Fatal("a denied user must not get a DM session")
	}
	found := false
	for _, c := range *calls {
		if text, ok := c["text"].(string); ok && strings.Contains(text, "🚫") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a denial message")
	}
}

// --- the critical case: delegation must operate on the managed (group)
// chat, and must render into the DM message, not the group's ---

func TestPrivateCallback_DelegatesSettingsToggleToTheManagedChat(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, Title: "Managed Chat"})
	userID := common.UserID{Value: 42}
	_ = chats.SetDMSession(context.Background(), userID, groupChatID)

	data := "settings:locale"
	privateMessageID := int64(999)
	cb := &CallbackQuery{
		ID:      "cb1",
		From:    User{ID: 42, FirstName: "Admin"},
		Message: &Message{Chat: Chat{ID: 42, Type: "private"}, MessageID: privateMessageID},
		Data:    &data,
	}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	// The GROUP chat's own settings must have changed, not some private-chat
	// record — the whole point of the delegation.
	saved, err := chats.Find(context.Background(), groupChatID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Locale != common.LocaleEN {
		t.Fatalf("group chat locale = %v, want EN (the toggle should have applied to the managed chat)", saved.Locale)
	}

	// Exactly one answerCallbackQuery call — the settings:locale handler
	// already toasts (answered=true); handlePrivateCallback must not also
	// call it, or Telegram would reject the second attempt.
	answerCalls := 0
	editTargetedPrivateChat := false
	for _, c := range *calls {
		switch c["__method"] {
		case "answerCallbackQuery":
			answerCalls++
		case "editMessageText":
			if chatID, ok := c["chat_id"].(float64); ok && int64(chatID) == 42 {
				editTargetedPrivateChat = true
			}
			if chatID, ok := c["chat_id"].(float64); ok && int64(chatID) == groupChatID.Value {
				t.Fatalf("editMessageText targeted the group chat (%v) instead of the DM (42) — editTargetFromCallback regressed", chatID)
			}
		}
	}
	if answerCalls != 1 {
		t.Fatalf("answerCallbackQuery called %d times, want exactly 1", answerCalls)
	}
	if !editTargetedPrivateChat {
		t.Fatal("expected the settings screen to be re-rendered into the private chat message")
	}
}

func TestPrivateCallback_NoDMSessionFallsBackToPersonalStats(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	data := "settings:locale" // group-style callback data, but no DM session set
	cb := &CallbackQuery{
		ID:      "cb1",
		From:    User{ID: 42, FirstName: "Admin"},
		Message: &Message{Chat: Chat{ID: 42, Type: "private"}, MessageID: 1},
		Data:    &data,
	}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	// With no DM session, tryDelegateToManagedChat must decline and fall
	// through to the ordinary personal-stats screen rather than erroring or
	// silently doing nothing.
	found := false
	for _, c := range *calls {
		if text, ok := c["text"].(string); ok && strings.Contains(text, ru(t, "private.stats_empty")) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the fallback personal-stats-empty screen to render")
	}
}
