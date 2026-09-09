package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// TeamStatsSync prefetches recent-form and head-to-head data (GRID) for
// teams playing in upcoming matches of actively-subscribed events, unlike
// ValveVRSSync's whole-catalog sync. GRID's request volume is the thing
// worth rationing here: each team/pair costs several GraphQL round trips,
// and most of the catalog will never appear in an upcoming poll.
type TeamStatsSync struct {
	TeamStats     enrichment.TeamStatsProvider
	MatchStats    enrichment.MatchStatsProvider
	Catalog       competition.Catalog
	Subscriptions subscription.Repository
	Form          enrichment.FormRepository
	H2H           enrichment.HeadToHeadRepository
	State         enrichment.SyncStateRepository
	Lock          common.ClusterLock
	Log           *slog.Logger
}

func (s *TeamStatsSync) Dispatch(ctx context.Context) {
	_, err := s.Lock.Execute(ctx, "cs2predictor:grid-team-stats-sync", func(ctx context.Context) error {
		s.sync(ctx)
		return nil
	})
	if err != nil {
		s.Log.Error("grid team stats sync dispatch failed", "error", err)
	}
}

func (s *TeamStatsSync) sync(ctx context.Context) {
	matches, err := s.upcomingMatches(ctx)
	if err != nil {
		s.recordFailure(ctx, err)
		return
	}

	// succeeded counts lookups that returned without error — including a
	// clean "GRID has nothing for this team yet" (nil, nil), which is not
	// itself a save. It's used only to detect the all-attempts-errored case
	// below, not as a count of rows actually written.
	succeeded, attempted, lastErr := 0, 0, error(nil)
	seenTeams := map[common.TeamID]bool{}
	for _, m := range matches {
		if m.FirstTeam == nil || m.SecondTeam == nil {
			continue
		}
		for _, team := range [2]competition.Team{*m.FirstTeam, *m.SecondTeam} {
			if seenTeams[team.ID] {
				continue
			}
			seenTeams[team.ID] = true
			attempted++
			if err := s.enrichForm(ctx, team); err != nil {
				lastErr = err
				s.Log.Warn("grid form lookup failed", "team", team.Name, "error", err)
				continue
			}
			succeeded++
		}

		attempted++
		if err := s.enrichH2H(ctx, *m.FirstTeam, *m.SecondTeam); err != nil {
			lastErr = err
			s.Log.Warn("grid head-to-head lookup failed", "teamA", m.FirstTeam.Name, "teamB", m.SecondTeam.Name, "error", err)
			continue
		}
		succeeded++
	}

	// A team/pair with simply no GRID data yet (nil, nil) is normal and
	// doesn't count against health. But if EVERY attempted lookup came back
	// an actual error (a bad key, GRID down, ...), that's worth recording
	// as a sync failure rather than silently reporting success.
	if attempted > 0 && succeeded == 0 && lastErr != nil {
		s.recordFailure(ctx, lastErr)
		return
	}

	if err := s.State.RecordSuccess(ctx, enrichment.SourceGRID); err != nil {
		s.Log.Error("grid sync state record-success failed", "error", err)
	}
	s.Log.Info("grid team stats synchronized", "matches", len(matches), "teams", len(seenTeams), "succeeded", succeeded, "attempted", attempted)
}

func (s *TeamStatsSync) upcomingMatches(ctx context.Context) ([]competition.Match, error) {
	eventIDs, err := s.Subscriptions.ActiveEventIDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(eventIDs) == 0 {
		return nil, nil
	}
	return s.Catalog.FindUnstartedMatchesForEvents(ctx, eventIDs)
}

func (s *TeamStatsSync) enrichForm(ctx context.Context, team competition.Team) error {
	form, err := s.TeamStats.GetTeamStats(ctx, enrichment.TeamIdentity{Name: team.Name})
	if err != nil {
		return err
	}
	if form == nil {
		return nil
	}
	return s.Form.SaveForm(ctx, team.ID, *form)
}

func (s *TeamStatsSync) enrichH2H(ctx context.Context, first, second competition.Team) error {
	h2h, err := s.MatchStats.GetHeadToHead(ctx, enrichment.TeamIdentity{Name: first.Name}, enrichment.TeamIdentity{Name: second.Name})
	if err != nil {
		return err
	}
	if h2h == nil {
		return nil
	}
	return s.H2H.SaveHeadToHead(ctx, first.ID, second.ID, *h2h)
}

func (s *TeamStatsSync) recordFailure(ctx context.Context, err error) {
	s.Log.Warn("grid team stats sync failed, keeping cached data", "error", err)
	if stateErr := s.State.RecordFailure(ctx, enrichment.SourceGRID, err.Error()); stateErr != nil {
		s.Log.Error("grid sync state record-failure failed", "error", stateErr)
	}
}
