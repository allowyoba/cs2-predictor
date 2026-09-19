package app

import (
	"context"
	"log/slog"
	"time"

	"cs2predictor/internal/platform/common"
)

// WebhookWatchdog watches the one channel nothing else can see.
//
// Every other health signal in this process answers "is the bot running":
// the readiness probe, the provider breakers, the job metrics. None of them
// notices the failure that actually silences the bot — Telegram being
// unable to deliver updates to it. A wrong DNS record, an expired
// certificate, a firewall change, a proxy returning 502: the process stays
// healthy and the chats simply stop responding.
//
// The test is deliberately "errors are still happening now", not "an error
// happened": every deploy restarts the bot and produces one failed delivery
// as the container comes back, and an alert on that would fire on every
// deploy until nobody read it any more.
type WebhookWatchdog struct {
	Inspector common.WebhookInspector
	Alerter   *AdminAlerter
	Metrics   *Metrics
	Clock     common.Clock
	Log       *slog.Logger
	// PendingThreshold is how many updates Telegram may be holding before
	// that alone counts as broken, whatever the error history says.
	// Backlogs clear in seconds when delivery works.
	PendingThreshold int

	lastErrorAt time.Time
	// consecutive counts checks that each saw a NEWER failure than the one
	// before, i.e. delivery is failing continuously rather than having
	// failed once during a restart.
	consecutive int
	broken      bool
}

// WebhookFailuresBeforeAlert is how many consecutive checks must each see a
// new delivery failure before the administrators hear about it. Two, with
// the check running every few minutes, distinguishes "the bot restarted"
// from "Telegram cannot reach us".
const WebhookFailuresBeforeAlert = 2

func (w *WebhookWatchdog) Check(ctx context.Context) {
	info, err := w.Inspector.WebhookInfo(ctx)
	if err != nil {
		// Telegram itself being unreachable is a different problem, and
		// one the provider health path already reports on.
		w.Log.Warn("webhook status check failed", "error", err)
		return
	}
	if w.Metrics != nil {
		w.Metrics.WebhookState.WithLabelValues("pending_updates").Set(float64(info.PendingUpdateCount))
		w.Metrics.WebhookState.WithLabelValues("failing").Set(boolGauge(w.broken))
	}

	if info.LastErrorAt.After(w.lastErrorAt) {
		if !w.lastErrorAt.IsZero() {
			w.consecutive++
		} else {
			// First observation: there is no way to tell an error from ten
			// minutes ago apart from one from ten days ago being reported
			// for the first time, so start counting rather than alerting.
			w.consecutive = 1
		}
		w.lastErrorAt = info.LastErrorAt
	} else {
		w.consecutive = 0
	}

	backlogged := w.PendingThreshold > 0 && info.PendingUpdateCount >= w.PendingThreshold
	failing := backlogged || w.consecutive >= WebhookFailuresBeforeAlert
	switch {
	case failing && !w.broken:
		w.broken = true
		w.Log.Error("telegram cannot deliver updates to this bot",
			"url", info.URL, "pending", info.PendingUpdateCount, "lastError", info.LastErrorMessage)
		if w.Alerter != nil {
			w.Alerter.WebhookBroken(ctx, info.PendingUpdateCount, info.LastErrorMessage)
		}
	case !failing && w.broken:
		w.broken = false
		w.Log.Info("telegram webhook delivery recovered", "url", info.URL)
		if w.Alerter != nil {
			w.Alerter.WebhookRecovered(ctx)
		}
	}
	if w.Metrics != nil {
		w.Metrics.WebhookState.WithLabelValues("failing").Set(boolGauge(w.broken))
	}
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
