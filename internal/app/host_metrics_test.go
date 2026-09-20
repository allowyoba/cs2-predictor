package app

import (
	"context"
	"log/slog"
	"testing"
)

// The machine running out of something is the failure that takes every job
// down at once and arrives disguised as unrelated errors everywhere. These
// cover the alerting shape around it: once on the way past a limit, once
// on the way back, and nothing in between.

func newHostMonitor(t *testing.T) (*HostMonitor, *fakeSyncOutbox) {
	t.Helper()
	outbox := &fakeSyncOutbox{}
	return &HostMonitor{
		Metrics: newTestMetrics(),
		Alerter: &AdminAlerter{Outbox: outbox, ChatIDs: []int64{1}, Switches: allNotificationsOn{}, Log: slog.Default()},
		Limits:  HostLimits{Memory: 0.9, Disk: 0.85, Load: 4},
		Log:     slog.Default(),
	}, outbox
}

func TestHostMonitor_AlertsOnceOnTheWayPastALimitAndOnceBack(t *testing.T) {
	monitor, outbox := newHostMonitor(t)
	ctx := context.Background()

	monitor.evaluate(ctx, "disk", 0.70, monitor.Limits.Disk)
	if len(outbox.enqueued) != 0 {
		t.Fatalf("a healthy reading alerts nobody, got %v", outbox.enqueued)
	}

	monitor.evaluate(ctx, "disk", 0.91, monitor.Limits.Disk)
	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected one alert on crossing the limit, got %v", outbox.enqueued)
	}
	// Still over on the next sample: repeating every few minutes is how an
	// alert becomes something people filter out.
	monitor.evaluate(ctx, "disk", 0.95, monitor.Limits.Disk)
	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected no repeat while it stays over, got %v", outbox.enqueued)
	}

	monitor.evaluate(ctx, "disk", 0.60, monitor.Limits.Disk)
	if len(outbox.enqueued) != 2 {
		t.Fatalf("a recovery must be announced too, or a fixed problem looks ongoing: %v", outbox.enqueued)
	}
}

// Each resource is watched independently: a full disk must not mask memory
// pressure that started afterwards.
func TestHostMonitor_TracksEachResourceSeparately(t *testing.T) {
	monitor, outbox := newHostMonitor(t)
	ctx := context.Background()

	monitor.evaluate(ctx, "disk", 0.99, monitor.Limits.Disk)
	monitor.evaluate(ctx, "memory", 0.95, monitor.Limits.Memory)

	if len(outbox.enqueued) != 2 {
		t.Fatalf("expected one alert per resource, got %v", outbox.enqueued)
	}
}

// A limit of zero switches that alert off — deliberate, and unlike the
// feedback limits it costs visibility rather than safety.
func TestHostMonitor_ADisabledLimitNeverFires(t *testing.T) {
	monitor, outbox := newHostMonitor(t)
	monitor.evaluate(context.Background(), "load", 99, 0)
	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected silence for a disabled limit, got %v", outbox.enqueued)
	}
}

// The reading itself has to come from somewhere real. On Linux that is
// /proc and statfs; elsewhere (a developer's Mac) it simply reports an
// error, which the caller logs and moves on from.
func TestReadHostUsage_ReportsPlausibleFractions(t *testing.T) {
	usage, err := ReadHostUsage("/")
	if err != nil {
		t.Skipf("host metrics are Linux-specific: %v", err)
	}
	for name, value := range map[string]float64{"memory": usage.MemoryUsed, "disk": usage.DiskUsed} {
		if value < 0 || value > 1 {
			t.Fatalf("%s usage = %f, want a fraction of 1", name, value)
		}
	}
	if usage.LoadPerCPU < 0 {
		t.Fatalf("load per CPU = %f, want a non-negative number", usage.LoadPerCPU)
	}
}
