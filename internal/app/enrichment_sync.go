package app

import (
	"context"
	"log/slog"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// ValveVRSSync fetches Valve's Regional Standings, resolves each team
// against the local catalog, and caches the result: a standalone
// scheduled job with its own interval, lock and logging, separate from
// CompetitionSynchronization's jobs. A failure here (Valve unreachable,
// malformed data, ...) is always best-effort: logged and recorded for
// /healthz, never propagated as a hard error that could affect anything
// else.
type ValveVRSSync struct {
	Provider enrichment.RankingProvider
	Teams    enrichment.TeamLister
	Rankings enrichment.RankingRepository
	Identity enrichment.IdentityRepository
	State    enrichment.SyncStateRepository
	Lock     common.ClusterLock
	Log      *slog.Logger
}

func (s *ValveVRSSync) Dispatch(ctx context.Context) {
	_, err := s.Lock.Execute(ctx, "cs2predictor:valve-vrs-sync", func(ctx context.Context) error {
		s.sync(ctx)
		return nil
	})
	if err != nil {
		s.Log.Error("valve vrs sync dispatch failed", "error", err)
	}
}

func (s *ValveVRSSync) sync(ctx context.Context) {
	ranked, err := s.Provider.FetchRankings(ctx)
	if err != nil {
		s.recordFailure(ctx, err)
		return
	}

	candidates, err := s.buildCandidates(ctx)
	if err != nil {
		s.recordFailure(ctx, err)
		return
	}

	matched, unmatched := 0, 0
	for _, rt := range ranked {
		teamID, ok := s.resolveTeam(ctx, candidates, rt.Identity)
		if !ok {
			unmatched++
			continue
		}
		matched++
		ranking := enrichment.TeamRanking{
			TeamID: teamID, GlobalRank: rt.GlobalRank, RegionalRank: rt.RegionalRank,
			Region: rt.Region, Points: rt.Points, Roster: rt.Identity.Roster,
			PublishedAt: rt.PublishedAt, Source: rt.Source,
		}
		if err := s.Rankings.SaveRanking(ctx, ranking); err != nil {
			s.Log.Error("valve vrs ranking save failed", "team", rt.Identity.Name, "error", err)
		}
	}

	if err := s.State.RecordSuccess(ctx, enrichment.SourceValveVRS); err != nil {
		s.Log.Error("valve vrs sync state record-success failed", "error", err)
	}
	s.Log.Info("valve vrs rankings synchronized", "fetched", len(ranked), "matched", matched, "unmatched", unmatched)
}

func (s *ValveVRSSync) recordFailure(ctx context.Context, err error) {
	s.Log.Warn("valve vrs sync failed, keeping cached rankings", "error", err)
	if stateErr := s.State.RecordFailure(ctx, enrichment.SourceValveVRS, err.Error()); stateErr != nil {
		s.Log.Error("valve vrs sync state record-failure failed", "error", stateErr)
	}
}

// buildCandidates loads every local team plus its known aliases (one batch
// query, not one per team) and, where we already have a cached Valve
// ranking for it, that ranking's roster — giving MatchTeam's roster-overlap
// step something to compare against even on a team that has never matched
// by name/alias before.
func (s *ValveVRSSync) buildCandidates(ctx context.Context) ([]enrichment.TeamCandidate, error) {
	teams, err := s.Teams.ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	aliases, err := s.Identity.AllAliases(ctx)
	if err != nil {
		return nil, err
	}
	teamIDs := make([]common.TeamID, len(teams))
	for i, t := range teams {
		teamIDs[i] = t.ID
	}
	rankings, err := s.Rankings.FindRankings(ctx, teamIDs, enrichment.SourceValveVRS)
	if err != nil {
		return nil, err
	}

	candidates := make([]enrichment.TeamCandidate, len(teams))
	for i, t := range teams {
		candidates[i] = enrichment.TeamCandidate{TeamID: t.ID, Name: t.Name, Aliases: aliases[t.ID]}
		if ranking, ok := rankings[t.ID]; ok {
			candidates[i].Roster = ranking.Roster
		}
	}
	return candidates, nil
}

// resolveTeam tries the cheap, already-confirmed mapping first (Valve
// publishes no team IDs, so the normalized team name is used as the stable
// "external ID" for this provider), falling back to MatchTeam's name/alias/
// roster pipeline and persisting a new mapping when that succeeds.
func (s *ValveVRSSync) resolveTeam(ctx context.Context, candidates []enrichment.TeamCandidate, identity enrichment.TeamIdentity) (common.TeamID, bool) {
	externalID := enrichment.NormalizeTeamName(identity.Name)
	if id, err := s.Identity.FindTeamByExternalID(ctx, enrichment.SourceValveVRS, externalID); err != nil {
		s.Log.Warn("valve vrs identity lookup failed, falling back to name/alias/roster matching", "team", identity.Name, "error", err)
	} else if id != nil {
		return *id, true
	}

	teamID, confidence, ok := enrichment.MatchTeam(candidates, identity)
	if !ok {
		return common.TeamID{}, false
	}
	if err := s.Identity.SaveIdentity(ctx, teamID, enrichment.SourceValveVRS, externalID, identity.Name, confidence); err != nil {
		s.Log.Warn("valve vrs identity save failed", "team", identity.Name, "error", err)
	}
	return teamID, true
}
