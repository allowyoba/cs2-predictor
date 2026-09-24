package subscription

// TournamentParticipants is what the scoping rule needs to know about one
// tournament's field: which teams and which players (by the same ids a
// TargetSubscription uses) are competing in it. Built from match sync data
// by the caller — this package stays pure and DB-free.
type TournamentParticipants struct {
	TeamIDs   []string
	PlayerIDs []string
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// ChatStatsScope is what a chat is entitled to see for one tournament: the
// full per-tournament breakdown, or a narrower cut restricted to specific
// teams/players, or nothing at all.
type ChatStatsScope struct {
	// FullTournament is true only when the chat is directly subscribed to
	// the tournament itself. It is never granted by a team/player
	// subscription alone, no matter how many of them match the field.
	FullTournament bool
	// TeamIDs / PlayerIDs are the targets this chat's subscriptions grant a
	// scoped cut for in this tournament — populated only when
	// FullTournament is false and the corresponding subscription's target
	// actually plays in this tournament. When FullTournament is true these
	// are left empty since the tournament view already covers them.
	TeamIDs   []string
	PlayerIDs []string
}

// HasAnyAccess reports whether the chat should see anything at all for this
// tournament — no tournament subscription and no matching team/player
// subscription means the chat has no stats view for it, full or scoped.
func (s ChatStatsScope) HasAnyAccess() bool {
	return s.FullTournament || len(s.TeamIDs) > 0 || len(s.PlayerIDs) > 0
}

// ResolveChatStatsScope implements requirement (a): a chat directly
// subscribed to the tournament gets the full breakdown. Otherwise, for each
// active team/player subscription this chat holds whose target actually
// plays in this tournament, the chat gets that target's scoped cut only —
// it never escalates to a full tournament view just because the chat
// follows a team/player active there.
//
// subscribedToTournament is the chat's own EventSubscription state for this
// tournament (Repository.Subscriptions filtered to Active). targets is the
// chat's active TargetSubscriptions (any kind, any tournament — filtering
// to this one's field is this function's job, via participants).
func ResolveChatStatsScope(subscribedToTournament bool, targets []TargetSubscription, participants TournamentParticipants) ChatStatsScope {
	if subscribedToTournament {
		return ChatStatsScope{FullTournament: true}
	}

	scope := ChatStatsScope{}
	seenTeam := map[string]bool{}
	seenPlayer := map[string]bool{}
	for _, t := range targets {
		if !t.Active {
			continue
		}
		switch t.Kind {
		case TargetTeam:
			if containsID(participants.TeamIDs, t.TargetID) && !seenTeam[t.TargetID] {
				seenTeam[t.TargetID] = true
				scope.TeamIDs = append(scope.TeamIDs, t.TargetID)
			}
		case TargetPlayer:
			if containsID(participants.PlayerIDs, t.TargetID) && !seenPlayer[t.TargetID] {
				seenPlayer[t.TargetID] = true
				scope.PlayerIDs = append(scope.PlayerIDs, t.TargetID)
			}
		}
	}
	return scope
}
