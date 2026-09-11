package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/platform/common"
)

// OutboxDispatcher polls the outbox and fans each message out to whichever
// publisher supports its type, marking a message failed if none do.
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
		_ = d.Outbox.Failed(ctx, message.ID, "no publisher for "+message.Type)
		d.Metrics.OutboxEvents.WithLabelValues("unsupported", message.Type).Inc()
		d.Log.Error("no outbox publisher", "eventId", message.ID, "type", message.Type)
		return
	}

	if err := publisher.Publish(ctx, message); err != nil {
		_ = d.Outbox.Failed(ctx, message.ID, err.Error())
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
	if err := d.Outbox.Published(ctx, message.ID); err != nil {
		d.Log.Error("failed to mark outbox message published", "eventId", message.ID, "error", err)
		return
	}
	d.Metrics.OutboxEvents.WithLabelValues("published", message.Type).Inc()
	d.Log.Info("outbox event published", "eventId", message.ID, "type", message.Type)
}
