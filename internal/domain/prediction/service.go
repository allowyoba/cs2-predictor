package prediction

import (
	"context"
	"fmt"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

type Service struct {
	repo    Repository
	gateway Gateway
	clock   common.Clock
}

func NewService(repo Repository, gateway Gateway, clock common.Clock) *Service {
	return &Service{repo: repo, gateway: gateway, clock: clock}
}

// Create is idempotent per (matchID, chatID): if a poll already exists for
// that pair it is returned as-is without re-sending to Telegram. Known
// limitation: if a prior call persisted the draft but then failed to send
// it to Telegram (so TelegramPollID is still nil), a later call just
// returns that broken draft rather than retrying the send.
func (s *Service) Create(ctx context.Context, match competition.Match, chatID common.ChatID, topicID *int64) (Poll, error) {
	if !match.ParticipantsKnown() {
		return Poll{}, fmt.Errorf("both participants must be known")
	}
	now := s.clock.Now()
	if match.ScheduledAt == nil || !match.ScheduledAt.After(now) {
		return Poll{}, fmt.Errorf("match must be scheduled in the future")
	}

	if existing, err := s.repo.FindByMatchAndChat(ctx, match.ID, chatID); err != nil {
		return Poll{}, err
	} else if existing != nil {
		return *existing, nil
	}

	scores := match.Format.PossibleScores()
	if len(scores) < 2 || len(scores) > 12 {
		return Poll{}, fmt.Errorf("telegram polls support between 2 and 12 options")
	}

	options := make([]Option, len(scores))
	for i, score := range scores {
		options[i] = Option{Index: i, Score: score}
	}

	draft := Poll{
		ID:       common.NewPollID(),
		ChatID:   chatID,
		MatchID:  match.ID,
		TopicID:  topicID,
		Options:  options,
		Status:   PollOpen,
		ClosesAt: *match.ScheduledAt,
	}

	stored, err := s.repo.SavePoll(ctx, draft)
	if err != nil {
		return Poll{}, err
	}

	sent, err := s.gateway.Send(ctx, stored)
	if err != nil {
		return Poll{}, err
	}

	stored.TelegramPollID = &sent.PollID
	stored.TelegramMessageID = &sent.MessageID
	stored.TopicID = sent.TopicID // overwritten from the gateway's resolved topic, not the original parameter
	return s.repo.SavePoll(ctx, stored)
}

// RecordVote applies a Telegram poll_answer update. optionIds with anything
// other than exactly one valid option index is treated as a withdrawal
// (Telegram sends an empty list when the user retracts their vote); a
// single invalid index is silently dropped, leaving any prior vote intact.
// Both "poll not found" and "poll not open / already past closesAt" are
// silent no-ops.
func (s *Service) RecordVote(ctx context.Context, telegramPollID string, userID common.UserID, optionIDs []int, username *string, displayName string) error {
	poll, err := s.repo.FindByTelegramPollID(ctx, telegramPollID)
	if err != nil {
		return err
	}
	if poll == nil {
		return nil
	}
	now := s.clock.Now()
	if poll.Status != PollOpen || !now.Before(poll.ClosesAt) {
		return nil
	}

	if len(optionIDs) != 1 {
		return s.repo.RemoveVote(ctx, poll.ID, userID)
	}

	option := optionIDs[0]
	valid := false
	for _, o := range poll.Options {
		if o.Index == option {
			valid = true
			break
		}
	}
	if !valid {
		return nil
	}

	return s.repo.SaveVote(ctx, Vote{
		PollID:      poll.ID,
		UserID:      userID,
		OptionIndex: option,
		Username:    username,
		DisplayName: displayName,
		VotedAt:     now,
	})
}

// CloseDue closes every OPEN poll whose ClosesAt has passed, returning how
// many were processed. Idempotent: calling it again immediately afterward
// processes zero, since the repository no longer reports them as open+due.
func (s *Service) CloseDue(ctx context.Context, at time.Time) (int, error) {
	due, err := s.repo.OpenPollsDue(ctx, at)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, poll := range due {
		if err := s.close(ctx, poll); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// close only marks the poll CLOSED in the repository if the gateway call
// succeeded — a gateway failure aborts processing (the error propagates to
// the caller, which in CloseDue's loop means any remaining due polls in
// that batch are left unprocessed until the next scheduler tick).
func (s *Service) close(ctx context.Context, poll Poll) error {
	if err := s.gateway.Close(ctx, poll); err != nil {
		return err
	}
	poll.Status = PollClosed
	_, err := s.repo.SavePoll(ctx, poll)
	return err
}

func (s *Service) CloseForRunningMatch(ctx context.Context, matchID common.MatchID) (int, error) {
	return s.closeMatching(ctx, matchID, PollClosed)
}

func (s *Service) CancelForMatch(ctx context.Context, matchID common.MatchID) (int, error) {
	return s.closeMatching(ctx, matchID, PollCancelled)
}

// closeMatching applies the same gateway-then-persist, abort-on-error
// pattern as close() — a gateway failure on one poll aborts the remaining
// polls in this batch too.
func (s *Service) closeMatching(ctx context.Context, matchID common.MatchID, status PollStatus) (int, error) {
	polls, err := s.repo.OpenPollsForMatch(ctx, matchID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, poll := range polls {
		var gatewayErr error
		if status == PollCancelled {
			gatewayErr = s.gateway.Cancel(ctx, poll)
		} else {
			gatewayErr = s.gateway.Close(ctx, poll)
		}
		if gatewayErr != nil {
			return count, gatewayErr
		}
		poll.Status = status
		if _, err := s.repo.SavePoll(ctx, poll); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// Reschedule updates only ClosesAt on every open poll for the match, keeping
// it OPEN and never touching Telegram — used when a match's scheduled time
// moves (POSTPONED handling) instead of cancelling the poll outright.
func (s *Service) Reschedule(ctx context.Context, matchID common.MatchID, closesAt time.Time) (int, error) {
	polls, err := s.repo.OpenPollsForMatch(ctx, matchID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, poll := range polls {
		poll.ClosesAt = closesAt
		if _, err := s.repo.SavePoll(ctx, poll); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
