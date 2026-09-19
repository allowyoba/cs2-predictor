package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"cs2predictor/internal/platform/common"
)

// fakeInspector replays a scripted sequence of getWebhookInfo answers, one
// per Check.
type fakeInspector struct {
	answers []common.WebhookInfo
	err     error
	calls   int
}

func (f *fakeInspector) WebhookInfo(context.Context) (common.WebhookInfo, error) {
	if f.err != nil {
		return common.WebhookInfo{}, f.err
	}
	i := f.calls
	f.calls++
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	return f.answers[i], nil
}

func newWatchdog(t *testing.T, answers []common.WebhookInfo) (*WebhookWatchdog, *fakeSyncOutbox) {
	t.Helper()
	outbox := &fakeSyncOutbox{}
	return &WebhookWatchdog{
		Inspector: &fakeInspector{answers: answers},
		Alerter:   &AdminAlerter{Outbox: outbox, ChatIDs: []int64{1}, Log: slog.Default()},
		Metrics:   newTestMetrics(), Clock: common.SystemUTCClock(), Log: slog.Default(),
		PendingThreshold: 100,
	}, outbox
}

// alertKinds lists the admin-alert kinds enqueued so far, in order.
func alertKinds(outbox *fakeSyncOutbox) []string {
	var out []string
	for _, call := range outbox.enqueued {
		out = append(out, call.aggregateID)
	}
	return out
}

// Every deploy restarts the bot and produces exactly one failed delivery
// while the container comes back. Alerting on that would fire on every
// single deploy, and an alert nobody reads is worse than no alert.
func TestWebhookWatchdog_IgnoresASingleFailureFromARestart(t *testing.T) {
	failedAt := time.Now().Add(-2 * time.Minute)
	w, outbox := newWatchdog(t, []common.WebhookInfo{
		{LastErrorAt: failedAt, LastErrorMessage: "Wrong response from the webhook: 502 Bad Gateway"},
		{LastErrorAt: failedAt, LastErrorMessage: "Wrong response from the webhook: 502 Bad Gateway"},
		{LastErrorAt: failedAt, LastErrorMessage: "Wrong response from the webhook: 502 Bad Gateway"},
	})

	for i := 0; i < 3; i++ {
		w.Check(context.Background())
	}

	if len(outbox.enqueued) != 0 {
		t.Fatalf("one old failure that never repeats is not an outage, got %v", alertKinds(outbox))
	}
}

// Delivery failing again on every check is the real thing: the bot is up,
// and nobody's messages are arriving.
func TestWebhookWatchdog_AlertsWhenDeliveryKeepsFailing(t *testing.T) {
	now := time.Now()
	w, outbox := newWatchdog(t, []common.WebhookInfo{
		{LastErrorAt: now.Add(-9 * time.Minute), LastErrorMessage: "connection refused"},
		{LastErrorAt: now.Add(-4 * time.Minute), LastErrorMessage: "connection refused"},
		{LastErrorAt: now.Add(-1 * time.Minute), LastErrorMessage: "connection refused"},
	})

	// The first check cannot tell a failure from ten minutes ago apart
	// from one from ten days ago being seen for the first time, so it only
	// starts counting.
	w.Check(context.Background())
	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected the first observation to alert nobody, got %v", alertKinds(outbox))
	}
	// A second check that sees a newer failure than the first means
	// delivery is failing now, not that it failed once during a restart.
	w.Check(context.Background())
	if len(outbox.enqueued) != 1 || alertKinds(outbox)[0] != "broken" {
		t.Fatalf("expected one broken alert, got %v", alertKinds(outbox))
	}

	// Still broken on the next check: the administrators have been told,
	// and repeating it every few minutes would only train them to mute it.
	w.Check(context.Background())
	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected no repeat alert while it stays broken, got %v", alertKinds(outbox))
	}
}

// A backlog Telegram is holding is broken delivery by itself, whatever the
// error history says — a working webhook drains its queue in seconds.
func TestWebhookWatchdog_AlertsOnABacklogAndClosesTheLoopOnRecovery(t *testing.T) {
	w, outbox := newWatchdog(t, []common.WebhookInfo{
		{PendingUpdateCount: 500},
		{PendingUpdateCount: 0},
	})

	w.Check(context.Background())
	if len(outbox.enqueued) != 1 || alertKinds(outbox)[0] != "broken" {
		t.Fatalf("expected a backlog to alert on its own, got %v", alertKinds(outbox))
	}

	w.Check(context.Background())
	if len(outbox.enqueued) != 2 || alertKinds(outbox)[1] != "recovered" {
		t.Fatalf("a recovery has to be announced too, or a fixed outage looks like an ongoing one: %v", alertKinds(outbox))
	}
}

// Telegram being unreachable is a different failure, already covered by
// provider health — and alerting through Telegram about Telegram being
// down would not arrive anyway.
func TestWebhookWatchdog_StaysQuietWhenItCannotAsk(t *testing.T) {
	outbox := &fakeSyncOutbox{}
	w := &WebhookWatchdog{
		Inspector: &fakeInspector{err: errors.New("api unreachable")},
		Alerter:   &AdminAlerter{Outbox: outbox, ChatIDs: []int64{1}, Log: slog.Default()},
		Metrics:   newTestMetrics(), Clock: common.SystemUTCClock(), Log: slog.Default(),
	}

	w.Check(context.Background())

	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no alert when the check itself failed, got %v", alertKinds(outbox))
	}
}
