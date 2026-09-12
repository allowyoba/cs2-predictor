package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// guardedDispatch runs fn under a named cluster lock, logging (not
// propagating) any error the lock itself returns — the same "best-effort,
// log don't fail" shape every scheduled sync job in this package shares.
// A lost lock race (someone else already running this job) is not an
// error and is silently skipped, same as it always was.
func guardedDispatch(ctx context.Context, lock common.ClusterLock, lockKey string, log *slog.Logger, jobName string, fn func(ctx context.Context)) {
	_, err := lock.Execute(ctx, lockKey, func(ctx context.Context) error {
		fn(ctx)
		return nil
	})
	if err != nil {
		log.Error(jobName+" dispatch failed", "error", err)
	}
}

// recordSyncFailure is the shared "keep the cache, just note the outage"
// failure path every enrichment sync job uses: a warn log plus
// SyncStateRepository.RecordFailure, itself best-effort (a failure to
// record the failure is merely logged, never escalated).
func recordSyncFailure(ctx context.Context, state enrichment.SyncStateRepository, source enrichment.Source, log *slog.Logger, jobName string, err error) {
	log.Warn(jobName+" sync failed, keeping cached data", "source", source, "error", err)
	if stateErr := state.RecordFailure(ctx, source, err.Error()); stateErr != nil {
		log.Error(jobName+" sync state record-failure failed", "source", source, "error", stateErr)
	}
}

// allAttemptsFailed reports whether a batch of best-effort lookups should
// be treated as a sync failure: not "zero results" (a clean "provider has
// nothing for this yet" is normal and expected for a fresh team/tournament)
// but "every single attempt actually errored," which points at something
// more structural (a bad key, the provider being down) worth recording.
func allAttemptsFailed(attempted, succeeded int, lastErr error) bool {
	return attempted > 0 && succeeded == 0 && lastErr != nil
}
