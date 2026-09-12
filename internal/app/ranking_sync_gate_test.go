package app

import (
	"context"
	"testing"
	"time"

	"cs2predictor/internal/domain/enrichment"
)

// mondayEndOfDay/mondayMorning/wednesdayMidday are fixed reference instants
// used across these tests — 2026-09-14 is a real Monday.
var (
	mondayEndOfDay  = time.Date(2026, 9, 14, 23, 30, 0, 0, time.UTC)
	mondayMorning   = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	wednesdayMidday = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
)

func TestApifyRankingGate_FiresOnMondayEndOfDayWhenNotYetRunThisWeek(t *testing.T) {
	gate := &ApifyRankingGate{State: newFakeEnrichmentStore(), Clock: &mutableClock{t: mondayEndOfDay}}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the gate to fire on Monday at end of day")
	}
}

func TestApifyRankingGate_SkipsOnMondayMorning(t *testing.T) {
	gate := &ApifyRankingGate{State: newFakeEnrichmentStore(), Clock: &mutableClock{t: mondayMorning}}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the gate to stay quiet on Monday morning, before the end-of-day cutoff")
	}
}

func TestApifyRankingGate_SkipsMidWeekRegardlessOfAnythingElse(t *testing.T) {
	gate := &ApifyRankingGate{State: newFakeEnrichmentStore(), Clock: &mutableClock{t: wednesdayMidday}}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the gate to stay quiet mid-week — the schedule is Monday-only now, tournament activity no longer matters")
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
	gate := &ApifyRankingGate{State: store, Clock: realClock{}}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the gate to skip a source already synced this week")
	}
}

func TestApifyRankingGate_PropagatesStateLookupError(t *testing.T) {
	gate := &ApifyRankingGate{State: &erroringSyncState{err: context.DeadlineExceeded}, Clock: &mutableClock{t: mondayEndOfDay}}
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
