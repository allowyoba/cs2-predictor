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
	if !strings.Contains(body, ru(t, "help.text")) {
		t.Fatalf("expected the RU help text, got %q", body)
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
	if !strings.Contains(body, ru(t, "help.text")) {
		t.Fatalf("expected the RU help text, got %q", body)
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
	if !strings.Contains(text, ru(t, "help.text")) {
		t.Fatalf("expected help text, got %q", text)
	}
	kb, _ := edited["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected exactly one back button row, got %+v", rows)
	}
	row, _ := rows[0].([]any)
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
	kb, _ := edited["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	row, _ := rows[0].([]any)
	btn, _ := row[0].(map[string]any)
	if btn["callback_data"] != "pstats:menu" {
		t.Fatalf("expected the back button to point at pstats:menu, got %+v", btn)
	}
}
