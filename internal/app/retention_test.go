package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

type fakeRetentionStore struct {
	cutoffs map[string]time.Time
	maxRows map[string]int
	deleted map[string]int64
	errs    map[string]error
}

func newFakeRetentionStore() *fakeRetentionStore {
	return &fakeRetentionStore{
		cutoffs: map[string]time.Time{},
		maxRows: map[string]int{},
		deleted: map[string]int64{},
		errs:    map[string]error{},
	}
}

func (f *fakeRetentionStore) record(name string, cutoff time.Time) (int64, error) {
	f.cutoffs[name] = cutoff
	if err := f.errs[name]; err != nil {
		return 0, err
	}
	return f.deleted[name], nil
}

func (f *fakeRetentionStore) recordCap(name string, maxRows int) (int64, error) {
	f.maxRows[name] = maxRows
	if err := f.errs[name]; err != nil {
		return 0, err
	}
	return f.deleted[name], nil
}

func (f *fakeRetentionStore) DeleteProcessedUpdatesBefore(_ context.Context, cutoff time.Time) (int64, error) {
	return f.record("updates", cutoff)
}
func (f *fakeRetentionStore) DeletePublishedOutboxBefore(_ context.Context, cutoff time.Time) (int64, error) {
	return f.record("outbox", cutoff)
}
func (f *fakeRetentionStore) DeleteResolvedUnsubscribesBefore(_ context.Context, cutoff time.Time) (int64, error) {
	return f.record("pending", cutoff)
}
func (f *fakeRetentionStore) DeleteAdminActionsBefore(_ context.Context, cutoff time.Time) (int64, error) {
	return f.record("admin", cutoff)
}
func (f *fakeRetentionStore) DeleteProcessedUpdatesExceeding(_ context.Context, maxRows int) (int64, error) {
	return f.recordCap("updates-cap", maxRows)
}
func (f *fakeRetentionStore) DeletePublishedOutboxExceeding(_ context.Context, maxRows int) (int64, error) {
	return f.recordCap("outbox-cap", maxRows)
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func newTestSweep(store *fakeRetentionStore, now time.Time) *RetentionSweep {
	return &RetentionSweep{
		Store: store, Lock: fakeClusterLock{}, Clock: fixedClock{now: now}, Log: slog.Default(),
		ProcessedUpdatesTTL:     48 * time.Hour,
		PublishedOutboxTTL:      14 * 24 * time.Hour,
		ResolvedRequestsTTL:     30 * 24 * time.Hour,
		AdminActionsTTL:         90 * 24 * time.Hour,
		ProcessedUpdatesMaxRows: 500_000,
		PublishedOutboxMaxRows:  200_000,
	}
}

func TestRetentionSweep_DeletesEachTableAtItsOwnCutoff(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newFakeRetentionStore()
	store.deleted["updates"] = 1200

	newTestSweep(store, now).Dispatch(context.Background())

	for name, ttl := range map[string]time.Duration{
		"updates": 48 * time.Hour,
		"outbox":  14 * 24 * time.Hour,
		"pending": 30 * 24 * time.Hour,
		"admin":   90 * 24 * time.Hour,
	} {
		got, ok := store.cutoffs[name]
		if !ok {
			t.Fatalf("%s was never swept", name)
		}
		if want := now.Add(-ttl); !got.Equal(want) {
			t.Fatalf("%s cutoff = %v, want %v (now minus its own TTL)", name, got, want)
		}
	}
}

// One table failing must not stop the others from reclaiming their space —
// the whole point of sweeping them independently.
func TestRetentionSweep_ContinuesAfterOneTableFails(t *testing.T) {
	now := time.Now()
	store := newFakeRetentionStore()
	store.errs["updates"] = errors.New("lock timeout")

	newTestSweep(store, now).Dispatch(context.Background())

	if _, swept := store.cutoffs["outbox"]; !swept {
		t.Fatal("outbox must still be swept after the updates delete failed")
	}
	if _, swept := store.cutoffs["pending"]; !swept {
		t.Fatal("pending_unsubscribe must still be swept after the updates delete failed")
	}
}

// A zero TTL is the operator's "keep everything" escape hatch, not a
// cutoff of "now" that would delete the entire table.
func TestRetentionSweep_ZeroTTLDisablesThatTable(t *testing.T) {
	store := newFakeRetentionStore()
	sweep := newTestSweep(store, time.Now())
	sweep.ProcessedUpdatesTTL = 0

	sweep.Dispatch(context.Background())

	if _, swept := store.cutoffs["updates"]; swept {
		t.Fatal("a zero TTL must disable the sweep for that table, not delete everything")
	}
	if _, swept := store.cutoffs["outbox"]; !swept {
		t.Fatal("the other tables must still be swept")
	}
}

// TestRetentionSweep_AppliesTheRowCountCapAlongsideTheTTL covers the
// backstop this session's size-stabilization pass added: the row-count cap
// runs independently of (in addition to) the TTL-based delete, for the two
// tables that have one.
func TestRetentionSweep_AppliesTheRowCountCapAlongsideTheTTL(t *testing.T) {
	store := newFakeRetentionStore()
	newTestSweep(store, time.Now()).Dispatch(context.Background())

	if got, want := store.maxRows["updates-cap"], 500_000; got != want {
		t.Fatalf("processed-updates row cap = %d, want %d", got, want)
	}
	if got, want := store.maxRows["outbox-cap"], 200_000; got != want {
		t.Fatalf("outbox row cap = %d, want %d", got, want)
	}
}

// A zero/negative MaxRows is the same "keep everything" escape hatch a zero
// TTL is — it must not be treated as "cap at zero rows".
func TestRetentionSweep_ZeroMaxRowsDisablesTheCap(t *testing.T) {
	store := newFakeRetentionStore()
	sweep := newTestSweep(store, time.Now())
	sweep.ProcessedUpdatesMaxRows = 0

	sweep.Dispatch(context.Background())

	if _, capped := store.maxRows["updates-cap"]; capped {
		t.Fatal("a zero MaxRows must disable the cap for that table, not cap it at zero rows")
	}
	if _, capped := store.maxRows["outbox-cap"]; !capped {
		t.Fatal("the other table's cap must still run")
	}
}

// One cap target failing must not stop the other from reclaiming its
// space — the same independence the TTL targets already have.
func TestRetentionSweep_RowCountCapContinuesAfterOneTargetFails(t *testing.T) {
	store := newFakeRetentionStore()
	store.errs["updates-cap"] = errors.New("lock timeout")

	newTestSweep(store, time.Now()).Dispatch(context.Background())

	if _, capped := store.maxRows["outbox-cap"]; !capped {
		t.Fatal("outbox row cap must still run after the updates cap failed")
	}
}
