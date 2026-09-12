package pandascore

import (
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
)

func TestMapMatch(t *testing.T) {
	beginAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	matchType := "best_of"
	games := 3
	dto := matchDTO{
		ID: 9, Name: strPtr("A vs B"), Status: "finished", BeginAt: &beginAt,
		MatchType: &matchType, NumberOfGames: &games,
		Serie:      namedDTO{ID: 4, Name: "Event"},
		Tournament: &namedDTO{ID: 5, Name: "Final"},
		Opponents:  []opponentDTO{{Opponent: &namedDTO{ID: 1, Name: "A"}}, {Opponent: &namedDTO{ID: 2, Name: "B"}}},
		Results:    []resultDTO{{TeamID: 1, Score: 2}, {TeamID: 2, Score: 1}},
	}

	match := mapMatch(dto)

	if match.Status != competition.MatchFinished {
		t.Errorf("status = %s, want FINISHED", match.Status)
	}
	if match.Format.Kind != competition.BestOf || match.Format.Size != 3 {
		t.Errorf("format = %+v, want BEST_OF 3", match.Format)
	}
	if match.Score == nil || match.Score.First != 2 || match.Score.Second != 1 {
		t.Errorf("score = %+v, want 2:1", match.Score)
	}
	if match.Stage == nil || *match.Stage != "Final" {
		t.Errorf("stage = %v, want Final", match.Stage)
	}
}

func TestMapMatch_MissingResultsEntryLeavesScoreUnset(t *testing.T) {
	// A real incident: PandaScore's results[] aggregate can lag the match's
	// own status flipping to "finished" (observed right after a BO3
	// decider), omitting one team's entry entirely. A plain map lookup
	// would silently read that as a 0, turning a true 1:2 into a false
	// 0:2 — this must instead leave the score unresolved so the next sync
	// tick retries once PandaScore's data has caught up, rather than
	// locking in a wrong score.
	beginAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	matchType := "best_of"
	games := 3
	dto := matchDTO{
		ID: 9, Name: strPtr("A vs B"), Status: "finished", BeginAt: &beginAt,
		MatchType: &matchType, NumberOfGames: &games,
		Serie:      namedDTO{ID: 4, Name: "Event"},
		Tournament: &namedDTO{ID: 5, Name: "Final"},
		Opponents:  []opponentDTO{{Opponent: &namedDTO{ID: 1, Name: "A"}}, {Opponent: &namedDTO{ID: 2, Name: "B"}}},
		// Only team 2's entry is present — team 1's is missing entirely.
		Results: []resultDTO{{TeamID: 2, Score: 2}},
	}

	match := mapMatch(dto)

	if match.Score != nil {
		t.Errorf("score = %+v, want nil (unresolved) when one team's result is missing", match.Score)
	}
}

func strPtr(s string) *string { return &s }

func TestMapEvent_AppendsYearWhenNotAlreadyInName(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	year := 2027
	dto := seriesDTO{ID: 12, Name: "IEM Cologne", Year: &year,
		BeginAt: timePtr(time.Date(2027, 1, 1, 10, 0, 0, 0, time.UTC)),
		EndAt:   timePtr(time.Date(2027, 1, 10, 10, 0, 0, 0, time.UTC))}

	event := mapEvent(dto, now)
	if event.Name != "IEM Cologne 2027" {
		t.Errorf("name = %q, want %q", event.Name, "IEM Cologne 2027")
	}
}

func timePtr(t time.Time) *time.Time { return &t }
