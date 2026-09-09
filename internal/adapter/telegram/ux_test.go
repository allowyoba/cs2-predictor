package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// A screen with no buttons at all is a dead end: in a private chat there
// is nothing else on screen to get out by. Every screen the bot renders
// must offer at least one way onward.
func TestScreens_NoDeadEnds(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	admin := common.UserID{Value: 1}
	handler.Authorization = chat.NewAuthorizationService(chats, &fakeAdminMembership{admins: []common.UserID{admin}})
	handler.AdminActions = &fakeAdminActions{}
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)

	for _, data := range []string{
		"menu:main", "menu:stats", "menu:events", "menu:settings", "menu:rules",
		"menu:upcoming", "events:add", "events:mine", "settings:moderators",
		"settings:history", "settings:timezone", "stats:all", "stats:years", "stats:events",
	} {
		*calls = nil
		cb := &CallbackQuery{ID: "cb", From: User{ID: 1, FirstName: "Admin"},
			Message: &Message{MessageID: 5, Chat: Chat{ID: -1, Type: "supergroup"}}, Data: &data}
		if _, err := handler.routeCallback(context.Background(), cb, settings, data); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if cds, _ := findKeyboardButtons(*calls); len(cds) == 0 {
			if !hasAnyKeyboard(*calls) {
				t.Errorf("%s renders a screen with no way onward", data)
			}
		}
	}
}

// hasAnyKeyboard reports whether any call carried a keyboard, including
// one made only of URL buttons (which carry no callback data).
func hasAnyKeyboard(calls []map[string]any) bool {
	for _, c := range calls {
		if kb, ok := c["reply_markup"].(map[string]any); ok {
			if rows, ok := kb["inline_keyboard"].([]any); ok && len(rows) > 0 {
				return true
			}
		}
	}
	return false
}

// The settings screen shows the timezone, so it must also be able to
// change it — a setting you can see and not touch is worse than one you
// cannot see.
func TestSettings_TimezoneIsChangeableFromWhereItIsShown(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	admin := common.UserID{Value: 1}
	handler.Authorization = chat.NewAuthorizationService(chats, &fakeAdminMembership{admins: []common.UserID{admin}})
	handler.AdminActions = &fakeAdminActions{}
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: "UTC", Active: true}
	_, _ = chats.Save(context.Background(), settings)

	cb := func(data string) *CallbackQuery {
		return &CallbackQuery{ID: "cb", From: User{ID: 1, FirstName: "Admin"},
			Message: &Message{MessageID: 5, Chat: Chat{ID: -1, Type: "supergroup"}}, Data: &data}
	}
	if _, err := handler.routeCallback(context.Background(), cb("menu:settings"), settings, "menu:settings"); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "settings:timezone") {
		t.Fatalf("settings has no timezone button, got %v", cds)
	}

	// The picker marks the zone in force, so it shows state as well as
	// choices.
	*calls = nil
	if _, err := handler.routeCallback(context.Background(), cb("settings:timezone"), settings, "settings:timezone"); err != nil {
		t.Fatal(err)
	}
	labels := buttonLabels(t, (*calls)[len(*calls)-1])
	if !slices.Contains(labels, "• UTC") {
		t.Fatalf("the picker does not mark the current zone, got %v", labels)
	}

	moscow := slices.Index(commonTimezones, "Europe/Moscow")
	if _, err := handler.routeCallback(context.Background(), cb(cbTimezone(moscow)), settings, cbTimezone(moscow)); err != nil {
		t.Fatal(err)
	}
	saved, _ := chats.Find(context.Background(), settings.ChatID)
	if saved.Timezone != "Europe/Moscow" {
		t.Fatalf("timezone = %q, want Europe/Moscow", saved.Timezone)
	}
	if log := handler.AdminActions.(*fakeAdminActions); len(log.recorded) != 1 || log.recorded[0].Kind != "timezone" {
		t.Fatalf("the change was not recorded in the history: %+v", log.recorded)
	}
}

func TestSetTimezone_RejectsAnOutOfRangeChoice(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	admin := common.UserID{Value: 1}
	handler.Authorization = chat.NewAuthorizationService(chats, &fakeAdminMembership{admins: []common.UserID{admin}})
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: "UTC", Active: true}
	_, _ = chats.Save(context.Background(), settings)

	data := "settings:tz:999"
	cb := &CallbackQuery{ID: "cb", From: User{ID: 1, FirstName: "Admin"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: -1, Type: "supergroup"}}, Data: &data}
	if _, err := handler.routeCallback(context.Background(), cb, settings, data); err == nil {
		t.Fatal("an index past the end of the list was accepted")
	}
	saved, _ := chats.Find(context.Background(), settings.ChatID)
	if saved.Timezone != "UTC" {
		t.Fatalf("timezone changed to %q on an invalid choice", saved.Timezone)
	}
}

// A capped list that silently drops its tail lets a chat believe it has
// fewer subscriptions than it does.
func TestSubscribedEvents_SaysHowManyAreHidden(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)

	events := map[common.EventID]competition.Event{}
	var subs []subscription.EventSubscription
	for range subscribedEventsPageSize + 3 {
		id := common.NewEventID()
		events[id] = competition.Event{ID: id, Name: "Event", Status: competition.EventRunning}
		subs = append(subs, subscription.EventSubscription{ChatID: settings.ChatID, EventID: id})
	}
	handler.Catalog = &dataCatalog{events: events}
	handler.Subscriptions = &dataSubs{subs: subs}

	data := "events:mine"
	cb := &CallbackQuery{ID: "cb", From: User{ID: 1, FirstName: "Admin"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: -1, Type: "supergroup"}}, Data: &data}
	if _, err := handler.routeCallback(context.Background(), cb, settings, data); err != nil {
		t.Fatal(err)
	}

	if text := lastText(*calls); !strings.Contains(text, ru(t, "events.mine_truncated", 3)) {
		t.Fatalf("the list hid 3 subscriptions without saying so: %q", text)
	}
}

// A leaderboard is sent as a standalone message, including from a match
// result notification's button — without a way onward, that message is
// where the user gets stuck.
func TestLeaderboard_OffersAWayBackToTheStatsMenu(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)

	if err := handler.renderLeaderboard(context.Background(), sendTarget(settings.ChatID, nil), settings, scoring.AllTime()); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "menu:stats") {
		t.Fatalf("leaderboard is a dead end, got %v", cds)
	}
}
