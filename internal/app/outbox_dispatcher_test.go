package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"cs2predictor/internal/platform/common"
)

type fakeClusterLock struct{}

func (fakeClusterLock) Execute(ctx context.Context, name string, action func(ctx context.Context) error) (bool, error) {
	return true, action(ctx)
}

type fakeOutbox struct {
	pending   []common.OutboxMessage
	published []uuid.UUID
	failed    map[uuid.UUID]string
	// deferred records what was held and until when — the distinction
	// between "not now" and "failed" is the whole point of Defer.
	deferred map[uuid.UUID]time.Time
}

func newFakeOutbox(messages ...common.OutboxMessage) *fakeOutbox {
	return &fakeOutbox{pending: messages, failed: map[uuid.UUID]string{}}
}
func (o *fakeOutbox) Enqueue(context.Context, string, string, string, string) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (o *fakeOutbox) Defer(_ context.Context, id uuid.UUID, _ time.Time, until time.Time) error {
	if o.deferred == nil {
		o.deferred = map[uuid.UUID]time.Time{}
	}
	o.deferred[id] = until
	return nil
}
func (o *fakeOutbox) Pending(context.Context, int) ([]common.OutboxMessage, error) {
	return o.pending, nil
}
func (o *fakeOutbox) Published(_ context.Context, id uuid.UUID, _ time.Time) error {
	o.published = append(o.published, id)
	return nil
}
func (o *fakeOutbox) Failed(_ context.Context, id uuid.UUID, _ time.Time, errText string) error {
	o.failed[id] = errText
	return nil
}

type fakePublisher struct {
	eventType string
	err       error
	published int
}

func (p *fakePublisher) Supports(eventType string) bool { return eventType == p.eventType }
func (p *fakePublisher) Publish(context.Context, common.OutboxMessage) error {
	p.published++
	return p.err
}

func newTestMetrics() *Metrics { return NewMetrics(prometheus.NewRegistry()) }

func TestOutboxDispatcher_PublishesSupportedMessage(t *testing.T) {
	msg := common.OutboxMessage{ID: uuid.New(), Type: "telegram.match-result", Payload: "{}", OccurredAt: time.Now()}
	outbox := newFakeOutbox(msg)
	publisher := &fakePublisher{eventType: "telegram.match-result"}
	d := &OutboxDispatcher{Outbox: outbox, Publishers: []common.OutboxPublisher{publisher}, Lock: fakeClusterLock{}, BatchSize: 10, Metrics: newTestMetrics(), Log: slog.Default()}

	d.Dispatch(context.Background())

	if publisher.published != 1 {
		t.Fatalf("publisher.published = %d, want 1", publisher.published)
	}
	if len(outbox.published) != 1 || outbox.published[0] != msg.ID {
		t.Fatalf("expected message %v marked published, got %v", msg.ID, outbox.published)
	}
	if len(outbox.failed) != 0 {
		t.Fatalf("expected no failures, got %v", outbox.failed)
	}
}

func TestOutboxDispatcher_MarksFailedWhenNoPublisherSupportsType(t *testing.T) {
	msg := common.OutboxMessage{ID: uuid.New(), Type: "unknown.type", Payload: "{}", OccurredAt: time.Now()}
	outbox := newFakeOutbox(msg)
	publisher := &fakePublisher{eventType: "telegram.match-result"}
	d := &OutboxDispatcher{Outbox: outbox, Publishers: []common.OutboxPublisher{publisher}, Lock: fakeClusterLock{}, BatchSize: 10, Metrics: newTestMetrics(), Log: slog.Default()}

	d.Dispatch(context.Background())

	if publisher.published != 0 {
		t.Fatalf("expected the mismatched publisher not to be called, got %d calls", publisher.published)
	}
	if _, failed := outbox.failed[msg.ID]; !failed {
		t.Fatal("expected the message to be marked failed")
	}
}

func TestOutboxDispatcher_MarksFailedWhenPublisherErrors(t *testing.T) {
	msg := common.OutboxMessage{ID: uuid.New(), Type: "telegram.match-result", Payload: "{}", OccurredAt: time.Now()}
	outbox := newFakeOutbox(msg)
	publisher := &fakePublisher{eventType: "telegram.match-result", err: errors.New("telegram is down")}
	d := &OutboxDispatcher{Outbox: outbox, Publishers: []common.OutboxPublisher{publisher}, Lock: fakeClusterLock{}, BatchSize: 10, Metrics: newTestMetrics(), Log: slog.Default()}

	d.Dispatch(context.Background())

	if len(outbox.published) != 0 {
		t.Fatalf("expected no messages marked published, got %v", outbox.published)
	}
	if outbox.failed[msg.ID] != "telegram is down" {
		t.Fatalf("failed reason = %q, want %q", outbox.failed[msg.ID], "telegram is down")
	}
}

// A message that fails on what Pending's attempts<OutboxMaxAttempts cutoff
// makes its LAST allowed attempt goes permanently quiet afterward — Pending
// will never return it again. That transition must be its own observable
// signal (a dedicated metric label), not indistinguishable from every
// ordinary retryable failure, or an operator has no way to notice a message
// silently gave up forever.
func TestOutboxDispatcher_RecordsExhaustedMetricOnTheFinalAttempt(t *testing.T) {
	msg := common.OutboxMessage{
		ID: uuid.New(), Type: "telegram.match-result", Payload: "{}", OccurredAt: time.Now(),
		Attempts: common.OutboxMaxAttempts - 1, // this failure will push it to the cap
	}
	outbox := newFakeOutbox(msg)
	publisher := &fakePublisher{eventType: "telegram.match-result", err: errors.New("telegram is down")}
	metrics := newTestMetrics()
	d := &OutboxDispatcher{Outbox: outbox, Publishers: []common.OutboxPublisher{publisher}, Lock: fakeClusterLock{}, BatchSize: 10, Metrics: metrics, Log: slog.Default()}

	d.Dispatch(context.Background())

	if got := testutil.ToFloat64(metrics.OutboxEvents.WithLabelValues("exhausted", msg.Type)); got != 1 {
		t.Fatalf("exhausted metric = %v, want 1", got)
	}
}

// A message with attempts still comfortably under the cap must NOT trip the
// exhausted signal — only the attempt that actually crosses the cap should.
func TestOutboxDispatcher_DoesNotRecordExhaustedBeforeTheFinalAttempt(t *testing.T) {
	msg := common.OutboxMessage{
		ID: uuid.New(), Type: "telegram.match-result", Payload: "{}", OccurredAt: time.Now(),
		Attempts: 1,
	}
	outbox := newFakeOutbox(msg)
	publisher := &fakePublisher{eventType: "telegram.match-result", err: errors.New("telegram is down")}
	metrics := newTestMetrics()
	d := &OutboxDispatcher{Outbox: outbox, Publishers: []common.OutboxPublisher{publisher}, Lock: fakeClusterLock{}, BatchSize: 10, Metrics: metrics, Log: slog.Default()}

	d.Dispatch(context.Background())

	if got := testutil.ToFloat64(metrics.OutboxEvents.WithLabelValues("exhausted", msg.Type)); got != 0 {
		t.Fatalf("exhausted metric = %v, want 0 (not yet at the retry cap)", got)
	}
}

// "Not now" and "failed" must stay different things. A chat's quiet hours
// can easily be nine hours long; counting each check as a failed attempt
// would exhaust the retry budget overnight and lose the message for good.
func TestDispatch_HeldMessageIsRescheduledWithoutSpendingAnAttempt(t *testing.T) {
	message := common.OutboxMessage{ID: uuid.New(), Type: "telegram.big-event-discovered", OccurredAt: time.Now()}
	outbox := newFakeOutbox(message)
	until := time.Now().Add(6 * time.Hour)
	dispatcher := &OutboxDispatcher{
		Outbox: outbox,
		Publishers: []common.OutboxPublisher{&fakePublisher{
			eventType: "telegram.big-event-discovered",
			err:       common.Deferred(until, "quiet hours"),
		}},
		Lock: fakeClusterLock{}, BatchSize: 10, Metrics: newTestMetrics(), Log: slog.Default(),
	}

	dispatcher.Dispatch(context.Background())

	if len(outbox.failed) != 0 {
		t.Fatalf("a held message must not be marked failed, got %v", outbox.failed)
	}
	if len(outbox.published) != 0 {
		t.Fatal("a held message was not published")
	}
	got, ok := outbox.deferred[message.ID]
	if !ok {
		t.Fatalf("expected the message to be deferred, got %v", outbox.deferred)
	}
	if !got.Equal(until) {
		t.Fatalf("deferred until %s, want %s", got, until)
	}
}
