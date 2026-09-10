package telegram

import (
	"context"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// TestHandleMessage_GroupMigrationRenamesChatInsteadOfDuplicating is the
// regression test for the "same group appears twice in 'chats you manage'"
// report: Telegram permanently renumbers a group's id when it's upgraded to
// a supergroup, announced by a migrate_to_chat_id service message. Before
// MigrateChatID existed, the bot's next message from the new id would just
// register it as an unrelated chat, leaving the old id's row (and its
// chat_manager_seen entry) behind — the same group listed twice under two
// different ids.
func TestHandleMessage_GroupMigrationRenamesChatInsteadOfDuplicating(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	oldChatID := int64(-100111)
	newChatID := int64(-100222222222)
	title := "Our Group"
	admin := common.UserID{Value: 1}

	// The group exists under the old (basic-group) id, and this admin has
	// been recorded as a manager of it (the same side effect requireManager
	// has on every real admin action — seeded directly here rather than via
	// a full command, since only the recorded fact matters for this test).
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: oldChatID}, Title: title, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.RecordManaged(context.Background(), common.ChatID{Value: oldChatID}, admin); err != nil {
		t.Fatal(err)
	}

	// Telegram's migration service message arrives.
	migrateMsg := &Message{MessageID: 2, Chat: Chat{ID: oldChatID, Type: "group", Title: &title}, MigrateToChatID: &newChatID}
	if err := handler.handleMessage(context.Background(), migrateMsg); err != nil {
		t.Fatal(err)
	}

	// The old id must be gone, the new id must hold the same chat.
	oldSettings, err := chats.Find(context.Background(), common.ChatID{Value: oldChatID})
	if err != nil {
		t.Fatal(err)
	}
	if oldSettings != nil {
		t.Fatalf("expected the old chat id to no longer exist, got %+v", oldSettings)
	}
	newSettings, err := chats.Find(context.Background(), common.ChatID{Value: newChatID})
	if err != nil {
		t.Fatal(err)
	}
	if newSettings == nil || newSettings.Title != title {
		t.Fatalf("expected the new chat id to hold the migrated chat, got %+v", newSettings)
	}

	// The admin's "chats you manage" list must show it exactly once, under
	// the new id — not twice, and not under the stale old id.
	managed, err := chats.ManagedChats(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 {
		t.Fatalf("expected exactly one managed chat after migration, got %d: %+v", len(managed), managed)
	}
	if managed[0].ChatID.Value != newChatID {
		t.Fatalf("expected the managed chat to be listed under the new id, got %+v", managed[0])
	}

	// A message arriving under the new id afterward (the normal case once
	// migration has happened) must be treated as the same chat, not create
	// yet another row.
	menuText := "/menu"
	postMigrateMsg := &Message{MessageID: 3, Chat: Chat{ID: newChatID, Type: "group", Title: &title}, Text: &menuText, From: &User{ID: admin.Value, FirstName: "Admin"}}
	if err := handler.handleMessage(context.Background(), postMigrateMsg); err != nil {
		t.Fatal(err)
	}
	managed, err = chats.ManagedChats(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 {
		t.Fatalf("expected still exactly one managed chat, got %d: %+v", len(managed), managed)
	}
}
