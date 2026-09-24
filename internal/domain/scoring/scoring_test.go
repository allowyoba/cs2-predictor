package scoring

import (
	"testing"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// Exact scoring rules the leaderboard depends on.
func TestCalculate(t *testing.T) {
	bo5, _ := competition.NewSeriesFormat(competition.BestOf, 5)
	fixedMaps2, _ := competition.NewSeriesFormat(competition.FixedMaps, 2)
	fixedMaps4, _ := competition.NewSeriesFormat(competition.FixedMaps, 4)

	t.Run("BO5 exact score", func(t *testing.T) {
		points, kind, ok := Calculate(competition.MatchScore{First: 3, Second: 1}, competition.MatchScore{First: 3, Second: 1}, bo5)
		if !ok || points != 3 || kind != AwardExactScore {
			t.Fatalf("got points=%d kind=%s ok=%v, want 3 EXACT_SCORE true", points, kind, ok)
		}
	})
	t.Run("BO5 correct winner wrong score", func(t *testing.T) {
		points, kind, ok := Calculate(competition.MatchScore{First: 3, Second: 0}, competition.MatchScore{First: 3, Second: 1}, bo5)
		if !ok || points != 1 || kind != AwardOutcome {
			t.Fatalf("got points=%d kind=%s ok=%v, want 1 OUTCOME true", points, kind, ok)
		}
	})
	t.Run("BO5 wrong winner", func(t *testing.T) {
		_, _, ok := Calculate(competition.MatchScore{First: 0, Second: 3}, competition.MatchScore{First: 3, Second: 1}, bo5)
		if ok {
			t.Fatal("expected no award for wrong winner")
		}
	})
	t.Run("FIXED_MAPS(2) exact draw", func(t *testing.T) {
		points, kind, ok := Calculate(competition.MatchScore{First: 1, Second: 1}, competition.MatchScore{First: 1, Second: 1}, fixedMaps2)
		if !ok || points != 2 || kind != AwardExactScore {
			t.Fatalf("got points=%d kind=%s ok=%v, want 2 EXACT_SCORE true", points, kind, ok)
		}
	})
	t.Run("FIXED_MAPS(4) correct draw outcome wrong exact score", func(t *testing.T) {
		points, kind, ok := Calculate(competition.MatchScore{First: 1, Second: 1}, competition.MatchScore{First: 2, Second: 2}, fixedMaps4)
		if !ok || points != 1 || kind != AwardOutcome {
			t.Fatalf("got points=%d kind=%s ok=%v, want 1 OUTCOME true", points, kind, ok)
		}
	})
}

func TestUserStandingAccuracyPercent(t *testing.T) {
	tests := []struct {
		name string
		s    UserStanding
		want int
	}{
		{name: "no votes", s: UserStanding{}, want: 0},
		{name: "all correct", s: UserStanding{CorrectPredictions: 4, Predictions: 4}, want: 100},
		{name: "rounded", s: UserStanding{CorrectPredictions: 4, Predictions: 7}, want: 57},
		{name: "one third", s: UserStanding{CorrectPredictions: 1, Predictions: 3}, want: 33},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.AccuracyPercent(); got != tt.want {
				t.Fatalf("AccuracyPercent() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestUserChatStandingAccuracyPercent(t *testing.T) {
	s := UserChatStanding{CorrectPredictions: 2, Predictions: 3}
	if got := s.AccuracyPercent(); got != 67 {
		t.Fatalf("AccuracyPercent() = %d, want 67", got)
	}
	if got := (UserChatStanding{}).AccuracyPercent(); got != 0 {
		t.Fatalf("zero-vote AccuracyPercent() = %d, want 0", got)
	}
}

// Ground truth: ties share rank (dense ranking); reordering equal-points
// rows by different tie-breakers still yields the same rank sequence.
func TestDenseRank(t *testing.T) {
	u := func(id int64, points, exact, predictions int) UserStanding {
		return UserStanding{UserID: common.UserID{Value: id}, Points: points, ExactPredictions: exact, Predictions: predictions}
	}

	rows := []UserStanding{u(1, 20, 1, 5), u(2, 20, 0, 5), u(3, 18, 0, 5)}
	ranked := DenseRank(rows)
	got := []int{ranked[0].Rank, ranked[1].Rank, ranked[2].Rank}
	if want := []int{1, 1, 2}; got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("ranks = %v, want %v", got, want)
	}

	rows2 := []UserStanding{u(1, 10, 0, 3), u(2, 10, 0, 5), u(3, 9, 0, 1)}
	ranked2 := DenseRank(rows2)
	got2 := []int{ranked2[0].Rank, ranked2[1].Rank, ranked2[2].Rank}
	if want := []int{1, 1, 2}; got2[0] != want[0] || got2[1] != want[1] || got2[2] != want[2] {
		t.Fatalf("ranks = %v, want %v", got2, want)
	}
}

func TestDenseRankChats(t *testing.T) {
	c := func(id int64, points, exact, predictions int) ChatStanding {
		return ChatStanding{ChatID: common.ChatID{Value: id}, Points: points, ExactPredictions: exact, Predictions: predictions}
	}

	rows := []ChatStanding{c(1, 20, 1, 5), c(2, 20, 0, 5), c(3, 18, 0, 5)}
	ranked := DenseRankChats(rows)
	got := []int{ranked[0].Rank, ranked[1].Rank, ranked[2].Rank}
	if want := []int{1, 1, 2}; got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("ranks = %v, want %v", got, want)
	}
	// Tie-break: equal points, higher exact-predictions sorts first even
	// though both share rank 1.
	if ranked[0].ChatID.Value != 1 {
		t.Fatalf("ranked[0].ChatID = %d, want 1 (higher exact count breaks the tie)", ranked[0].ChatID.Value)
	}
}

func TestChatStandingAccuracyPercent(t *testing.T) {
	if got := (ChatStanding{CorrectPredictions: 3, Predictions: 4}).AccuracyPercent(); got != 75 {
		t.Fatalf("AccuracyPercent() = %d, want 75", got)
	}
	if got := (ChatStanding{}).AccuracyPercent(); got != 0 {
		t.Fatalf("zero-prediction AccuracyPercent() = %d, want 0", got)
	}
}

func TestTeamSynergyInsightAccuracyPercent(t *testing.T) {
	if got := (TeamSynergyInsight{Correct: 3, Predictions: 4}).AccuracyPercent(); got != 75 {
		t.Fatalf("AccuracyPercent() = %d, want 75", got)
	}
	if got := (TeamSynergyInsight{}).AccuracyPercent(); got != 0 {
		t.Fatalf("zero-prediction AccuracyPercent() = %d, want 0", got)
	}
}

func TestPeriodConstructors(t *testing.T) {
	if got := AllTime(); got.Kind != PeriodAllTime {
		t.Errorf("AllTime().Kind = %s, want ALL_TIME", got.Kind)
	}
	if got := ForYear(2026); got.Kind != PeriodYear || got.Year != 2026 {
		t.Errorf("ForYear(2026) = %+v, want Kind=YEAR Year=2026", got)
	}
	if got := ForMonth(2026, 9); got.Kind != PeriodMonth || got.Year != 2026 || got.Month != 9 {
		t.Errorf("ForMonth(2026, 9) = %+v, want Kind=MONTH Year=2026 Month=9", got)
	}
	eventID := common.NewEventID()
	if got := ForEvent(eventID); got.Kind != PeriodEvent || got.EventID != eventID {
		t.Errorf("ForEvent(id) = %+v, want Kind=EVENT EventID=%v", got, eventID)
	}
	day := common.SystemUTCClock().Now()
	if got := ForDay(day); got.Kind != PeriodDay || !got.Day.Equal(day) {
		t.Errorf("ForDay(day) = %+v, want Kind=DAY Day=%v", got, day)
	}
}
