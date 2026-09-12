package scoring

import (
	"testing"
	"time"

	"cs2predictor/internal/platform/common"
)

var insightsNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// testTeamIDs gives every distinct team name in a test its own stable id —
// grouping is by TeamID now (see UserPrediction's doc comment), so tests
// need one team name to always map to the same id, and two different names
// to never collide, the same guarantee production gets from the real team
// table's primary key.
var testTeamIDs = map[string]common.TeamID{}

func teamID(name string) common.TeamID {
	if id, ok := testTeamIDs[name]; ok {
		return id
	}
	id := common.NewTeamID()
	testTeamIDs[name] = id
	return id
}

// at builds a prediction daysAgo days before insightsNow.
func at(daysAgo int, team string, correct bool) UserPrediction {
	return UserPrediction{
		PlayedAt: insightsNow.Add(-time.Duration(daysAgo) * 24 * time.Hour),
		TeamID:   teamID(team), TeamName: team, Correct: correct,
	}
}

func TestBuildPersonalInsights_StreaksReadFromTheMostRecentPrediction(t *testing.T) {
	// Deliberately shuffled: the caller must not have to sort.
	predictions := []UserPrediction{
		at(1, "NAVI", true),
		at(5, "NAVI", true),
		at(6, "G2", false),
		at(2, "NAVI", true),
		at(4, "G2", true),
		at(3, "NAVI", true),
	}

	got := BuildPersonalInsights(predictions, insightsNow)

	if got.CurrentStreak != 5 {
		t.Fatalf("CurrentStreak = %d, want 5 (everything since the loss six days ago)", got.CurrentStreak)
	}
	if got.LongestStreak != 5 {
		t.Fatalf("LongestStreak = %d, want 5", got.LongestStreak)
	}
}

func TestBuildPersonalInsights_CurrentStreakIsZeroAfterALoss(t *testing.T) {
	got := BuildPersonalInsights([]UserPrediction{
		at(3, "NAVI", true), at(2, "NAVI", true), at(1, "G2", false),
	}, insightsNow)

	if got.CurrentStreak != 0 {
		t.Fatalf("CurrentStreak = %d, want 0 — the latest prediction was wrong", got.CurrentStreak)
	}
	if got.LongestStreak != 2 {
		t.Fatalf("LongestStreak = %d, want 2", got.LongestStreak)
	}
}

// The form guide reads left to right like every other form guide.
func TestBuildPersonalInsights_RecentFormIsOldestFirstAndBounded(t *testing.T) {
	var predictions []UserPrediction
	for i := range InsightsFormSize + 5 {
		predictions = append(predictions, at(InsightsFormSize+5-i, "NAVI", i%2 == 0))
	}

	got := BuildPersonalInsights(predictions, insightsNow)

	if len(got.RecentForm) != InsightsFormSize {
		t.Fatalf("RecentForm has %d entries, want %d", len(got.RecentForm), InsightsFormSize)
	}
	// The newest prediction is the last one built, index len-1.
	want := (InsightsFormSize+4)%2 == 0
	if got.RecentForm[len(got.RecentForm)-1] != want {
		t.Fatal("RecentForm's last entry is not the most recent prediction")
	}
}

func TestBuildPersonalInsights_TeamAccuracyNeedsARealSample(t *testing.T) {
	predictions := []UserPrediction{
		at(9, "NAVI", true), at(8, "NAVI", true), at(7, "NAVI", false),
		at(6, "G2", true), at(5, "G2", true), at(4, "G2", true),
		// Two predictions is not a sample worth quoting a percentage for.
		at(3, "Vitality", true), at(2, "Vitality", true),
	}

	got := BuildPersonalInsights(predictions, insightsNow)

	if len(got.Teams) != 2 {
		t.Fatalf("got %d teams, want 2 (Vitality is below the threshold): %+v", len(got.Teams), got.Teams)
	}
	if got.Teams[0].TeamName != "G2" || got.Teams[0].AccuracyPercent() != 100 {
		t.Fatalf("best team = %+v, want G2 at 100%%", got.Teams[0])
	}
	if got.Teams[1].TeamName != "NAVI" || got.Teams[1].AccuracyPercent() != 67 {
		t.Fatalf("second team = %+v, want NAVI at 67%%", got.Teams[1])
	}
}

func TestBuildPersonalInsights_TrendComparesTwoAdjacentWindows(t *testing.T) {
	predictions := []UserPrediction{
		// Recent window: 2 of 2.
		at(3, "NAVI", true), at(4, "NAVI", true),
		// Previous window: 1 of 2.
		at(35, "NAVI", true), at(36, "NAVI", false),
	}

	got := BuildPersonalInsights(predictions, insightsNow)

	if got.Recent.Predictions != 2 || got.Recent.Correct != 2 {
		t.Fatalf("Recent = %+v, want 2 of 2", got.Recent)
	}
	if got.Previous.Predictions != 2 || got.Previous.Correct != 1 {
		t.Fatalf("Previous = %+v, want 1 of 2", got.Previous)
	}
	delta, ok := got.Trend()
	if !ok || delta != 50 {
		t.Fatalf("Trend() = %d, %v; want +50 points and comparable", delta, ok)
	}
}

// Someone who only started this month has nothing to be compared against.
// Calling that a decline would be wrong, so the comparison is withheld.
func TestPersonalInsights_TrendIsNotComparableWithoutAnEarlierWindow(t *testing.T) {
	got := BuildPersonalInsights([]UserPrediction{at(1, "NAVI", true), at(2, "NAVI", false)}, insightsNow)

	if delta, ok := got.Trend(); ok {
		t.Fatalf("Trend() reported %d as comparable with no previous window", delta)
	}
}

// Predictions older than both windows still count towards streaks and
// team accuracy — they are history, not noise.
func TestBuildPersonalInsights_OldPredictionsCountOutsideTheTrendWindows(t *testing.T) {
	got := BuildPersonalInsights([]UserPrediction{
		at(200, "NAVI", true), at(199, "NAVI", true), at(198, "NAVI", true),
	}, insightsNow)

	if got.LongestStreak != 3 {
		t.Fatalf("LongestStreak = %d, want 3", got.LongestStreak)
	}
	if len(got.Teams) != 1 {
		t.Fatalf("got %d teams, want NAVI counted despite being old", len(got.Teams))
	}
	if got.Recent.Predictions != 0 || got.Previous.Predictions != 0 {
		t.Fatal("old predictions leaked into the trend windows")
	}
}

func TestBuildPersonalInsights_EmptyHistoryHasNoData(t *testing.T) {
	if BuildPersonalInsights(nil, insightsNow).HasData() {
		t.Fatal("an empty history must not render as a screen full of zeroes")
	}
}
