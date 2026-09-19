package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/platform/common"
)

// DeadLetterWatch reports messages that have exhausted their retry budget.
//
// A dead letter is the quietest failure this system has: the dispatcher
// stops selecting it, the logs said so once, and nothing ever mentions it
// again — a chat simply never received its poll, its recap or its
// invitation. Fifteen of them sat undelivered in production before anybody
// noticed, which is what this exists to prevent.
type DeadLetterWatch struct {
	Store   common.DeadLetterStore
	Alerter *AdminAlerter
	Metrics *Metrics
	Log     *slog.Logger

	// alerted is the count the administrators were last told about. Only a
	// growing pile is worth another message: the same fifteen dead letters
	// reported every ten minutes would train everyone to ignore the alert,
	// which is the failure mode this is meant to fix.
	alerted int
}

func (w *DeadLetterWatch) Check(ctx context.Context) {
	groups, err := w.Store.DeadLetters(ctx)
	if err != nil {
		w.Log.Error("dead letter check failed", "error", err)
		return
	}
	total := 0
	for _, g := range groups {
		total += g.Count
		if w.Metrics != nil {
			w.Metrics.DeadLetters.WithLabelValues(g.EventType).Set(float64(g.Count))
		}
	}
	if total == 0 {
		w.alerted = 0
		return
	}
	if total <= w.alerted {
		return
	}
	w.alerted = total
	w.Log.Error("undelivered messages have exhausted their retries", "count", total, "kinds", len(groups))
	if w.Alerter != nil {
		w.Alerter.DeadLetters(ctx, total, describeDeadLetters(groups))
	}
}

// describeDeadLetters renders the worst offenders for a chat message —
// enough to tell "one flaky chat" from "every recap is failing" without
// opening a terminal.
func describeDeadLetters(groups []common.DeadLetterGroup) string {
	const shown = 3
	var detail string
	for i, g := range groups {
		if i == shown {
			break
		}
		if i > 0 {
			detail += ", "
		}
		detail += g.EventType + "×" + itoa(g.Count)
	}
	return detail
}

// itoa avoids pulling strconv in for one call site in one string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
