package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

var milestoneNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// fakeMilestones is the claim and the counts, in memory.
type fakeMilestones struct {
	exact   map[common.UserID]int
	claimed map[string]bool
	journey scoring.MilestoneJourney
	claims  []int
}

func newFakeMilestones(exact int) *fakeMilestones {
	return &fakeMilestones{
		exact:   map[common.UserID]int{milestoneVoter: exact},
		claimed: map[string]bool{},
		journey: scoring.MilestoneJourney{Predictions: 400, Events: 12},
	}
}

func (f *fakeMilestones) ClaimMilestone(_ context.Context, chatID common.ChatID, userID common.UserID, milestone int, _ time.Time) (bool, error) {
	key := chatID.String() + ":" + userID.String() + ":" + string(rune(milestone))
	if f.claimed[key] {
		return false, nil
	}
	f.claimed[key] = true
	f.claims = append(f.claims, milestone)
	return true, nil
}

func (f *fakeMilestones) MilestoneJourney(context.Context, common.ChatID, common.UserID, int) (scoring.MilestoneJourney, error) {
	return f.journey, nil
}
func (f *fakeMilestones) UserMilestones(context.Context, common.UserID) ([]scoring.MilestoneRecord, error) {
	return nil, nil
}
func (f *fakeMilestones) ExactCount(_ context.Context, _ common.ChatID, userID common.UserID) (int, error) {
	return f.exact[userID], nil
}
func (f *fakeMilestones) UserExactTotal(context.Context, common.UserID) (int, error) { return 0, nil }

// milestoneVoter is whose predictions these are; the fakes above are keyed
// by the same id.
var milestoneVoter = common.UserID{Value: 7}

func exactAward() scoring.Award {
	return scoring.Award{UserID: milestoneVoter, Kind: scoring.AwardExactScore, Points: 3}
}

func newMilestoneService(store *fakeMilestones, outbox common.Outbox, switches common.NotifySwitchboard) *MilestoneService {
	return &MilestoneService{
		Milestones: store, Outbox: outbox, Gate: NotifyGate{Switches: switches},
		Clock: fixedClock{now: milestoneNow}, Log: slog.Default(),
	}
}

// The hundredth exact prediction is congratulated, with the story attached.
func TestMilestoneService_CongratulatesOnCrossing(t *testing.T) {
	store := newFakeMilestones(100) // count after this settlement
	outbox := &fakeOutboxForCrossSell{}
	names := map[common.UserID]string{milestoneVoter: "Serj"}

	newMilestoneService(store, outbox, alwaysOnSwitchboard{}).
		Record(context.Background(), common.ChatID{Value: -1}, names, []scoring.Award{exactAward()})

	if len(outbox.enqueued) != 1 || outbox.enqueued[0] != "telegram.milestone" {
		t.Fatalf("expected one milestone notification, got %v", outbox.enqueued)
	}
	var n common.MilestoneNotification
	if err := json.Unmarshal([]byte(outbox.payloads[0]), &n); err != nil {
		t.Fatal(err)
	}
	if n.Milestone != 100 || n.DisplayName != "Serj" {
		t.Fatalf("unexpected notification: %+v", n)
	}
	if n.Predictions != 400 || n.Events != 12 || n.AccuracyPercent != 25 {
		t.Fatalf("the congratulation must carry what it took: %+v", n)
	}
}

// Settling the same poll twice — a retry, or a corrected score — must not
// congratulate the same person for the same hundredth prediction again.
func TestMilestoneService_NeverCongratulatesTheSameMilestoneTwice(t *testing.T) {
	store := newFakeMilestones(100)
	outbox := &fakeOutboxForCrossSell{}
	service := newMilestoneService(store, outbox, alwaysOnSwitchboard{})
	names := map[common.UserID]string{milestoneVoter: "Serj"}

	service.Record(context.Background(), common.ChatID{Value: -1}, names, []scoring.Award{exactAward()})
	service.Record(context.Background(), common.ChatID{Value: -1}, names, []scoring.Award{exactAward()})

	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected exactly one congratulation across two settlements, got %v", outbox.enqueued)
	}
}

// Nowhere near a milestone: nothing is claimed and nothing is said.
func TestMilestoneService_SaysNothingBetweenMilestones(t *testing.T) {
	store := newFakeMilestones(57)
	outbox := &fakeOutboxForCrossSell{}

	newMilestoneService(store, outbox, alwaysOnSwitchboard{}).
		Record(context.Background(), common.ChatID{Value: -1}, nil, []scoring.Award{exactAward()})

	if len(outbox.enqueued) != 0 || len(store.claims) != 0 {
		t.Fatalf("expected silence between milestones, got %v / %v", outbox.enqueued, store.claims)
	}
}

// Guessing the winner is not the achievement: only exact scores count.
func TestMilestoneService_IgnoresAwardsThatAreNotExact(t *testing.T) {
	store := newFakeMilestones(100)
	outbox := &fakeOutboxForCrossSell{}

	newMilestoneService(store, outbox, alwaysOnSwitchboard{}).
		Record(context.Background(), common.ChatID{Value: -1}, nil,
			[]scoring.Award{{UserID: milestoneVoter, Kind: scoring.AwardOutcome, Points: 1}})

	if len(outbox.enqueued) != 0 {
		t.Fatalf("a correct-winner award is not a milestone, got %v", outbox.enqueued)
	}
}

// The achievement is theirs whether or not the room wants the message: the
// switch silences the congratulation, it does not withhold the milestone.
func TestMilestoneService_ClaimsTheMilestoneEvenWithTheChatSilenced(t *testing.T) {
	store := newFakeMilestones(100)
	outbox := &fakeOutboxForCrossSell{}

	newMilestoneService(store, outbox, offSwitchboard{}).
		Record(context.Background(), common.ChatID{Value: -1}, nil, []scoring.Award{exactAward()})

	if len(outbox.enqueued) != 0 {
		t.Fatalf("a silenced chat gets no message, got %v", outbox.enqueued)
	}
	if len(store.claims) != 1 || store.claims[0] != 100 {
		t.Fatalf("the milestone is still theirs: %v", store.claims)
	}
}

// Two exact scores in one settlement can carry somebody across a boundary
// they were one short of — the "before" has to be derived from the awards,
// not read as a second query.
func TestMilestoneService_HandlesSeveralExactScoresInOneSettlement(t *testing.T) {
	store := newFakeMilestones(101)
	outbox := &fakeOutboxForCrossSell{}

	newMilestoneService(store, outbox, alwaysOnSwitchboard{}).
		Record(context.Background(), common.ChatID{Value: -1}, nil,
			[]scoring.Award{exactAward(), exactAward()})

	if len(store.claims) != 1 || store.claims[0] != 100 {
		t.Fatalf("expected the 100 boundary crossed, got %v", store.claims)
	}
}

// A service that was never wired must be a no-op rather than a panic: the
// settlement path calls it unconditionally.
func TestMilestoneService_NilIsHarmless(t *testing.T) {
	var service *MilestoneService
	service.Record(context.Background(), common.ChatID{Value: -1}, nil, []scoring.Award{exactAward()})
}
