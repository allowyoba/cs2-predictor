package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

type fakeAdminActions struct {
	recorded  []chat.AdminAction
	recordErr error
}

func (f *fakeAdminActions) Record(_ context.Context, action chat.AdminAction) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, action)
	return nil
}

func (f *fakeAdminActions) Recent(_ context.Context, chatID common.ChatID, limit int) ([]chat.AdminAction, error) {
	var out []chat.AdminAction
	for i := len(f.recorded) - 1; i >= 0 && len(out) < limit; i-- {
		if f.recorded[i].ChatID == chatID {
			out = append(out, f.recorded[i])
		}
	}
	return out, nil
}

func setupHistoryTest(t *testing.T) (*UpdateHandler, *fakeAdminActions, chat.Settings, *[]map[string]any) {
	t.Helper()
	server, calls := newRecordingServer(t)
	t.Cleanup(server.Close)
	handler, chats := newTestHandler(t, server)
	admin := common.UserID{Value: 1}
	handler.Authorization = chat.NewAuthorizationService(chats, &fakeAdminMembership{admins: []common.UserID{admin}})
	log := &fakeAdminActions{}
	handler.AdminActions = log
	settings := chat.Settings{ChatID: common.ChatID{Value: -100}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)
	return handler, log, settings, calls
}

func TestSettings_ChangesAreRecordedInTheHistory(t *testing.T) {
	handler, log, settings, _ := setupHistoryTest(t)

	data := "settings:top_tier"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 7, Chat: Chat{ID: -100, Type: "supergroup"}}, Data: &data}
	if _, err := handler.routeCallback(context.Background(), cb, settings, data); err != nil {
		t.Fatal(err)
	}

	if len(log.recorded) != 1 {
		t.Fatalf("expected 1 recorded action, got %d", len(log.recorded))
	}
	got := log.recorded[0]
	if got.Kind != "top_tier" {
		t.Fatalf("Kind = %q, want top_tier", got.Kind)
	}
	if got.ActorID.Value != 1 || got.ActorName != "Admin" {
		t.Fatalf("actor = %d/%q, want 1/Admin", got.ActorID.Value, got.ActorName)
	}
	if got.ChatID != settings.ChatID {
		t.Fatalf("ChatID = %v, want %v", got.ChatID, settings.ChatID)
	}
}

// The history exists so a co-manager can see a change they never witnessed.
// It must therefore name both the change and who made it.
func TestHistoryView_ListsRecentChangesWithTheirAuthor(t *testing.T) {
	handler, log, settings, calls := setupHistoryTest(t)
	log.recorded = []chat.AdminAction{{
		ChatID: settings.ChatID, ActorID: common.UserID{Value: 2}, ActorName: "Other Admin",
		Kind: "timezone", Detail: "Europe/Berlin",
		CreatedAt: time.Date(2026, 9, 8, 10, 30, 0, 0, time.UTC),
	}}

	data := "settings:history"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 7, Chat: Chat{ID: -100, Type: "supergroup"}}, Data: &data}
	if _, err := handler.routeCallback(context.Background(), cb, settings, data); err != nil {
		t.Fatal(err)
	}

	text := lastText(*calls)
	for _, want := range []string{
		handler.Texts.Get(historyKindKey("timezone"), common.LocaleRU),
		"Europe/Berlin",
		"Other Admin",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("history screen %q is missing %q", text, want)
		}
	}
}

func TestHistoryView_EmptyLogSaysSoInsteadOfRenderingAnEmptyList(t *testing.T) {
	handler, _, settings, calls := setupHistoryTest(t)

	data := "settings:history"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 7, Chat: Chat{ID: -100, Type: "supergroup"}}, Data: &data}
	if _, err := handler.routeCallback(context.Background(), cb, settings, data); err != nil {
		t.Fatal(err)
	}

	text := lastText(*calls)
	if !strings.Contains(text, handler.Texts.Get("history.empty", common.LocaleRU)) {
		t.Fatalf("expected the empty-history screen, got %q", text)
	}
}

// The log is bookkeeping about a change that already happened. Failing to
// write it must never fail the change itself.
func TestLogAdminAction_StorageFailureDoesNotFailTheChange(t *testing.T) {
	handler, log, settings, _ := setupHistoryTest(t)
	log.recordErr = errors.New("database is on fire")

	data := "settings:top_tier"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 7, Chat: Chat{ID: -100, Type: "supergroup"}}, Data: &data}
	if _, err := handler.routeCallback(context.Background(), cb, settings, data); err != nil {
		t.Fatalf("a failed history write must not fail the setting change: %v", err)
	}
}

// lastText returns the text of the last message the bot sent or edited.
func lastText(calls []map[string]any) string {
	for i := len(calls) - 1; i >= 0; i-- {
		if text, ok := calls[i]["text"].(string); ok {
			return text
		}
	}
	return ""
}
