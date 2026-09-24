package scoring

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/platform/common"
)

func TestMostActiveChat(t *testing.T) {
	activity := []ChatActivity{
		{ChatID: common.ChatID{Value: 1}, Predictions: 5},
		{ChatID: common.ChatID{Value: 2}, Predictions: 12},
		{ChatID: common.ChatID{Value: 3}, Predictions: 12},
	}
	best, ok := MostActiveChat(activity)
	if !ok {
		t.Fatal("expected a winner")
	}
	// Tie between chat 2 and 3 breaks on the lower chat id.
	if best.ChatID.Value != 2 {
		t.Fatalf("expected chat 2 to win the tie, got %d", best.ChatID.Value)
	}
}

func TestMostActiveChat_Empty(t *testing.T) {
	if _, ok := MostActiveChat(nil); ok {
		t.Fatal("expected no winner for an empty period")
	}
}

func TestBestTeamWinRate(t *testing.T) {
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	preds := []UserPrediction{
		{TeamID: teamA, TeamName: "A", Correct: true},
		{TeamID: teamA, TeamName: "A", Correct: true},
		{TeamID: teamA, TeamName: "A", Correct: false},
		{TeamID: teamB, TeamName: "B", Correct: true},
	}
	best, ok := BestTeamWinRate(preds, teamA)
	if !ok {
		t.Fatal("expected a result")
	}
	if best.Predictions != 3 || best.Correct != 2 {
		t.Fatalf("expected 2/3 for team A, got %+v", best)
	}
}

func TestBestTeamWinRate_BelowMinimumSample(t *testing.T) {
	teamA := common.TeamID{Value: uuid.New()}
	preds := []UserPrediction{{TeamID: teamA, TeamName: "A", Correct: true}}
	if _, ok := BestTeamWinRate(preds, teamA); ok {
		t.Fatal("expected no result below InsightsTeamMinPredictions")
	}
}

func TestLongestCorrectStreak(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	userA := common.UserID{Value: 1}
	userB := common.UserID{Value: 2}
	byUser := map[common.UserID]struct {
		DisplayName string
		Predictions []UserPrediction
	}{
		userA: {DisplayName: "Alice", Predictions: []UserPrediction{
			{PlayedAt: now.Add(-3 * time.Hour), Correct: true},
			{PlayedAt: now.Add(-2 * time.Hour), Correct: true},
			{PlayedAt: now.Add(-1 * time.Hour), Correct: true},
		}},
		userB: {DisplayName: "Bob", Predictions: []UserPrediction{
			{PlayedAt: now.Add(-2 * time.Hour), Correct: true},
			{PlayedAt: now.Add(-1 * time.Hour), Correct: false},
		}},
	}
	award, ok := LongestCorrectStreak(byUser, now)
	if !ok {
		t.Fatal("expected a winner")
	}
	if award.UserID != userA || award.Streak != 3 {
		t.Fatalf("expected Alice with streak 3, got %+v", award)
	}
}

func TestLongestCorrectStreak_NoneActive(t *testing.T) {
	userA := common.UserID{Value: 1}
	byUser := map[common.UserID]struct {
		DisplayName string
		Predictions []UserPrediction
	}{
		userA: {DisplayName: "Alice", Predictions: []UserPrediction{{Correct: false}}},
	}
	if _, ok := LongestCorrectStreak(byUser, time.Now()); ok {
		t.Fatal("expected no winner when nobody has a live streak")
	}
}

func TestBiggestUpsetCalled(t *testing.T) {
	now := time.Now()
	calls := []UpsetCall{
		{UserID: common.UserID{Value: 1}, UnderdogRankGap: 10, PlayedAt: now},
		{UserID: common.UserID{Value: 2}, UnderdogRankGap: 40, PlayedAt: now.Add(time.Hour)},
		{UserID: common.UserID{Value: 3}, UnderdogRankGap: 40, PlayedAt: now}, // earlier tie
	}
	best, ok := BiggestUpsetCalled(calls)
	if !ok {
		t.Fatal("expected a winner")
	}
	if best.UserID.Value != 3 {
		t.Fatalf("expected the earlier of the tied biggest upsets, got user %d", best.UserID.Value)
	}
}

func TestBiggestUpsetCalled_NoRealUpset(t *testing.T) {
	calls := []UpsetCall{{UnderdogRankGap: 0}, {UnderdogRankGap: -3}}
	if _, ok := BiggestUpsetCalled(calls); ok {
		t.Fatal("expected no nomination when nothing was actually an upset")
	}
}
