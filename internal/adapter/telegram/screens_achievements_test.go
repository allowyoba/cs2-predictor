package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

type fakeMilestoneStore struct {
	total   int
	reached []scoring.MilestoneRecord
}

func (f *fakeMilestoneStore) ClaimMilestone(context.Context, common.ChatID, common.UserID, int, time.Time) (bool, error) {
	return false, nil
}
func (f *fakeMilestoneStore) MilestoneJourney(context.Context, common.ChatID, common.UserID, int) (scoring.MilestoneJourney, error) {
	return scoring.MilestoneJourney{}, nil
}
func (f *fakeMilestoneStore) UserMilestones(context.Context, common.UserID) ([]scoring.MilestoneRecord, error) {
	return f.reached, nil
}
func (f *fakeMilestoneStore) ExactCount(context.Context, common.ChatID, common.UserID) (int, error) {
	return 0, nil
}
func (f *fakeMilestoneStore) UserExactTotal(context.Context, common.UserID) (int, error) {
	return f.total, nil
}

func achievementsText(t *testing.T, calls []map[string]any) string {
	t.Helper()
	var body strings.Builder
	for _, c := range calls {
		if text, ok := c["text"].(string); ok {
			body.WriteString(text + "\n")
		}
	}
	return body.String()
}

// The shelf shows the whole ladder, not only what is earned: the rungs ahead
// are what give the earned ones their scale.
func TestAchievements_ShowsEarnedAndRemainingRungs(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.Milestones = &fakeMilestoneStore{
		total: 120,
		reached: []scoring.MilestoneRecord{
			{Milestone: 100, ReachedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		},
	}

	if err := handler.achievementsView(context.Background(),
		sendTarget(common.ChatID{Value: 7}, nil), common.UserID{Value: 7}, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	body := achievementsText(t, *calls)

	if !strings.Contains(body, "01.09.2026") {
		t.Fatalf("an earned milestone must be dated: %q", body)
	}
	if !strings.Contains(body, "🏅") || !strings.Contains(body, "▫️") {
		t.Fatalf("the ladder must show both earned and remaining rungs: %q", body)
	}
	// 120 exact scores: the next rung is 250, 130 away.
	if !strings.Contains(body, "250") || !strings.Contains(body, "130") {
		t.Fatalf("the shelf must say how far the next milestone is: %q", body)
	}
}

// Somebody who passed a milestone before this feature existed has earned it
// even though nothing recorded the date.
func TestAchievements_CountsMilestonesPassedBeforeAnythingRecordedThem(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.Milestones = &fakeMilestoneStore{total: 60}

	if err := handler.achievementsView(context.Background(),
		sendTarget(common.ChatID{Value: 7}, nil), common.UserID{Value: 7}, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	body := achievementsText(t, *calls)

	// 10, 25 and 50 are earned; 100 is not. Counted as ladder rungs rather
	// than as medals in the text, since the heading carries one too.
	earned := strings.Count(body, "\n🏅 ")
	if earned != 3 {
		t.Fatalf("expected three earned rungs for 60 exact scores, got %d: %q", earned, body)
	}
	if !strings.Contains(body, "\n▫️ 100") {
		t.Fatalf("100 must still be ahead of them: %q", body)
	}
}

// An empty shelf says what this is and how to get one, rather than nothing.
func TestAchievements_EmptyShelfExplainsItself(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.Milestones = &fakeMilestoneStore{}

	if err := handler.achievementsView(context.Background(),
		sendTarget(common.ChatID{Value: 7}, nil), common.UserID{Value: 7}, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	body := achievementsText(t, *calls)
	if !strings.Contains(body, stripHTML(handler.Texts.Get("achievements.empty", common.LocaleRU))) {
		t.Fatalf("an empty shelf must explain what this is: %q", body)
	}
	if strings.Contains(body, "\n🏅 ") {
		t.Fatalf("nothing is earned yet: %q", body)
	}
}
