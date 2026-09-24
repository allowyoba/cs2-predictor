package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeTargetsForCrossSell is fakeTargetsForScope plus a working
// ChatsForTarget, keyed by (kind, targetID).
type fakeTargetsForCrossSell struct {
	fakeTargetsForScope
	chatsByTarget map[string][]common.ChatID
}

func (f *fakeTargetsForCrossSell) ChatsForTarget(_ context.Context, kind subscription.TargetKind, targetID string) ([]common.ChatID, error) {
	return f.chatsByTarget[string(kind)+":"+targetID], nil
}

type fakeCrossSellOffers struct {
	recorded []subscription.CrossSellOffer
	isNew    bool
}

func (f *fakeCrossSellOffers) RecordOffer(_ context.Context, o subscription.CrossSellOffer) (bool, error) {
	f.recorded = append(f.recorded, o)
	return f.isNew, nil
}
func (f *fakeCrossSellOffers) MarkDismissed(context.Context, common.ChatID, common.EventID) error {
	return nil
}
func (f *fakeCrossSellOffers) MarkSubscribed(context.Context, common.ChatID, common.EventID) error {
	return nil
}

type fakeOutboxForCrossSell struct {
	enqueued []string // eventType values
}

func (f *fakeOutboxForCrossSell) Enqueue(_ context.Context, _, _, eventType, _ string) (uuid.UUID, error) {
	f.enqueued = append(f.enqueued, eventType)
	return uuid.New(), nil
}
func (f *fakeOutboxForCrossSell) Pending(context.Context, int) ([]common.OutboxMessage, error) {
	return nil, nil
}
func (f *fakeOutboxForCrossSell) Published(context.Context, uuid.UUID, time.Time) error { return nil }
func (f *fakeOutboxForCrossSell) Failed(context.Context, uuid.UUID, time.Time, string) error {
	return nil
}
func (f *fakeOutboxForCrossSell) Defer(context.Context, uuid.UUID, time.Time, time.Time) error {
	return nil
}

// alwaysOnSwitchboard turns every chat notification kind on, so the
// service's own gate never blocks a test on its own.
type alwaysOnSwitchboard struct{}

func (alwaysOnSwitchboard) NotifyEnabled(context.Context, common.NotifyScope, int64, string) (bool, error) {
	return true, nil
}
func (alwaysOnSwitchboard) NotifySettings(context.Context, common.NotifyScope, int64) (map[string]bool, error) {
	return nil, nil
}
func (alwaysOnSwitchboard) SetNotifyEnabled(context.Context, common.NotifyScope, int64, string, bool) error {
	return nil
}
func (alwaysOnSwitchboard) NotifySubjects(context.Context, common.NotifyScope, string, []int64) ([]int64, error) {
	return nil, nil
}

// A chat following a team playing in this event, not tournament-subscribed:
// it gets exactly one offer.
func TestTargetCrossSellService_OffersUnsubscribedChat(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	chatID := common.ChatID{Value: 42}

	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {matchBetween(eventID, teamA, teamB)},
	}}
	targets := &fakeTargetsForCrossSell{chatsByTarget: map[string][]common.ChatID{
		"TEAM:" + teamA.Value.String(): {chatID},
	}}
	offers := &fakeCrossSellOffers{isNew: true}
	outbox := &fakeOutboxForCrossSell{}

	svc := &TargetCrossSellService{
		Catalog: catalog,
		Subs:    &fakeSubsForCompletion{}, // no chats tournament-subscribed
		Targets: targets,
		Offers:  offers,
		Outbox:  outbox,
		Gate:    NotifyGate{Switches: alwaysOnSwitchboard{}},
	}

	if err := svc.DiscoverAndOffer(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers.recorded) != 1 || offers.recorded[0].ChatID != chatID {
		t.Fatalf("expected exactly one recorded offer for the following chat, got %+v", offers.recorded)
	}
	if len(outbox.enqueued) != 1 || outbox.enqueued[0] != "telegram.target-cross-sell-offer" {
		t.Fatalf("expected exactly one cross-sell notification enqueued, got %v", outbox.enqueued)
	}
}

// A chat already tournament-subscribed to the event must never be offered
// a cross-sell for it, even if it also follows a competing team.
func TestTargetCrossSellService_SkipsAlreadyTournamentSubscribedChat(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	chatID := common.ChatID{Value: 42}

	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {matchBetween(eventID, teamA, teamB)},
	}}
	targets := &fakeTargetsForCrossSell{chatsByTarget: map[string][]common.ChatID{
		"TEAM:" + teamA.Value.String(): {chatID},
	}}
	offers := &fakeCrossSellOffers{isNew: true}
	outbox := &fakeOutboxForCrossSell{}

	svc := &TargetCrossSellService{
		Catalog: catalog,
		Subs:    &fakeSubsForCompletion{chats: []common.ChatID{chatID}},
		Targets: targets,
		Offers:  offers,
		Outbox:  outbox,
		Gate:    NotifyGate{Switches: alwaysOnSwitchboard{}},
	}

	if err := svc.DiscoverAndOffer(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers.recorded) != 0 {
		t.Fatalf("expected no offer for an already tournament-subscribed chat, got %+v", offers.recorded)
	}
	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no notification enqueued, got %v", outbox.enqueued)
	}
}

// A repeat discovery (isNew=false, meaning RecordOffer found the offer
// already exists) must not enqueue a second notification — the dedup.
func TestTargetCrossSellService_DoesNotRepeatAnExistingOffer(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	chatID := common.ChatID{Value: 42}

	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {matchBetween(eventID, teamA, teamB)},
	}}
	targets := &fakeTargetsForCrossSell{chatsByTarget: map[string][]common.ChatID{
		"TEAM:" + teamA.Value.String(): {chatID},
	}}
	offers := &fakeCrossSellOffers{isNew: false}
	outbox := &fakeOutboxForCrossSell{}

	svc := &TargetCrossSellService{
		Catalog: catalog,
		Subs:    &fakeSubsForCompletion{},
		Targets: targets,
		Offers:  offers,
		Outbox:  outbox,
		Gate:    NotifyGate{Switches: alwaysOnSwitchboard{}},
	}

	if err := svc.DiscoverAndOffer(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no repeat notification, got %v", outbox.enqueued)
	}
}
