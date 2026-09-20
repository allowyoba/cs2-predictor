package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// "My form" is one person's record, and somebody who plays two games is
// keeping two of them: being good at reading Dota 2 says nothing about
// reading Counter-Strike, so a single merged percentage is an average of
// two unrelated things rather than a summary of either.

// insightPredictions builds a history: n predictions in a game, of which
// correct are right.
func insightPredictions(game competition.GameCode, n, correct int, from time.Time) []scoring.UserPrediction {
	out := make([]scoring.UserPrediction, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, scoring.UserPrediction{
			PlayedAt: from.Add(-time.Duration(i) * time.Hour),
			TeamID:   common.NewTeamID(), TeamName: string(game) + " team",
			Correct: i < correct, Game: game,
		})
	}
	return out
}

// stubScoringWithInsights is the test scoring repository plus the one
// personal-insights read this screen makes.
type stubScoringWithInsights struct {
	fakeScoring
	predictions []scoring.UserPrediction
}

func (s stubScoringWithInsights) UserPredictions(context.Context, common.UserID, int) ([]scoring.UserPrediction, error) {
	return s.predictions, nil
}

func renderInsights(t *testing.T, predictions []scoring.UserPrediction, game competition.GameCode) (string, string) {
	t.Helper()
	server, calls := newRecordingServer(t)
	t.Cleanup(server.Close)
	h, _ := newTestHandler(t, server)
	h.Scoring = stubScoringWithInsights{predictions: predictions}
	if err := h.renderPersonalInsights(context.Background(), sendTarget(common.ChatID{Value: 7}, nil),
		common.UserID{Value: 7}, common.LocaleRU, game); err != nil {
		t.Fatal(err)
	}
	var markup []byte
	for i := len(*calls) - 1; i >= 0; i-- {
		if _, ok := (*calls)[i]["reply_markup"]; ok {
			markup, _ = json.Marshal((*calls)[i]["reply_markup"])
			break
		}
	}
	return lastText(*calls), string(markup)
}

func TestPersonalInsights_SplitsByGameOnlyWhenThereIsMoreThanOne(t *testing.T) {
	now := time.Now()
	oneGame := insightPredictions(competition.GameCS2, 12, 9, now)

	text, markup := renderInsights(t, oneGame, "")
	if strings.Contains(markup, "pstats:insights:") {
		t.Fatalf("one game is one record — no picker belongs here: %s", markup)
	}
	if strings.Contains(text, ru(t, "insights.by_game_heading")) {
		t.Fatalf("the breakdown would just repeat the card above it: %q", text)
	}

	twoGames := append(oneGame, insightPredictions(competition.GameDota2, 8, 2, now)...)
	text, markup = renderInsights(t, twoGames, "")
	if !strings.Contains(markup, "pstats:insights:"+string(competition.GameCS2)) ||
		!strings.Contains(markup, "pstats:insights:"+string(competition.GameDota2)) {
		t.Fatalf("expected a button per game, got %s", markup)
	}
	if !strings.Contains(text, ru(t, "insights.by_game_heading")) {
		t.Fatalf("the combined card must say what it is an average of: %q", text)
	}
	// Both games' own numbers are on the combined view, so the disagreement
	// is visible without tapping.
	if !strings.Contains(text, ru(t, "game.cs2_short")) || !strings.Contains(text, ru(t, "game.dota2_short")) {
		t.Fatalf("expected both games named in the breakdown: %q", text)
	}
}

func TestPersonalInsights_OneGamesCardIsAboutThatGameAlone(t *testing.T) {
	now := time.Now()
	// Ten CS2 predictions, all correct; four Dota 2 ones, all wrong. A
	// merged view would read as 71% and describe neither.
	history := append(insightPredictions(competition.GameCS2, 10, 10, now),
		insightPredictions(competition.GameDota2, 4, 0, now)...)

	text, _ := renderInsights(t, history, competition.GameDota2)

	if !strings.Contains(text, ru(t, "game.dota2_short")) {
		t.Fatalf("a narrowed card must name its game: %q", text)
	}
	if !strings.Contains(text, "0%") {
		t.Fatalf("expected Dota 2's own record, not the blended one: %q", text)
	}
	if strings.Contains(text, ru(t, "insights.by_game_heading")) {
		t.Fatalf("the breakdown belongs to the combined view only: %q", text)
	}
}

// A button from an older message naming a game the person has since
// stopped playing must open the combined view, not an empty card.
func TestPersonalInsights_StaleGameFallsBackToTheCombinedView(t *testing.T) {
	text, markup := renderInsights(t, insightPredictions(competition.GameCS2, 6, 3, time.Now()), competition.GameDota2)

	if strings.Contains(text, ru(t, "insights.empty")) {
		t.Fatalf("expected the combined view rather than an empty card: %q", text)
	}
	if strings.Contains(markup, "pstats:insights:") {
		t.Fatalf("with one game played there is still nothing to pick: %s", markup)
	}
}
