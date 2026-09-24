package subscription

import (
	"testing"
	"time"
)

var now = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func teamSub(chatID, teamID int64) TargetSubscription {
	return TargetSubscription{Kind: TargetTeam, TargetID: teamID, Active: true, SubscribedAt: now}
}

func playerSub(playerID int64) TargetSubscription {
	return TargetSubscription{Kind: TargetPlayer, TargetID: playerID, Active: true, SubscribedAt: now}
}

// Chat with only a team subscription, no tournament subscription: must get
// team-scoped stats only, never a full tournament view.
func TestResolveChatStatsScope_TeamOnly_NoTournamentSub(t *testing.T) {
	scope := ResolveChatStatsScope(false, []TargetSubscription{teamSub(1, 100)}, TournamentParticipants{TeamIDs: []int64{100, 200}})

	if scope.FullTournament {
		t.Fatal("team subscription alone must not grant a full tournament view")
	}
	if len(scope.TeamIDs) != 1 || scope.TeamIDs[0] != 100 {
		t.Fatalf("expected team-scoped cut for team 100, got %v", scope.TeamIDs)
	}
	if len(scope.PlayerIDs) != 0 {
		t.Fatalf("expected no player scope, got %v", scope.PlayerIDs)
	}
	if !scope.HasAnyAccess() {
		t.Fatal("expected some access from the team scope")
	}
}

// Chat with team subscription AND a tournament subscription for a
// tournament that team plays in: full tournament view, and the team scope
// is not separately populated (redundant once full view is granted).
func TestResolveChatStatsScope_TeamPlusTournamentSub(t *testing.T) {
	scope := ResolveChatStatsScope(true, []TargetSubscription{teamSub(1, 100)}, TournamentParticipants{TeamIDs: []int64{100}})

	if !scope.FullTournament {
		t.Fatal("expected full tournament view when chat is tournament-subscribed")
	}
	if len(scope.TeamIDs) != 0 || len(scope.PlayerIDs) != 0 {
		t.Fatalf("full view should not also carry a redundant scoped cut, got teams=%v players=%v", scope.TeamIDs, scope.PlayerIDs)
	}
}

// Team subscription, team plays in two tournaments: subscribed one gets
// the full view, the other gets team-scoped-only. Modeled as two separate
// calls, one per tournament, since scope is always resolved per tournament.
func TestResolveChatStatsScope_SameTeam_TwoTournaments_MixedSubscription(t *testing.T) {
	targets := []TargetSubscription{teamSub(1, 100)}

	subscribedTournament := ResolveChatStatsScope(true, targets, TournamentParticipants{TeamIDs: []int64{100}})
	if !subscribedTournament.FullTournament {
		t.Fatal("expected full view for the tournament-subscribed event")
	}

	unsubscribedTournament := ResolveChatStatsScope(false, targets, TournamentParticipants{TeamIDs: []int64{100}})
	if unsubscribedTournament.FullTournament {
		t.Fatal("must not leak a full view into the non-subscribed tournament just because the team plays there too")
	}
	if len(unsubscribedTournament.TeamIDs) != 1 || unsubscribedTournament.TeamIDs[0] != 100 {
		t.Fatalf("expected team-scoped-only cut for the non-subscribed tournament, got %v", unsubscribedTournament.TeamIDs)
	}
}

// Same combinations, for player subscriptions.
func TestResolveChatStatsScope_PlayerOnly_NoTournamentSub(t *testing.T) {
	scope := ResolveChatStatsScope(false, []TargetSubscription{playerSub(55)}, TournamentParticipants{PlayerIDs: []int64{55, 56}})

	if scope.FullTournament {
		t.Fatal("player subscription alone must not grant a full tournament view")
	}
	if len(scope.PlayerIDs) != 1 || scope.PlayerIDs[0] != 55 {
		t.Fatalf("expected player-scoped cut for player 55, got %v", scope.PlayerIDs)
	}
}

func TestResolveChatStatsScope_PlayerPlusTournamentSub(t *testing.T) {
	scope := ResolveChatStatsScope(true, []TargetSubscription{playerSub(55)}, TournamentParticipants{PlayerIDs: []int64{55}})
	if !scope.FullTournament {
		t.Fatal("expected full tournament view")
	}
	if len(scope.PlayerIDs) != 0 {
		t.Fatalf("full view should not also carry a redundant player scope, got %v", scope.PlayerIDs)
	}
}

func TestResolveChatStatsScope_SamePlayer_TwoTournaments_MixedSubscription(t *testing.T) {
	targets := []TargetSubscription{playerSub(55)}

	full := ResolveChatStatsScope(true, targets, TournamentParticipants{PlayerIDs: []int64{55}})
	scoped := ResolveChatStatsScope(false, targets, TournamentParticipants{PlayerIDs: []int64{55}})

	if !full.FullTournament {
		t.Fatal("expected full view for the subscribed tournament")
	}
	if scoped.FullTournament || len(scoped.PlayerIDs) != 1 {
		t.Fatalf("expected player-scoped-only for the other tournament, got %+v", scoped)
	}
}

// Overlapping team+player subscriptions both pointing at data relevant to
// the same tournament: both scoped cuts are present simultaneously, neither
// one escalating the other into a full view.
func TestResolveChatStatsScope_OverlappingTeamAndPlayer(t *testing.T) {
	targets := []TargetSubscription{teamSub(1, 100), playerSub(55)}
	participants := TournamentParticipants{TeamIDs: []int64{100}, PlayerIDs: []int64{55}}

	scope := ResolveChatStatsScope(false, targets, participants)
	if scope.FullTournament {
		t.Fatal("combined team+player subscriptions still must not grant a full view without a tournament subscription")
	}
	if len(scope.TeamIDs) != 1 || scope.TeamIDs[0] != 100 {
		t.Fatalf("expected team scope, got %v", scope.TeamIDs)
	}
	if len(scope.PlayerIDs) != 1 || scope.PlayerIDs[0] != 55 {
		t.Fatalf("expected player scope, got %v", scope.PlayerIDs)
	}
}

// A target subscription whose team/player does not actually play in this
// tournament must not leak in — it simply contributes nothing here.
func TestResolveChatStatsScope_TargetNotInThisTournament(t *testing.T) {
	scope := ResolveChatStatsScope(false, []TargetSubscription{teamSub(1, 999)}, TournamentParticipants{TeamIDs: []int64{100, 200}})
	if scope.HasAnyAccess() {
		t.Fatalf("expected no access when the subscribed team doesn't play in this tournament, got %+v", scope)
	}
}

// Inactive (unsubscribed) target subscriptions must not grant access.
func TestResolveChatStatsScope_InactiveTargetIgnored(t *testing.T) {
	inactive := TargetSubscription{Kind: TargetTeam, TargetID: 100, Active: false}
	scope := ResolveChatStatsScope(false, []TargetSubscription{inactive}, TournamentParticipants{TeamIDs: []int64{100}})
	if scope.HasAnyAccess() {
		t.Fatalf("inactive subscription must not grant access, got %+v", scope)
	}
}

// No subscriptions of any kind: no access at all.
func TestResolveChatStatsScope_NoSubscriptions(t *testing.T) {
	scope := ResolveChatStatsScope(false, nil, TournamentParticipants{TeamIDs: []int64{100}})
	if scope.HasAnyAccess() {
		t.Fatalf("expected no access, got %+v", scope)
	}
}

// Duplicate active subscriptions to the same team must not duplicate the
// scoped id in the result.
func TestResolveChatStatsScope_DuplicateTeamSubscriptionsDeduped(t *testing.T) {
	targets := []TargetSubscription{teamSub(1, 100), teamSub(1, 100)}
	scope := ResolveChatStatsScope(false, targets, TournamentParticipants{TeamIDs: []int64{100}})
	if len(scope.TeamIDs) != 1 {
		t.Fatalf("expected deduped single team id, got %v", scope.TeamIDs)
	}
}
