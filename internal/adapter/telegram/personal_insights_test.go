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
	handler, personal, calls := setupInsightsTest(t, []scoring.UserPrediction{
		{PlayedAt: day(1), TeamName: "NAVI", Correct: true},
		{PlayedAt: day(2), TeamName: "NAVI", Correct: true},
		{PlayedAt: day(3), TeamName: "NAVI", Correct: true},
		{PlayedAt: day(40), TeamName: "G2", Correct: false},
		{PlayedAt: day(41), TeamName: "G2", Correct: false},
		{PlayedAt: day(42), TeamName: "G2", Correct: true},
	})

	if err := handler.handlePrivateCallback(context.Background(), insightsCallback()); err != nil {
		t.Fatal(err)
	}

	if personal.predictionLimit != scoring.InsightsMaxPredictions {
		t.Fatalf("asked for %d predictions, want the bounded %d", personal.predictionLimit, scoring.InsightsMaxPredictions)
	}
	text := lastText(*calls)
	for _, want := range []string{
		handler.Texts.Get("insights.streak_current", common.LocaleRU, 3),
		handler.Texts.Get("insights.streak_longest", common.LocaleRU, 3),
		"NAVI",
		"G2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("insights screen %q is missing %q", text, want)
		}
	}
	// Recent 100% against a previous 33% is rendered as a numeric delta,
	// without another decorative marker competing with the data.
	if !strings.Contains(text, "+67") {
		t.Fatalf("expected a +67 pp trend delta in %q", text)
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
