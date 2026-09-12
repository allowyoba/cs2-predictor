package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

func notifyCallback(data string) *CallbackQuery {
	return &CallbackQuery{ID: "cb", From: User{ID: 42, FirstName: "Alex"},
		Message: &Message{MessageID: 3, Chat: Chat{ID: 42, Type: "private"}}, Data: &data}
}

// Nothing may be enabled until the person turns it on themselves: an
// unsolicited private message from a bot is spam.
func TestNotifications_DefaultToOff(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)

	if err := handler.handlePrivateCallback(context.Background(), notifyCallback("notify:menu")); err != nil {
		t.Fatal(err)
	}

	prefs, err := chats.NotificationPrefs(context.Background(), common.UserID{Value: 42})
	if err != nil {
		t.Fatal(err)
	}
	if prefs.ResultRecaps || prefs.PollReminders {
		t.Fatalf("prefs = %+v, want everything off until asked for", prefs)
	}
	_, labels := findKeyboardButtons(*calls)
	for _, label := range labels {
		if strings.Contains(label, handler.Texts.Get("notify.recaps", common.LocaleRU)) &&
			!strings.Contains(label, handler.Texts.Get("notify.disabled", common.LocaleRU)) {
			t.Fatalf("recaps switch does not read as off: %q", label)
		}
	}
}

func TestNotifications_ToggleTurnsOneKindOnAndBackOff(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	ctx := context.Background()
	userID := common.UserID{Value: 42}

	if err := handler.handlePrivateCallback(ctx, notifyCallback("notify:toggle:"+string(common.NotifyResultRecaps))); err != nil {
		t.Fatal(err)
	}
	prefs, _ := chats.NotificationPrefs(ctx, userID)
	if !prefs.ResultRecaps {
		t.Fatal("the recaps opt-in did not take effect")
	}
	if prefs.PollReminders {
		t.Fatal("toggling one kind switched on the other as well")
	}

	// The same button turns it back off — a person who regrets it must not
	// have to go looking for where.
	if err := handler.handlePrivateCallback(ctx, notifyCallback("notify:toggle:"+string(common.NotifyResultRecaps))); err != nil {
		t.Fatal(err)
	}
	prefs, _ = chats.NotificationPrefs(ctx, userID)
	if prefs.ResultRecaps {
		t.Fatal("the opt-in could not be reversed")
	}
}

// An unknown notification kind is a validationError internally, but by the
// time it reaches the caller it must already be handled — the same
// handleCommandError treatment every callback error gets — not leaked out
// raw: the person just gets a generic "couldn't complete that" reply.
func TestNotifications_UnknownKindIsRejected(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	err := handler.handlePrivateCallback(context.Background(), notifyCallback("notify:toggle:everything"))
	if err != nil {
		t.Fatalf("err = %v, want nil — a validationError must already be handled by handleCommandError", err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "error.generic")) {
		t.Fatalf("expected the generic error reply, got %q", lastText(*calls))
	}
}

func TestPrivateStatsMenu_OffersNotifications(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	if err := handler.handlePrivateCallback(context.Background(), notifyCallback("pstats:menu")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "pstats:settings") {
		t.Fatalf("expected a settings button in the DM menu, got %v", cds)
	}

	if err := handler.handlePrivateCallback(context.Background(), notifyCallback("pstats:settings")); err != nil {
		t.Fatal(err)
	}
	cds, _ = findKeyboardButtons(*calls)
	if !slices.Contains(cds, "notify:menu") {
		t.Fatalf("expected a notifications button in the settings menu, got %v", cds)
	}
}

var _ chat.NotificationPrefsRepository = (*fakeChats)(nil)
