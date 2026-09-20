package telegram

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"cs2predictor/internal/platform/common"
)

// Golden files for the messages people actually read.
//
// The i18n parity test proves every key exists in both bundles, and the
// screen tests prove the right numbers reach the right places. Neither
// notices when an assembled message's layout quietly changes: a stray
// blank line, a heading that lost its bold, a block that moved above
// another. Those only show up to a human looking at Telegram, which is
// the worst place to find them.
//
// So the composed text of each message is written down, and any change to
// it shows up as a diff in review. Updating is deliberate:
//
//	go test ./internal/adapter/telegram/ -run Golden -update
var updateGolden = flag.Bool("update", false, "rewrite the golden files from the current output")

func goldenFile(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — run: go test ./internal/adapter/telegram/ -run Golden -update", err)
	}
	if got != string(want) {
		t.Fatalf("rendered message changed.\n--- want ---\n%s\n--- got ---\n%s\n\nIf the change is intended, re-record with -update.", want, got)
	}
}

// TestGolden_PersonalEventRecap pins the DM a participant receives when a
// tournament they played in finishes.
func TestGolden_PersonalEventRecap(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(common.EventRecapNotification{
		UserID: 7, ChatTitle: "Прогнозы", EventName: "IEM Katowice",
		Rank: 2, Participants: 11, Points: 24,
		Predictions: 9, ExactPredictions: 3, CorrectPredictions: 6,
		Award: "sniper",
	})
	if err != nil {
		t.Fatal(err)
	}

	server, calls := newRecordingServer(t)
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	publisher := NewEventRecapPublisher(client, chats, texts, nil)
	if err := publisher.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	goldenFile(t, "event_recap_personal", lastText(*calls))
}

// TestGolden_EventEveNudge pins the day-before message a chat receives.
func TestGolden_EventEveNudge(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(common.EventEveNotification{
		ChatID: -1, EventName: "IEM Katowice", StartsAt: "10:00", FirstDayMatches: 2,
		Matches: []common.EventEveMatchNotification{
			{LocalTime: "10:00", FirstTeam: "G2", SecondTeam: "NAVI"},
			{LocalTime: "13:00", FirstTeam: "Spirit", SecondTeam: "Vitality"},
		},
		Champion: "Аня",
	})
	if err != nil {
		t.Fatal(err)
	}

	server, calls := newRecordingServer(t)
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	publisher := NewEventEvePublisher(client, chats, texts)
	if err := publisher.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	goldenFile(t, "event_eve", lastText(*calls))
}

// TestGolden_MonthlyDigest pins the monthly leaderboard message.
func TestGolden_MonthlyDigest(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(common.MonthlyDigestNotification{
		ChatID: -1, Year: 2026, Month: 3,
		MonthStandings: []common.DigestStanding{
			{DisplayName: "Аня", Rank: 1, Points: 30, Predictions: 12, Accuracy: 58},
			{DisplayName: "Борис", Rank: 2, Points: 22, Predictions: 11, Accuracy: 45},
		},
		YearStandings: []common.DigestStanding{
			{DisplayName: "Аня", Rank: 1, Points: 120, Predictions: 60, Accuracy: 52},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	server, calls := newRecordingServer(t)
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	publisher := NewMonthlyDigestPublisher(client, chats, texts)
	if err := publisher.Publish(context.Background(), common.OutboxMessage{Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}

	goldenFile(t, "monthly_digest", lastText(*calls))
}

// TestGolden_PollDescription pins the block attached to every poll — the
// densest text this bot produces and the one most easily broken by an
// added line.
func TestGolden_PollDescription(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	description := composePollDescription(texts, common.LocaleRU,
		"IEM Katowice · CS2", "Playoffs", "BO3", "12.03", "18:00 MSK",
		"#3 (1820) · #8 (1412)", "12.03", "#2 · #9", "12.03",
		"4–1 · 3–2", "6–4")

	goldenFile(t, "poll_description", description)
}
