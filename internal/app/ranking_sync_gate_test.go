package app

import (
	"context"
	"testing"
	"time"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// mondayEndOfDay/mondayMorning/wednesdayMidday are fixed reference instants
// used across these tests — 2026-09-14 is a real Monday.
var (
	mondayEndOfDay  = time.Date(2026, 9, 14, 23, 30, 0, 0, time.UTC)
	mondayMorning   = time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	wednesdayMidday = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
)

func newGate(now time.Time, runs enrichment.ProviderRunRepository) *ApifyRankingGate {
	return &ApifyRankingGate{Runs: runs, Key: "hltv", Clock: &mutableClock{t: now}}
}

func TestApifyRankingGate_FiresOnMondayEndOfDayWhenNotYetRunThisWeek(t *testing.T) {
	ok, err := newGate(mondayEndOfDay, newFakeRunStore()).ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the gate to fire on Monday at end of day")
	}
}

func TestApifyRankingGate_SkipsOnMondayMorning(t *testing.T) {
	ok, err := newGate(mondayMorning, newFakeRunStore()).ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the gate to stay quiet on Monday morning, before the end-of-day cutoff")
	}
}

// Mid-week the gate stays open as long as the week has produced no
// ranking: Monday's attempt failing used to cost the whole week, since the
// only tick that ever saw an open gate was the one at the cutoff.
func TestApifyRankingGate_StaysOpenMidWeekUntilTheWeekSucceeds(t *testing.T) {
	ok, err := newGate(wednesdayMidday, newFakeRunStore()).ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the gate to keep retrying mid-week while this week has no ranking yet")
	}
}

func TestApifyRankingGate_SkipsWhenThisWeeksRunIsAlreadyCollected(t *testing.T) {
	runs := newFakeRunStore()
	if err := runs.SaveRun(context.Background(), enrichment.ProviderRun{
		Provider: enrichment.SourceHLTV, Key: "hltv", RunID: "r1",
		Status: enrichment.RunStatusCollected, PeriodStart: common.StartOfWeekUTC(mondayEndOfDay),
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := newGate(wednesdayMidday, runs).ShouldRun(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected the gate to skip a ranking already collected this week")
	}
}

// The paid Valve feed shares its Source with the free, frequent valvevrs
// sync. Keying "already ran this week" on that shared Source's SyncState
// kept this gate shut every week the paid feed existed — it must key on its
// own run instead.
func TestApifyRankingGate_IsNotShutByAnotherJobSharingTheSource(t *testing.T) {
	runs := newFakeRunStore()
	// The free sync's own bookkeeping is irrelevant here; what matters is
	// that this ranking mode has no collected run of its own.
	if err := runs.SaveRun(context.Background(), enrichment.ProviderRun{
		Provider: enrichment.SourceValveVRS, Key: "hltv", RunID: "r1",
		Status: enrichment.RunStatusCollected, PeriodStart: common.StartOfWeekUTC(mondayEndOfDay),
	}); err != nil {
		t.Fatal(err)
	}
	gate := &ApifyRankingGate{Runs: runs, Key: "valve", Clock: &mutableClock{t: wednesdayMidday}}
	ok, err := gate.ShouldRun(context.Background(), enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the valve ranking has never run; its gate must be open")
	}
}

func TestApifyRankingGate_PropagatesRunLookupError(t *testing.T) {
	gate := &ApifyRankingGate{Runs: &erroringRunStore{err: context.DeadlineExceeded}, Key: "hltv", Clock: &mutableClock{t: mondayEndOfDay}}
	if _, err := gate.ShouldRun(context.Background(), enrichment.SourceHLTV); err == nil {
		t.Fatal("expected the run lookup error to propagate")
	}
}

// fakeRunStore is an in-memory enrichment.ProviderRunRepository.
type fakeRunStore struct {
	runs map[string]enrichment.ProviderRun
}

func newFakeRunStore() *fakeRunStore { return &fakeRunStore{runs: map[string]enrichment.ProviderRun{}} }

func (f *fakeRunStore) SaveRun(_ context.Context, run enrichment.ProviderRun) error {
	f.runs[string(run.Provider)+"/"+run.Key] = run
	return nil
}

func (f *fakeRunStore) Run(_ context.Context, provider enrichment.Source, key string) (*enrichment.ProviderRun, error) {
	run, ok := f.runs[string(provider)+"/"+key]
	if !ok {
		return nil, nil
	}
	return &run, nil
}

func (f *fakeRunStore) ClearRun(_ context.Context, provider enrichment.Source, key string) error {
	delete(f.runs, string(provider)+"/"+key)
	return nil
}

type erroringRunStore struct{ err error }

func (e *erroringRunStore) SaveRun(context.Context, enrichment.ProviderRun) error { return nil }
func (e *erroringRunStore) Run(context.Context, enrichment.Source, string) (*enrichment.ProviderRun, error) {
	return nil, e.err
}
func (e *erroringRunStore) ClearRun(context.Context, enrichment.Source, string) error { return nil }
