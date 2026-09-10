package telegram

import (
	"context"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// TestHandle_MyChatMemberKickedMarksChatInactive is the regression test for
// the confirmed audit finding: the webhook subscribes to "my_chat_member"
// (see ansible/roles/register_webhook), but until this fix nothing in the
// Update struct or dispatch even parsed it — the bot had no way to learn it
// had been removed from a group and would keep listing that chat as
// manageable/active forever.
func TestHandle_MyChatMemberKickedMarksChatInactive(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Title: "Our Group", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	update := Update{UpdateID: 1, MyChatMember: &ChatMemberUpdated{
		Chat:          Chat{ID: chatID.Value, Type: "group"},
		NewChatMember: ChatMember{Status: "kicked"},
	}}
	if err := handler.Handle(context.Background(), update); err != nil {
		t.Fatal(err)
	}

	settings, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if settings == nil || settings.Active {
		t.Fatalf("expected the chat to be marked inactive after being kicked, got %+v", settings)
	}
}

// TestHandle_MyChatMemberReAddedMarksChatActiveAgain covers the reverse
// transition: the bot is re-added (or re-promoted) after having been
// removed, and the chat becomes manageable again.
func TestHandle_MyChatMemberReAddedMarksChatActiveAgain(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Title: "Our Group", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: false}); err != nil {
		t.Fatal(err)
	}

	update := Update{UpdateID: 1, MyChatMember: &ChatMemberUpdated{
		Chat:          Chat{ID: chatID.Value, Type: "group"},
		NewChatMember: ChatMember{Status: "member"},
	}}
	if err := handler.Handle(context.Background(), update); err != nil {
		t.Fatal(err)
	}

	settings, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if settings == nil || !settings.Active {
		t.Fatalf("expected the chat to be marked active again after being re-added, got %+v", settings)
	}
}

// A my_chat_member update for a chat the bot never exchanged a message in
// (Find returns nil) must be a harmless no-op, not an error.
func TestHandle_MyChatMemberForUnknownChatIsANoOp(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)

	update := Update{UpdateID: 1, MyChatMember: &ChatMemberUpdated{
		Chat:          Chat{ID: -999, Type: "group"},
		NewChatMember: ChatMember{Status: "kicked"},
	}}
	if err := handler.Handle(context.Background(), update); err != nil {
		t.Fatal(err)
	}
}
