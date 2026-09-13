package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/platform/common"
)

// OutboxDispatcher polls the outbox and fans each message out to whichever
// publisher supports its type, marking a message failed if none do.
//
// dispatchOne calls Publish and then, on success, Outbox.Published as two
// separate steps rather than one atomic action — a crash between them (the
// message genuinely reached Telegram, but the process dies before the
// UPDATE marking it published commits) leaves the message looking
// unpublished, so the next Dispatch run sends it again. This is the
// accepted tradeoff of at-least-once delivery over a non-transactional
// external call: Publish can't be made part of the same DB transaction as
// Published (it's an HTTP call to Telegram, not a database write), and
// Telegram's own API has no per-message idempotency key to de-duplicate
// against. Narrowing the crash window further (e.g. marking published
// immediately before, rather than after, Publish) would trade this failure
// mode for the opposite and strictly worse one — marking a message
// published when it was never actually sent.
type OutboxDispatcher struct {
	Outbox     common.Outbox
	Publishers []common.OutboxPublisher
	Lock       common.ClusterLock
	BatchSize  int
	Metrics    *Metrics
	Log        *slog.Logger
}

func (d *OutboxDispatcher) Dispatch(ctx context.Context) {
	_, err := d.Lock.Execute(ctx, "cs2predictor:outbox", func(ctx context.Context) error {
		messages, err := d.Outbox.Pending(ctx, d.BatchSize)
		if err != nil {
			return err
		}
		for _, message := range messages {
			d.dispatchOne(ctx, message)
		}
		return nil
	})
	if err != nil {
		d.Log.Error("outbox dispatch failed", "error", err)
	}
}

func (d *OutboxDispatcher) dispatchOne(ctx context.Context, message common.OutboxMessage) {
	var publisher common.OutboxPublisher
	for _, p := range d.Publishers {
		if p.Supports(message.Type) {
			publisher = p
			break
		}
	}
	if publisher == nil {
		_ = d.Outbox.Failed(ctx, message.ID, message.OccurredAt, "no publisher for "+message.Type)
		d.Metrics.OutboxEvents.WithLabelValues("unsupported", message.Type).Inc()
		d.Log.Error("no outbox publisher", "eventId", message.ID, "type", message.Type)
		return
	}

	if err := publisher.Publish(ctx, message); err != nil {
		_ = d.Outbox.Failed(ctx, message.ID, message.OccurredAt, err.Error())
		d.Metrics.OutboxEvents.WithLabelValues("failed", message.Type).Inc()
		d.Log.Error("outbox publish failed", "eventId", message.ID, "type", message.Type, "error", err, "attempts", message.Attempts+1)
		// message.Attempts is the value BEFORE this failure's increment
		// (see Outbox.Failed); once it reaches OutboxMaxAttempts, Pending
		// stops returning this message forever — it goes permanently quiet
		// rather than erroring loudly, so that transition needs its own
		// distinct, alertable signal instead of looking identical to every
		// ordinary retryable failure above.
		if message.Attempts+1 >= common.OutboxMaxAttempts {
			d.Metrics.OutboxEvents.WithLabelValues("exhausted", message.Type).Inc()
			d.Log.Error("outbox message exhausted its retry budget, will not be retried again",
				"eventId", message.ID, "type", message.Type, "attempts", message.Attempts+1)
		}
		return
	}
	if err := d.Outbox.Published(ctx, message.ID, message.OccurredAt); err != nil {
		d.Log.Error("failed to mark outbox message published", "eventId", message.ID, "error", err)
		return
	}
	d.Metrics.OutboxEvents.WithLabelValues("published", message.Type).Inc()
	d.Log.Info("outbox event published", "eventId", message.ID, "type", message.Type)
}
