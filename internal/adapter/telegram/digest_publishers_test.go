package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// The digest and event-eve publishers are the last step before a chat sees
// a scheduled message — everything upstream of them is already covered, and
// what is left is whether the message that goes out is the right one.

// publishTo runs one publisher over a payload and returns the sendMessage
// calls it made.
func publishTo(t *testing.T, publish func(client *Client, chats chat.Repository, texts *Texts) common.OutboxPublisher, n any) []map[string]any {
	t.Helper()
	server, calls := newRecordingServer(t)
	t.Cleanup(server.Close)
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := publish(client, chats, texts).Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	return *calls
}

func digestRow(name string, points int) common.DigestStanding {
	return common.DigestStanding{DisplayName: name, Rank: 1, Points: points, Predictions: 10, Accuracy: 60}
}

// Each publisher answers for exactly one outbox event type; a Supports that
// drifted would silently divert somebody else's message.
func TestPublishers_EachSupportsOnlyItsOwnEventType(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		publisher common.OutboxPublisher
		eventType string
	}{
		{NewMonthlyDigestPublisher(nil, nil, texts), "telegram.monthly-digest"},
		{NewAnnualDigestPublisher(nil, nil, texts), "telegram.annual-digest"},
		{NewEventEvePublisher(nil, nil, texts), "telegram.event-eve"},
	}
	for _, c := range cases {
		if !c.publisher.Supports(c.eventType) {
			t.Fatalf("%T must support %q", c.publisher, c.eventType)
		}
		if c.publisher.Supports("telegram.something-else") {
			t.Fatalf("%T must not claim an unrelated event type", c.publisher)
		}
	}
}

func TestMonthlyDigestPublisher_ShowsTheMonthAndYearBoardsWithTheirButtons(t *testing.T) {
	calls := publishTo(t, func(c *Client, ch chat.Repository, tx *Texts) common.OutboxPublisher {
		return NewMonthlyDigestPublisher(c, ch, tx)
	}, common.MonthlyDigestNotification{
		ChatID: -1, Year: 2026, Month: 3,
		MonthStandings: []common.DigestStanding{digestRow("Мартовский", 30)},
		YearStandings:  []common.DigestStanding{digestRow("Годовой", 120)},
	})

	if len(calls) != 1 {
		t.Fatalf("expected one message, got %d", len(calls))
	}
	text, _ := calls[0]["text"].(string)
	if !strings.Contains(text, "Мартовский") || !strings.Contains(text, "Годовой") {
		t.Fatalf("expected both boards in the digest, got %q", text)
	}
	// The buttons are the point of sending it in-chat rather than as a
	// bare leaderboard: one tap into the full month, one into the year.
	markup, _ := json.Marshal(calls[0]["reply_markup"])
	for _, want := range []string{"stats:month:2026-03", "stats:year:2026"} {
		if !strings.Contains(string(markup), want) {
			t.Fatalf("expected a %q button, got %s", want, markup)
		}
	}
}

// A chat in its first month has no year board yet; the digest must simply
// leave that section out rather than print an empty one.
func TestMonthlyDigestPublisher_OmitsTheYearBoardWhenThereIsNone(t *testing.T) {
	calls := publishTo(t, func(c *Client, ch chat.Repository, tx *Texts) common.OutboxPublisher {
		return NewMonthlyDigestPublisher(c, ch, tx)
	}, common.MonthlyDigestNotification{
		ChatID: -1, Year: 2026, Month: 3,
		MonthStandings: []common.DigestStanding{digestRow("Мартовский", 30)},
	})

	text, _ := calls[0]["text"].(string)
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, texts.Get("digest.year_progress", common.LocaleRU, 2026)) {
		t.Fatalf("expected no year section without year standings, got %q", text)
	}
}

func TestAnnualDigestPublisher_SummarisesTheYearAndItsDecember(t *testing.T) {
	calls := publishTo(t, func(c *Client, ch chat.Repository, tx *Texts) common.OutboxPublisher {
		return NewAnnualDigestPublisher(c, ch, tx)
	}, common.AnnualDigestNotification{
		ChatID: -1, Year: 2026,
		DecemberStandings: []common.DigestStanding{digestRow("Декабрьский", 25)},
		YearStandings:     []common.DigestStanding{digestRow("Годовой", 300)},
		Participants:      7, Predictions: 210,
	})

	text, _ := calls[0]["text"].(string)
	for _, want := range []string{"Декабрьский", "Годовой", "7", "210"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in the annual digest, got %q", want, text)
		}
	}
}

// The eve nudge exists to get people to look at tomorrow's schedule, so the
// schedule itself has to be in it.
func TestEventEvePublisher_ListsTheOpeningDayAndInvitesAction(t *testing.T) {
	topic := int64(42)
	calls := publishTo(t, func(c *Client, ch chat.Repository, tx *Texts) common.OutboxPublisher {
		return NewEventEvePublisher(c, ch, tx)
	}, common.EventEveNotification{
		ChatID: -1, TopicID: &topic, EventName: "IEM Katowice",
		StartsAt: "10:00", FirstDayMatches: 2,
		Matches: []common.EventEveMatchNotification{
			{LocalTime: "10:00", FirstTeam: "G2", SecondTeam: "NAVI"},
			{LocalTime: "13:00", FirstTeam: "Spirit", SecondTeam: "Vitality"},
		},
		Champion: "Прошлый чемпион",
	})

	if len(calls) != 1 {
		t.Fatalf("expected one message, got %d", len(calls))
	}
	text, _ := calls[0]["text"].(string)
	for _, want := range []string{"IEM Katowice", "G2", "Vitality", "13:00", "Прошлый чемпион"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in the eve nudge, got %q", want, text)
		}
	}
	if calls[0]["message_thread_id"] != float64(topic) && calls[0]["message_thread_id"] != topic {
		t.Fatalf("expected the nudge in the tournament's own topic, got %v", calls[0]["message_thread_id"])
	}
	markup, _ := json.Marshal(calls[0]["reply_markup"])
	if !strings.Contains(string(markup), "menu:upcoming") {
		t.Fatalf("expected the one-tap route into the schedule, got %s", markup)
	}
}
