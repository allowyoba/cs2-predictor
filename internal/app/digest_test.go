package app

import (
	"testing"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

func TestDueAnnualDigest_LocalTwentyHundredAndRecoveryWindow(t *testing.T) {
	loc := time.FixedZone("test", 3*60*60)
	cases := []struct {
		at   time.Time
		year int
		due  bool
	}{
		{time.Date(2026, time.December, 31, 19, 59, 0, 0, loc), 0, false},
		{time.Date(2026, time.December, 31, 20, 0, 0, 0, loc), 2026, true},
		{time.Date(2027, time.January, 1, 5, 59, 0, 0, loc), 2026, true},
		{time.Date(2027, time.January, 1, 6, 0, 0, 0, loc), 0, false},
	}
	for _, tc := range cases {
		year, due := dueAnnualDigest(tc.at)
		if year != tc.year || due != tc.due {
			t.Fatalf("dueAnnualDigest(%s) = (%d,%v), want (%d,%v)", tc.at, year, due, tc.year, tc.due)
		}
	}
}

func TestDueMonthlyDigest_LastDayAndRecoveryWindow(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		at    time.Time
		year  int
		month time.Month
		due   bool
	}{
		{time.Date(2026, time.September, 30, 19, 59, 0, 0, loc), 0, 0, false},
		{time.Date(2026, time.September, 30, 20, 0, 0, 0, loc), 2026, time.September, true},
		{time.Date(2026, time.October, 1, 5, 59, 0, 0, loc), 2026, time.September, true},
		{time.Date(2026, time.October, 1, 6, 0, 0, 0, loc), 0, 0, false},
	}
	for _, tc := range cases {
		year, month, due := dueMonthlyDigest(tc.at)
		if year != tc.year || month != tc.month || due != tc.due {
			t.Fatalf("dueMonthlyDigest(%s) = (%d,%s,%v), want (%d,%s,%v)", tc.at, year, month, due, tc.year, tc.month, tc.due)
		}
	}
}

func TestBuildAnnualHighlights_UsesMeaningfulSamplesAndDeterministicWinners(t *testing.T) {
	rows := []scoring.UserStanding{
		{UserID: common.UserID{Value: 2}, DisplayName: "Bob", Rank: 1, Points: 100, CorrectPredictions: 14, ExactPredictions: 8, Predictions: 20},
		{UserID: common.UserID{Value: 3}, DisplayName: "Carol", Rank: 2, Points: 90, CorrectPredictions: 18, ExactPredictions: 10, Predictions: 30},
		{UserID: common.UserID{Value: 1}, DisplayName: "Alice", Rank: 3, Points: 70, CorrectPredictions: 15, ExactPredictions: 5, Predictions: 20},
		// 100% from two predictions must not win the sniper nomination.
		{UserID: common.UserID{Value: 4}, DisplayName: "Dave", Rank: 4, Points: 10, CorrectPredictions: 2, ExactPredictions: 2, Predictions: 2},
	}
	specials := scoring.AnnualSpecials{
		ComebackBaselines: []scoring.ComebackBaseline{
			{UserID: common.UserID{Value: 1}, DisplayName: "Alice", Rank: 8},
			{UserID: common.UserID{Value: 2}, DisplayName: "Bob", Rank: 2},
			{UserID: common.UserID{Value: 3}, DisplayName: "Carol", Rank: 7},
		},
		LongestStreak: &scoring.CorrectStreakInsight{UserID: common.UserID{Value: 4}, DisplayName: "Dave", Streak: 7},
		BestTeamSynergy: &scoring.TeamSynergyInsight{
			UserID: common.UserID{Value: 2}, DisplayName: "Bob", TeamName: "Team Spirit", Correct: 6, Predictions: 7,
		},
	}

	h := buildAnnualHighlights(rows, specials)
	if h.Comeback == nil || h.Comeback.DisplayName != "Carol" || h.Comeback.StartRank != 7 || h.Comeback.FinalRank != 2 {
		t.Fatalf("unexpected comeback: %+v", h.Comeback)
	}
	if h.Sniper == nil || h.Sniper.DisplayName != "Alice" || h.Sniper.Accuracy != 75 || h.Sniper.Predictions != 20 {
		t.Fatalf("unexpected sniper: %+v", h.Sniper)
	}
	if h.Expert == nil || h.Expert.DisplayName != "Carol" || h.Expert.Value != 18 {
		t.Fatalf("unexpected expert: %+v", h.Expert)
	}
	if h.Exact == nil || h.Exact.DisplayName != "Carol" || h.Exact.Value != 10 {
		t.Fatalf("unexpected exact-score winner: %+v", h.Exact)
	}
	if h.Streak == nil || h.Streak.DisplayName != "Dave" || h.Streak.Value != 7 {
		t.Fatalf("unexpected streak: %+v", h.Streak)
	}
	if h.TeamSynergy == nil || h.TeamSynergy.DisplayName != "Bob" || h.TeamSynergy.TeamName != "Team Spirit" || h.TeamSynergy.Accuracy != 86 {
		t.Fatalf("unexpected team synergy: %+v", h.TeamSynergy)
	}
}

func TestBuildAnnualHighlights_OmitsComebackWithoutPositiveClimb(t *testing.T) {
	rows := []scoring.UserStanding{{UserID: common.UserID{Value: 1}, DisplayName: "Alice", Rank: 3, Predictions: 25}}
	specials := scoring.AnnualSpecials{ComebackBaselines: []scoring.ComebackBaseline{{UserID: common.UserID{Value: 1}, DisplayName: "Alice", Rank: 2}}}
	if got := buildAnnualHighlights(rows, specials); got.Comeback != nil {
		t.Fatalf("expected no comeback for a rank decline, got %+v", got.Comeback)
	}
}
