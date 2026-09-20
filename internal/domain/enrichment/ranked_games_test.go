package enrichment

import (
	"testing"

	"cs2predictor/internal/domain/competition"
)

// The organisations behind these rankings field rosters in several games
// under one name. A ranking earned by a Counter-Strike roster says nothing
// about the Dota 2 roster next to it, so the source has to declare what it
// covers rather than be matched against whatever shares a name.
func TestRanksGame_CoversCounterStrikeOnly(t *testing.T) {
	for _, source := range []Source{SourceValveVRS, SourceHLTV} {
		if !RanksGame(source, competition.GameCS2) {
			t.Fatalf("%s ranks CS2 teams", source)
		}
		if RanksGame(source, competition.GameDota2) {
			t.Fatalf("%s must not be applied to a Dota 2 team", source)
		}
	}
}

// A source that produces no rankings must not inherit a permissive
// default: silence is the safe answer for a question it cannot answer.
func TestRanksGame_RefusesSourcesThatDoNotRankTeams(t *testing.T) {
	for _, source := range []Source{SourceGRID, SourceLiquipedia, Source("SOMETHING_NEW")} {
		for _, game := range competition.Games {
			if RanksGame(source, game) {
				t.Fatalf("%s does not publish a team ranking for %s", source, game)
			}
		}
	}
}

func TestRankedGames_IsTheFilterASyncApplies(t *testing.T) {
	games := RankedGames(SourceValveVRS)
	if len(games) != 1 || games[0] != competition.GameCS2 {
		t.Fatalf("RankedGames(VRS) = %v, want [CS2]", games)
	}
	if got := RankedGames(SourceGRID); len(got) != 0 {
		t.Fatalf("RankedGames(GRID) = %v, want none", got)
	}
}
