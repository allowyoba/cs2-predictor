package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeSyncCatalog is a minimal, stateful competition.Catalog for
// DiscoverEvents tests: unlike the completion_test.go fakes, FindEvent/
// SaveEvent behave like a real map-backed store, so a test can actually
// observe "was this the first time we saved this event" — the exact signal
// DiscoverEvents/announceBigEvent key on.
type fakeSyncCatalog struct {
	events map[common.EventID]competition.Event
	// failSaveFor, when set, makes SaveEvent fail for exactly this one
	// event id — used to prove one event's failure doesn't stop the rest
	// of a DiscoverEvents batch from being processed.
	failSaveFor *common.EventID
}

func newFakeSyncCatalog() *fakeSyncCatalog {
	return &fakeSyncCatalog{events: map[common.EventID]competition.Event{}}
}
func (f *fakeSyncCatalog) SearchEvents(context.Context, string, int, bool, []competition.GameCode) ([]competition.Event, error) {
	return nil, nil
}
func (f *fakeSyncCatalog) FindEvent(_ context.Context, id common.EventID) (*competition.Event, error) {
	if e, ok := f.events[id]; ok {
		return &e, nil
	}
	return nil, nil
}
func (f *fakeSyncCatalog) FindEvents(context.Context, []common.EventID) ([]competition.Event, error) {
	return nil, nil
}
func (f *fakeSyncCatalog) FindMatch(context.Context, common.MatchID) (*competition.Match, error) {
	return nil, nil
}
func (f *fakeSyncCatalog) FindUnstartedMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeSyncCatalog) FindUnstartedMatchesForEvents(context.Context, []common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeSyncCatalog) FindMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeSyncCatalog) SaveEvent(_ context.Context, e competition.Event) (competition.Event, error) {
	if f.failSaveFor != nil && e.ID == *f.failSaveFor {
		return competition.Event{}, errors.New("save event failed")
	}
	f.events[e.ID] = e
	return e, nil
}
func (f *fakeSyncCatalog) SaveMatch(_ context.Context, m competition.Match) (competition.Match, error) {
	return m, nil
}

// fakeSyncSubs is a subscription.Repository fake with a per-event
// subscribed-chats map, for announceBigEvent's "exclude already-subscribed
// chats" filter.
type fakeSyncSubs struct {
	subscribedByEvent map[common.EventID][]common.ChatID
	subscribed        []subscription.EventSubscription
	subscribeErr      error
}

func (f *fakeSyncSubs) Subscribe(_ context.Context, s subscription.EventSubscription) (subscription.EventSubscription, error) {
	if f.subscribeErr != nil {
		return subscription.EventSubscription{}, f.subscribeErr
	}
	f.subscribed = append(f.subscribed, s)
	return s, nil
}
func (f *fakeSyncSubs) Unsubscribe(context.Context, common.ChatID, common.EventID) error { return nil }
func (f *fakeSyncSubs) SubscribedChats(_ context.Context, eventID common.EventID) ([]common.ChatID, error) {
	return f.subscribedByEvent[eventID], nil
}
func (f *fakeSyncSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) { return nil, nil }
func (f *fakeSyncSubs) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return nil, nil
}

// fakeSyncChats is a chat.Repository that also implements
// chat.ActiveChatLister, exactly like the production *postgres.ChatRepository
// — needed so announceBigEvent's optional-capability type assertion succeeds
// in tests instead of silently no-op'ing.
type fakeSyncChats struct {
	active []chat.Settings
}

func (f *fakeSyncChats) Find(_ context.Context, chatID common.ChatID) (*chat.Settings, error) {
	for _, s := range f.active {
		if s.ChatID == chatID {
			return &s, nil
		}
	}
	return nil, nil
}
func (f *fakeSyncChats) Save(_ context.Context, s chat.Settings) (chat.Settings, error) {
	return s, nil
}
func (f *fakeSyncChats) SetEnabledGames(context.Context, common.ChatID, []competition.GameCode) error {
	return nil
}
func (f *fakeSyncChats) IsModerator(context.Context, common.ChatID, common.UserID) (bool, error) {
	return false, nil
}
func (f *fakeSyncChats) AddModerator(context.Context, chat.Moderator) error { return nil }
func (f *fakeSyncChats) RemoveModerator(context.Context, common.ChatID, common.UserID) error {
	return nil
}
func (f *fakeSyncChats) EventTopic(context.Context, common.ChatID, common.EventID) (*int64, error) {
	return nil, nil
}
func (f *fakeSyncChats) SaveEventTopic(context.Context, chat.EventTopic) error { return nil }
func (f *fakeSyncChats) MigrateChatID(context.Context, common.ChatID, common.ChatID) error {
	return nil
}
func (f *fakeSyncChats) ModeratorPermissions(context.Context, common.ChatID, common.UserID) ([]chat.Permission, error) {
	return nil, nil
}
func (f *fakeSyncChats) SetModeratorPermissions(context.Context, common.ChatID, common.UserID, []chat.Permission) error {
	return nil
}
func (f *fakeSyncChats) UserProfile(context.Context, common.UserID) (*chat.UserProfile, error) {
	return nil, nil
}
func (f *fakeSyncChats) UserProfiles(context.Context, []common.UserID) (map[common.UserID]chat.UserProfile, error) {
	return nil, nil
}
func (f *fakeSyncChats) ClearEventTopic(context.Context, common.ChatID, common.EventID) error {
	return nil
}
func (f *fakeSyncChats) ListActive(context.Context) ([]chat.Settings, error) { return f.active, nil }

// fakeSyncOutbox records aggregateID/eventType per Enqueue call, so a test
// can assert exactly which chats got which notification.
type fakeSyncOutbox struct {
	enqueued []fakeSyncOutboxCall
}
type fakeSyncOutboxCall struct{ aggregateID, eventType string }

func (o *fakeSyncOutbox) Enqueue(_ context.Context, _, aggregateID, eventType, _ string) (uuid.UUID, error) {
	o.enqueued = append(o.enqueued, fakeSyncOutboxCall{aggregateID, eventType})
	return uuid.New(), nil
}
func (o *fakeSyncOutbox) Pending(context.Context, int) ([]common.OutboxMessage, error) {
	return nil, nil
}
func (o *fakeSyncOutbox) Published(context.Context, uuid.UUID, time.Time) error      { return nil }
func (o *fakeSyncOutbox) Failed(context.Context, uuid.UUID, time.Time, string) error { return nil }

// newTestSync builds a CompetitionSynchronization wired only for
// DiscoverEvents (Predictions/Settlement/EventCompletion stay nil — none of
// DiscoverEvents's paths exercised below call into them).
func newTestSync(t *testing.T, provider *fixedProvider, catalog *fakeSyncCatalog, subs *fakeSyncSubs, chats *fakeSyncChats, outbox *fakeSyncOutbox) *CompetitionSynchronization {
	t.Helper()
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{provider},
		ProviderRoutingConfig{Order: []string{provider.name}}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}
	return &CompetitionSynchronization{
		Gateway: gw, Catalog: catalog, Subscriptions: subs, Chats: chats, ActiveChats: chats, Outbox: outbox,
		Lock: fakeClusterLock{}, Clock: common.SystemUTCClock(), Metrics: newTestMetrics(), Log: slog.Default(),
	}
}

func topTierEvent(name string) competition.Event {
	return competition.Event{
		ID: common.NewEventID(), Game: competition.GameCS2, Name: name,
		ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE", Tier: competition.TierS,
	}
}

func TestDiscoverEvents_AnnouncesNewTopTierEventToActiveChatsNotYetSubscribed(t *testing.T) {
	event := topTierEvent("IEM Katowice")
	provider := &fixedProvider{name: "PANDASCORE", events: []competition.Event{event}}
	catalog := newFakeSyncCatalog()
	subs := &fakeSyncSubs{}
	subscribedChat, unsubscribedChat := common.ChatID{Value: -1}, common.ChatID{Value: -2}
	subs.subscribedByEvent = map[common.EventID][]common.ChatID{event.ID: {subscribedChat}}
	chats := &fakeSyncChats{active: []chat.Settings{
		{ChatID: subscribedChat, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}},
		{ChatID: unsubscribedChat, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}},
	}}
	outbox := &fakeSyncOutbox{}
	sync := newTestSync(t, provider, catalog, subs, chats, outbox)

	sync.DiscoverEvents(context.Background())

	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected exactly 1 announcement (only the unsubscribed chat), got %d: %+v", len(outbox.enqueued), outbox.enqueued)
	}
	call := outbox.enqueued[0]
	if call.eventType != "telegram.big-event-discovered" {
		t.Fatalf("eventType = %q, want telegram.big-event-discovered", call.eventType)
	}
	wantAggregateID := "-2:big-event:" + event.ID.Value.String()
	if call.aggregateID != wantAggregateID {
		t.Fatalf("aggregateID = %q, want %q", call.aggregateID, wantAggregateID)
	}
}

// A chat with AutoSubscribeTopTier set skips the offer entirely: it's
// actually subscribed, and told via a different ("auto-subscribed")
// notification instead of the usual subscribe-button offer.
func TestDiscoverEvents_AutoSubscribesChatsThatOptedIn(t *testing.T) {
	event := topTierEvent("IEM Katowice")
	provider := &fixedProvider{name: "PANDASCORE", events: []competition.Event{event}}
	catalog := newFakeSyncCatalog()
	subs := &fakeSyncSubs{}
	autoChat, plainChat := common.ChatID{Value: -1}, common.ChatID{Value: -2}
	chats := &fakeSyncChats{active: []chat.Settings{
		{ChatID: autoChat, Active: true, AutoSubscribeTopTier: true, EnabledGames: []competition.GameCode{competition.GameCS2}},
		{ChatID: plainChat, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}},
	}}
	outbox := &fakeSyncOutbox{}
	sync := newTestSync(t, provider, catalog, subs, chats, outbox)

	sync.DiscoverEvents(context.Background())

	if len(subs.subscribed) != 1 || subs.subscribed[0].ChatID != autoChat || subs.subscribed[0].EventID != event.ID {
		t.Fatalf("expected exactly one auto-subscribe call for the opted-in chat, got %+v", subs.subscribed)
	}
	if len(outbox.enqueued) != 2 {
		t.Fatalf("expected 2 notifications (one per chat), got %d: %+v", len(outbox.enqueued), outbox.enqueued)
	}
	byChat := map[string]string{}
	for _, call := range outbox.enqueued {
		byChat[call.aggregateID] = call.eventType
	}
	if byChat["-1:big-event:"+event.ID.Value.String()] != "telegram.auto-subscribed" {
		t.Fatalf("expected the auto-subscribed event type for the opted-in chat, got %+v", byChat)
	}
	if byChat["-2:big-event:"+event.ID.Value.String()] != "telegram.big-event-discovered" {
		t.Fatalf("expected the usual offer event type for the plain chat, got %+v", byChat)
	}
}

// A Subscribe failure for one auto-subscribing chat must not stop the rest
// of the batch (a different chat, or a plain offer) from being processed —
// same per-item resilience as an outbox enqueue failure already gets.
func TestDiscoverEvents_AutoSubscribeFailureDoesNotStopTheRestOfTheBatch(t *testing.T) {
	event := topTierEvent("IEM Katowice")
	provider := &fixedProvider{name: "PANDASCORE", events: []competition.Event{event}}
	catalog := newFakeSyncCatalog()
	subs := &fakeSyncSubs{subscribeErr: errors.New("db unavailable")}
	failingChat, plainChat := common.ChatID{Value: -1}, common.ChatID{Value: -2}
	chats := &fakeSyncChats{active: []chat.Settings{
		{ChatID: failingChat, Active: true, AutoSubscribeTopTier: true, EnabledGames: []competition.GameCode{competition.GameCS2}},
		{ChatID: plainChat, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}},
	}}
	outbox := &fakeSyncOutbox{}
	sync := newTestSync(t, provider, catalog, subs, chats, outbox)

	sync.DiscoverEvents(context.Background())

	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected only the plain chat's offer to be enqueued, got %+v", outbox.enqueued)
	}
	if outbox.enqueued[0].aggregateID != "-2:big-event:"+event.ID.Value.String() {
		t.Fatalf("expected the plain chat's notification, got %+v", outbox.enqueued[0])
	}
}

func TestDiscoverEvents_DoesNotReannounceOnASubsequentSyncTick(t *testing.T) {
	event := topTierEvent("IEM Katowice")
	provider := &fixedProvider{name: "PANDASCORE", events: []competition.Event{event}}
	catalog := newFakeSyncCatalog()
	subs := &fakeSyncSubs{}
	chats := &fakeSyncChats{active: []chat.Settings{{ChatID: common.ChatID{Value: -1}, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}}}}
	outbox := &fakeSyncOutbox{}
	sync := newTestSync(t, provider, catalog, subs, chats, outbox)

	sync.DiscoverEvents(context.Background()) // first tick: discovers + announces
	sync.DiscoverEvents(context.Background()) // second tick: same event, already known

	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected exactly 1 announcement across both ticks, got %d: %+v", len(outbox.enqueued), outbox.enqueued)
	}
}

// One event's SaveEvent failing must not stop the rest of the batch from
// being discovered and announced.
func TestDiscoverEvents_ContinuesPastOneEventsFailure(t *testing.T) {
	failing := topTierEvent("Broken Event")
	ok := topTierEvent("IEM Katowice")
	provider := &fixedProvider{name: "PANDASCORE", events: []competition.Event{failing, ok}}
	catalog := newFakeSyncCatalog()
	catalog.failSaveFor = &failing.ID
	subs := &fakeSyncSubs{}
	chats := &fakeSyncChats{active: []chat.Settings{{ChatID: common.ChatID{Value: -1}, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}}}}
	outbox := &fakeSyncOutbox{}
	sync := newTestSync(t, provider, catalog, subs, chats, outbox)

	sync.DiscoverEvents(context.Background())

	if _, saved := catalog.events[failing.ID]; saved {
		t.Fatal("the failing event must not have been persisted")
	}
	if _, saved := catalog.events[ok.ID]; !saved {
		t.Fatal("the event after the failing one must still have been persisted")
	}
	if len(outbox.enqueued) != 1 || outbox.enqueued[0].aggregateID != "-1:big-event:"+ok.ID.Value.String() {
		t.Fatalf("expected exactly one announcement for the event that succeeded, got %+v", outbox.enqueued)
	}
}

func TestDiscoverEvents_DoesNotAnnounceNonTopTierEvent(t *testing.T) {
	event := topTierEvent("Regional Qualifier")
	event.Tier = competition.TierC
	provider := &fixedProvider{name: "PANDASCORE", events: []competition.Event{event}}
	catalog := newFakeSyncCatalog()
	subs := &fakeSyncSubs{}
	chats := &fakeSyncChats{active: []chat.Settings{{ChatID: common.ChatID{Value: -1}, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}}}}
	outbox := &fakeSyncOutbox{}
	sync := newTestSync(t, provider, catalog, subs, chats, outbox)

	sync.DiscoverEvents(context.Background())

	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no announcement for a C-tier event, got %+v", outbox.enqueued)
	}
}

func TestDiscoverEvents_SkipsAnnouncementWhenChatsDoesNotSupportListActive(t *testing.T) {
	event := topTierEvent("IEM Katowice")
	provider := &fixedProvider{name: "PANDASCORE", events: []competition.Event{event}}
	catalog := newFakeSyncCatalog()
	subs := &fakeSyncSubs{}
	outbox := &fakeSyncOutbox{}
	sync := newTestSync(t, provider, catalog, subs, &fakeSyncChats{}, outbox)
	// ActiveChats is now a declared dependency rather than a capability
	// type-asserted off Chats: leaving it unwired must still degrade to a
	// silent no-op rather than panicking on the nil interface.
	sync.ActiveChats = nil

	sync.DiscoverEvents(context.Background())

	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no announcement when ActiveChats is unwired, got %+v", outbox.enqueued)
	}
}

func (f *fakeSyncChats) ListModerators(context.Context, common.ChatID) ([]chat.ModeratorInfo, error) {
	return nil, nil
}
func (f *fakeSyncChats) RecordManaged(context.Context, common.ChatID, common.UserID) error {
	return nil
}
func (f *fakeSyncChats) ManagedChats(context.Context, common.UserID) ([]chat.Settings, error) {
	return nil, nil
}
func (f *fakeSyncChats) SetDMSession(context.Context, common.UserID, common.ChatID) error { return nil }
func (f *fakeSyncChats) DMSession(context.Context, common.UserID) (*common.ChatID, error) {
	return nil, nil
}
func (f *fakeSyncChats) ClearDMSession(context.Context, common.UserID) error { return nil }
func (f *fakeSyncChats) UserLocale(context.Context, common.UserID) (*common.LocaleCode, error) {
	return nil, nil
}
func (f *fakeSyncChats) SetUserLocale(context.Context, common.UserID, common.LocaleCode) error {
	return nil
}

func (f *fakeSyncChats) SetDMReachable(context.Context, common.UserID, bool) error { return nil }
func (f *fakeSyncChats) FilterDMReachable(context.Context, []common.UserID) ([]common.UserID, error) {
	return nil, nil
}
func (f *fakeSyncChats) Nickname(context.Context, common.UserID) (*string, error) { return nil, nil }
func (f *fakeSyncChats) SetNickname(context.Context, common.UserID, string) error { return nil }

// TestReconcileTeamOrder is a regression test for a real production
// incident: a provider's opponents order for a match isn't guaranteed
// stable across two fetches. A poll's options are built once, from
// whichever order the match had the first time its participants were
// resolved; if a later fetch (e.g. the one reporting the match finished)
// swaps the two teams, the final score must be swapped back before it's
// ever saved or settled — otherwise it gets compared against poll options
// built under the original labeling, silently crediting the wrong side.
func TestReconcileTeamOrder(t *testing.T) {
	teamA := &competition.Team{ID: common.TeamID{Value: uuid.New()}, Name: "MIBR"}
	teamB := &competition.Team{ID: common.TeamID{Value: uuid.New()}, Name: "Legacy"}
	teamC := &competition.Team{ID: common.TeamID{Value: uuid.New()}, Name: "Alliance"}

	t.Run("swapped order relative to previous is corrected, score included", func(t *testing.T) {
		previous := &competition.Match{FirstTeam: teamA, SecondTeam: teamB}
		incoming := competition.Match{FirstTeam: teamB, SecondTeam: teamA, Score: &competition.MatchScore{First: 2, Second: 1}}

		got := reconcileTeamOrder(previous, incoming)

		if got.FirstTeam != teamA || got.SecondTeam != teamB {
			t.Fatalf("teams = (%v, %v), want (%v, %v)", got.FirstTeam, got.SecondTeam, teamA, teamB)
		}
		if got.Score == nil || got.Score.First != 1 || got.Score.Second != 2 {
			t.Errorf("score = %+v, want 1:2 (swapped back with the teams)", got.Score)
		}
	})

	t.Run("matching order relative to previous is left untouched", func(t *testing.T) {
		previous := &competition.Match{FirstTeam: teamA, SecondTeam: teamB}
		incoming := competition.Match{FirstTeam: teamA, SecondTeam: teamB, Score: &competition.MatchScore{First: 2, Second: 1}}

		got := reconcileTeamOrder(previous, incoming)

		if got.FirstTeam != teamA || got.SecondTeam != teamB || got.Score.First != 2 || got.Score.Second != 1 {
			t.Errorf("match unexpectedly changed: %+v", got)
		}
	})

	t.Run("no previous match is left untouched", func(t *testing.T) {
		incoming := competition.Match{FirstTeam: teamA, SecondTeam: teamB, Score: &competition.MatchScore{First: 2, Second: 1}}

		got := reconcileTeamOrder(nil, incoming)

		if got.FirstTeam != teamA || got.SecondTeam != teamB {
			t.Errorf("match unexpectedly changed: %+v", got)
		}
	})

	t.Run("different teams entirely (not a swap) are left untouched", func(t *testing.T) {
		previous := &competition.Match{FirstTeam: teamA, SecondTeam: teamB}
		incoming := competition.Match{FirstTeam: teamA, SecondTeam: teamC, Score: &competition.MatchScore{First: 2, Second: 1}}

		got := reconcileTeamOrder(previous, incoming)

		if got.FirstTeam != teamA || got.SecondTeam != teamC || got.Score.First != 2 || got.Score.Second != 1 {
			t.Errorf("match unexpectedly changed: %+v", got)
		}
	})

	t.Run("participants not yet known on either side are left untouched", func(t *testing.T) {
		previous := &competition.Match{FirstTeam: nil, SecondTeam: nil}
		incoming := competition.Match{FirstTeam: teamA, SecondTeam: teamB, Score: nil}

		got := reconcileTeamOrder(previous, incoming)

		if got.FirstTeam != teamA || got.SecondTeam != teamB {
			t.Errorf("match unexpectedly changed: %+v", got)
		}
	})
}

// A chat that hasn't enabled the event's game must not be offered it or
// auto-subscribed to it at all — the whole point of per-chat game
// preferences (see chat.Settings.GameEnabled).
func TestDiscoverEvents_SkipsChatsThatHaveNotEnabledTheEventsGame(t *testing.T) {
	event := topTierEvent("IEM Katowice")
	provider := &fixedProvider{name: "PANDASCORE", events: []competition.Event{event}}
	catalog := newFakeSyncCatalog()
	subs := &fakeSyncSubs{}
	noGames, wrongGame, rightGame := common.ChatID{Value: -1}, common.ChatID{Value: -2}, common.ChatID{Value: -3}
	chats := &fakeSyncChats{active: []chat.Settings{
		{ChatID: noGames, Active: true},
		{ChatID: wrongGame, Active: true, EnabledGames: []competition.GameCode{competition.GameDota2}},
		{ChatID: rightGame, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}},
	}}
	outbox := &fakeSyncOutbox{}
	sync := newTestSync(t, provider, catalog, subs, chats, outbox)

	sync.DiscoverEvents(context.Background())

	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected exactly 1 announcement (only the chat with CS2 enabled), got %d: %+v", len(outbox.enqueued), outbox.enqueued)
	}
	if outbox.enqueued[0].aggregateID != "-3:big-event:"+event.ID.Value.String() {
		t.Fatalf("expected the CS2-enabled chat's notification, got %+v", outbox.enqueued[0])
	}
}
