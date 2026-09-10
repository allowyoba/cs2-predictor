package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// PollReminderScheduler nudges people who play in a chat but haven't voted
// on a poll that is about to close. The nudge is private and strictly
// opt-in (common.NotifyPollReminders): a poll's own message is already in
// the group, so anyone who wants a public reminder has one — what this
// adds is for people who asked to be told directly.
//
// "People who play in this chat" is activity-based rather than
// membership-based because Telegram gives a bot no member list: whoever
// has voted here recently is the closest available truth, and it has the
// right shape anyway — someone who has never predicted in this chat has
// not asked to be reminded about it.
type PollReminderScheduler struct {
	Predictions prediction.Repository
	Catalog     competition.Catalog
	Audience    common.NotificationAudience
	ChatTitles  ChatTitleLookup
	Outbox      common.Outbox
	Lock        common.ClusterLock
	Clock       common.Clock
	RunTx       TxRunner
	Log         *slog.Logger

	// Lead is how far ahead of closing a reminder goes out. Zero disables
	// the job entirely.
	Lead time.Duration
	// ParticipantWindow bounds how far back activity counts as "plays
	// here"; zero uses DefaultParticipantWindow.
	ParticipantWindow time.Duration
	// BatchSize bounds how many polls one run handles.
	BatchSize int
}

const (
	// DefaultReminderLead is close enough to a deadline to be useful and
	// far enough out to still act on.
	DefaultReminderLead = 30 * time.Minute
	// DefaultParticipantWindow is how recently someone must have voted in
	// a chat to count as playing there.
	DefaultParticipantWindow = 60 * 24 * time.Hour
	// defaultReminderBatch bounds one run; polls not reached stay
	// unmarked and are picked up by the next one.
	defaultReminderBatch = 50
)

func (s *PollReminderScheduler) Dispatch(ctx context.Context) {
	if s.Lead <= 0 {
		return
	}
	_, err := s.Lock.Execute(ctx, "cs2predictor:poll-reminders", func(ctx context.Context) error {
		return s.run(ctx)
	})
	if err != nil {
		s.Log.Error("poll reminder dispatch failed", "error", err)
	}
}

func (s *PollReminderScheduler) run(ctx context.Context) error {
	batch := s.BatchSize
	if batch <= 0 {
		batch = defaultReminderBatch
	}
	now := s.Clock.Now()
	polls, err := s.Predictions.PollsAwaitingReminder(ctx, now.Add(s.Lead), batch)
	if err != nil {
		return err
	}
	for _, poll := range polls {
		// One transaction per poll: a failure on one chat's reminder must
		// not undo another's, and marking must commit with the enqueue that
		// justifies it, or the next run would send it twice.
		if err := s.RunTx(ctx, func(txCtx context.Context) error {
			return s.remind(txCtx, poll, now)
		}); err != nil {
			s.Log.Warn("poll reminder failed", "pollId", poll.ID.Value, "error", err)
		}
	}
	return nil
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (s *PollReminderScheduler) remind(ctx context.Context, poll prediction.Poll, now time.Time) error {
	// Marked first, and unconditionally: a poll nobody needs reminding
	// about must not be re-examined every minute until it closes.
	if err := s.Predictions.MarkReminded(ctx, poll.ID, now); err != nil {
		return err
	}

	window := s.ParticipantWindow
	if window <= 0 {
		window = DefaultParticipantWindow
	}
	participants, err := s.Predictions.ChatParticipants(ctx, poll.ChatID, now.Add(-window))
	if err != nil {
		return err
	}
	if len(participants) == 0 {
		return nil
	}
	votes, err := s.Predictions.Votes(ctx, poll.ID)
	if err != nil {
		return err
	}
	voted := make(map[common.UserID]bool, len(votes))
	for _, v := range votes {
		voted[v.UserID] = true
	}
	pending := make([]common.UserID, 0, len(participants))
	for _, id := range participants {
		if !voted[id] {
			pending = append(pending, id)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	recipients, err := s.Audience.Recipients(ctx, common.NotifyPollReminders, pending)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return nil
	}

	match, err := s.Catalog.FindMatch(ctx, poll.MatchID)
	if err != nil {
		return err
	}
	if match == nil {
		return nil // the match vanished from the catalog; nothing to name
	}
	first, second := "", ""
	if match.FirstTeam != nil {
		first = match.FirstTeam.Name
	}
	if match.SecondTeam != nil {
		second = match.SecondTeam.Name
	}
	chatTitle := ""
	if s.ChatTitles != nil {
		chatTitle = s.ChatTitles(ctx, poll.ChatID)
	}
	minutes := int(math.Round(poll.ClosesAt.Sub(now).Minutes()))
	if minutes < 1 {
		minutes = 1
	}

	for _, userID := range recipients {
		payload, err := json.Marshal(common.PollReminderNotification{
			UserID: userID.Value, ChatTitle: chatTitle,
			FirstTeam: first, SecondTeam: second, MinutesLeft: minutes,
		})
		if err != nil {
			return err
		}
		if _, err := s.Outbox.Enqueue(ctx, "MATCH_POLL", poll.ID.Value.String(), "telegram.poll-reminder", string(payload)); err != nil {
			return err
		}
	}
	return nil
}
