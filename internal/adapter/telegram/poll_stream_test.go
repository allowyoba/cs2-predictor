package telegram

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// recordingStreamMarker stands in for the poll repository's record of which
// link a chat has already been given.
type recordingStreamMarker struct{ url string }

func (m *recordingStreamMarker) MarkPollStreamAnnounced(_ context.Context, _ common.PollID, url string) error {
	m.url = url
	return nil
}

// streamPoll wires a gateway over a match carrying streams, and closes a
// poll whose already-announced link is announced.
func streamPoll(t *testing.T, streams []competition.Stream, announced string) (*recordingStreamMarker, *[]map[string]any) {
	return streamPollFor(t, streams, announced, true)
}

// streamPollFor is streamPoll with the chat's opt-in spelled out, for the
// test that covers a chat which never asked for these messages.
func streamPollFor(t *testing.T, streams []competition.Stream, announced string, announce bool) (*recordingStreamMarker, *[]map[string]any) {
	t.Helper()
	srv, calls := newRecordingServer(t)
	t.Cleanup(srv.Close)
	client := NewClient(Config{BaseURL: srv.URL, Token: "test-token"}, srv.Client())
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	chats := newFakeChats()
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetNotifyEnabled(context.Background(), common.ScopeChat, chatID.Value, string(common.ChatNotifyStreams), announce); err != nil {
		t.Fatal(err)
	}
	eventID, matchID := common.NewEventID(), common.NewMatchID()
	catalog := &dataCatalog{
		events:           map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major", Game: competition.GameCS2}},
		unstartedMatches: map[common.EventID][]competition.Match{eventID: {{ID: matchID, EventID: eventID, Streams: streams}}},
	}
	marker := &recordingStreamMarker{}
	gateway := NewPollGateway(client, catalog, chats, texts, slog.Default(), PollEnrichmentSources{})
	gateway.StreamRecorder = marker
	messageID := int64(42)
	poll := prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: matchID,
		TelegramMessageID: &messageID, StreamURL: announced,
	}
	if err := gateway.Close(context.Background(), poll); err != nil {
		t.Fatal(err)
	}
	return marker, calls
}

// streamMessages returns the texts of every sendMessage the close produced.
func streamMessages(calls *[]map[string]any) []string {
	var texts []string
	for _, c := range *calls {
		if c["__method"] == "sendMessage" {
			text, _ := c["text"].(string)
			texts = append(texts, text)
		}
	}
	return texts
}

// The poll goes out hours ahead, when the providers often list no broadcast
// yet; closing it is both the last chance to catch one and the moment the
// link is finally worth having.
func TestClose_PostsABroadcastThatAppearedAfterThePollWasSent(t *testing.T) {
	marker, calls := streamPoll(t, []competition.Stream{
		{Language: "ru", URL: "https://twitch.tv/major_ru", Main: true, Official: true},
	}, "")

	sent := streamMessages(calls)
	if len(sent) != 1 {
		t.Fatalf("expected exactly one broadcast message, got %v", sent)
	}
	if !strings.Contains(sent[0], "https://twitch.tv/major_ru") || !strings.Contains(sent[0], "Twitch") {
		t.Fatalf("expected the message to link the stream by platform, got %q", sent[0])
	}
	if marker.url != "https://twitch.tv/major_ru" {
		t.Fatalf("the announced link must be recorded, got %q", marker.url)
	}
}

// Recording the link is what keeps a retried closure from posting it twice.
func TestClose_DoesNotRepeatALinkTheChatAlreadyHas(t *testing.T) {
	_, calls := streamPoll(t, []competition.Stream{
		{Language: "ru", URL: "https://twitch.tv/major_ru", Main: true, Official: true},
	}, "https://twitch.tv/major_ru")

	if sent := streamMessages(calls); len(sent) != 0 {
		t.Fatalf("expected no second message for the same link, got %v", sent)
	}
}

// Most matches never get an official broadcast listed. Closing those must
// stay silent rather than posting an empty invitation to watch.
func TestClose_SaysNothingWhenNoOfficialBroadcastExists(t *testing.T) {
	_, calls := streamPoll(t, []competition.Stream{
		{Language: "ru", URL: "https://twitch.tv/some_fan", Official: false},
	}, "")

	if sent := streamMessages(calls); len(sent) != 0 {
		t.Fatalf("unofficial channels are never linked, got %v", sent)
	}
}

// The announcement is an extra message in the room, so a chat that never
// asked for it hears nothing — the default for every chat.
func TestClose_SaysNothingUnlessTheChatAskedForBroadcastLinks(t *testing.T) {
	_, calls := streamPollFor(t, []competition.Stream{
		{Language: "ru", URL: "https://twitch.tv/major_ru", Main: true, Official: true},
	}, "", false)

	if sent := streamMessages(calls); len(sent) != 0 {
		t.Fatalf("expected silence for a chat with broadcast links switched off, got %v", sent)
	}
}
