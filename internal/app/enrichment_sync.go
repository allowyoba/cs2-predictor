package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// RankingSync fetches one named ranking feed (Valve VRS, HLTV, ...),
// resolves each team against the local catalog via the shared identity-
// matching pipeline (internal/domain/enrichment/identity.go), and caches
// the result: a standalone scheduled job with its own interval, lock and
// logging, separate from CompetitionSynchronization's jobs. One instance
// per Source — the matching logic itself has nothing source-specific about
// it, so Valve VRS and HLTV are wired identically, just pointed at
// different providers. A failure here (the feed unreachable, malformed
// data, ...) is always best-effort: logged and recorded for /healthz,
// never propagated as a hard error that could affect anything else.
type RankingSync struct {
	// Source names which enrichment.Source this instance maintains — used
	// throughout as the identity/ranking lookup key, and folded into this
	// job's cluster-lock name and log lines so two sources' jobs never
	// contend with each other and their logs stay distinguishable.
	Source    enrichment.Source
	Provider  enrichment.RankingProvider
	Teams     enrichment.TeamLister
	Rankings  enrichment.RankingRepository
	Identity  enrichment.IdentityRepository
	State     enrichment.SyncStateRepository
	Snapshots enrichment.SnapshotRepository
	Lock      common.ClusterLock
	// LockKey overrides the cluster-lock name Dispatch acquires — normally
	// derived from Source alone, which is fine as long as only one
	// RankingSync instance ever maintains a given Source. That stops being
	// true once a Source has both a free, frequent job and a paid, rarely-
	// ticking one (see cmd/bot/main.go's vrsApifyRankingSync, sharing
	// enrichment.SourceValveVRS with valveVRSSync): with no jitter and one
	// interval an exact multiple of the other, their ticks stay phase-
	// locked, and a shared lock name means the paid job can silently and
	// indefinitely lose the advisory-lock race to the free one every single
	// time they collide — ClusterLock.Execute returns (false, nil) on a
	// lost race, not an error, so Dispatch logs nothing when that happens.
	// Set LockKey to give such a job its own lock identity instead; both
	// still write the same Source's rows, but do so independently rather
	// than under one mutual-exclusion umbrella that was never required for
	// correctness (every write here is a per-team UPSERT).
	LockKey string
	// Gate, if set, is consulted at the start of every Dispatch: a false
	// result skips this tick's fetch entirely (no provider call, no
	// snapshot/ranking writes, no success/failure recorded) without it
	// being treated as an error. Nil always runs — the free/frequent
	// sources (valvevrs, GRID, Liquipedia) leave this nil and keep their
	// existing "every tick fetches" behavior; only the paid Apify-backed
	// sources set one (see ApifyRankingGate).
	Gate RankingSyncGate
	Log  *slog.Logger
}

func (s *RankingSync) lockKey() string {
	if s.LockKey != "" {
		return s.LockKey
	}
	return "cs2predictor:ranking-sync:" + string(s.Source)
}

func (s *RankingSync) Dispatch(ctx context.Context) {
	guardedDispatch(ctx, s.Lock, s.lockKey(), s.Log, "ranking sync ("+string(s.Source)+")", func(ctx context.Context) {
		if s.Gate != nil {
			ok, gateErr := s.Gate.ShouldRun(ctx, s.Source)
			if gateErr != nil {
				// Recorded, not just logged: a gate that fails forever
				// (e.g. its own dependency is down) must show up in
				// /provider_status and /healthz/ready the same way any
				// other persistent sync failure does, rather than quietly
				// reporting the source as healthy while it never fetches.
				s.recordFailure(ctx, fmt.Errorf("gate check failed: %w", gateErr))
				return
			}
			if !ok {
				return
			}
		}
		s.sync(ctx)
	})
}

func (s *RankingSync) sync(ctx context.Context) {
	ranked, err := s.Provider.FetchRankings(ctx)
	if errors.Is(err, enrichment.ErrFetchPending) {
		// The provider started (or is still waiting on) a remote job it
		// will collect on a later tick. Neither a success nor a failure:
		// recording either would make a provider that simply takes minutes
		// look healthy-but-stale or outright broken in /provider_status.
		s.Log.Info("ranking sync waiting on the provider's remote job", "source", s.Source)
		return
	}
	if err != nil {
		s.recordFailure(ctx, err)
		return
	}

	// Cached regardless of match outcome, ahead of the matching loop below
	// — this is the raw material app.TeamMatchService searches against
	// when a team with no ranking of its own shows up in a new poll, so it
	// needs the full feed, not just the teams that happened to match here.
	if s.Snapshots != nil {
		if err := s.Snapshots.SaveSnapshot(ctx, ranked); err != nil {
			s.Log.Error("ranking snapshot save failed", "source", s.Source, "error", err)
		}
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
		if err := s.Rankings.SaveRanking(ctx, enrichment.NewTeamRanking(teamID, rt)); err != nil {
			s.Log.Error("ranking save failed", "source", s.Source, "team", rt.Identity.Name, "error", err)
		}
	}

	if err := s.State.RecordSuccess(ctx, s.Source); err != nil {
		s.Log.Error("ranking sync state record-success failed", "source", s.Source, "error", err)
	}
	s.Log.Info("rankings synchronized", "source", s.Source, "fetched", len(ranked), "matched", matched, "unmatched", unmatched)
}

func (s *RankingSync) recordFailure(ctx context.Context, err error) {
	recordSyncFailure(ctx, s.State, s.Source, s.Log, "ranking", err)
}

// buildCandidates loads every local team plus its known aliases (one batch
// query, not one per team) and, where we already have a cached ranking for
// it from this same Source, that ranking's roster — giving MatchTeam's
// roster-overlap step something to compare against even on a team that has
// never matched by name/alias before.
func (s *RankingSync) buildCandidates(ctx context.Context) ([]enrichment.TeamCandidate, error) {
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
	rankings, err := s.Rankings.FindRankings(ctx, teamIDs, s.Source)
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

// resolveTeam tries the cheap, already-confirmed mapping first (this
// Source's provider publishes no stable team IDs of its own, so the
// normalized team name is used as the stable "external ID" for it),
// falling back to MatchTeam's name/alias/roster pipeline and persisting a
// new mapping when that succeeds.
func (s *RankingSync) resolveTeam(ctx context.Context, candidates []enrichment.TeamCandidate, identity enrichment.TeamIdentity) (common.TeamID, bool) {
	externalID := enrichment.NormalizeTeamName(identity.Name)
	if id, err := s.Identity.FindTeamByExternalID(ctx, s.Source, externalID); err != nil {
		s.Log.Warn("ranking identity lookup failed, falling back to name/alias/roster matching", "source", s.Source, "team", identity.Name, "error", err)
	} else if id != nil {
		return *id, true
	}

	teamID, confidence, ok := enrichment.MatchTeam(candidates, identity)
	if !ok {
		return common.TeamID{}, false
	}
	if err := s.Identity.SaveIdentity(ctx, teamID, s.Source, externalID, identity.Name, confidence); err != nil {
		s.Log.Warn("ranking identity save failed", "source", s.Source, "team", identity.Name, "error", err)
	}
	return teamID, true
}
