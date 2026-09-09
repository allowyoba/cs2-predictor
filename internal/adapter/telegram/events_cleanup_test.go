package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// setupCleanupTest gives the chat one live tournament and two that are
// over — the state that accumulates on its own over a season.
func setupCleanupTest(t *testing.T) (*UpdateHandler, chat.Settings, []common.EventID, *[]map[string]any) {
	t.Helper()
	server, calls := newRecordingServer(t)
	t.Cleanup(server.Close)
	handler, chats := newTestHandler(t, server)
	admin := common.UserID{Value: 1}
	handler.Authorization = chat.NewAuthorizationService(chats, &fakeAdminMembership{admins: []common.UserID{admin}})
	handler.AdminActions = &fakeAdminActions{}

	live := common.NewEventID()
	overA, overB := common.NewEventID(), common.NewEventID()
	handler.Catalog = &dataCatalog{events: map[common.EventID]competition.Event{
		live:  {ID: live, Name: "Live Major", Status: competition.EventRunning},
		overA: {ID: overA, Name: "Last Year's Major", Status: competition.EventFinished},
		overB: {ID: overB, Name: "Old Qualifier", Status: competition.EventFinished},
	}}
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)
	handler.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{
		{ChatID: settings.ChatID, EventID: overA}, // deliberately first
		{ChatID: settings.ChatID, EventID: live},
		{ChatID: settings.ChatID, EventID: overB},
	}}
	return handler, settings, []common.EventID{live, overA, overB}, calls
}

func dmCallback(data string) *CallbackQuery {
	return &CallbackQuery{ID: "cb", From: User{ID: 1, FirstName: "Admin"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: 1, Type: "private"}}, Data: &data}
}

func TestSubscribedEvents_OffersCleanupAndSortsFinishedLast(t *testing.T) {
	handler, settings, ids, calls := setupCleanupTest(t)
	live := ids[0]

	data := "events:mine"
	if _, err := handler.routeCallback(context.Background(), dmCallback(data), settings, data); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "events:cleanup") {
		t.Fatalf("expected a cleanup button with two finished subscriptions, got %v", cds)
	}
	// The live tournament must come first even though a finished one was
	// subscribed earlier: that is the whole point of the sort.
	liveAt := slices.Index(cds, cbEventView(live))
	if liveAt != 0 {
		t.Fatalf("live tournament is at position %d, want first (buttons: %v)", liveAt, cds)
	}
}

// Bulk removal is administration, so it must not appear in the group.
func TestSubscribedEvents_NoCleanupButtonInGroup(t *testing.T) {
	handler, settings, _, calls := setupCleanupTest(t)

	data := "events:mine"
	cb := &CallbackQuery{ID: "cb", From: User{ID: 1, FirstName: "Admin"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: -1, Type: "supergroup"}}, Data: &data}
	if _, err := handler.routeCallback(context.Background(), cb, settings, data); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if slices.Contains(cds, "events:cleanup") {
		t.Fatalf("cleanup must be DM-only, but the group menu offered it: %v", cds)
	}
}

func TestCleanupMenu_NamesEveryFinishedTournamentBeforeRemoving(t *testing.T) {
	handler, settings, _, calls := setupCleanupTest(t)

	data := "events:cleanup"
	if _, err := handler.routeCallback(context.Background(), dmCallback(data), settings, data); err != nil {
		t.Fatal(err)
	}

	text := lastText(*calls)
	for _, want := range []string{"Last Year's Major", "Old Qualifier"} {
		if !strings.Contains(text, escapeHTML(want)) {
			t.Fatalf("confirmation screen %q is missing %q", text, want)
		}
	}
	if strings.Contains(text, "Live Major") {
		t.Fatal("the live tournament must not be listed for removal")
	}
	if subs := handler.Subscriptions.(*dataSubs); len(subs.unsubscribed) != 0 {
		t.Fatal("the confirmation screen removed subscriptions before being confirmed")
	}
}

func TestCleanupFinished_RemovesOnlyFinishedSubscriptions(t *testing.T) {
	handler, settings, ids, _ := setupCleanupTest(t)
	live, overA, overB := ids[0], ids[1], ids[2]

	data := "events:cleanup:go"
	if _, err := handler.routeCallback(context.Background(), dmCallback(data), settings, data); err != nil {
		t.Fatal(err)
	}

	subs := handler.Subscriptions.(*dataSubs)
	if len(subs.unsubscribed) != 2 || !slices.Contains(subs.unsubscribed, overA) || !slices.Contains(subs.unsubscribed, overB) {
		t.Fatalf("unsubscribed = %v, want exactly the two finished tournaments", subs.unsubscribed)
	}
	if slices.Contains(subs.unsubscribed, live) {
		t.Fatal("the live tournament was unsubscribed — its polls would stop")
	}
	// Each removal belongs in the chat's history like any other.
	if log := handler.AdminActions.(*fakeAdminActions); len(log.recorded) != 2 {
		t.Fatalf("recorded %d history entries, want 2", len(log.recorded))
	}
}

// A tournament that has ended since the list was rendered must be swept
// too: the action re-derives what is finished rather than trusting the
// count baked into the button it was tapped from.
func TestCleanupFinished_UsesCurrentStateNotTheRenderedCount(t *testing.T) {
	handler, settings, ids, _ := setupCleanupTest(t)
	live := ids[0]
	catalog := handler.Catalog.(*dataCatalog)
	ended := catalog.events[live]
	ended.Status = competition.EventFinished
	catalog.events[live] = ended

	data := "events:cleanup:go"
	if _, err := handler.routeCallback(context.Background(), dmCallback(data), settings, data); err != nil {
		t.Fatal(err)
	}

	if subs := handler.Subscriptions.(*dataSubs); len(subs.unsubscribed) != 3 {
		t.Fatalf("unsubscribed %d, want all 3 — the third finished after the menu was drawn", len(subs.unsubscribed))
	}
}

func TestCleanupMenu_NothingFinishedSaysSo(t *testing.T) {
	handler, settings, ids, calls := setupCleanupTest(t)
	handler.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{ChatID: settings.ChatID, EventID: ids[0]}}}

	data := "events:cleanup"
	if _, err := handler.routeCallback(context.Background(), dmCallback(data), settings, data); err != nil {
		t.Fatal(err)
	}

	if text := lastText(*calls); !strings.Contains(text, handler.Texts.Get("events.cleanup_none", common.LocaleRU)) {
		t.Fatalf("expected the nothing-to-clean screen, got %q", text)
	}
}

// splitByLiveness decides what "finished" means for the cleanup, including
// the case where the tournament is gone from the catalog entirely.
func TestSplitByLiveness_TreatsUnknownEventsAsFinished(t *testing.T) {
	known, unknown := common.NewEventID(), common.NewEventID()
	now := time.Now()
	byID := map[common.EventID]competition.Event{known: {ID: known, Status: competition.EventRunning}}
	live, finished := splitByLiveness([]subscription.EventSubscription{{EventID: known}, {EventID: unknown}}, byID, now)

	if len(live) != 1 || live[0].EventID != known {
		t.Fatalf("live = %v, want just the known running tournament", live)
	}
	if len(finished) != 1 || finished[0].EventID != unknown {
		t.Fatalf("finished = %v, want the tournament missing from the catalog", finished)
	}
}
