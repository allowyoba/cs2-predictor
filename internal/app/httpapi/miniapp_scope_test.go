package httpapi

import (
	"net/http/httptest"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

func scopeOf(t *testing.T, query string) scope {
	t.Helper()
	selected, err := scopeFrom(httptest.NewRequest("GET", "/x"+query, nil))
	if err != nil {
		t.Fatalf("scopeFrom(%q): %v", query, err)
	}
	return selected
}

// One filter, read the same way by every endpoint. A screen that parsed it
// slightly differently is exactly how the history page ended up showing a
// different set of matches than the bar above it claimed.
func TestScope_ReadsTheSameFilterForEveryScreen(t *testing.T) {
	empty := scopeOf(t, "")
	if empty.Game != "" || empty.ChatID != nil {
		t.Fatalf("scope = %+v, want everything", empty)
	}

	narrowed := scopeOf(t, "?game=cs2&chat=-100")
	if narrowed.Game != competition.GameCS2 {
		t.Fatalf("game = %q, want CS2 whatever case it arrived in", narrowed.Game)
	}
	if narrowed.ChatID == nil || narrowed.ChatID.Value != -100 {
		t.Fatalf("chat = %v, want -100", narrowed.ChatID)
	}

	// Refused, not ignored: a screen showing every chat while the bar says
	// one is a lie the app has no way to notice.
	if _, err := scopeFrom(httptest.NewRequest("GET", "/x?chat=friends", nil)); err == nil {
		t.Fatal("an unparsable chat filter was accepted")
	}
}

func TestScope_NarrowsEveryKindOfRowTheSameWay(t *testing.T) {
	selected := scopeOf(t, "?game=cs2&chat=-100")
	inScope := common.ChatID{Value: -100}
	other := common.ChatID{Value: -200}

	if !selected.keepsBet(scoring.UserBet{Game: competition.GameCS2, ChatID: inScope}) {
		t.Error("a bet inside the scope was dropped")
	}
	if selected.keepsBet(scoring.UserBet{Game: competition.GameDota2, ChatID: inScope}) {
		t.Error("another discipline's bet survived the filter")
	}
	if selected.keepsBet(scoring.UserBet{Game: competition.GameCS2, ChatID: other}) {
		t.Error("another chat's bet survived the filter")
	}
	if selected.keepsActive(scoring.ActivePrediction{Game: competition.GameDota2, ChatID: inScope}) {
		t.Error("active predictions ignore the discipline filter")
	}
	if selected.keepsMedal(scoring.EventMedal{Game: competition.GameDota2, ChatID: inScope}) {
		t.Error("medals ignore the discipline filter")
	}

	predictions := []scoring.UserPrediction{
		{Game: competition.GameCS2, ChatID: inScope, PlayedAt: time.Now()},
		{Game: competition.GameDota2, ChatID: inScope, PlayedAt: time.Now()},
		{Game: competition.GameCS2, ChatID: other, PlayedAt: time.Now()},
	}
	if kept := selected.filterPredictions(predictions); len(kept) != 1 {
		t.Fatalf("kept %d predictions, want the one inside the scope", len(kept))
	}
	// An unnarrowed scope hands back the original slice untouched, which is
	// what makes the common case free.
	if kept := scopeOf(t, "").filterPredictions(predictions); len(kept) != 3 {
		t.Fatalf("kept %d predictions, want all of them", len(kept))
	}
}

// Both ends of one ranking, never the same team twice.
func TestTeamExtremes_NamesBothEndsWithoutOverlap(t *testing.T) {
	var predictions []scoring.UserPrediction
	add := func(team string, correct, total int) {
		id := common.NewTeamID()
		for i := 0; i < total; i++ {
			predictions = append(predictions, scoring.UserPrediction{
				TeamID: id, TeamName: team, Correct: i < correct,
				PlayedAt: time.Now(), Game: competition.GameCS2,
			})
		}
	}
	add("Best", 10, 10)
	add("Middle", 5, 10)
	add("Worst", 1, 10)

	best, worst := teamExtremes(predictions)
	if len(best) == 0 || len(worst) == 0 {
		t.Fatalf("best=%v worst=%v, want both ends named", best, worst)
	}
	if best[0].Team != "Best" {
		t.Fatalf("best[0] = %q, want the team read most accurately", best[0].Team)
	}
	if worst[0].Team != "Worst" {
		t.Fatalf("worst[0] = %q, want the team read least accurately first", worst[0].Team)
	}
	named := map[string]int{}
	for _, team := range append(append([]teamDTO{}, best...), worst...) {
		named[team.Team]++
	}
	for team, times := range named {
		if times > 1 {
			t.Errorf("%s appears in both tables, so one of them is wrong", team)
		}
	}
}
