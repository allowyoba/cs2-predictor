package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// findKeyboardButtons walks every recorded API call's reply_markup and
// returns the set of callback_data/url values seen — used to assert which
// buttons a screen actually offered.
func findKeyboardButtons(calls []map[string]any) (callbackData, urls []string) {
	for _, c := range calls {
		kb, ok := c["reply_markup"].(map[string]any)
		if !ok {
			continue
		}
		rows, _ := kb["inline_keyboard"].([]any)
		for _, row := range rows {
			for _, btn := range row.([]any) {
				b := btn.(map[string]any)
				if cd, ok := b["callback_data"].(string); ok {
					// Assert on the action, not the scope envelope the send
					// boundary wraps it in (see callbackdata.go) — a test
					// about which buttons a screen offers shouldn't care
					// whether that screen happens to be owner- or chat-bound.
					_, payload := splitCallbackScope(cd)
					callbackData = append(callbackData, payload)
				}
				if u, ok := b["url"].(string); ok {
					urls = append(urls, u)
				}
			}
		}
	}
	return callbackData, urls
}

func containsPrefix(values []string, prefix string) bool {
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

// --- subscribedEvents dmContext behavior ---

func TestSubscribedEvents_GroupContextDefersActionsUntilTournamentIsSelected(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	eventID := common.NewEventID()
	catalog := &dataCatalog{events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major", Game: competition.GameCS2}}}
	handler.Catalog = catalog
	handler.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{ChatID: common.ChatID{Value: -1}, EventID: eventID}}}
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU}

	topicID := int64(555)
	target := replyTarget{chatID: settings.ChatID, topicID: &topicID}
	if err := handler.subscribedEvents(context.Background(), target, settings, false); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if containsPrefix(cds, "unsubscribe:") {
		t.Fatalf("group context must not offer Unsubscribe, got buttons %v", cds)
	}
	if containsPrefix(cds, "event-topic:") {
		t.Fatalf("subscription list should not render per-tournament actions inline, got buttons %v", cds)
	}
	if !containsPrefix(cds, "events:view:") {
		t.Fatalf("subscription list must offer a tournament detail entry, got buttons %v", cds)
	}

	*calls = nil
	if err := handler.eventDetails(context.Background(), target, settings, eventID, false); err != nil {
		t.Fatal(err)
	}
	cds, _ = findKeyboardButtons(*calls)
	if !containsPrefix(cds, "event-topic:") {
		t.Fatalf("group tournament detail inside a topic should offer Bind topic, got buttons %v", cds)
	}
}

func TestSubscribedEvents_DMContextDefersUnsubscribeUntilTournamentIsSelected(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	eventID := common.NewEventID()
	catalog := &dataCatalog{events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major", Game: competition.GameCS2}}}
	handler.Catalog = catalog
	handler.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{ChatID: common.ChatID{Value: -1}, EventID: eventID}}}
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU}

	// A DM target never carries a topicID at all — private chats have none.
	target := sendTarget(common.ChatID{Value: 42}, nil)
	if err := handler.subscribedEvents(context.Background(), target, settings, true); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if containsPrefix(cds, "unsubscribe:") {
		t.Fatalf("subscription list should not render destructive actions inline, got buttons %v", cds)
	}
	if containsPrefix(cds, "event-topic:") {
		t.Fatalf("DM context must never offer Bind topic (no topic concept in a DM), got buttons %v", cds)
	}
	if !containsPrefix(cds, "events:view:") {
		t.Fatalf("subscription list must offer a tournament detail entry, got buttons %v", cds)
	}

	*calls = nil
	if err := handler.eventDetails(context.Background(), target, settings, eventID, true); err != nil {
		t.Fatal(err)
	}
	cds, _ = findKeyboardButtons(*calls)
	if !containsPrefix(cds, "unsubscribe:") {
		t.Fatalf("DM tournament detail should offer Unsubscribe, got buttons %v", cds)
	}
}

// --- group command redirects ---

func TestGroupEventsCommand_RedirectsToDMWhenBotUsernameSet(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.BotUsername = "cs2predictor_bot"
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})

	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: strPtr("/events FISSURE")}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	_, urls := findKeyboardButtons(*calls)
	if !containsPrefix(urls, "https://t.me/cs2predictor_bot?start=admin_") {
		t.Fatalf("expected a DM deep-link redirect, got urls %v", urls)
	}
	for _, c := range *calls {
		if text, ok := c["text"].(string); ok && strings.Contains(text, "FISSURE") {
			t.Fatalf("expected no in-group search results, got a message referencing the query: %q", text)
		}
	}
}

func TestGroupEventsCommand_FallsBackToDirectSearchWhenBotUsernameEmpty(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server) // BotUsername left empty
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})

	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: strPtr("/events top")}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	// No BotUsername => falls back to the old direct behavior: renderEventBrowse
	// runs (fakeCatalog returns no events, but the browse screen itself must
	// still render into the group, not bounce to a DM redirect).
	_, urls := findKeyboardButtons(*calls)
	if containsPrefix(urls, "https://t.me/") {
		t.Fatalf("expected no DM redirect when BotUsername is unresolved, got urls %v", urls)
	}
}

func TestGroupTimezoneCommand_RedirectsToDMWhenBotUsernameSet(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.BotUsername = "cs2predictor_bot"
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: strPtr("/timezone Europe/London")}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	saved, err := chats.Find(context.Background(), groupChatID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Timezone == "Europe/London" {
		t.Fatal("expected the group timezone command to be redirected to DM, not applied directly")
	}
	_, urls := findKeyboardButtons(*calls)
	if !containsPrefix(urls, "https://t.me/cs2predictor_bot?start=admin_") {
		t.Fatalf("expected a DM deep-link redirect, got urls %v", urls)
	}
}

func TestGroupTimezoneCommand_FallsBackToDirectChangeWhenBotUsernameEmpty(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server) // BotUsername left empty
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: strPtr("/timezone Europe/London")}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	saved, err := chats.Find(context.Background(), groupChatID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Timezone != "Europe/London" {
		t.Fatalf("expected the direct timezone change to still work when BotUsername is unresolved, got %q", saved.Timezone)
	}
}

func TestTopicsCommand_RendersTournamentPickerBeforeBindAction(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	eventID := common.NewEventID()
	handler.Catalog = &dataCatalog{events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major", Game: competition.GameCS2}}}
	handler.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{ChatID: common.ChatID{Value: -1}, EventID: eventID}}}

	msg := &Message{Chat: Chat{ID: -1, Type: "group"}, From: &User{ID: 1}, Text: strPtr("/topics"), MessageThreadID: int64Ptr(555)}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if containsPrefix(cds, "unsubscribe:") {
		t.Fatalf("/topics must never offer Unsubscribe, got %v", cds)
	}
	if containsPrefix(cds, "event-topic:") {
		t.Fatalf("/topics should keep the first screen selection-only, got %v", cds)
	}
	if !containsPrefix(cds, "events:view:") {
		t.Fatalf("/topics should offer tournament selection, got %v", cds)
	}
}

func int64Ptr(n int64) *int64 { return &n }

// --- DM-side command handling ---

func TestPrivateMessage_TimezoneCommandAppliesToManagedChatAndRepliesInDM(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	userID := common.UserID{Value: 42}
	_ = chats.SetDMSession(context.Background(), userID, groupChatID)

	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42}, Text: strPtr("/timezone Europe/London")}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	saved, err := chats.Find(context.Background(), groupChatID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Timezone != "Europe/London" {
		t.Fatalf("group chat timezone = %q, want Europe/London (the DM command should apply to the managed chat)", saved.Timezone)
	}
	targetedGroupChat := false
	for _, c := range *calls {
		if chatID, ok := c["chat_id"].(float64); ok && int64(chatID) == groupChatID.Value {
			targetedGroupChat = true
		}
	}
	if targetedGroupChat {
		t.Fatal("the confirmation must be sent to the DM, not posted into the group chat")
	}
}

func TestPrivateMessage_EventsCommandRepliesIntoDMNotManagedChat(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	userID := common.UserID{Value: 42}
	_ = chats.SetDMSession(context.Background(), userID, groupChatID)

	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42}, Text: strPtr("/events top")}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	targetedGroupChat := false
	targetedDM := false
	for _, c := range *calls {
		chatID, ok := c["chat_id"].(float64)
		if !ok {
			continue
		}
		if int64(chatID) == groupChatID.Value {
			targetedGroupChat = true
		}
		if int64(chatID) == 42 {
			targetedDM = true
		}
	}
	if targetedGroupChat {
		t.Fatal("the search/browse screen must not be posted into the managed group chat")
	}
	if !targetedDM {
		t.Fatal("the search/browse screen must be posted into the DM")
	}
}

func TestPrivateMessage_SearchReplyDelegatesToManagedChat(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	userID := common.UserID{Value: 42}
	_ = chats.SetDMSession(context.Background(), userID, groupChatID)

	promptText := stripHTML(handler.Texts.Get("events.search_prompt", common.LocaleRU))
	reply := &Message{Chat: Chat{ID: 42, Type: "private"}, MessageID: 100, Text: strPtr(promptText)}
	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42}, Text: strPtr("FISSURE"), ReplyToMessage: reply}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, c := range *calls {
		if chatID, ok := c["chat_id"].(float64); ok && int64(chatID) == 42 {
			if text, ok := c["text"].(string); ok && strings.Contains(text, "FISSURE") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected the search-by-reply flow to run against the managed chat and reply in the DM")
	}
}

func TestPrivateMessage_NoDMSessionEventsCommandFallsBackToPersonalStats(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42}, Text: strPtr("/events top")}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, c := range *calls {
		if text, ok := c["text"].(string); ok && strings.Contains(text, ru(t, "private.cabinet_choose")) {
			found = true
		}
	}
	if !found {
		t.Fatal("with no active DM session, /events should fall back to the personal-stats screen")
	}
}
