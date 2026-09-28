package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// LateVoteService records a prediction that was made in the room but never
// reached the poll, and puts it back into the scoreboard.
//
// This exists because the schedule is not ours. A match is brought forward,
// the poll closes on the time it was given, and the predictions people
// actually made are sitting in the chat with nowhere to go. The scoreboard
// is then wrong in a way nothing in the bot can notice or fix, and the only
// route back was somebody writing SQL against production.
//
// It is deliberately a manager's action, deliberately logged, and
// deliberately visible in the chat's own history: there is no technical way
// to tell a prediction made before the match from one made after it, so the
// guard is that a person with standing in the room puts their name to it.
type LateVoteService struct {
	Predictions prediction.Repository
	Catalog     competition.Catalog
	Scoring     *scoring.Service
	Settlements scoring.SettlementRepository
	// Milestones runs again after a re-score, so a late vote that carries
	// somebody past a round number is congratulated like any other.
	Milestones *MilestoneService
	Clock      common.Clock
	RunTx      TxRunner
	Log        *slog.Logger
}

// ErrPollNotInChat guards the one thing a forged callback could try: naming
// another room's poll.
var ErrPollNotInChat = errors.New("poll does not belong to this chat")

// LateVoteResult is what happened, so the confirmation can say it rather
// than just claiming success.
type LateVoteResult struct {
	// Predicted is the scoreline recorded.
	Predicted competition.MatchScore
	// Scored is true when the match was already finished and the vote was
	// scored immediately; false means the match has not been played yet and
	// the vote will be scored with everybody else's.
	Scored bool
	// Points is what it earned, valid only when Scored.
	Points int
}

// Record saves the vote and, if the match has already been played, rescores
// that one poll so the standings catch up at once.
//
// Everything happens in one transaction: a vote recorded without its award
// would sit in the scoreboard as a prediction that earned nothing, which is
// indistinguishable from a wrong one.
func (s *LateVoteService) Record(ctx context.Context, chatID common.ChatID, pollID common.PollID,
	voter common.UserID, displayName string, username *string, optionIndex int) (LateVoteResult, error) {
	var result LateVoteResult
	err := s.RunTx(ctx, func(txCtx context.Context) error {
		poll, err := s.Predictions.FindPoll(txCtx, pollID)
		if err != nil {
			return err
		}
		if poll == nil {
			return fmt.Errorf("poll %s: %w", pollID.Value, competition.ErrMatchNotFound)
		}
		if poll.ChatID != chatID {
			return ErrPollNotInChat
		}
		predicted, ok := optionScore(*poll, optionIndex)
		if !ok {
			return fmt.Errorf("option %d is not on this poll", optionIndex)
		}
		result.Predicted = predicted

		// Dated to the moment the poll shut rather than now: this is a
		// prediction, and a vote stamped after the match would read as one
		// made knowing the answer — including to us, later, in the history.
		if err := s.Predictions.SaveVote(txCtx, prediction.Vote{
			PollID: pollID, UserID: voter, OptionIndex: optionIndex,
			DisplayName: displayName, Username: username, VotedAt: poll.ClosesAt,
		}); err != nil {
			return err
		}

		points, scored, err := s.rescore(txCtx, chatID, *poll, voter, displayName)
		if err != nil {
			return err
		}
		result.Scored, result.Points = scored, points
		return nil
	})
	if err != nil {
		return LateVoteResult{}, err
	}
	return result, nil
}

// optionScore finds the scoreline behind an option index.
func optionScore(poll prediction.Poll, index int) (competition.MatchScore, bool) {
	for _, option := range poll.Options {
		if option.Index == index {
			return option.Score, true
		}
	}
	return competition.MatchScore{}, false
}

// rescore settles the poll again now that it has one more vote on it, and
// reports what the late voter earned. A match still to be played is not an
// error: the vote simply waits and is scored with everybody else's.
func (s *LateVoteService) rescore(ctx context.Context, chatID common.ChatID, poll prediction.Poll,
	voter common.UserID, displayName string) (points int, scored bool, err error) {
	match, err := s.Catalog.FindMatch(ctx, poll.MatchID)
	if err != nil {
		return 0, false, err
	}
	if match == nil || match.Status != competition.MatchFinished || match.Score == nil {
		return 0, false, nil
	}
	event, err := s.Catalog.FindEvent(ctx, match.EventID)
	if err != nil {
		return 0, false, err
	}
	if event == nil {
		return 0, false, competition.ErrEventNotFound
	}

	// Settle replaces this poll's awards from every vote on it, so the late
	// one is scored and nobody else's changes.
	awards, err := s.Scoring.Settle(ctx, *event, *match, poll)
	if err != nil {
		return 0, false, err
	}
	for _, award := range awards {
		if award.UserID == voter {
			points = award.Points
		}
	}
	// Rewritten rather than left alone: the hash is already this score, but
	// the awards behind it have changed, and the timestamp is what says when.
	if err := s.Settlements.MarkSettled(ctx, poll.ID, match.Score.String(), s.Clock.Now()); err != nil {
		return 0, false, err
	}
	s.Milestones.Record(ctx, chatID, map[common.UserID]string{voter: displayName}, awards)
	return points, true, nil
}
