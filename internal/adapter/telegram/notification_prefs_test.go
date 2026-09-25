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

	prefs, err := chats.NotifySettings(context.Background(), common.ScopeUser, 42)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range common.PersonalNotificationKinds {
		if prefs[string(kind)] {
			t.Fatalf("%s is on, want everything off until asked for", kind)
		}
	}
	_, labels := findKeyboardButtons(*calls)
	for _, label := range labels {
		if strings.Contains(label, handler.Texts.Get("notify.recaps", common.LocaleRU)) &&
			!strings.HasPrefix(label, "❌ ") {
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
	prefs, _ := chats.NotifySettings(ctx, common.ScopeUser, userID.Value)
	if !prefs[string(common.NotifyResultRecaps)] {
		t.Fatal("the recaps opt-in did not take effect")
	}
	if prefs[string(common.NotifyPollReminders)] {
		t.Fatal("toggling one kind switched on the other as well")
	}

	// The same button turns it back off — a person who regrets it must not
	// have to go looking for where.
	if err := handler.handlePrivateCallback(ctx, notifyCallback("notify:toggle:"+string(common.NotifyResultRecaps))); err != nil {
		t.Fatal(err)
	}
	prefs, _ = chats.NotifySettings(ctx, common.ScopeUser, userID.Value)
	if prefs[string(common.NotifyResultRecaps)] {
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

// The chat's own switchboard: the same rule as the DM one, for the room
// rather than for a person.
func TestChatNotifications_DefaultToOffAndToggleIndependently(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	ctx := context.Background()
	chatID := common.ChatID{Value: -4242}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	if err := handler.chatNotificationsMenu(ctx, replyTarget{chatID: common.ChatID{Value: 42}}, chatID, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	cds, labels := findKeyboardButtons(*calls)
	for _, kind := range common.ChatNotificationKinds {
		if !slices.Contains(cds, "settings:notify:"+string(kind)) {
			t.Fatalf("no switch for %s, so it could never be turned off: %v", kind, cds)
		}
	}
	for _, label := range labels {
		if !strings.HasPrefix(label, "❌ ") {
			t.Fatalf("a chat switch reads as on before anybody asked: %q", label)
		}
	}

	if err := handler.toggleChatNotification(ctx, replyTarget{chatID: common.ChatID{Value: 42}}, chatID, common.LocaleRU, string(common.ChatNotifyDigests)); err != nil {
		t.Fatal(err)
	}
	prefs, _ := chats.NotifySettings(ctx, common.ScopeChat, chatID.Value)
	if !prefs[string(common.ChatNotifyDigests)] {
		t.Fatal("the digest switch did not take effect")
	}
	for _, kind := range common.ChatNotificationKinds {
		if kind != common.ChatNotifyDigests && prefs[string(kind)] {
			t.Fatalf("turning digests on also turned %s on", kind)
		}
	}

	// The toggled switch's own button must now read as on, and every other
	// switch must still read as off — the state has to be visible per
	// button, not just true in the database.
	_, labels = findKeyboardButtons(*calls)
	digestsLabel := handler.Texts.Get(chatNotifyLabelKey(common.ChatNotifyDigests), common.LocaleRU)
	for _, label := range labels {
		wantOn := strings.Contains(label, digestsLabel)
		if wantOn && !strings.HasPrefix(label, "✅ ") {
			t.Fatalf("digests switch does not read as on: %q", label)
		}
		if !wantOn && !strings.HasPrefix(label, "❌ ") {
			t.Fatalf("an untouched chat switch does not read as off: %q", label)
		}
	}
}

// Every operator alert must be reachable from the alerts screen, or it is
// an alert nobody can silence.
func TestOperatorAlerts_EveryKindHasASwitchAndStartsOff(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	if err := handler.alertsMenu(context.Background(), replyTarget{chatID: common.ChatID{Value: 42}}, common.ChatID{Value: 42}, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	cds, labels := findKeyboardButtons(*calls)
	for _, kind := range common.AdminAlertKinds {
		if !slices.Contains(cds, "alerts:toggle:"+string(kind)) {
			t.Fatalf("no switch for the %s alert: %v", kind, cds)
		}
	}
	for _, label := range labels {
		if !strings.HasPrefix(label, "❌ ") {
			t.Fatalf("an alert reads as on before anybody asked: %q", label)
		}
	}
}

// Through the real callback route, not the screen function: an exact route
// is handed the whole string it matched, so a handler that treats the
// remainder as a notification kind reads "settings:notify" as one and
// answers the group with "could not do that". The direct-call test above
// could never have caught it.
func TestChatNotifications_OpenAndToggleFromTheGroupSettingsMenu(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	ctx := context.Background()
	chatID := common.ChatID{Value: -100}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	tap := func(data string) {
		t.Helper()
		cb := &CallbackQuery{ID: "cb", From: User{ID: 7, FirstName: "Admin"},
			Message: &Message{MessageID: 1, Chat: Chat{ID: chatID.Value, Type: "group"}}, Data: &data}
		if err := handler.handleCallback(ctx, cb); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(lastText(*calls), ru(t, "error.generic")) {
			t.Fatalf("%q answered with the generic failure", data)
		}
	}

	tap("settings:notify")
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "settings:notify:"+string(common.ChatNotifyDigests)) {
		t.Fatalf("the notifications screen did not render its switches: %v", cds)
	}

	tap("settings:notify:" + string(common.ChatNotifyDigests))
	prefs, _ := chats.NotifySettings(ctx, common.ScopeChat, chatID.Value)
	if !prefs[string(common.ChatNotifyDigests)] {
		t.Fatal("tapping the switch through the route did not store anything")
	}
}

// The scoring rules belong on the screen that shows the scores, and
// nowhere twice. Somebody looking at a points table and wondering how the
// points were arrived at should not have to go up a menu to a command
// reference to find out.
func TestRules_LiveOnTheBoardThatShowsThePoints(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	ctx := context.Background()
	chatID := common.ChatID{Value: -100}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	data := "stats:all"
	cb := &CallbackQuery{ID: "cb", From: User{ID: 7, FirstName: "Admin"},
		Message: &Message{MessageID: 1, Chat: Chat{ID: chatID.Value, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(ctx, cb); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	var rules int
	for _, cd := range cds {
		if strings.HasPrefix(cd, "menu:rules") {
			rules++
		}
	}
	if rules != 1 {
		t.Fatalf("the leaderboard offers the rules %d times, want exactly one: %v", rules, cds)
	}
}
