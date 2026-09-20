package scoring

import (
	"context"
	"fmt"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

type Service struct {
	predictions prediction.Repository
	repo        Repository
	clock       common.Clock
}

func NewService(predictions prediction.Repository, repo Repository, clock common.Clock) *Service {
	return &Service{predictions: predictions, repo: repo, clock: clock}
}

// Settle computes and atomically replaces every award for poll's votes
// against the match's final score. Votes on options that can't be resolved,
// or whose predicted outcome was wrong, are simply skipped (no award row) —
// re-running settlement (e.g. after a score correction) is always safe: old
// awards for this poll are wiped even if the freshly computed set is empty.
func (s *Service) Settle(ctx context.Context, event competition.Event, match competition.Match, poll prediction.Poll) ([]Award, error) {
	if match.Score == nil {
		return nil, fmt.Errorf("finished match must have a score")
	}
	actual := realignToPollTeamOrder(*match.Score, match, poll)

	var startedAt = match.ActualStartedAt
	if startedAt == nil {
		startedAt = match.ScheduledAt
	}
	if startedAt == nil {
		return nil, fmt.Errorf("match start is required")
	}

	votes, err := s.predictions.Votes(ctx, poll.ID)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	var awards []Award
	for _, vote := range votes {
		var predicted *competition.MatchScore
		for _, opt := range poll.Options {
			if opt.Index == vote.OptionIndex {
				score := opt.Score
				predicted = &score
				break
			}
		}
		if predicted == nil {
			continue
		}
		points, kind, ok := Calculate(*predicted, actual, match.Format)
		if !ok {
			continue
		}
		awards = append(awards, Award{
			PollID: poll.ID, UserID: vote.UserID,
			Points: points, Kind: kind, AwardedAt: now,
		})
	}

	if err := s.repo.ReplaceAwards(ctx, poll.ID, awards); err != nil {
		return nil, err
	}
	return awards, nil
}

// realignToPollTeamOrder corrects for a team order that drifted between
// this poll's creation and now: poll.Options was built from
// match.FirstTeam/SecondTeam at creation time, but that's not guaranteed to
// still be the current order (PandaScore's own opponents order is not
// stable across two fetches of the same match — confirmed in production).
// Swapping actual back onto the poll's own anchor here means every
// vote/option comparison in Settle is correct regardless of what happened
// to the shared match record since this poll was created. A poll saved
// before this anchor existed (FirstTeamID/SecondTeamID both the zero
// value) has nothing to check against, so it scores exactly as before.
func realignToPollTeamOrder(actual competition.MatchScore, match competition.Match, poll prediction.Poll) competition.MatchScore {
	if poll.FirstTeamID == (common.TeamID{}) || match.FirstTeam == nil || match.SecondTeam == nil {
		return actual
	}
	if poll.FirstTeamID == match.SecondTeam.ID && poll.SecondTeamID == match.FirstTeam.ID {
		return competition.MatchScore{First: actual.Second, Second: actual.First}
	}
	return actual
}
