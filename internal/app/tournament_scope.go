package app

import (
	"context"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// TournamentScopeService is where requirement (a)'s scoping rule actually
// takes effect: it is the one place that decides, for a real chat and a
// real tournament, whether that chat sees the full leaderboard or only a
// team/player-scoped cut of it, and then fetches exactly that.
//
// It is deliberately thin — subscription.ResolveChatStatsScope carries all
// of the decision logic and is tested exhaustively on its own; this type's
// job is only to gather the real inputs that function needs (the chat's
// subscriptions, and which teams/players play in the tournament) and turn
// its decision into an actual leaderboard fetch.
type TournamentScopeService struct {
	Subscriptions subscription.Repository
	Targets       subscription.TargetRepository
	Catalog       competition.Catalog
	Scoring       scoring.Repository
}

// TournamentStatsView is what a chat is allowed to see for one tournament:
// either the full leaderboard, or a merged team/player-scoped cut of it (or
// neither, when Scope.HasAnyAccess() is false).
type TournamentStatsView struct {
	Scope     subscription.ChatStatsScope
	Standings []scoring.UserStanding
}

// Resolve implements requirement (a) end to end for one (chat, tournament)
// pair: it does not grant a full breakdown from a team/player subscription
// alone, and a chat with both a team subscription and a tournament
// subscription for that tournament gets the full view.
func (s *TournamentScopeService) Resolve(ctx context.Context, chatID common.ChatID, eventID common.EventID) (TournamentStatsView, error) {
	subscribed, err := s.subscribedToTournament(ctx, chatID, eventID)
	if err != nil {
		return TournamentStatsView{}, err
	}

	targets, err := s.Targets.TargetSubscriptions(ctx, chatID)
	if err != nil {
		return TournamentStatsView{}, err
	}

	participants, err := s.participantsOf(ctx, eventID)
	if err != nil {
		return TournamentStatsView{}, err
	}

	scope := subscription.ResolveChatStatsScope(subscribed, targets, participants)
	if !scope.HasAnyAccess() {
		return TournamentStatsView{Scope: scope}, nil
	}
	if scope.FullTournament {
		standings, err := s.Scoring.Leaderboard(ctx, chatID, scoring.ForEvent(eventID))
		if err != nil {
			return TournamentStatsView{}, err
		}
		return TournamentStatsView{Scope: scope, Standings: standings}, nil
	}

	standings, err := s.mergedTeamScopedStandings(ctx, chatID, eventID, scope.TeamIDs)
	if err != nil {
		return TournamentStatsView{}, err
	}
	// Player-scoped standings need per-player match data this codebase does
	// not track yet (no Player/roster domain concept — see the target
	// package's doc comment on TargetPlayer). The scope decision itself is
	// still correct and reported; only the standings fetch for a
	// player-only subscription is left empty until that data exists.
	return TournamentStatsView{Scope: scope, Standings: standings}, nil
}

func (s *TournamentScopeService) subscribedToTournament(ctx context.Context, chatID common.ChatID, eventID common.EventID) (bool, error) {
	subs, err := s.Subscriptions.Subscriptions(ctx, chatID)
	if err != nil {
		return false, err
	}
	for _, sub := range subs {
		if sub.Active && sub.EventID == eventID {
			return true, nil
		}
	}
	return false, nil
}

// participantsOf builds the tournament's field from its actual matches, so
// the scoping rule is checked against what really played rather than what
// a subscription merely names.
func (s *TournamentScopeService) participantsOf(ctx context.Context, eventID common.EventID) (subscription.TournamentParticipants, error) {
	matches, err := s.Catalog.FindMatches(ctx, eventID)
	if err != nil {
		return subscription.TournamentParticipants{}, err
	}
	seen := map[string]bool{}
	var participants subscription.TournamentParticipants
	for _, m := range matches {
		for _, team := range []*competition.Team{m.FirstTeam, m.SecondTeam} {
			if team == nil {
				continue
			}
			id := team.ID.Value.String()
			if seen[id] {
				continue
			}
			seen[id] = true
			participants.TeamIDs = append(participants.TeamIDs, id)
		}
	}
	return participants, nil
}

// mergedTeamScopedStandings sums each user's team-scoped contribution
// across every team the chat's subscriptions grant a cut for in this
// tournament — a chat following two teams that both play in the same
// event sees one combined "matches involving either of my teams" table
// rather than two separate, harder-to-read ones.
func (s *TournamentScopeService) mergedTeamScopedStandings(ctx context.Context, chatID common.ChatID, eventID common.EventID, teamKeys []string) ([]scoring.UserStanding, error) {
	if len(teamKeys) == 0 {
		return nil, nil
	}
	byUser := map[common.UserID]*scoring.UserStanding{}
	for _, key := range teamKeys {
		parsed, err := uuid.Parse(key)
		if err != nil {
			// Not a well-formed team id (should not happen for a real
			// TargetTeam subscription) — skip rather than fail the whole
			// merge over one bad row.
			continue
		}
		teamID := common.TeamID{Value: parsed}
		rows, err := s.Scoring.Leaderboard(ctx, chatID, scoring.ForEvent(eventID).ForTeam(&teamID))
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			entry, ok := byUser[row.UserID]
			if !ok {
				copyRow := row
				byUser[row.UserID] = &copyRow
				continue
			}
			entry.Points += row.Points
			entry.ExactPredictions += row.ExactPredictions
			entry.CorrectPredictions += row.CorrectPredictions
			entry.Predictions += row.Predictions
		}
	}
	out := make([]scoring.UserStanding, 0, len(byUser))
	for _, v := range byUser {
		out = append(out, *v)
	}
	return scoring.DenseRank(out), nil
}
