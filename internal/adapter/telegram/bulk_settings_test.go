package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// perChatMembership grants administrator rights only in the chats named,
// so a manager who has since been demoted in one of their chats can be
// modelled — the case a bulk write has to survive.
type perChatMembership struct {
	admins map[int64][]common.UserID
}

func (m perChatMembership) Role(_ context.Context, chatID common.ChatID, userID common.UserID) (chat.MemberRole, error) {
	if slices.Contains(m.admins[chatID.Value], userID) {
		return chat.RoleAdministrator, nil
	}
	return chat.RoleMember, nil
}

func (m perChatMembership) Administrators(_ context.Context, chatID common.ChatID) ([]common.UserID, error) {
	return m.admins[chatID.Value], nil
}

// setupBulkTest builds a manager with three chats: the source they are
// looking at, another they still manage, and one they were demoted in.
func setupBulkTest(t *testing.T) (*UpdateHandler, *fakeChats, chat.Settings, *[]map[string]any) {
	t.Helper()
	server, calls := newRecordingServer(t)
	t.Cleanup(server.Close)
	handler, chats := newTestHandler(t, server)
	admin := common.UserID{Value: 1}

	source := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Source", Locale: common.LocaleRU, Timezone: "Europe/Moscow", Active: true}
	other := chat.Settings{ChatID: common.ChatID{Value: -2}, Title: "Other", Locale: common.LocaleEN, Timezone: "UTC", Active: true}
	demoted := chat.Settings{ChatID: common.ChatID{Value: -3}, Title: "Demoted", Locale: common.LocaleEN, Timezone: "UTC", Active: true}
	ctx := context.Background()
	for _, s := range []chat.Settings{source, other, demoted} {
		if _, err := chats.Save(ctx, s); err != nil {
			t.Fatal(err)
		}
		_ = chats.RecordManaged(ctx, s.ChatID, admin)
	}

	handler.Authorization = chat.NewAuthorizationService(chats, perChatMembership{admins: map[int64][]common.UserID{
		source.ChatID.Value: {admin},
		other.ChatID.Value:  {admin},
		// none for the demoted chat: they are only in the managed index
		// because they used to have rights there.
	}})
	handler.AdminActions = &fakeAdminActions{}
	return handler, chats, source, calls
}

func bulkCallback(data string, private bool) *CallbackQuery {
	chatType, chatID := "supergroup", int64(-1)
	if private {
		chatType, chatID = "private", int64(1)
	}
	return &CallbackQuery{ID: "cb", From: User{ID: 1, FirstName: "Admin"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: chatID, Type: chatType}}, Data: &data}
}

// Copying settings between chats is a DM-panel idea: in a group it would
// reach into chats nobody in that room can see.
func TestSettingsView_OffersBulkApplyOnlyInDM(t *testing.T) {
	handler, _, source, calls := setupBulkTest(t)

	for _, private := range []bool{false, true} {
		*calls = nil
		data := "menu:settings"
		if _, err := handler.routeCallback(context.Background(), bulkCallback(data, private), source, data); err != nil {
			t.Fatal(err)
		}
		cds, _ := findKeyboardButtons(*calls)
		if got := slices.Contains(cds, "bulk:menu"); got != private {
			t.Fatalf("private=%v: bulk button present = %v, want %v (buttons: %v)", private, got, private, cds)
		}
	}
}

func TestBulkApply_WritesEveryManagedChatButTheSource(t *testing.T) {
	handler, chats, source, _ := setupBulkTest(t)

	data := "bulk:do:timezone"
	if _, err := handler.routeCallback(context.Background(), bulkCallback(data, true), source, data); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	other, _ := chats.Find(ctx, common.ChatID{Value: -2})
	if other.Timezone != source.Timezone {
		t.Fatalf("other chat timezone = %q, want the source's %q", other.Timezone, source.Timezone)
	}
	// Only the named setting travels: the other chat keeps its own language.
	if other.Locale != common.LocaleEN {
		t.Fatalf("other chat locale = %q, want it untouched (en)", other.Locale)
	}
	// The chat they were demoted in must be left alone.
	demoted, _ := chats.Find(ctx, common.ChatID{Value: -3})
	if demoted.Timezone != "UTC" {
		t.Fatalf("a chat the user no longer manages was written to (timezone = %q)", demoted.Timezone)
	}
}

func TestBulkApply_ReportsWhatWasAppliedAndWhatWasSkipped(t *testing.T) {
	handler, _, source, calls := setupBulkTest(t)

	data := "bulk:do:timezone"
	if _, err := handler.routeCallback(context.Background(), bulkCallback(data, true), source, data); err != nil {
		t.Fatal(err)
	}

	text := lastText(*calls)
	if !strings.Contains(text, handler.Texts.Get("bulk.applied", common.LocaleRU, bold(handler.Texts.Get(historyKindKey("timezone"), common.LocaleRU)), 1)) {
		t.Fatalf("expected a report of 1 applied chat, got %q", text)
	}
	if !strings.Contains(text, handler.Texts.Get("bulk.skipped", common.LocaleRU, 1)) {
		t.Fatalf("expected the demoted chat to be reported as skipped, got %q", text)
	}
}

// A bulk write is the one action here whose blast radius is larger than
// one chat, so it must name the chats it will touch before touching them.
func TestBulkConfirm_NamesTheTargetChatsBeforeApplying(t *testing.T) {
	handler, chats, source, calls := setupBulkTest(t)

	data := "bulk:ask:locale"
	if _, err := handler.routeCallback(context.Background(), bulkCallback(data, true), source, data); err != nil {
		t.Fatal(err)
	}

	text := lastText(*calls)
	if !strings.Contains(text, "Other") || !strings.Contains(text, "Demoted") {
		t.Fatalf("confirmation screen must list the target chats, got %q", text)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "bulk:do:locale") {
		t.Fatalf("expected an apply button, got %v", cds)
	}
	// Nothing may have been written yet.
	other, _ := chats.Find(context.Background(), common.ChatID{Value: -2})
	if other.Locale != common.LocaleEN {
		t.Fatal("the confirmation screen wrote the setting before it was confirmed")
	}
}

func TestBulkApply_RecordsEachChangeInTheTargetChatsOwnHistory(t *testing.T) {
	handler, _, source, _ := setupBulkTest(t)
	log := handler.AdminActions.(*fakeAdminActions)

	data := "bulk:do:timezone"
	if _, err := handler.routeCallback(context.Background(), bulkCallback(data, true), source, data); err != nil {
		t.Fatal(err)
	}

	if len(log.recorded) != 1 {
		t.Fatalf("recorded %d history entries, want 1 (one per chat actually changed)", len(log.recorded))
	}
	got := log.recorded[0]
	if got.ChatID.Value != -2 || got.Kind != "timezone" || got.Detail != source.Timezone {
		t.Fatalf("history entry = %+v, want the target chat's own timezone change", got)
	}
}
