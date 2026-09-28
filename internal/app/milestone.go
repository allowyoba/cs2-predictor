package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// MilestoneService notices when somebody's exact-score count passes a round
// number and hands the chat a congratulation with the story behind it.
//
// Driven off the awards a settlement just made rather than polled: the only
// moment the count can change is the moment an award is written, and asking
// the question anywhere else would be asking it constantly for an answer
// that almost never moves.
//
// Everything here is best effort. A missed congratulation is a missed
// pleasantry; a settlement that failed because of one would be a scoreboard
// that did not update.
type MilestoneService struct {
	Milestones scoring.MilestoneRepository
	Outbox     common.Outbox
	Gate       NotifyGate
	Clock      common.Clock
	Log        *slog.Logger
}

// Record takes the awards from one settled poll and congratulates whoever
// crossed a milestone with them.
//
// The count is read after the awards are stored, and the "before" is derived
// by subtracting the exact scores just added — rather than reading the count
// twice — so this cannot be thrown off by another settlement landing in
// between.
func (s *MilestoneService) Record(ctx context.Context, chatID common.ChatID, names map[common.UserID]string,
	awards []scoring.Award) {
	if s == nil || s.Milestones == nil {
		return
	}
	gained := map[common.UserID]int{}
	for _, award := range awards {
		if award.Kind == scoring.AwardExactScore {
			gained[award.UserID]++
		}
	}
	for userID, justGained := range gained {
		if err := s.recordOne(ctx, chatID, userID, names[userID], justGained); err != nil {
			s.Log.Warn("milestone check failed", "chatId", chatID.Value, "userId", userID.Value, "error", err)
		}
	}
}

func (s *MilestoneService) recordOne(ctx context.Context, chatID common.ChatID, userID common.UserID,
	displayName string, justGained int) error {
	after, err := s.Milestones.ExactCount(ctx, chatID, userID)
	if err != nil {
		return err
	}
	milestone, crossed := scoring.CrossedMilestone(after-justGained, after)
	if !crossed {
		return nil
	}
	now := s.Clock.Now()
	// The journey is read before the claim, so the "previous milestone"
	// it looks for is the one before this — claiming first would make this
	// milestone its own predecessor and report a gap of zero days.
	journey, err := s.Milestones.MilestoneJourney(ctx, chatID, userID, milestone)
	if err != nil {
		return err
	}
	claimed, err := s.Milestones.ClaimMilestone(ctx, chatID, userID, milestone, now)
	if err != nil || !claimed {
		return err
	}
	// Claimed either way: the achievement is theirs whether or not the room
	// asked to hear about it, and it still belongs on their own shelf.
	wanted, err := s.Gate.ChatWants(ctx, chatID, common.ChatNotifyMilestones)
	if err != nil || !wanted {
		return err
	}

	// Both set here rather than trusted from the repository: the caller is
	// the one that knows which milestone this is and when it landed, and a
	// journey missing either would quietly report a rate of zero.
	journey.Milestone, journey.ReachedAt = milestone, now
	notification := common.MilestoneNotification{
		ChatID: chatID.Value, UserID: userID.Value, DisplayName: displayName,
		Milestone: milestone, Predictions: journey.Predictions, Events: journey.Events,
		AccuracyPercent: journey.AccuracyPercent(),
		Days:            journey.DaysTaken(), DaysSincePrevious: journey.DaysSincePrevious(),
	}
	payload, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	aggregateID := chatID.String() + ":" + userID.String()
	_, err = s.Outbox.Enqueue(ctx, "MILESTONE", aggregateID, "telegram.milestone", string(payload))
	return err
}
