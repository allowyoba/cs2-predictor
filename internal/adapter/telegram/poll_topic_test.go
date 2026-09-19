package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// A forum topic can be deleted while a tournament is still running. The
// poll must still reach the chat — sent to the group's main thread — and
// the dead topic override must be cleared, or every later poll pays the
// same failed call first.
func TestSend_FallsBackToTheMainThreadWhenTheTopicIsGone(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		w.Header().Set("Content-Type", "application/json")
		if _, inTopic := payload["message_thread_id"]; inTopic && strings.HasSuffix(r.URL.Path, "/sendPoll") {
			attempts++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message thread not found"}`))
			return
		}
		attempts++
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7,"poll":{"id":"tg-1"}}}`))
	}))
	defer srv.Close()

	client := NewClient(Config{BaseURL: srv.URL, Token: "test-token"}, srv.Client())
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	chats := newFakeChats()
	chatID := common.ChatID{Value: -1}
	topic := int64(99)
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, DefaultTopicID: &topic}); err != nil {
		t.Fatal(err)
	}
	eventID, matchID := common.NewEventID(), common.NewMatchID()
	format, err := competition.NewSeriesFormat(competition.BestOf, 3)
	if err != nil {
		t.Fatal(err)
	}
	scheduledAt := time.Now().Add(time.Hour)
	catalog := &dataCatalog{
		events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major"}},
		unstartedMatches: map[common.EventID][]competition.Match{eventID: {{
			ID: matchID, EventID: eventID, Format: format, Status: competition.MatchNotStarted,
			FirstTeam:   &competition.Team{ID: common.NewTeamID(), Name: "G2"},
			SecondTeam:  &competition.Team{ID: common.NewTeamID(), Name: "NAVI"},
			ScheduledAt: &scheduledAt,
		}}},
	}
	gateway := NewPollGateway(client, catalog, chats, texts, slog.Default(), PollEnrichmentSources{})
	score, err := competition.NewMatchScore(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := gateway.Send(context.Background(), prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: matchID, TopicID: &topic,
		Options: []prediction.Option{{Index: 0, Score: score}}, Status: prediction.PollOpen, ClosesAt: scheduledAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sent.TopicID != nil {
		t.Fatalf("the poll ended up in the main thread, so SentPoll must say so, got %v", *sent.TopicID)
	}
	if attempts != 2 {
		t.Fatalf("expected one failed topic attempt and one retry, got %d calls", attempts)
	}
	after, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if after.DefaultTopicID != nil {
		t.Fatal("the dead topic override must be cleared, or every later poll repeats the failed call")
	}
}
