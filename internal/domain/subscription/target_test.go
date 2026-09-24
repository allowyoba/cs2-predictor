package subscription

import (
	"testing"

	"github.com/google/uuid"

	"cs2predictor/internal/platform/common"
)

func teamID() common.TeamID   { return common.TeamID{Value: uuid.New()} }
func eventID() common.EventID { return common.EventID{Value: uuid.New()} }

func TestScopeForTournamentSubscriptionIsFull(t *testing.T) {
	event := eventID()
	team := TeamTarget(teamID(), "NaVi")
	got := ScopeFor(event, []Target{team, TournamentTarget(event)}, map[string][]common.TeamID{team.Key(): {team.TeamID}})
	if !got.Full {
		t.Fatalf("tournament-subscribed chat must see the full tournament, got %+v", got)
	}
}

func TestScopeForNoFollowsKeepsFullHistory(t *testing.T) {
	got := ScopeFor(eventID(), []Target{TournamentTarget(eventID())}, nil)
	if !got.Full {
		t.Fatalf("a chat without team/player targets keeps the full view, got %+v", got)
	}
}

func TestScopeForOverlappingTeamAndPlayerDeduplicates(t *testing.T) {
	navi, vitality := teamID(), teamID()
	team := TeamTarget(navi, "NaVi")
	player := PlayerTarget("  s1mple ")
	other := PlayerTarget("ZywOo")
	resolved := map[string][]common.TeamID{
		team.Key():   {navi},
		player.Key(): {navi},
		other.Key():  {vitality, navi},
	}
	// Subscribed to a different tournament only: this one stays sliced.
	got := ScopeFor(eventID(), []Target{TournamentTarget(eventID()), team, player, other}, resolved)
	if got.Full {
		t.Fatal("a team/player follower not subscribed to this tournament must not get the full view")
	}
	if len(got.Teams) != 2 {
		t.Fatalf("overlapping targets must yield each team once, got %v", got.Teams)
	}
}

func TestScopeForUnresolvedPlayerShowsNothing(t *testing.T) {
	got := ScopeFor(eventID(), []Target{PlayerTarget("nobody")}, map[string][]common.TeamID{})
	if got.Full || got.Teams == nil || len(got.Teams) != 0 {
		t.Fatalf("an unresolved player must restrict to an empty slice, not widen to full: %+v", got)
	}
}

func TestPlayerTargetNormalizes(t *testing.T) {
	a, b := PlayerTarget(" S1mple"), PlayerTarget("s1mple ")
	if a.Key() != b.Key() || a.Label != "S1mple" {
		t.Fatalf("keys %q/%q label %q", a.Key(), b.Key(), a.Label)
	}
}
