package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// fakeGateCatalog is a minimal competition.Catalog for ApifyRankingGate
// tests — only SearchEvents is exercised.
type fakeGateCatalog struct {
	events []competition.Event
	err    error
}

func (f *fakeGateCatalog) SearchEvents(context.Context, string, int, bool) ([]competition.Event, error) {
	return f.events, f.err
}
func (f *fakeGateCatalog) FindEvent(context.Context, common.EventID) (*competition.Event, error) {
	return nil, nil
}
func (f *fakeGateCatalog) FindEvents(context.Context, []common.EventID) ([]competition.Event, error) {
	return nil, nil
}
func (f *fakeGateCatalog) FindMatch(context.Context, common.MatchID) (*competition.Match, error) {
	return nil, nil
}
func (f *fakeGateCatalog) FindUnstartedMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeGateCatalog) FindUnstartedMatchesForEvents(context.Context, []common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeGateCatalog) FindMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeGateCatalog) SaveEvent(_ context.Context, e competition.Event) (competition.Event, error) {
	return e, nil
}
func (f *fakeGateCatalog) SaveMatch(_ context.Context, m competition.Match) (competition.Match, error) {
	return m, nil
}

// mondayEndOfDay/wednesdayMidday are fixed reference instants used across
// these tests — 2026-09-14 is a real Monday.
var (
	mondayEndOfDay  = time.Date(2026, 9, 14, 23, 30, 0, 0, time.UTC)
	mondayMorning   = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	wednesdayMidday = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
)

func TestApifyRankingGate_FiresOnMondayEndOfDayWhenNotYetRunThisWeek(t *testing.T) {
	gate := &ApifyRankingGate{
		State: newFakeEnrichmentStore(), Catalog: &fakeGateCatalog{}, Clock: &mutableClock{t: mondayEndOfDay}, Log: slog.Default(),
	}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the gate to fire on Monday at end of day")
	}
}

func TestApifyRankingGate_SkipsOnMondayMorningWithNoTournamentActivity(t *testing.T) {
	gate := &ApifyRankingGate{
		State: newFakeEnrichmentStore(), Catalog: &fakeGateCatalog{}, Clock: &mutableClock{t: mondayMorning}, Log: slog.Default(),
	}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the gate to stay quiet on Monday morning, before the end-of-day cutoff")
	}
}

func TestApifyRankingGate_SkipsWhenAlreadyRunThisWeek(t *testing.T) {
	store := newFakeEnrichmentStore()
	if err := store.RecordSuccess(context.Background(), enrichment.SourceHLTV); err != nil {
		t.Fatal(err)
	}
	// RecordSuccess timestamps with time.Now(), which is (real) "now" —
	// always within the current real-world week, so any Clock.Now() this
	// test picks from the same real week must see it as already-run.
	gate := &ApifyRankingGate{State: store, Catalog: &fakeGateCatalog{}, Clock: realClock{}, Log: slog.Default()}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the gate to skip a source already synced this week")
	}
}

func TestApifyRankingGate_FiresMidWeekWhenATopTierTournamentIsRunning(t *testing.T) {
	catalog := &fakeGateCatalog{events: []competition.Event{
		{ID: common.NewEventID(), Status: competition.EventRunning, Tier: competition.TierS},
	}}
	gate := &ApifyRankingGate{State: newFakeEnrichmentStore(), Catalog: catalog, Clock: &mutableClock{t: wednesdayMidday}, Log: slog.Default()}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the gate to fire mid-week for a running top-tier tournament")
	}
}

func TestApifyRankingGate_FiresMidWeekWhenATopTierTournamentStartsSoon(t *testing.T) {
	startsIn3Days := wednesdayMidday.Add(3 * 24 * time.Hour)
	catalog := &fakeGateCatalog{events: []competition.Event{
		{ID: common.NewEventID(), Status: competition.EventUpcoming, Tier: competition.TierA, StartsAt: &startsIn3Days},
	}}
	gate := &ApifyRankingGate{State: newFakeEnrichmentStore(), Catalog: catalog, Clock: &mutableClock{t: wednesdayMidday}, Log: slog.Default()}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the gate to fire ahead of schedule for a top-tier tournament starting within the lookahead window")
	}
}

func TestApifyRankingGate_DoesNotFireForATournamentFarInTheFuture(t *testing.T) {
	startsIn30Days := wednesdayMidday.Add(30 * 24 * time.Hour)
	catalog := &fakeGateCatalog{events: []competition.Event{
		{ID: common.NewEventID(), Status: competition.EventUpcoming, Tier: competition.TierS, StartsAt: &startsIn30Days},
	}}
	gate := &ApifyRankingGate{State: newFakeEnrichmentStore(), Catalog: catalog, Clock: &mutableClock{t: wednesdayMidday}, Log: slog.Default()}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the gate to stay quiet for a tournament far outside the lookahead window")
	}
}

func TestApifyRankingGate_PropagatesStateLookupError(t *testing.T) {
	gate := &ApifyRankingGate{
		State: &erroringSyncState{err: context.DeadlineExceeded}, Catalog: &fakeGateCatalog{}, Clock: &mutableClock{t: mondayEndOfDay}, Log: slog.Default(),
	}
	if _, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV); err == nil {
		t.Fatal("expected the state lookup error to propagate")
	}
}

type erroringSyncState struct{ err error }

func (e *erroringSyncState) RecordSuccess(context.Context, enrichment.Source) error { return nil }
func (e *erroringSyncState) RecordFailure(context.Context, enrichment.Source, string) error {
	return nil
}
func (e *erroringSyncState) State(context.Context, enrichment.Source) (*enrichment.SyncState, error) {
	return nil, e.err
}

// realClock is a common.Clock backed by the real wall clock — used only by
// TestApifyRankingGate_SkipsWhenAlreadyRunThisWeek, which needs "now" to
// genuinely be in the same week RecordSuccess just stamped.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
