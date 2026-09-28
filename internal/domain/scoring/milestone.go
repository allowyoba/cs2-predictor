package scoring

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// Milestones on exact-score predictions: the round numbers worth stopping to
// mark, and the figures that say what it took to get there.
//
// Counting exact scores rather than correct winners on purpose. Picking the
// winner of a best-of-three is close to a coin flip; calling 2:1 is not, and
// a hundred of those is a real thing to have done. A milestone on the easy
// number would be a participation trophy.

// ExactMilestones are the counts a chat is told about, smallest first.
//
// Roughly logarithmic rather than every hundred: the gap between milestones
// has to grow with the count, or somebody active ends up triggering one every
// other week and the message stops meaning anything. Ten is the first because
// it is the point where it stops being luck.
var ExactMilestones = []int{10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

// CrossedMilestone reports the milestone passed by going from before to
// after, and whether any was.
//
// The highest one crossed, not the lowest: a settlement can only ever add a
// handful at once, but a backfill or a repair could add many, and announcing
// five milestones in a row for the same person in the same second would read
// as a bug rather than a celebration.
func CrossedMilestone(before, after int) (int, bool) {
	crossed, found := 0, false
	for _, milestone := range ExactMilestones {
		if before < milestone && after >= milestone {
			crossed, found = milestone, true
		}
	}
	return crossed, found
}

// NextMilestone is the one still ahead of count, for the progress line in
// somebody's own cabinet. ok is false once they are past the last one, which
// is a finished ladder rather than an error.
func NextMilestone(count int) (int, bool) {
	for _, milestone := range ExactMilestones {
		if count < milestone {
			return milestone, true
		}
	}
	return 0, false
}

// MilestoneJourney is what it took: not just the number reached, but the
// scale of the effort behind it.
//
// All four figures answer a different question people actually ask — how
// often was I right, across how many tournaments, over how long, and am I
// getting faster. A congratulation that says only "100!" says nothing the
// leaderboard does not.
type MilestoneJourney struct {
	// Milestone is the count just reached.
	Milestone int
	// Predictions is every finished prediction made along the way, so the
	// milestone can be read as a rate rather than a total.
	Predictions int
	// Events is how many distinct tournaments those predictions span.
	Events int
	// FirstPredictionAt is when this all started; zero when unknown.
	FirstPredictionAt time.Time
	// PreviousMilestoneAt is when the milestone before this one was
	// reached; zero when this is the first.
	PreviousMilestoneAt time.Time
	// ReachedAt is when this one landed.
	ReachedAt time.Time
}

// DaysTaken is how long the whole run took, in days, or 0 when the start is
// unknown. Days rather than a duration because that is the unit the sentence
// is written in.
func (j MilestoneJourney) DaysTaken() int {
	return wholeDaysBetween(j.FirstPredictionAt, j.ReachedAt)
}

// DaysSincePrevious is how long the last stretch took — the figure that says
// whether somebody is speeding up. Zero when there is no previous milestone,
// which is what makes it safe to leave the line out for a first one.
func (j MilestoneJourney) DaysSincePrevious() int {
	return wholeDaysBetween(j.PreviousMilestoneAt, j.ReachedAt)
}

// AccuracyPercent is the share of predictions that were exactly right, which
// is the figure that makes the milestone mean something at very different
// levels of activity.
func (j MilestoneJourney) AccuracyPercent() int {
	return accuracyPercent(j.Milestone, j.Predictions)
}

func wholeDaysBetween(from, to time.Time) int {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return 0
	}
	return int(to.Sub(from).Hours() / 24)
}

// MilestoneRecord is one milestone somebody has reached, as stored.
type MilestoneRecord struct {
	ChatID    common.ChatID
	UserID    common.UserID
	Milestone int
	ReachedAt time.Time
}

// MilestoneRepository claims a milestone and answers what it took.
type MilestoneRepository interface {
	// ClaimMilestone records that this person reached this milestone in this
	// chat, reporting false when it was already recorded — which is what
	// makes the congratulation happen exactly once, however many times a
	// settlement is retried.
	ClaimMilestone(ctx context.Context, chatID common.ChatID, userID common.UserID, milestone int, at time.Time) (bool, error)
	// MilestoneJourney gathers the figures behind a milestone: how many
	// predictions, across how many tournaments, since when, and when the
	// previous milestone landed.
	MilestoneJourney(ctx context.Context, chatID common.ChatID, userID common.UserID, milestone int) (MilestoneJourney, error)
	// UserMilestones lists what somebody has reached across every chat,
	// newest first — the achievements shelf in their own cabinet.
	UserMilestones(ctx context.Context, userID common.UserID) ([]MilestoneRecord, error)
	// ExactCount is how many exact scores somebody has to their name,
	// scoped the same way the milestones are claimed.
	ExactCount(ctx context.Context, chatID common.ChatID, userID common.UserID) (int, error)
	// UserExactTotal is the same count across every chat, for the cabinet.
	UserExactTotal(ctx context.Context, userID common.UserID) (int, error)
}
