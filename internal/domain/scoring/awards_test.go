package scoring

import (
	"testing"

	"cs2predictor/internal/platform/common"
)

func standing(id int64, name string, points, exact, correct, predictions int) UserStanding {
	return UserStanding{
		UserID: common.UserID{Value: id}, DisplayName: name, Points: points,
		ExactPredictions: exact, CorrectPredictions: correct, Predictions: predictions,
	}
}

func specialsFor(users ...EventUserSpecials) EventSpecials {
	return EventSpecials{TotalPolls: 10, Users: users}
}

func kinds(awards []EventAward) []string {
	out := make([]string, 0, len(awards))
	for _, a := range awards {
		out = append(out, a.Kind)
	}
	return out
}

func winnerOf(awards []EventAward, kind string) *EventAward {
	for i := range awards {
		if awards[i].Kind == kind {
			return &awards[i]
		}
	}
	return nil
}

// The rarest achievement leads. Being the only person in the chat who
// called a match is weighted above volume precisely so a recap opens with
// the thing worth talking about.
func TestPickEventAwards_RarestAchievementsWin(t *testing.T) {
	standings := []UserStanding{
		standing(1, "alpha", 30, 1, 9, 10),
		standing(2, "beta", 20, 0, 6, 10),
		standing(3, "gamma", 10, 0, 3, 10),
	}
	specials := specialsFor(
		EventUserSpecials{UserID: common.UserID{Value: 2}, DisplayName: "beta", LoneCorrect: 2, VotedPolls: 10},
		EventUserSpecials{UserID: common.UserID{Value: 3}, DisplayName: "gamma", ContrarianWins: 2, VotedPolls: 10},
	)

	awards := PickEventAwards(standings, specials, 3)

	if len(awards) != 3 {
		t.Fatalf("awards = %v, want three", kinds(awards))
	}
	if awards[0].Kind != AwardLoneVoice || awards[0].DisplayName != "beta" {
		t.Fatalf("expected the lone-voice nomination to lead, got %+v", awards[0])
	}
}

// Nobody collects two titles: a runaway winner sweeping the section reads
// as padding rather than as a recap of the tournament.
func TestPickEventAwards_OnePersonCannotWinTwice(t *testing.T) {
	dominant := standing(1, "alpha", 40, 5, 10, 10)
	standings := []UserStanding{dominant, standing(2, "beta", 12, 0, 4, 9)}
	specials := specialsFor(
		EventUserSpecials{UserID: common.UserID{Value: 1}, DisplayName: "alpha", LoneCorrect: 3, ContrarianWins: 3, LongestStreak: 8, VotedPolls: 10},
		EventUserSpecials{UserID: common.UserID{Value: 2}, DisplayName: "beta", ContrarianWins: 2, VotedPolls: 9},
	)

	awards := PickEventAwards(standings, specials, 3)

	seen := map[string]int{}
	for _, a := range awards {
		seen[a.DisplayName]++
	}
	if seen["alpha"] > 1 {
		t.Fatalf("alpha won %d titles, want at most one: %v", seen["alpha"], kinds(awards))
	}
	if len(awards) == 0 {
		t.Fatal("expected at least one award")
	}
}

// Thresholds keep a single lucky call from becoming an accolade: a
// two-match group stage with one correct guess each produces nothing.
func TestPickEventAwards_TooLittleDataProducesNothing(t *testing.T) {
	standings := []UserStanding{standing(1, "alpha", 3, 0, 1, 2), standing(2, "beta", 3, 0, 1, 2)}
	specials := EventSpecials{TotalPolls: 2, Users: []EventUserSpecials{
		{UserID: common.UserID{Value: 1}, DisplayName: "alpha", ContrarianWins: 1, VotedPolls: 2, LongestStreak: 1},
	}}

	if awards := PickEventAwards(standings, specials, 3); len(awards) != 0 {
		t.Fatalf("expected no nominations from two matches, got %v", kinds(awards))
	}
}

// A perfect record and a merely good one are different nominations, and
// the perfect one must not also be offered as "best accuracy".
func TestPickEventAwards_FlawlessAndSniperAreDistinct(t *testing.T) {
	standings := []UserStanding{
		standing(1, "alpha", 30, 0, 6, 6), // perfect
		standing(2, "beta", 20, 0, 7, 9),  // best non-perfect accuracy
	}

	awards := PickEventAwards(standings, specialsFor(), 3)

	flawless := winnerOf(awards, AwardFlawless)
	if flawless == nil || flawless.DisplayName != "alpha" || flawless.Value != 6 {
		t.Fatalf("expected alpha's perfect record, got %+v", awards)
	}
	if sniper := winnerOf(awards, AwardSniper); sniper != nil && sniper.DisplayName == "alpha" {
		t.Fatalf("a perfect record must not also take the accuracy title: %+v", awards)
	}
}

// The accuracy nomination carries its sample: "78%" means nothing without
// knowing it came from nine predictions rather than two.
func TestPickEventAwards_AccuracyCarriesItsSample(t *testing.T) {
	standings := []UserStanding{standing(2, "beta", 20, 0, 7, 9)}

	awards := PickEventAwards(standings, specialsFor(), 3)

	sniper := winnerOf(awards, AwardSniper)
	if sniper == nil {
		t.Fatalf("expected an accuracy nomination, got %v", kinds(awards))
	}
	if sniper.Value != 78 || sniper.Detail != 9 {
		t.Fatalf("sniper = %+v, want 78%% of 9", *sniper)
	}
}

// "Voted in every match" is measured against the tournament's poll count,
// not against the votes that happen to exist.
func TestPickEventAwards_IronmanNeedsEveryPoll(t *testing.T) {
	standings := []UserStanding{standing(1, "alpha", 10, 0, 3, 9)}
	nearly := EventSpecials{TotalPolls: 10, Users: []EventUserSpecials{
		{UserID: common.UserID{Value: 1}, DisplayName: "alpha", VotedPolls: 9},
	}}
	if winnerOf(PickEventAwards(standings, nearly, 3), AwardIronman) != nil {
		t.Fatal("nine of ten polls is not every poll")
	}

	complete := EventSpecials{TotalPolls: 10, Users: []EventUserSpecials{
		{UserID: common.UserID{Value: 1}, DisplayName: "alpha", VotedPolls: 10},
	}}
	if winnerOf(PickEventAwards(standings, complete, 3), AwardIronman) == nil {
		t.Fatal("expected the attendance nomination when every poll was voted in")
	}
}

// Same input, same output: the recap must not shuffle between two runs of
// the same completion pass.
func TestPickEventAwards_IsDeterministic(t *testing.T) {
	standings := []UserStanding{
		standing(1, "alpha", 30, 2, 8, 10),
		standing(2, "beta", 28, 2, 8, 10),
	}
	specials := specialsFor(
		EventUserSpecials{UserID: common.UserID{Value: 1}, DisplayName: "alpha", ContrarianWins: 2, LongestStreak: 4, VotedPolls: 10},
		EventUserSpecials{UserID: common.UserID{Value: 2}, DisplayName: "beta", ContrarianWins: 2, LongestStreak: 4, VotedPolls: 10},
	)

	first := kinds(PickEventAwards(standings, specials, 3))
	for i := 0; i < 5; i++ {
		if got := kinds(PickEventAwards(standings, specials, 3)); len(got) != len(first) {
			t.Fatalf("run %d = %v, want %v", i, got, first)
		} else {
			for j := range got {
				if got[j] != first[j] {
					t.Fatalf("run %d = %v, want %v", i, got, first)
				}
			}
		}
	}
}

// The pool is larger than the shown limit on purpose — that is what makes
// two tournaments in a row read differently.
func TestPickEventAwards_PoolIsLargerThanWhatIsShown(t *testing.T) {
	standings := []UserStanding{
		standing(1, "alpha", 30, 3, 8, 10),
		standing(2, "beta", 25, 0, 7, 9),
		standing(3, "gamma", 20, 0, 6, 10),
		standing(4, "delta", 18, 0, 5, 10),
		standing(5, "epsilon", 15, 0, 5, 5),
	}
	specials := specialsFor(
		EventUserSpecials{UserID: common.UserID{Value: 3}, DisplayName: "gamma", ContrarianWins: 4, VotedPolls: 10},
		EventUserSpecials{UserID: common.UserID{Value: 4}, DisplayName: "delta", LoneCorrect: 2, VotedPolls: 10},
		EventUserSpecials{UserID: common.UserID{Value: 2}, DisplayName: "beta", LongestStreak: 6, VotedPolls: 9},
	)

	shown := PickEventAwards(standings, specials, 3)
	if len(shown) != 3 {
		t.Fatalf("expected three shown, got %v", kinds(shown))
	}
	all := PickEventAwards(standings, specials, 99)
	if len(all) <= len(shown) {
		t.Fatalf("expected the pool to hold more than it shows, got %v", kinds(all))
	}
}
