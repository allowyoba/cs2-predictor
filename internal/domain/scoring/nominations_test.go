package scoring

import (
	"testing"
	"time"

	"cs2predictor/internal/platform/common"
)

func nomFact(user int64, chat int64, day int, correct bool) PredictionFact {
	return PredictionFact{
		UserID: common.UserID{Value: user}, UserName: "u", ChatID: common.ChatID{Value: chat},
		PlayedAt: time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC), Correct: correct,
	}
}

func intp(v int) *int { return &v }

func TestNominateEmpty(t *testing.T) {
	if got := Nominate(nil); got != nil {
		t.Fatalf("got %v", got)
	}
}

func TestNominatePicksWinners(t *testing.T) {
	var facts []PredictionFact
	// User 1: 10 predictions, 9 correct, streak 6 (days 1-6), miss on 7.
	for d := 1; d <= 10; d++ {
		facts = append(facts, nomFact(1, 100, d, d != 7))
	}
	// User 2: 12 predictions, 6 correct — more active, worse accuracy.
	for d := 1; d <= 12; d++ {
		facts = append(facts, nomFact(2, 200, d, d%2 == 0))
	}
	upset := nomFact(2, 200, 13, true)
	upset.PickedRank, upset.OpponentRank = intp(40), intp(3)
	upset.PickedTeam, upset.OpponentTeam = "Underdogs", "Giants"
	facts = append(facts, upset)

	got := map[NominationKind]Nomination{}
	for _, n := range Nominate(facts) {
		got[n.Kind] = n
	}
	if n := got[NominationBestAccuracy]; n.UserID.Value != 1 || n.Value != 90 {
		t.Errorf("accuracy: %+v", n)
	}
	if n := got[NominationMostActive]; n.UserID.Value != 2 || n.Value != 13 {
		t.Errorf("active: %+v", n)
	}
	if n := got[NominationBestStreak]; n.UserID.Value != 1 || n.Value != 6 {
		t.Errorf("streak: %+v", n)
	}
	if n := got[NominationBiggestUpset]; n.Value != 37 || n.Picked != "Underdogs" {
		t.Errorf("upset: %+v", n)
	}
	if n := got[NominationActiveChat]; n.ChatID.Value != 200 || n.Value != 13 {
		t.Errorf("chat: %+v", n)
	}
}

func TestNominateSkipsSmallSamples(t *testing.T) {
	facts := []PredictionFact{nomFact(1, 1, 1, true), nomFact(1, 1, 2, true)}
	for _, n := range Nominate(facts) {
		if n.Kind == NominationBestAccuracy || n.Kind == NominationBestStreak || n.Kind == NominationBiggestUpset {
			t.Fatalf("unexpected %+v on a tiny sample", n)
		}
	}
}
