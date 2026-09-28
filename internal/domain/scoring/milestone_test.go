package scoring

import (
	"testing"
	"time"
)

func TestCrossedMilestone_FiresOnceAtTheBoundary(t *testing.T) {
	if _, ok := CrossedMilestone(98, 99); ok {
		t.Fatal("no milestone between 98 and 99")
	}
	got, ok := CrossedMilestone(99, 100)
	if !ok || got != 100 {
		t.Fatalf("crossing 100 = (%d, %v), want (100, true)", got, ok)
	}
	// Already past it: settling another exact score must not repeat it.
	if _, ok := CrossedMilestone(100, 101); ok {
		t.Fatal("a milestone already passed must not fire again")
	}
}

// A repair or a backfill can add many at once. Announcing every milestone in
// between would read as a malfunction, so only the highest is reported.
func TestCrossedMilestone_ReportsOnlyTheHighestWhenSeveralArePassed(t *testing.T) {
	got, ok := CrossedMilestone(5, 300)
	if !ok || got != 250 {
		t.Fatalf("crossing 5 → 300 = (%d, %v), want (250, true)", got, ok)
	}
}

func TestNextMilestone_PointsAtTheOneStillAhead(t *testing.T) {
	if got, ok := NextMilestone(0); !ok || got != 10 {
		t.Fatalf("next after 0 = (%d, %v), want (10, true)", got, ok)
	}
	if got, ok := NextMilestone(100); !ok || got != 250 {
		t.Fatalf("next after 100 = (%d, %v), want (250, true)", got, ok)
	}
	// A finished ladder is not an error.
	if _, ok := NextMilestone(10000); ok {
		t.Fatal("there is nothing after the last milestone")
	}
}

func TestMilestoneJourney_SaysWhatItTook(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	previous := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reached := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	journey := MilestoneJourney{
		Milestone: 100, Predictions: 400, Events: 12,
		FirstPredictionAt: start, PreviousMilestoneAt: previous, ReachedAt: reached,
	}

	if got := journey.DaysTaken(); got != 270 {
		t.Fatalf("days taken = %d, want 270", got)
	}
	if got := journey.DaysSincePrevious(); got != 119 {
		t.Fatalf("days since the previous milestone = %d, want 119", got)
	}
	if got := journey.AccuracyPercent(); got != 25 {
		t.Fatalf("accuracy = %d, want 25", got)
	}
}

// A first milestone has no previous one, and an unknown start has no span:
// both must read as "leave that line out", never as a wrong number.
func TestMilestoneJourney_MissingDatesAreZeroNotNonsense(t *testing.T) {
	reached := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	first := MilestoneJourney{Milestone: 10, Predictions: 40, ReachedAt: reached}

	if got := first.DaysSincePrevious(); got != 0 {
		t.Fatalf("a first milestone has no previous stretch, got %d", got)
	}
	if got := first.DaysTaken(); got != 0 {
		t.Fatalf("an unknown start has no span, got %d", got)
	}
	// A clock that ran backwards must not produce a negative day count.
	backwards := MilestoneJourney{FirstPredictionAt: reached, ReachedAt: reached.Add(-time.Hour)}
	if got := backwards.DaysTaken(); got != 0 {
		t.Fatalf("time running backwards must be 0 days, got %d", got)
	}
	// And no sample means no rate, rather than a division by zero.
	if got := (MilestoneJourney{Milestone: 10}).AccuracyPercent(); got != 0 {
		t.Fatalf("no predictions means no rate, got %d", got)
	}
}

// The gaps have to widen, or somebody active trips a milestone every other
// week and the congratulation stops meaning anything.
func TestExactMilestones_GrowApart(t *testing.T) {
	for i := 1; i < len(ExactMilestones); i++ {
		previous, current := ExactMilestones[i-1], ExactMilestones[i]
		if current <= previous {
			t.Fatalf("milestones must ascend: %v", ExactMilestones)
		}
		if i > 1 && current-previous < ExactMilestones[i-1]-ExactMilestones[i-2] {
			t.Fatalf("the gap narrowed at %d: %v", current, ExactMilestones)
		}
	}
}
