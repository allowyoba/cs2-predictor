package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// TournamentMetadataSync prefetches Liquipedia tournament metadata for
// actively-subscribed events rather than the whole catalog: most events
// never get subscribed to, and a resolved tournament's metadata never
// changes, so this skips any event that already has a cached row rather
// than re-querying every tick.
//
// A miss (EnrichTournament returning nil — Liquipedia's own tournament page
// naming frequently doesn't match PandaScore's event name exactly) leaves
// no cached row, so that event IS retried on every subsequent run. That's
// an accepted trade-off for now: the set of actively-subscribed events is
// small and LIQUIPEDIA_SYNC_INTERVAL defaults to 24h, so the extra requests
// are cheap against Liquipedia's rate limit — a negative cache could be
// added later if that stops being true.
type TournamentMetadataSync struct {
	Provider      enrichment.TournamentMetadataProvider
	Catalog       competition.Catalog
	Subscriptions subscription.Repository
	Metadata      enrichment.TournamentMetadataRepository
	State         enrichment.SyncStateRepository
	Lock          common.ClusterLock
	Log           *slog.Logger
}

func (s *TournamentMetadataSync) Dispatch(ctx context.Context) {
	_, err := s.Lock.Execute(ctx, "cs2predictor:liquipedia-tournament-sync", func(ctx context.Context) error {
		s.sync(ctx)
		return nil
	})
	if err != nil {
		s.Log.Error("liquipedia tournament sync dispatch failed", "error", err)
	}
}

func (s *TournamentMetadataSync) sync(ctx context.Context) {
	eventIDs, err := s.Subscriptions.ActiveEventIDs(ctx)
	if err != nil {
		s.recordFailure(ctx, err)
		return
	}
	var events []competition.Event
	if len(eventIDs) > 0 {
		events, err = s.Catalog.FindEvents(ctx, eventIDs)
		if err != nil {
			s.recordFailure(ctx, err)
			return
		}
	}

	succeeded, attempted, lastErr := 0, 0, error(nil)
	for _, event := range events {
		existing, err := s.Metadata.FindTournamentMetadata(ctx, event.ID, enrichment.SourceLiquipedia)
		if err != nil {
			attempted++
			lastErr = err
			s.Log.Warn("liquipedia tournament metadata cache lookup failed", "event", event.Name, "error", err)
			continue
		}
		if existing != nil {
			continue
		}

		attempted++
		meta, err := s.Provider.EnrichTournament(ctx, event.Name)
		if err != nil {
			lastErr = err
			s.Log.Warn("liquipedia tournament lookup failed", "event", event.Name, "error", err)
			continue
		}
		succeeded++
		if meta == nil {
			continue
		}
		if err := s.Metadata.SaveTournamentMetadata(ctx, event.ID, *meta, enrichment.SourceLiquipedia); err != nil {
			s.Log.Error("liquipedia tournament metadata save failed", "event", event.Name, "error", err)
		}
	}

	if attempted > 0 && succeeded == 0 && lastErr != nil {
		s.recordFailure(ctx, lastErr)
		return
	}

	if err := s.State.RecordSuccess(ctx, enrichment.SourceLiquipedia); err != nil {
		s.Log.Error("liquipedia sync state record-success failed", "error", err)
	}
	s.Log.Info("liquipedia tournament metadata synchronized", "events", len(events), "succeeded", succeeded, "attempted", attempted)
}

func (s *TournamentMetadataSync) recordFailure(ctx context.Context, err error) {
	s.Log.Warn("liquipedia tournament sync failed, keeping cached data", "error", err)
	if stateErr := s.State.RecordFailure(ctx, enrichment.SourceLiquipedia, err.Error()); stateErr != nil {
		s.Log.Error("liquipedia sync state record-failure failed", "error", stateErr)
	}
}
