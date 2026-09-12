package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

func setupInsightsTest(t *testing.T, predictions []scoring.UserPrediction) (*UpdateHandler, *personalDataScoring, *[]map[string]any) {
	t.Helper()
	server, calls := newRecordingServer(t)
	t.Cleanup(server.Close)
	handler, _ := newTestHandler(t, server)
	personal := &personalDataScoring{dataScoring: &dataScoring{}, predictions: predictions}
	handler.Scoring = personal
	return handler, personal, calls
}

func insightsCallback() *CallbackQuery {
	data := "pstats:insights"
	return &CallbackQuery{ID: "cb", From: User{ID: 42, FirstName: "Alex"},
		Message: &Message{MessageID: 3, Chat: Chat{ID: 42, Type: "private"}}, Data: &data}
}

func TestPersonalInsights_RendersStreaksTeamsAndTrend(t *testing.T) {
	now := time.Now()
	day := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	navi, g2 := common.NewTeamID(), common.NewTeamID()
	handler, personal, calls := setupInsightsTest(t, []scoring.UserPrediction{
		{PlayedAt: day(1), TeamID: navi, TeamName: "NAVI", Correct: true},
		{PlayedAt: day(2), TeamID: navi, TeamName: "NAVI", Correct: true},
		{PlayedAt: day(3), TeamID: navi, TeamName: "NAVI", Correct: true},
		{PlayedAt: day(40), TeamID: g2, TeamName: "G2", Correct: false},
		{PlayedAt: day(41), TeamID: g2, TeamName: "G2", Correct: false},
		{PlayedAt: day(42), TeamID: g2, TeamName: "G2", Correct: true},
	})

	if err := handler.handlePrivateCallback(context.Background(), insightsCallback()); err != nil {
		t.Fatal(err)
	}

	if personal.predictionLimit != scoring.InsightsMaxPredictions {
		t.Fatalf("asked for %d predictions, want the bounded %d", personal.predictionLimit, scoring.InsightsMaxPredictions)
	}
	text := lastText(*calls)
	for _, want := range []string{
		handler.Texts.Get("insights.streak_warm", common.LocaleRU, 3),
		handler.Texts.Get("insights.streak_best", common.LocaleRU, 3),
		"NAVI",
		"G2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("insights screen %q is missing %q", text, want)
		}
	}
	// Recent 100% against a previous 33% is rendered as a numeric delta.
	if !strings.Contains(text, "+67") {
		t.Fatalf("expected a +67 pp trend delta in %q", text)
	}
	// The visual form guide/accuracy bar are the whole point of the redesign.
	if !strings.Contains(text, "🟩") || !strings.Contains(text, "▰") {
		t.Fatalf("expected a visual form guide and accuracy bar in %q", text)
	}
}

func TestPersonalInsights_NoSettledPredictionsSaysSo(t *testing.T) {
	handler, _, calls := setupInsightsTest(t, nil)

	if err := handler.handlePrivateCallback(context.Background(), insightsCallback()); err != nil {
		t.Fatal(err)
	}

	if text := lastText(*calls); !strings.Contains(text, handler.Texts.Get("insights.empty", common.LocaleRU)) {
		t.Fatalf("expected the empty-insights screen, got %q", text)
	}
}

// Somebody who only started predicting this month has no earlier window,
// and calling that a decline would be wrong — the screen shows their
// recent accuracy on its own instead of a comparison.
func TestPersonalInsights_WithoutAnEarlierWindowShowsNoTrendArrow(t *testing.T) {
	now := time.Now()
	handler, _, calls := setupInsightsTest(t, []scoring.UserPrediction{
		{PlayedAt: now.Add(-24 * time.Hour), TeamName: "NAVI", Correct: true},
		{PlayedAt: now.Add(-48 * time.Hour), TeamName: "NAVI", Correct: false},
	})

	if err := handler.handlePrivateCallback(context.Background(), insightsCallback()); err != nil {
		t.Fatal(err)
	}

	text := lastText(*calls)
	if strings.Contains(text, "п.п.") {
		t.Fatalf("screen compared against a window that doesn't exist: %q", text)
	}
	if !strings.Contains(text, handler.Texts.Get("insights.trend_recent_only", common.LocaleRU, 50, 2)) {
		t.Fatalf("expected the standalone recent-accuracy line, got %q", text)
	}
}

func TestAccuracyBar_FillsProportionallyAndClampsToSlots(t *testing.T) {
	cases := map[int]string{
		0:   "▱▱▱▱▱▱▱▱▱▱",
		50:  "▰▰▰▰▰▱▱▱▱▱",
		100: "▰▰▰▰▰▰▰▰▰▰",
	}
	for percent, want := range cases {
		if got := accuracyBar(percent); got != want {
			t.Fatalf("accuracyBar(%d) = %q, want %q", percent, got, want)
		}
	}
}

func TestFormGuide_RendersWinsAndLossesAsSquaresInOrder(t *testing.T) {
	got := formGuide([]bool{true, false, true})
	if want := "🟩🟥🟩"; got != want {
		t.Fatalf("formGuide = %q, want %q", got, want)
	}
	if formGuide(nil) != "" {
		t.Fatal("an empty form must render as an empty string, not a stray guide with nothing in it")
	}
}

func TestStreakLine_FramingEscalatesWithLength(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		streak int
		key    string
	}{
		{0, "insights.streak_broken"},
		{1, "insights.streak_active"},
		{3, "insights.streak_warm"},
		{5, "insights.streak_hot"},
	}
	for _, c := range cases {
		got := streakLine(texts, common.LocaleRU, c.streak)
		var want string
		if c.streak == 0 {
			want = texts.Get(c.key, common.LocaleRU)
		} else {
			want = texts.Get(c.key, common.LocaleRU, c.streak)
		}
		if got != want {
			t.Fatalf("streakLine(%d) = %q, want %q (key %s)", c.streak, got, want, c.key)
		}
	}
}

func TestPrivateStatsMenu_OffersInsights(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	personal.months = []scoring.StatsMonth{{Year: 2026, Month: time.September}}

	data := "pstats:menu"
	cb := &CallbackQuery{ID: "cb", From: User{ID: 42, FirstName: "Alex"},
		Message: &Message{MessageID: 3, Chat: Chat{ID: 42, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "pstats:insights") {
		t.Fatalf("expected an insights button on the personal stats menu, got %v", cds)
	}
}
