package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

var offerNow = time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)

// fakeOfferCatalog answers SearchEvents with a canned top-tier candidate
// list — the only catalogue read EventOfferReconciler makes.
type fakeOfferCatalog struct {
	fakeCatalogForCompletion
	topTier []competition.Event
}

func (f *fakeOfferCatalog) FindTeam(context.Context, common.TeamID) (*competition.Team, error) {
	return nil, nil
}

func (f *fakeOfferCatalog) SearchEvents(context.Context, string, int, bool, []competition.GameCode) ([]competition.Event, error) {
	return f.topTier, nil
}

// fakeOfferSubs records what got subscribed and reports what already is.
type fakeOfferSubs struct {
	following  []subscription.EventSubscription
	subscribed []subscription.EventSubscription
}

func (f *fakeOfferSubs) Subscribe(_ context.Context, s subscription.EventSubscription) (subscription.EventSubscription, error) {
	f.subscribed = append(f.subscribed, s)
	return s, nil
}
func (f *fakeOfferSubs) Unsubscribe(context.Context, common.ChatID, common.EventID) error { return nil }
func (f *fakeOfferSubs) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (f *fakeOfferSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) { return nil, nil }
func (f *fakeOfferSubs) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return f.following, nil
}

// fakeOfferStore is the (chat, event) claim, in memory.
type fakeOfferStore struct {
	decided  map[string]subscription.EventOfferKind
	recorded []subscription.EventOfferKind
	at       []time.Time
}

func newOfferStore() *fakeOfferStore {
	return &fakeOfferStore{decided: map[string]subscription.EventOfferKind{}}
}

func offerKey(chatID common.ChatID, eventID common.EventID) string {
	return chatID.String() + ":" + eventID.Value.String()
}

func (f *fakeOfferStore) RecordEventOffer(_ context.Context, chatID common.ChatID, eventID common.EventID,
	kind subscription.EventOfferKind, at time.Time) (bool, error) {
	key := offerKey(chatID, eventID)
	if _, exists := f.decided[key]; exists {
		return false, nil
	}
	f.decided[key] = kind
	f.recorded = append(f.recorded, kind)
	f.at = append(f.at, at)
	return true, nil
}

func (f *fakeOfferStore) UndecidedEvents(_ context.Context, chatID common.ChatID, candidates []common.EventID) ([]common.EventID, error) {
	var out []common.EventID
	for _, id := range candidates {
		if _, exists := f.decided[offerKey(chatID, id)]; !exists {
			out = append(out, id)
		}
	}
	return out, nil
}

// offSwitchboard is alwaysOnSwitchboard's opposite: every proactive message
// is off, which is the shipped default for every chat.
type offSwitchboard struct{ alwaysOnSwitchboard }

func (offSwitchboard) NotifyEnabled(context.Context, common.NotifyScope, int64, string) (bool, error) {
	return false, nil
}

type fakeActiveChats struct{ chats []chat.Settings }

func (f fakeActiveChats) ListActive(context.Context) ([]chat.Settings, error) { return f.chats, nil }

func offerTopTierEvent(name string) competition.Event {
	return competition.Event{
		ID: common.EventID{Value: uuid.New()}, Name: name,
		Game: competition.GameCS2, Tier: competition.TierS, Status: competition.EventUpcoming,
	}
}

func offerChat(auto bool) chat.Settings {
	settings := chat.Settings{
		ChatID: common.ChatID{Value: 42}, Active: true,
		EnabledGames: []competition.GameCode{competition.GameCS2},
	}
	if auto {
		settings.AutoSubscribeGames = []competition.GameCode{competition.GameCS2}
	}
	return settings
}

func newReconciler(catalog competition.Catalog, subs subscription.Repository, store subscription.EventOfferRepository,
	outbox common.Outbox, switches common.NotifySwitchboard, chats []chat.Settings) *EventOfferReconciler {
	return &EventOfferReconciler{
		Chats: fakeActiveChats{chats: chats}, Catalog: catalog, Subscriptions: subs, Offers: store,
		Outbox: outbox, Switches: switches, Lock: fakeClusterLock{}, Clock: fixedClock{now: offerNow},
		Log: slog.Default(),
	}
}

// The plain case the old one-shot path could only ever hit at the instant
// the event row was inserted: a joinable top-tier tournament this chat is
// not in, and a chat that asked to hear about them.
func TestEventOfferReconciler_OffersAJoinableTournament(t *testing.T) {
	event := offerTopTierEvent("PGL Wallachia")
	store, outbox := newOfferStore(), &fakeOutboxForCrossSell{}
	r := newReconciler(&fakeOfferCatalog{topTier: []competition.Event{event}}, &fakeOfferSubs{},
		store, outbox, alwaysOnSwitchboard{}, []chat.Settings{offerChat(false)})

	r.Dispatch(context.Background())

	if len(store.recorded) != 1 || store.recorded[0] != subscription.EventOffered {
		t.Fatalf("expected one OFFERED claim, got %v", store.recorded)
	}
	if len(store.at) != 1 || !store.at[0].Equal(offerNow) {
		t.Fatalf("expected the claim stamped with the clock, got %v", store.at)
	}
	if len(outbox.enqueued) != 1 || outbox.enqueued[0] != "telegram.big-event-discovered" {
		t.Fatalf("expected one discovery offer enqueued, got %v", outbox.enqueued)
	}
}

// The whole point of the reconciler. A chat with the switch off and no
// auto-subscription must be left UNDECIDED, not recorded as handled:
// recording it is what made turning the switch on afterwards a no-op, which
// is how the offer went missing in the first place.
func TestEventOfferReconciler_LeavesASilentChatUndecided(t *testing.T) {
	event := offerTopTierEvent("PGL Wallachia")
	store, outbox := newOfferStore(), &fakeOutboxForCrossSell{}
	catalog := &fakeOfferCatalog{topTier: []competition.Event{event}}
	chats := []chat.Settings{offerChat(false)}

	newReconciler(catalog, &fakeOfferSubs{}, store, outbox, offSwitchboard{}, chats).Dispatch(context.Background())

	if len(store.recorded) != 0 || len(outbox.enqueued) != 0 {
		t.Fatalf("expected nothing decided and nothing sent, got %v / %v", store.recorded, outbox.enqueued)
	}

	// Same chat, same tournament, switch now on: the offer must arrive.
	newReconciler(catalog, &fakeOfferSubs{}, store, outbox, alwaysOnSwitchboard{}, chats).Dispatch(context.Background())

	if len(outbox.enqueued) != 1 {
		t.Fatalf("turning the switch on must deliver the offer that was skipped, got %v", outbox.enqueued)
	}
}

// Auto-subscription joins the chat even with the announcement switched off —
// the switch gates the message, never the membership.
func TestEventOfferReconciler_AutoSubscribesWithTheAnnouncementOff(t *testing.T) {
	event := offerTopTierEvent("PGL Wallachia")
	store, outbox, subs := newOfferStore(), &fakeOutboxForCrossSell{}, &fakeOfferSubs{}
	r := newReconciler(&fakeOfferCatalog{topTier: []competition.Event{event}}, subs,
		store, outbox, offSwitchboard{}, []chat.Settings{offerChat(true)})

	r.Dispatch(context.Background())

	if len(subs.subscribed) != 1 || subs.subscribed[0].EventID != event.ID {
		t.Fatalf("expected the chat joined to the tournament, got %+v", subs.subscribed)
	}
	if len(store.recorded) != 1 || store.recorded[0] != subscription.EventAutoSubscribed {
		t.Fatalf("expected one AUTO_SUBSCRIBED claim, got %v", store.recorded)
	}
	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no announcement for a chat that turned it off, got %v", outbox.enqueued)
	}
}

// A tournament the chat already follows is settled business.
func TestEventOfferReconciler_SkipsATournamentAlreadyFollowed(t *testing.T) {
	event := offerTopTierEvent("PGL Wallachia")
	store, outbox := newOfferStore(), &fakeOutboxForCrossSell{}
	subs := &fakeOfferSubs{following: []subscription.EventSubscription{{EventID: event.ID, Active: true}}}
	r := newReconciler(&fakeOfferCatalog{topTier: []competition.Event{event}}, subs,
		store, outbox, alwaysOnSwitchboard{}, []chat.Settings{offerChat(false)})

	r.Dispatch(context.Background())

	if len(store.recorded) != 0 || len(outbox.enqueued) != 0 {
		t.Fatalf("expected nothing for a tournament already followed, got %v / %v", store.recorded, outbox.enqueued)
	}
}

// Running twice must not send twice, which is what makes it safe to put on
// a schedule at all.
func TestEventOfferReconciler_NeverOffersTheSameTournamentTwice(t *testing.T) {
	event := offerTopTierEvent("PGL Wallachia")
	store, outbox := newOfferStore(), &fakeOutboxForCrossSell{}
	r := newReconciler(&fakeOfferCatalog{topTier: []competition.Event{event}}, &fakeOfferSubs{},
		store, outbox, alwaysOnSwitchboard{}, []chat.Settings{offerChat(false)})

	r.Dispatch(context.Background())
	r.Dispatch(context.Background())

	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected exactly one offer across two runs, got %v", outbox.enqueued)
	}
}

// The backlog the first run faces is drained a few at a time: a room that
// gets fifteen messages at once has been spammed, not informed.
func TestEventOfferReconciler_CapsOffersPerRun(t *testing.T) {
	var events []competition.Event
	for i := 0; i < 8; i++ {
		events = append(events, offerTopTierEvent("Tournament"))
	}
	store, outbox := newOfferStore(), &fakeOutboxForCrossSell{}
	r := newReconciler(&fakeOfferCatalog{topTier: events}, &fakeOfferSubs{},
		store, outbox, alwaysOnSwitchboard{}, []chat.Settings{offerChat(false)})

	r.Dispatch(context.Background())
	if len(outbox.enqueued) != defaultEventOffersPerChatPerRun {
		t.Fatalf("expected %d offers in one run, got %d", defaultEventOffersPerChatPerRun, len(outbox.enqueued))
	}

	r.Dispatch(context.Background())
	if len(outbox.enqueued) != 2*defaultEventOffersPerChatPerRun {
		t.Fatalf("expected the backlog to keep draining, got %d", len(outbox.enqueued))
	}
}

// A chat following no games has no candidates, so it must cost no queries
// and produce nothing.
func TestEventOfferReconciler_IgnoresAChatWithNoGames(t *testing.T) {
	event := offerTopTierEvent("PGL Wallachia")
	store, outbox := newOfferStore(), &fakeOutboxForCrossSell{}
	settings := offerChat(false)
	settings.EnabledGames = nil
	r := newReconciler(&fakeOfferCatalog{topTier: []competition.Event{event}}, &fakeOfferSubs{},
		store, outbox, alwaysOnSwitchboard{}, []chat.Settings{settings})

	r.Dispatch(context.Background())

	if len(store.recorded) != 0 || len(outbox.enqueued) != 0 {
		t.Fatalf("expected nothing for a chat following no games, got %v / %v", store.recorded, outbox.enqueued)
	}
}
