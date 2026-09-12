package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// TxRunner runs fn atomically — every repository call made through the ctx
// passed to fn commits or rolls back together. Implemented by
// postgres.RunInTx and wired in at composition time, keeping this package
// decoupled from the specific adapter.
type TxRunner func(ctx context.Context, fn func(ctx context.Context) error) error

// ResultSettlementService computes and publishes per-poll match results,
// guarded by a per-poll idempotency check (a result hash stored via
// SettlementRepository) and the transactional-outbox pattern (award
// replacement + outbox enqueue +
// settlement mark all commit together via TxRunner).
type ResultSettlementService struct {
	predictions prediction.Repository
	scoringRepo scoring.Repository
	settlements scoring.SettlementRepository
	scoringSvc  *scoring.Service
	outbox      common.Outbox
	clock       common.Clock
	runTx       TxRunner
	// audience and chatTitles power the opt-in personal result recap. Both
	// are optional: left nil, settlement behaves exactly as before and no
	// recaps are enqueued.
	audience   common.NotificationAudience
	chatTitles ChatTitleLookup
	log        *slog.Logger
}

// ChatTitleLookup names a chat for a message sent outside it — a recap
// lands in a private conversation, where "IEM Katowice" alone doesn't say
// which of the reader's groups it came from.
type ChatTitleLookup func(ctx context.Context, chatID common.ChatID) string

// WithRecaps enables the opt-in personal result recap. Kept as an option
// rather than a constructor parameter so every existing caller (and every
// test that only cares about settlement) stays untouched.
func (s *ResultSettlementService) WithRecaps(audience common.NotificationAudience, titles ChatTitleLookup, log *slog.Logger) *ResultSettlementService {
	s.audience, s.chatTitles, s.log = audience, titles, log
	return s
}

func NewResultSettlementService(predictions prediction.Repository, scoringRepo scoring.Repository, settlements scoring.SettlementRepository,
	scoringSvc *scoring.Service, outbox common.Outbox, clock common.Clock, runTx TxRunner) *ResultSettlementService {
	return &ResultSettlementService{
		predictions: predictions, scoringRepo: scoringRepo, settlements: settlements,
		scoringSvc: scoringSvc, outbox: outbox, clock: clock, runTx: runTx,
	}
}

// Settle processes every poll for match, skipping any poll whose stored
// result hash already matches this score (so re-processing an unchanged
// final score is a no-op, while a corrected score — a different hash —
// re-triggers settlement). Returns how many polls were actually settled.
func (s *ResultSettlementService) Settle(ctx context.Context, event competition.Event, match competition.Match) (int, error) {
	if match.Score == nil || match.Status != competition.MatchFinished {
		return 0, nil
	}
	hash := match.Score.String()

	polls, err := s.predictions.PollsForMatch(ctx, match.ID)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, poll := range polls {
		existing, ok, err := s.settlements.ResultHash(ctx, poll.ID)
		if err != nil {
			return count, err
		}
		if ok && existing == hash {
			continue
		}

		if err := s.runTx(ctx, func(txCtx context.Context) error {
			return s.settleOne(txCtx, event, match, poll, hash)
		}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *ResultSettlementService) settleOne(ctx context.Context, event competition.Event, match competition.Match, poll prediction.Poll, hash string) error {
	before, err := s.scoringRepo.Leaderboard(ctx, poll.ChatID, scoring.ForEvent(event.ID))
	if err != nil {
		return err
	}
	beforeByUser := map[common.UserID]scoring.UserStanding{}
	for _, st := range before {
		beforeByUser[st.UserID] = st
	}

	awards, err := s.scoringSvc.Settle(ctx, event, match, poll)
	if err != nil {
		return err
	}
	deltas := map[common.UserID]int{}
	for _, a := range awards {
		deltas[a.UserID] = a.Points
	}

	after, err := s.scoringRepo.Leaderboard(ctx, poll.ChatID, scoring.ForEvent(event.ID))
	if err != nil {
		return err
	}
	if len(after) > 5 {
		after = after[:5]
	}

	standings := make([]common.StandingNotification, 0, len(after))
	for _, st := range after {
		var previousRank *int
		if b, ok := beforeByUser[st.UserID]; ok {
			r := b.Rank
			previousRank = &r
		}
		standings = append(standings, common.StandingNotification{
			UserID: st.UserID.Value, DisplayName: st.DisplayName, Rank: st.Rank,
			PreviousRank: previousRank, Points: st.Points, PointsDelta: deltas[st.UserID],
			ExactPredictions: st.ExactPredictions, CorrectPredictions: st.CorrectPredictions, Predictions: st.Predictions,
		})
	}

	var stage *string
	if match.Stage != nil {
		stage = match.Stage
	}
	var firstName, secondName string
	if match.FirstTeam != nil {
		firstName = match.FirstTeam.Name
	}
	if match.SecondTeam != nil {
		secondName = match.SecondTeam.Name
	}
	notification := common.MatchResultNotification{
		ChatID: poll.ChatID.Value, TopicID: poll.TopicID, ReplyToMessageID: poll.TelegramMessageID,
		EventID: event.ID.Value.String(), EventName: event.Name, Stage: stage, Format: match.Format.Label(),
		FirstTeam: firstName, SecondTeam: secondName, Score: hash, Standings: standings,
	}
	payload, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	if _, err := s.outbox.Enqueue(ctx, "MATCH_POLL", poll.ID.Value.String(), "telegram.match-result", string(payload)); err != nil {
		return err
	}
	if err := s.enqueueRecaps(ctx, poll, match, event, hash, deltas); err != nil {
		return err
	}
	return s.settlements.MarkSettled(ctx, poll.ID, hash, s.clock.Now())
}

// enqueueRecaps hands one private result recap to the outbox per voter who
// asked for them. It runs inside settleOne's transaction, so a recap is
// never owed for a settlement that rolled back — the same
// transactional-outbox rule the group notification follows.
//
// A vote with no award is still worth a recap: "you had 2:1, it finished
// 2:0" is the interesting half of the message, and only telling people
// when they were right makes the feature feel like flattery.
//
//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (s *ResultSettlementService) enqueueRecaps(ctx context.Context, poll prediction.Poll, match competition.Match,
	event competition.Event, score string, deltas map[common.UserID]int) error {
	if s.audience == nil {
		return nil
	}
	votes, err := s.predictions.Votes(ctx, poll.ID)
	if err != nil {
		return err
	}
	if len(votes) == 0 {
		return nil
	}
	candidates := make([]common.UserID, 0, len(votes))
	for _, v := range votes {
		candidates = append(candidates, v.UserID)
	}
	recipients, err := s.audience.Recipients(ctx, common.NotifyResultRecaps, candidates)
	if err != nil {
		// A recap is a courtesy; failing to work out who wants one must not
		// roll back the settlement it describes.
		s.logf("recap recipients lookup failed", "pollId", poll.ID.Value, "error", err)
		return nil
	}
	if len(recipients) == 0 {
		return nil
	}
	wants := make(map[common.UserID]bool, len(recipients))
	for _, id := range recipients {
		wants[id] = true
	}

	chatTitle := ""
	if s.chatTitles != nil {
		chatTitle = s.chatTitles(ctx, poll.ChatID)
	}
	firstName, secondName := "", ""
	if match.FirstTeam != nil {
		firstName = match.FirstTeam.Name
	}
	if match.SecondTeam != nil {
		secondName = match.SecondTeam.Name
	}
	byIndex := map[int]string{}
	for _, option := range poll.Options {
		byIndex[option.Index] = option.Score.String()
	}

	for _, vote := range votes {
		if !wants[vote.UserID] {
			continue
		}
		payload, err := json.Marshal(common.ResultRecapNotification{
			UserID: vote.UserID.Value, ChatTitle: chatTitle, EventName: event.Name,
			FirstTeam: firstName, SecondTeam: secondName,
			Score: score, Predicted: byIndex[vote.OptionIndex], Points: deltas[vote.UserID],
		})
		if err != nil {
			return err
		}
		if _, err := s.outbox.Enqueue(ctx, "MATCH_POLL", poll.ID.Value.String(), "telegram.result-recap", string(payload)); err != nil {
			return err
		}
	}
	return nil
}

func (s *ResultSettlementService) logf(msg string, args ...any) {
	if s.log != nil {
		s.log.Warn(msg, args...)
	}
}
