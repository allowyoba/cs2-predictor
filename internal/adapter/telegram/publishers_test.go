package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// Ground truth: a byte-based s[:n] can split a multi-byte UTF-8 rune in
// half — verified live: "Александр Великолепный"[:24] produces
// "Александр Ве\xd0", invalid UTF-8. truncate must cut by rune count
// instead, since RU (Cyrillic) is this bot's default locale, not an edge
// case.
func TestTruncate_IsRuneSafeForCyrillic(t *testing.T) {
	name := "Александр Великолепный" // 22 runes, 43 bytes (2 bytes/rune)

	got := truncate(name, 12)

	if !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
	}
	if runeCount := len([]rune(got)); runeCount != 12 {
		t.Fatalf("truncate(name, 12) = %q with %d runes, want exactly 12", got, runeCount)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("truncate result contains the UTF-8 replacement character: %q", got)
	}
}

func TestTruncate_LeavesShortStringUnchanged(t *testing.T) {
	if got := truncate("Alex", 24); got != "Alex" {
		t.Fatalf("truncate(%q, 24) = %q, want unchanged", "Alex", got)
	}
}

func TestTruncate_ExactLengthUnchanged(t *testing.T) {
	name := "Александр" // exactly 9 runes
	if got := truncate(name, 9); got != name {
		t.Fatalf("truncate at exact length = %q, want %q", got, name)
	}
}

func TestAnnualHighlightsText_RendersAllAgreedAwards(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	h := common.AnnualHighlightsNotification{
		Comeback:    &common.AnnualComebackNotification{DisplayName: "Alex", StartRank: 8, FinalRank: 2},
		Sniper:      &common.AnnualUserMetricNotification{DisplayName: "Max", Accuracy: 74, Predictions: 61},
		Expert:      &common.AnnualUserMetricNotification{DisplayName: "Ivan", Value: 83},
		Exact:       &common.AnnualUserMetricNotification{DisplayName: "Alex", Value: 21},
		Streak:      &common.AnnualUserMetricNotification{DisplayName: "Max", Value: 9},
		TeamSynergy: &common.AnnualTeamSynergyNotification{DisplayName: "Ivan", TeamName: "Team Spirit", Correct: 14, Predictions: 17, Accuracy: 82},
	}

	got := annualHighlightsText(texts, common.LocaleRU, 2026, h)
	for _, want := range []string{
		ru(t, "digest.highlights", 2026),
		ru(t, "digest.comeback", code("Alex"), 8, 2),
		ru(t, "digest.sniper", code("Max"), 74, 61),
		ru(t, "digest.expert", code("Ivan"), 83),
		ru(t, "digest.exact", code("Alex"), 21),
		ru(t, "digest.streak", code("Max"), 9),
		ru(t, "digest.team_synergy", code("Ivan"), bold("Team Spirit"), 14, 17, 82),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("annualHighlightsText missing %q in %q", want, got)
		}
	}
}

// TestBigEventPublisher_SendsBadgedAnnouncementWithSubscribeButton covers
// the new "big event discovered" notification end to end: correct chat/
// topic targeting, the tier badge on the event name, escaping of a
// provider-supplied name, and a subscribe button wired to the exact same
// "subscribe:<eventId>" callback data /events search results use.
func TestBigEventPublisher_SendsBadgedAnnouncementWithSubscribeButton(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	eventID := "11111111-1111-1111-1111-111111111111"
	topicID := int64(42)
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	pub := NewBigEventPublisher(client, chats, texts)
	if !pub.Supports("telegram.big-event-discovered") {
		t.Fatal("expected Supports(telegram.big-event-discovered) = true")
	}
	if pub.Supports("telegram.match-result") {
		t.Fatal("expected Supports to reject an unrelated event type")
	}

	n := common.BigEventDiscoveredNotification{
		ChatID: -1, TopicID: &topicID, EventID: eventID, EventName: "IEM & Cologne <2027>", Tier: "s",
	}
	payload, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	if len(*calls) != 1 {
		t.Fatalf("expected exactly 1 sendMessage call, got %d: %+v", len(*calls), *calls)
	}
	call := (*calls)[0]
	if call["chat_id"] != float64(-1) {
		t.Fatalf("chat_id = %v, want -1", call["chat_id"])
	}
	if call["message_thread_id"] != float64(42) {
		t.Fatalf("message_thread_id = %v, want 42", call["message_thread_id"])
	}
	text, _ := call["text"].(string)
	if !strings.Contains(text, "🌟") {
		t.Fatalf("expected the S-tier badge in text, got %q", text)
	}
	if !strings.Contains(text, "IEM &amp; Cologne &lt;2027&gt;") {
		t.Fatalf("expected the event name HTML-escaped in text, got %q", text)
	}
	labels := buttonLabels(t, call)
	if len(labels) != 1 {
		t.Fatalf("expected exactly 1 button, got %+v", labels)
	}
	markup, _ := call["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	firstRow, _ := rows[0].([]any)
	firstBtn, _ := firstRow[0].(map[string]any)
	if firstBtn["callback_data"] != "subscribe:"+eventID {
		t.Fatalf("callback_data = %v, want %q", firstBtn["callback_data"], "subscribe:"+eventID)
	}
}
