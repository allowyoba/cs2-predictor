package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeTargetsForCrossSell is fakeTargetsForScope plus a working
// SubscribersOf, keyed by (kind, targetID).
type fakeTargetsForCrossSell struct {
	fakeTargetsForScope
	subsByTarget map[string][]subscription.TargetSubscription
}

func (f *fakeTargetsForCrossSell) SubscribersOf(_ context.Context, kind subscription.TargetKind, targetID string) ([]subscription.TargetSubscription, error) {
	return f.subsByTarget[string(kind)+":"+targetID], nil
}

// followedBy is one chat following one team under the name it picked, which
// is the name the offer has to end up quoting.
func followedBy(chatID common.ChatID, teamID common.TeamID) []subscription.TargetSubscription {
	return []subscription.TargetSubscription{{
		ChatID: chatID, Kind: subscription.TargetTeam, TargetID: teamID.Value.String(),
		TargetName: followedTeamName, Active: true,
	}}
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
	payloads []string
}

func (f *fakeOutboxForCrossSell) Enqueue(_ context.Context, _, _, eventType, payload string) (uuid.UUID, error) {
	f.enqueued = append(f.enqueued, eventType)
	f.payloads = append(f.payloads, payload)
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

// crossSellNow is what the offers must be stamped with. They used to be
// stamped with the zero time, because the service built a CrossSellOffer
// without one and the insert wrote it verbatim into a NOT NULL column.
var crossSellNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// followedTeamName is what the chat followed, and therefore what the offer
// has to quote instead of the team's id.
const followedTeamName = "Natus Vincere"

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
	targets := &fakeTargetsForCrossSell{subsByTarget: map[string][]subscription.TargetSubscription{
		"TEAM:" + teamA.Value.String(): followedBy(chatID, teamA),
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
		Clock:   fixedClock{now: crossSellNow},
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
	targets := &fakeTargetsForCrossSell{subsByTarget: map[string][]subscription.TargetSubscription{
		"TEAM:" + teamA.Value.String(): followedBy(chatID, teamA),
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
		Clock:   fixedClock{now: crossSellNow},
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
	targets := &fakeTargetsForCrossSell{subsByTarget: map[string][]subscription.TargetSubscription{
		"TEAM:" + teamA.Value.String(): followedBy(chatID, teamA),
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
		Clock:   fixedClock{now: crossSellNow},
	}

	if err := svc.DiscoverAndOffer(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no repeat notification, got %v", outbox.enqueued)
	}
}

// The offer's text names the team the chat actually followed. It used to
// carry sub.TargetID instead, so every one of these messages showed a bare
// UUID where the team name belonged.
func TestTargetCrossSellService_OfferNamesTheTeamNotItsID(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	chatID := common.ChatID{Value: 42}

	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {matchBetween(eventID, teamA, teamB)},
	}}
	targets := &fakeTargetsForCrossSell{subsByTarget: map[string][]subscription.TargetSubscription{
		"TEAM:" + teamA.Value.String(): followedBy(chatID, teamA),
	}}
	offers := &fakeCrossSellOffers{isNew: true}
	outbox := &fakeOutboxForCrossSell{}

	svc := &TargetCrossSellService{
		Catalog: catalog, Subs: &fakeSubsForCompletion{}, Targets: targets,
		Offers: offers, Outbox: outbox,
		Gate:  NotifyGate{Switches: alwaysOnSwitchboard{}},
		Clock: fixedClock{now: crossSellNow},
	}
	if err := svc.DiscoverAndOffer(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outbox.payloads) != 1 {
		t.Fatalf("expected one payload, got %v", outbox.payloads)
	}
	var n TargetCrossSellNotification
	if err := json.Unmarshal([]byte(outbox.payloads[0]), &n); err != nil {
		t.Fatalf("payload is not a cross-sell notification: %v", err)
	}
	if n.TargetName != followedTeamName {
		t.Fatalf("expected the followed team's name, got %q", n.TargetName)
	}
	if strings.Contains(n.TargetName, "-") && n.TargetName == teamA.Value.String() {
		t.Fatal("the offer is quoting the team's UUID at people")
	}
	if len(offers.recorded) != 1 || !offers.recorded[0].OfferedAt.Equal(crossSellNow) {
		t.Fatalf("expected the offer stamped with the clock, got %+v", offers.recorded)
	}
}

// fakeRostersForCrossSell answers "who plays for this team" from a fixed map.
type fakeRostersForCrossSell struct {
	byTeam map[common.TeamID][]competition.RosterMember
}

func (f *fakeRostersForCrossSell) SavePlayer(context.Context, competition.GameCode, string, string, competition.Player) (competition.Player, error) {
	return competition.Player{}, nil
}
func (f *fakeRostersForCrossSell) ReplaceRoster(context.Context, common.TeamID, []competition.RosterMember) error {
	return nil
}
func (f *fakeRostersForCrossSell) Roster(_ context.Context, teamID common.TeamID) ([]competition.RosterMember, error) {
	return f.byTeam[teamID], nil
}
func (f *fakeRostersForCrossSell) FindPlayer(context.Context, common.PlayerID) (*competition.Player, error) {
	return nil, nil
}
func (f *fakeRostersForCrossSell) SearchPlayers(context.Context, string, int, []competition.GameCode) ([]competition.Player, error) {
	return nil, nil
}
func (f *fakeRostersForCrossSell) TeamsOfPlayer(context.Context, common.PlayerID) ([]common.TeamID, error) {
	return nil, nil
}

// Following a player, not their team, must still produce the offer when that
// player turns up in a tournament — read off the roster, since a transfer is
// exactly when "the team" and "the person" stop being the same answer.
func TestTargetCrossSellService_OffersAChatFollowingAPlayer(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	chatID := common.ChatID{Value: 42}
	playerID := common.PlayerID{Value: uuid.New()}

	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {matchBetween(eventID, teamA, teamB)},
	}}
	targets := &fakeTargetsForCrossSell{subsByTarget: map[string][]subscription.TargetSubscription{
		"PLAYER:" + playerID.Value.String(): {{
			ChatID: chatID, Kind: subscription.TargetPlayer, TargetID: playerID.Value.String(),
			TargetName: "s1mple", Active: true,
		}},
	}}
	rosters := &fakeRostersForCrossSell{byTeam: map[common.TeamID][]competition.RosterMember{
		teamA: {{Player: competition.Player{ID: playerID, Nickname: "s1mple"}}},
	}}
	offers, outbox := &fakeCrossSellOffers{isNew: true}, &fakeOutboxForCrossSell{}

	svc := &TargetCrossSellService{
		Catalog: catalog, Subs: &fakeSubsForCompletion{}, Targets: targets, Rosters: rosters,
		Offers: offers, Outbox: outbox,
		Gate:  NotifyGate{Switches: alwaysOnSwitchboard{}},
		Clock: fixedClock{now: crossSellNow},
	}
	if err := svc.DiscoverAndOffer(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outbox.payloads) != 1 {
		t.Fatalf("expected one offer for the chat following the player, got %v", outbox.enqueued)
	}
	var n TargetCrossSellNotification
	if err := json.Unmarshal([]byte(outbox.payloads[0]), &n); err != nil {
		t.Fatal(err)
	}
	if n.TargetKind != string(subscription.TargetPlayer) || n.TargetName != "s1mple" {
		t.Fatalf("expected the offer to name the followed player, got %+v", n)
	}
}

// With rosters unwired the team half must still work — a missing player
// catalogue is a feature that is off, not a fan-out that fails.
func TestTargetCrossSellService_WorksWithoutRosters(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	chatID := common.ChatID{Value: 42}

	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {matchBetween(eventID, teamA, teamB)},
	}}
	targets := &fakeTargetsForCrossSell{subsByTarget: map[string][]subscription.TargetSubscription{
		"TEAM:" + teamA.Value.String(): followedBy(chatID, teamA),
	}}
	offers, outbox := &fakeCrossSellOffers{isNew: true}, &fakeOutboxForCrossSell{}

	svc := &TargetCrossSellService{
		Catalog: catalog, Subs: &fakeSubsForCompletion{}, Targets: targets,
		Offers: offers, Outbox: outbox,
		Gate:  NotifyGate{Switches: alwaysOnSwitchboard{}},
		Clock: fixedClock{now: crossSellNow},
	}
	if err := svc.DiscoverAndOffer(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected the team offer regardless, got %v", outbox.enqueued)
	}
}
