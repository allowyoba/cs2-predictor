package app

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/platform/common"
)

// fakeDeadLetters is a DeadLetterStore whose contents a test can change
// between checks.
type fakeDeadLetters struct {
	groups   []common.DeadLetterGroup
	replayed int
}

func (f *fakeDeadLetters) DeadLetters(context.Context) ([]common.DeadLetterGroup, error) {
	return f.groups, nil
}
func (f *fakeDeadLetters) ReplayDeadLetters(context.Context) (int, error) {
	f.replayed++
	f.groups = nil
	return 1, nil
}

func newDeadLetterWatch(store *fakeDeadLetters) (*DeadLetterWatch, *fakeSyncOutbox) {
	outbox := &fakeSyncOutbox{}
	return &DeadLetterWatch{
		Store:   store,
		Alerter: &AdminAlerter{Outbox: outbox, ChatIDs: []int64{1}, Log: slog.Default()},
		Metrics: newTestMetrics(), Log: slog.Default(),
	}, outbox
}

func deadLetterGroup(eventType string, count int) common.DeadLetterGroup {
	return common.DeadLetterGroup{
		EventType: eventType, Count: count,
		Oldest: time.Now().Add(-time.Hour), Newest: time.Now(), LastError: "chat not found",
	}
}

// The failure this exists for: messages nobody will ever receive, reported
// nowhere. Fifteen of them accumulated in production unnoticed.
func TestDeadLetterWatch_AlertsOnceAndAgainOnlyWhenThePileGrows(t *testing.T) {
	store := &fakeDeadLetters{groups: []common.DeadLetterGroup{deadLetterGroup("telegram.result-recap", 3)}}
	watch, outbox := newDeadLetterWatch(store)

	watch.Check(context.Background())
	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected an alert for undelivered messages, got %v", outbox.enqueued)
	}
	if !strings.Contains(outbox.enqueued[0].aggregateID, "3") {
		t.Fatalf("the alert must carry the count, got %q", outbox.enqueued[0].aggregateID)
	}

	// Same pile on the next tick: repeating it every few minutes is how an
	// alert stops being read.
	watch.Check(context.Background())
	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected no repeat for an unchanged pile, got %v", outbox.enqueued)
	}

	// More messages have died since: that is news again.
	store.groups = []common.DeadLetterGroup{deadLetterGroup("telegram.result-recap", 9)}
	watch.Check(context.Background())
	if len(outbox.enqueued) != 2 {
		t.Fatalf("expected a fresh alert once the pile grew, got %v", outbox.enqueued)
	}
}

// After a replay clears them, the next pile has to alert again rather than
// being suppressed by the count from before.
func TestDeadLetterWatch_ForgetsWhatWasCleared(t *testing.T) {
	store := &fakeDeadLetters{groups: []common.DeadLetterGroup{deadLetterGroup("telegram.poll-reminder", 5)}}
	watch, outbox := newDeadLetterWatch(store)

	watch.Check(context.Background())
	store.groups = nil
	watch.Check(context.Background())
	store.groups = []common.DeadLetterGroup{deadLetterGroup("telegram.poll-reminder", 2)}
	watch.Check(context.Background())

	if len(outbox.enqueued) != 2 {
		t.Fatalf("expected the smaller, later pile to alert on its own, got %v", outbox.enqueued)
	}
}

// Nothing undelivered is the normal state, and it must be silent.
func TestDeadLetterWatch_SaysNothingWhenEverythingWasDelivered(t *testing.T) {
	watch, outbox := newDeadLetterWatch(&fakeDeadLetters{})

	watch.Check(context.Background())

	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected silence, got %v", outbox.enqueued)
	}
}
