package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

func TestHelpCommand_GroupRendersReferenceInLocale(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	text := "/help"
	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: &text}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	// A group is shown the commands that work in a group; the private-chat
	// half of the old combined page was always about somewhere else.
	if !strings.Contains(body, ru(t, "help.group")) {
		t.Fatalf("expected the RU group help text, got %q", body)
	}
}

func TestHelpCommand_PrivateWorksWithNoActiveSession(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv) // no chat/session set up at all

	text := "/help"
	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 1}, Text: &text}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, ru(t, "help.private")) {
		t.Fatalf("expected the RU private help text, got %q", body)
	}
}

func TestMenuHelpCallback_RendersAndBacksToMainMenu(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	data := "menu:help"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Any"}, Message: &Message{MessageID: 1, Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	var edited map[string]any
	for _, c := range *calls {
		if c["__method"] == "editMessageText" {
			edited = c
		}
	}
	if edited == nil {
		t.Fatal("expected an editMessageText call for menu:help")
	}
	text, _ := edited["text"].(string)
	if !strings.Contains(text, ru(t, "help.group")) {
		t.Fatalf("expected the group help text, got %q", text)
	}
	kb, _ := edited["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	// Two rows: the rules shortcut — the question people ask right after
	// "what are the commands" — and the back button beneath it.
	if len(rows) != 2 {
		t.Fatalf("expected a rules row and a back row, got %+v", rows)
	}
	row, _ := rows[len(rows)-1].([]any)
	btn, _ := row[0].(map[string]any)
	if btn["callback_data"] != "menu:main" {
		t.Fatalf("expected the back button to point at menu:main, got %+v", btn)
	}
}

func TestPstatsHelpCallback_RendersAndBacksToPersonalMenu(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)

	data := "pstats:help"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Any"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	var edited map[string]any
	for _, c := range *calls {
		if c["__method"] == "editMessageText" {
			edited = c
		}
	}
	if edited == nil {
		t.Fatal("expected an editMessageText call for pstats:help")
	}
	// The back button is the last row on every screen; the rules shortcut
	// sits above it.
	kb, _ := edited["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	lastRow, _ := rows[len(rows)-1].([]any)
	btn, _ := lastRow[0].(map[string]any)
	if btn["callback_data"] != "pstats:menu" {
		t.Fatalf("expected the back button to point at pstats:menu, got %+v", btn)
	}
}

// TestPstatsHelpCallback_WithHubAccessBacksToHub covers the same route for
// someone who manages a chat: modeHub offers Help itself in that case (see
// hub.go), not privateStatsMenu, so the way out has to be the hub too — this
// used to be hardcoded to pstats:menu regardless, the same bug Settings had.
func TestPstatsHelpCallback_WithHubAccessBacksToHub(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.RecordManaged(context.Background(), chatID, common.UserID{Value: 1}); err != nil {
		t.Fatal(err)
	}

	data := "pstats:help"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Any"}, Message: &Message{MessageID: 1, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	var edited map[string]any
	for _, c := range *calls {
		if c["__method"] == "editMessageText" {
			edited = c
		}
	}
	if edited == nil {
		t.Fatal("expected an editMessageText call for pstats:help")
	}
	kb, _ := edited["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	lastRow, _ := rows[len(rows)-1].([]any)
	btn, _ := lastRow[0].(map[string]any)
	if btn["callback_data"] != "hub:root" {
		t.Fatalf("expected the back button to point at hub:root, got %+v", btn)
	}
}
