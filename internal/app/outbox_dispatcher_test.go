package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

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
}

func newFakeOutbox(messages ...common.OutboxMessage) *fakeOutbox {
	return &fakeOutbox{pending: messages, failed: map[uuid.UUID]string{}}
}
func (o *fakeOutbox) Enqueue(context.Context, string, string, string, string) (uuid.UUID, error) {
	return uuid.New(), nil
}
func (o *fakeOutbox) Pending(context.Context, int) ([]common.OutboxMessage, error) {
	return o.pending, nil
}
func (o *fakeOutbox) Published(_ context.Context, id uuid.UUID) error {
	o.published = append(o.published, id)
	return nil
}
func (o *fakeOutbox) Failed(_ context.Context, id uuid.UUID, errText string) error {
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
