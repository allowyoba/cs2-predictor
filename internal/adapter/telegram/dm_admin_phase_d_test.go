package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// fakeScoringWithStandings embeds fakeScoring (handler_test.go) and
// overrides Leaderboard to return a single standing for user id 1 — used to
// verify openPersonalEventStats/personalStats picks the caller's own row
// out of the chat's per-event leaderboard.
type fakeScoringWithStandings struct{ fakeScoring }

func (fakeScoringWithStandings) Leaderboard(context.Context, common.ChatID, scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	return []scoring.UserStanding{
		{UserID: common.UserID{Value: 1}, DisplayName: "TestUser", Points: 10, Rank: 1, Predictions: 5, ExactPredictions: 2},
	}, nil
}

func TestParsePersonalStatsDeepLink_ParsesValidPayload(t *testing.T) {
	chatID := common.ChatID{Value: -1001234567890}
	eventID := common.NewEventID()
	text := "/start pstats_" + int64Base36(chatID.Value) + ":" + strings.ReplaceAll(eventID.Value.String(), "-", "")

	gotChatID, gotEventID, ok := parsePersonalStatsDeepLink(text)
	if !ok || gotChatID != chatID || gotEventID != eventID {
		t.Fatalf("parsePersonalStatsDeepLink(%q) = %v, %v, %v, want %v, %v, true", text, gotChatID, gotEventID, ok, chatID, eventID)
	}
}

func TestParsePersonalStatsDeepLink_RejectsMalformedPayloads(t *testing.T) {
	cases := []string{
		"/start",
		"/start admin_123",
		"/start pstats_",
		"/start pstats_123",            // missing event id half
		"/start pstats_123:not-a-uuid", // invalid event id
		"/start pstats_not-a-number:" + "11111111111111111111111111111111",
	}
	for _, text := range cases {
		if _, _, ok := parsePersonalStatsDeepLink(text); ok {
			t.Fatalf("parsePersonalStatsDeepLink(%q) = ok, want not-ok", text)
		}
	}
}

func TestMatchResultPublisher_UsesDeepLinkButtonWhenBotUsernameSet(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	pub := NewMatchResultPublisher(client, chats, texts, "cs2predictor_bot")
	eventID := common.NewEventID()
	n := common.MatchResultNotification{
		ChatID: -1, EventID: eventID.Value.String(), EventName: "Major", Format: "BO3",
		FirstTeam: "Spirit", SecondTeam: "NAVI", Score: "2-0",
	}
	payload, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	_, urls := findKeyboardButtons(*calls)
	wantPrefix := "https://t.me/cs2predictor_bot?start=pstats_" + int64Base36(-1) + ":"
	if !containsPrefix(urls, wantPrefix) {
		t.Fatalf("expected a personal-stats deep-link button with prefix %q, got urls %v", wantPrefix, urls)
	}
	cds, _ := findKeyboardButtons(*calls)
	if containsPrefix(cds, "stats:mine:") {
		t.Fatalf("expected no in-group stats:mine callback button when BotUsername is set, got %v", cds)
	}
}

func TestMatchResultPublisher_FallsBackToCallbackButtonWhenBotUsernameEmpty(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	pub := NewMatchResultPublisher(client, chats, texts, "") // BotUsername unresolved
	eventID := common.NewEventID()
	n := common.MatchResultNotification{
		ChatID: -1, EventID: eventID.Value.String(), EventName: "Major", Format: "BO3",
		FirstTeam: "Spirit", SecondTeam: "NAVI", Score: "2-0",
	}
	payload, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if !containsPrefix(cds, "stats:notif:mine:") {
		t.Fatalf("expected the old in-group callback button as a fallback, got %v", cds)
	}
}

// TestMatchResultPublisher_RendersPredictionBreakdown covers the
// "(exact-outcome-wrong)" addition next to each standing's points: a
// notification carrying stale (zero-value) prediction counts would silently
// render "(0-0-0)" for everyone, so this pins the real values through.
func TestMatchResultPublisher_RendersPredictionBreakdown(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	pub := NewMatchResultPublisher(client, chats, texts, "")
	eventID := common.NewEventID()
	n := common.MatchResultNotification{
		ChatID: -1, EventID: eventID.Value.String(), EventName: "Major", Format: "BO3",
		FirstTeam: "Spirit", SecondTeam: "NAVI", Score: "2-0",
		Standings: []common.StandingNotification{
			{UserID: 1, DisplayName: "Voter", Rank: 1, Points: 9, ExactPredictions: 2, CorrectPredictions: 5, Predictions: 8},
		},
	}
	payload, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	text, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(text, "(2-3-3)") {
		t.Fatalf("expected the prediction breakdown (2-3-3) in the message, got %q", text)
	}
}

func TestEventFinishedPublisher_RendersPredictionBreakdown(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	pub := NewEventFinishedPublisher(client, chats, texts)
	n := common.EventFinishedNotification{
		ChatID: -1, EventName: "Major",
		Standings: []common.StandingNotification{
			{UserID: 1, DisplayName: "Winner", Rank: 1, Points: 20, ExactPredictions: 4, CorrectPredictions: 6, Predictions: 10},
		},
	}
	payload, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	text, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(text, "(4-2-4)") {
		t.Fatalf("expected the prediction breakdown (4-2-4) in the message, got %q", text)
	}
}

func TestPrivateMessage_PersonalStatsDeepLinkRendersEventRank(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	groupChatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: groupChatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	eventID := common.NewEventID()
	handler.Scoring = fakeScoringWithStandings{}

	text := "/start pstats_" + int64Base36(groupChatID.Value) + ":" + strings.ReplaceAll(eventID.Value.String(), "-", "")
	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 1}, Text: &text}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, c := range *calls {
		if cid, ok := c["chat_id"].(float64); ok && int64(cid) == 42 {
			if text, ok := c["text"].(string); ok && strings.Contains(text, "TestUser") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected the caller's own standing to be rendered into the DM")
	}
}

func TestPrivateMessage_PersonalStatsDeepLinkUnknownChatFallsBackToDashboard(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server) // no chat saved at all

	text := "/start pstats_" + int64Base36(-999) + ":" + strings.ReplaceAll(common.NewEventID().Value.String(), "-", "")
	msg := &Message{Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 1}, Text: &text}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, c := range *calls {
		if text, ok := c["text"].(string); ok && strings.Contains(text, ru(t, "private.stats_empty")) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a fallback to the personal dashboard for an unknown chat")
	}
}
