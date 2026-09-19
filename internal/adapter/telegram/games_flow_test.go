package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// dota2ToggleCallback is the tap on Dota 2's toggle, made by manager 1 —
// every test here is about what the toggle does, not about who tapped it
// or which game it was.
func dota2ToggleCallback() *CallbackQuery {
	data := "settings:games:toggle:" + string(competition.GameDota2)
	return &CallbackQuery{
		ID: "cb-games", From: User{ID: 1, FirstName: "Manager"},
		Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data,
	}
}

// Enabling changes what everyone in the room sees, so the room is told —
// a settings change made in somebody's DM must not be invisible in the chat
// it applies to.
func TestGames_EnablingAnnouncesItInTheChat(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	if err := handler.handleCallback(context.Background(), dota2ToggleCallback()); err != nil {
		t.Fatal(err)
	}

	updated, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.GameEnabled(competition.GameDota2) {
		t.Fatal("expected the game to be enabled right away")
	}
	var announced bool
	for _, c := range *calls {
		text, _ := c["text"].(string)
		if c["__method"] == "sendMessage" && strings.Contains(text, ru(t, "game.dota2")) &&
			strings.Contains(text, strings.SplitN(ru(t, "games.enabled_announcement"), "{0}", 2)[0]) {
			announced = true
		}
	}
	if !announced {
		t.Fatal("expected the chat to be told the game was switched on")
	}
}

// Disabling waits for a second manager. One tap otherwise silences every
// tournament of that game at once, and nothing in the chat says why the
// polls stopped.
func TestGames_DisablingWaitsForASecondManager(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(context.Background(), chatID, []competition.GameCode{competition.GameCS2, competition.GameDota2}); err != nil {
		t.Fatal(err)
	}
	// Two managers, both reachable by DM: the requester cannot approve alone.
	admins := []common.UserID{{Value: 1}, {Value: 2}}
	handler.Authorization = chat.NewAuthorizationService(chats, &fakeAdminMembership{admins: admins})
	for _, admin := range admins {
		if err := chats.SetDMReachable(context.Background(), admin, true); err != nil {
			t.Fatal(err)
		}
	}
	handler.Outbox = &fakeOutbox{}
	approvals := newFakePendingApprovals()
	handler.PendingApprovals = approvals

	if err := handler.handleCallback(context.Background(), dota2ToggleCallback()); err != nil {
		t.Fatal(err)
	}

	stillOn, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !stillOn.GameEnabled(competition.GameDota2) {
		t.Fatal("the game must stay enabled until somebody approves the request")
	}
	if len(approvals.items) != 1 {
		t.Fatalf("expected one pending approval, got %d", len(approvals.items))
	}
	var pending chat.PendingApproval
	for _, p := range approvals.items {
		pending = p
	}
	if pending.Kind != chat.ApprovalDisableGame || pending.Subject != string(competition.GameDota2) {
		t.Fatalf("pending approval = %+v", pending)
	}
	if pending.SelfConfirmable {
		t.Fatal("with another reachable manager around, the requester must not be able to confirm alone")
	}

	// The requester's own confirmation is refused...
	confirm := "unsubok:" + pending.ID.String()
	selfCB := &CallbackQuery{ID: "cb-self", From: User{ID: 1, FirstName: "Manager"}, Message: &Message{Chat: Chat{ID: 1, Type: "private"}}, Data: &confirm}
	if err := handler.handleCallback(context.Background(), selfCB); err != nil {
		t.Fatal(err)
	}
	if after, _ := chats.Find(context.Background(), chatID); !after.GameEnabled(competition.GameDota2) {
		t.Fatal("the requester approving their own request must change nothing")
	}

	// ...and the other manager's is honoured.
	*calls = nil
	otherCB := &CallbackQuery{ID: "cb-other", From: User{ID: 2, FirstName: "Second"}, Message: &Message{Chat: Chat{ID: 2, Type: "private"}}, Data: &confirm}
	if err := handler.handleCallback(context.Background(), otherCB); err != nil {
		t.Fatal(err)
	}
	after, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if after.GameEnabled(competition.GameDota2) {
		t.Fatal("expected the game to be switched off once a second manager approved")
	}
	if after.GameEnabled(competition.GameCS2) != true {
		t.Fatal("switching one game off must not disturb the others")
	}
	var announced bool
	for _, c := range *calls {
		if text, _ := c["text"].(string); c["__method"] == "sendMessage" &&
			strings.Contains(text, strings.SplitN(ru(t, "games.disabled_announcement"), "{0}", 2)[0]) {
			announced = true
		}
	}
	if !announced {
		t.Fatal("expected the chat to be told the game was switched off")
	}
}

// With nobody else to ask, the request degrades to a one-person
// confirmation rather than becoming impossible to complete.
func TestGames_DisablingIsSelfConfirmableWithNoOtherManager(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(context.Background(), chatID, []competition.GameCode{competition.GameDota2}); err != nil {
		t.Fatal(err)
	}
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	approvals := newFakePendingApprovals()
	handler.PendingApprovals = approvals

	if err := handler.handleCallback(context.Background(), dota2ToggleCallback()); err != nil {
		t.Fatal(err)
	}
	var pending chat.PendingApproval
	for _, p := range approvals.items {
		pending = p
	}
	if !pending.SelfConfirmable {
		t.Fatal("with no other manager to ask, the requester's own confirmation must be enough")
	}

	confirm := "unsubok:" + pending.ID.String()
	cb := &CallbackQuery{ID: "cb-self", From: User{ID: 1, FirstName: "Manager"}, Message: &Message{Chat: Chat{ID: 1, Type: "private"}}, Data: &confirm}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	after, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if after.GameEnabled(competition.GameDota2) {
		t.Fatal("expected the game to be switched off after the self-confirmation")
	}
}

// Rejecting a game request must read as a game request: the shared
// approval path would otherwise reuse the tournament wording, with an
// empty name where the tournament would have been.
func TestGames_RejectingADisableRequestUsesGameWording(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(context.Background(), chatID, []competition.GameCode{competition.GameDota2}); err != nil {
		t.Fatal(err)
	}
	admins := []common.UserID{{Value: 1}, {Value: 2}}
	handler.Authorization = chat.NewAuthorizationService(chats, &fakeAdminMembership{admins: admins})
	for _, admin := range admins {
		if err := chats.SetDMReachable(context.Background(), admin, true); err != nil {
			t.Fatal(err)
		}
	}
	handler.Outbox = &fakeOutbox{}
	approvals := newFakePendingApprovals()
	handler.PendingApprovals = approvals

	if err := handler.handleCallback(context.Background(), dota2ToggleCallback()); err != nil {
		t.Fatal(err)
	}
	var pending chat.PendingApproval
	for _, p := range approvals.items {
		pending = p
	}

	*calls = nil
	reject := "unsubreject:" + pending.ID.String()
	cb := &CallbackQuery{ID: "cb-reject", From: User{ID: 2, FirstName: "Second"}, Message: &Message{Chat: Chat{ID: 2, Type: "private"}}, Data: &reject}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	still, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !still.GameEnabled(competition.GameDota2) {
		t.Fatal("a rejected request must leave the game switched on")
	}
	gameName := ru(t, "game.dota2")
	var named bool
	for _, c := range *calls {
		if text, _ := c["text"].(string); strings.Contains(text, gameName) {
			named = true
		}
		if text, _ := c["text"].(string); strings.Contains(text, "<b></b>") {
			t.Fatalf("the rejection left an empty name where the subject belongs: %q", text)
		}
	}
	if !named {
		t.Fatal("expected the rejection to name the game")
	}
}
