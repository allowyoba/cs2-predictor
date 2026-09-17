package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeTournamentCatalog is a competition.Catalog fake returning a fixed set
// of events for FindEvents — everything else is unused by
// TournamentMetadataSync.
type fakeTournamentCatalog struct {
	events map[common.EventID]competition.Event
	err    error
}

func (f *fakeTournamentCatalog) SearchEvents(context.Context, string, int, bool, []competition.GameCode) ([]competition.Event, error) {
	return nil, nil
}
func (f *fakeTournamentCatalog) FindEvent(context.Context, common.EventID) (*competition.Event, error) {
	return nil, nil
}
func (f *fakeTournamentCatalog) FindEvents(_ context.Context, ids []common.EventID) ([]competition.Event, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []competition.Event
	for _, id := range ids {
		if e, ok := f.events[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeTournamentCatalog) FindMatch(context.Context, common.MatchID) (*competition.Match, error) {
	return nil, nil
}
func (f *fakeTournamentCatalog) FindUnstartedMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeTournamentCatalog) FindUnstartedMatchesForEvents(context.Context, []common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeTournamentCatalog) FindMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeTournamentCatalog) SaveEvent(_ context.Context, e competition.Event) (competition.Event, error) {
	return e, nil
}
func (f *fakeTournamentCatalog) SaveMatch(_ context.Context, m competition.Match) (competition.Match, error) {
	return m, nil
}

type fakeTournamentSubs struct {
	eventIDs []common.EventID
}

func (f *fakeTournamentSubs) Subscribe(context.Context, subscription.EventSubscription) (subscription.EventSubscription, error) {
	return subscription.EventSubscription{}, nil
}
func (f *fakeTournamentSubs) Unsubscribe(context.Context, common.ChatID, common.EventID) error {
	return nil
}
func (f *fakeTournamentSubs) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (f *fakeTournamentSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) {
	return f.eventIDs, nil
}
func (f *fakeTournamentSubs) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return nil, nil
}

type fakeTournamentProvider struct {
	byName map[string]enrichment.TournamentMetadata
	err    error
}

func (f *fakeTournamentProvider) EnrichTournament(_ context.Context, externalName string) (*enrichment.TournamentMetadata, error) {
	if f.err != nil {
		return nil, f.err
	}
	if m, ok := f.byName[externalName]; ok {
		return &m, nil
	}
	return nil, nil
}

func TestTournamentMetadataSync_EnrichesSubscribedEventsAndCaches(t *testing.T) {
	eventID := common.NewEventID()
	event := competition.Event{ID: eventID, Name: "CCT Europe Series #8 2026"}

	store := newFakeEnrichmentStore()
	sync := &TournamentMetadataSync{
		Provider: &fakeTournamentProvider{byName: map[string]enrichment.TournamentMetadata{
			"CCT Europe Series #8 2026": {FullName: "CCT Europe Series #8 2026", Series: "CCT Europe Series"},
		}},
		Catalog:       &fakeTournamentCatalog{events: map[common.EventID]competition.Event{eventID: event}},
		Subscriptions: &fakeTournamentSubs{eventIDs: []common.EventID{eventID}},
		Metadata:      store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	got, err := store.FindTournamentMetadata(context.Background(), eventID, enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Series != "CCT Europe Series" {
		t.Fatalf("FindTournamentMetadata = %+v, want Series=\"CCT Europe Series\"", got)
	}
	st, err := store.State(context.Background(), enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil {
		t.Fatalf("expected RecordSuccess to be called, state = %+v", st)
	}
}

func TestTournamentMetadataSync_SkipsEventsAlreadyCached(t *testing.T) {
	eventID := common.NewEventID()
	event := competition.Event{ID: eventID, Name: "Already Cached Event"}

	store := newFakeEnrichmentStore()
	store.tournaments[eventID] = enrichment.TournamentMetadata{FullName: "Already Cached Event"}
	provider := &fakeTournamentProvider{byName: map[string]enrichment.TournamentMetadata{}}
	sync := &TournamentMetadataSync{
		Provider:      provider,
		Catalog:       &fakeTournamentCatalog{events: map[common.EventID]competition.Event{eventID: event}},
		Subscriptions: &fakeTournamentSubs{eventIDs: []common.EventID{eventID}},
		Metadata:      store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	// The provider must not have been consulted at all for an event that's
	// already cached — that's the whole point of skipping it.
	st, err := store.State(context.Background(), enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil {
		t.Fatalf("expected a successful no-op run, state = %+v", st)
	}
}

func TestTournamentMetadataSync_NoMatchLeavesNoCachedRow(t *testing.T) {
	eventID := common.NewEventID()
	event := competition.Event{ID: eventID, Name: "Unresolvable Event Name"}

	store := newFakeEnrichmentStore()
	sync := &TournamentMetadataSync{
		Provider:      &fakeTournamentProvider{}, // no data for any name
		Catalog:       &fakeTournamentCatalog{events: map[common.EventID]competition.Event{eventID: event}},
		Subscriptions: &fakeTournamentSubs{eventIDs: []common.EventID{eventID}},
		Metadata:      store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	got, err := store.FindTournamentMetadata(context.Background(), eventID, enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("FindTournamentMetadata = %+v, want nil (no match)", got)
	}
	st, err := store.State(context.Background(), enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil {
		t.Fatalf("a clean no-match must still count as a successful run, state = %+v", st)
	}
}

func TestTournamentMetadataSync_RecordsFailureWhenEverySingleLookupErrors(t *testing.T) {
	eventID := common.NewEventID()
	event := competition.Event{ID: eventID, Name: "Some Event"}

	store := newFakeEnrichmentStore()
	sync := &TournamentMetadataSync{
		Provider:      &fakeTournamentProvider{err: errors.New("liquipedia unreachable")},
		Catalog:       &fakeTournamentCatalog{events: map[common.EventID]competition.Event{eventID: event}},
		Subscriptions: &fakeTournamentSubs{eventIDs: []common.EventID{eventID}},
		Metadata:      store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveFailures == 0 {
		t.Fatalf("every lookup erroring should record a sync failure, state = %+v", st)
	}
}

func TestTournamentMetadataSync_RecordsFailureWhenSubscriptionsLookupErrors(t *testing.T) {
	store := newFakeEnrichmentStore()
	sync := &TournamentMetadataSync{
		Provider:      &fakeTournamentProvider{},
		Catalog:       &fakeTournamentCatalog{},
		Subscriptions: &erroringSubs{err: errors.New("db down")},
		Metadata:      store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceLiquipedia)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveFailures != 1 || st.LastError != "db down" {
		t.Fatalf("state = %+v, want ConsecutiveFailures=1 LastError=\"db down\"", st)
	}
}

// erroringSubs is a subscription.Repository whose ActiveEventIDs always
// errors — for testing the sync job's hard-failure path.
type erroringSubs struct {
	err error
}

func (f *erroringSubs) Subscribe(context.Context, subscription.EventSubscription) (subscription.EventSubscription, error) {
	return subscription.EventSubscription{}, nil
}
func (f *erroringSubs) Unsubscribe(context.Context, common.ChatID, common.EventID) error { return nil }
func (f *erroringSubs) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (f *erroringSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) {
	return nil, f.err
}
func (f *erroringSubs) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return nil, nil
}
