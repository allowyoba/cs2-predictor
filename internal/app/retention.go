package app

import (
	"context"
	"log/slog"
	"time"

	"cs2predictor/internal/platform/common"
)

// RetentionSweep deletes rows that grow with traffic rather than with the
// domain. Without it the webhook dedup ledger in particular grows by one
// row per Telegram update forever — it is only ever deleted from on the
// failure path, so a healthy deployment never removes anything.
//
// Each table gets its own window because "safe to delete" means something
// different for each: a dedup row only has to outlive Telegram's retry
// window, a published outbox row is worth keeping a while for debugging,
// and a resolved confirmation is worth keeping about as long as anyone
// might ask what happened to it.
type RetentionSweep struct {
	Store common.RetentionRepository
	Lock  common.ClusterLock
	Clock common.Clock
	Log   *slog.Logger

	ProcessedUpdatesTTL time.Duration
	PublishedOutboxTTL  time.Duration
	ResolvedRequestsTTL time.Duration
	AdminActionsTTL     time.Duration

	// ProcessedUpdatesMaxRows/PublishedOutboxMaxRows are a hard row-count
	// backstop applied after the TTL deletes above: a time window alone caps
	// how long a row lives, not how big the table gets between sweeps at high
	// traffic volume, so this caps that directly regardless of how much
	// traffic grows. <= 0 disables it, the same "keep everything" escape
	// hatch a zero TTL is.
	ProcessedUpdatesMaxRows int
	PublishedOutboxMaxRows  int
}

func (s *RetentionSweep) Dispatch(ctx context.Context) {
	_, err := s.Lock.Execute(ctx, "cs2predictor:retention-sweep", func(ctx context.Context) error {
		s.sweep(ctx)
		return nil
	})
	if err != nil {
		s.Log.Error("retention sweep dispatch failed", "error", err)
	}
}

func (s *RetentionSweep) sweep(ctx context.Context) {
	now := s.Clock.Now()

	// Each delete is independent: one failing (a lock timeout on a busy
	// table, say) must not stop the others from reclaiming their space.
	type target struct {
		name   string
		ttl    time.Duration
		delete func(context.Context, time.Time) (int64, error)
	}
	targets := []target{
		{"processed_telegram_update", s.ProcessedUpdatesTTL, s.Store.DeleteProcessedUpdatesBefore},
		{"outbox_event", s.PublishedOutboxTTL, s.Store.DeletePublishedOutboxBefore},
		{"pending_unsubscribe", s.ResolvedRequestsTTL, s.Store.DeleteResolvedUnsubscribesBefore},
		{"admin_action_log", s.AdminActionsTTL, s.Store.DeleteAdminActionsBefore},
	}

	total := int64(0)
	for _, t := range targets {
		if t.ttl <= 0 {
			continue // retention disabled for this table
		}
		deleted, err := t.delete(ctx, now.Add(-t.ttl))
		if err != nil {
			s.Log.Warn("retention delete failed", "table", t.name, "error", err)
			continue
		}
		total += deleted
		if deleted > 0 {
			s.Log.Info("retention deleted rows", "table", t.name, "rows", deleted, "olderThan", t.ttl.String())
		}
	}

	// Row-count backstop, on top of the TTL sweep above — see
	// ProcessedUpdatesMaxRows's doc comment for why the TTL alone isn't
	// enough at high traffic volume. Independent of the TTL targets for the
	// same reason they're independent of each other: one failing must not
	// stop the other from reclaiming its space.
	type capTarget struct {
		name    string
		maxRows int
		delete  func(context.Context, int) (int64, error)
	}
	capTargets := []capTarget{
		{"processed_telegram_update", s.ProcessedUpdatesMaxRows, s.Store.DeleteProcessedUpdatesExceeding},
		{"outbox_event", s.PublishedOutboxMaxRows, s.Store.DeletePublishedOutboxExceeding},
	}
	for _, t := range capTargets {
		if t.maxRows <= 0 {
			continue // row-count cap disabled for this table
		}
		deleted, err := t.delete(ctx, t.maxRows)
		if err != nil {
			s.Log.Warn("retention row-count cap failed", "table", t.name, "error", err)
			continue
		}
		total += deleted
		if deleted > 0 {
			s.Log.Info("retention deleted rows exceeding cap", "table", t.name, "rows", deleted, "maxRows", t.maxRows)
		}
	}
	s.Log.Debug("retention sweep finished", "rowsDeleted", total)

	s.maintainOutboxPartitions(ctx, now)
}

// outboxPartitionLookahead: days ahead of today to keep pre-created.
const outboxPartitionLookahead = 2

// outboxPartitionBacklogScanDays bounds how far back to look for droppable
// partitions each sweep — generous since checks are cheap and this also
// catches up after the sweep was disabled or down for a while.
const outboxPartitionBacklogScanDays = 60

// maintainOutboxPartitions keeps outbox_event's daily partitions (migration
// 0031) ahead of writes and drops old empty ones, one day past
// PublishedOutboxTTL as margin for the deletes above to empty them first. A
// partition still holding rows (e.g. a message that exhausted its retry
// budget, see common.OutboxMaxAttempts) is left alone and retried later.
func (s *RetentionSweep) maintainOutboxPartitions(ctx context.Context, now time.Time) {
	if s.PublishedOutboxTTL <= 0 {
		return // partition lifecycle follows the same TTL; disabled means disabled
	}
	today := now.UTC().Truncate(24 * time.Hour)

	for i := 0; i <= outboxPartitionLookahead; i++ {
		day := today.AddDate(0, 0, i)
		if err := s.Store.EnsureOutboxPartition(ctx, day); err != nil {
			s.Log.Warn("failed to ensure outbox partition", "date", day.Format(time.DateOnly), "error", err)
		}
	}

	cutoff := today.Add(-s.PublishedOutboxTTL - 24*time.Hour)
	for day := cutoff.AddDate(0, 0, -outboxPartitionBacklogScanDays); day.Before(cutoff); day = day.AddDate(0, 0, 1) {
		dropped, err := s.Store.DropOutboxPartitionIfEmpty(ctx, day)
		if err != nil {
			s.Log.Warn("failed to drop outbox partition", "date", day.Format(time.DateOnly), "error", err)
			continue
		}
		if dropped {
			s.Log.Info("dropped empty outbox partition", "date", day.Format(time.DateOnly))
		}
	}
}
