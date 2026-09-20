package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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
	Source   enrichment.Source
	Provider enrichment.RankingProvider
	Teams    enrichment.TeamLister
	// TeamLogos stores the crest this feed publishes alongside each team.
	// Optional: a deployment without it simply keeps whatever logo the
	// match provider supplied.
	TeamLogos enrichment.TeamLogoWriter
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

	s.apply(ctx, ranked, "rankings synchronized")
}

// apply caches the feed, matches it against the local catalog and records
// the success. Shared by the scheduled sync and the startup refresh so both
// leave the database in exactly the same state.
func (s *RankingSync) apply(ctx context.Context, ranked []enrichment.RankedTeam, logMessage string) {
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
		// Best effort: a missing crest or flag is a cosmetic loss in one
		// surface, and failing the ranking sync over it would cost the
		// rankings themselves.
		//
		// The country arrives as a name and leaves as a code; a country
		// nothing recognises resolves to empty, which stores nothing and
		// leaves the provider's own answer in place.
		country := enrichment.CountryCode(rt.Identity.Country)
		if s.TeamLogos != nil && (rt.Identity.LogoURL != "" || country != "") {
			if err := s.TeamLogos.SetRankingAppearance(ctx, teamID, s.Source, rt.Identity.LogoURL, country); err != nil {
				s.Log.Warn("team appearance save failed", "source", s.Source, "team", rt.Identity.Name, "error", err)
			}
		}
	}

	if err := s.State.RecordSuccess(ctx, s.Source); err != nil {
		s.Log.Error("ranking sync state record-success failed", "source", s.Source, "error", err)
	}
	s.Log.Info(logMessage, "source", s.Source, "fetched", len(ranked), "matched", matched, "unmatched", unmatched)
}

// RefreshFromCache adopts a result the provider already holds, if it is
// newer than what this source last recorded. Meant for startup: a remote
// run can finish while the bot is down — during the very deploy that
// restarts it, even — and the weekly gate would otherwise leave that
// already-paid-for ranking unread for days.
//
// The provider call happens OUTSIDE the cluster lock on purpose. That lock
// pins a pooled database connection for as long as it is held, and this
// fetch is network I/O against someone else's API: holding a connection
// across it starved the pool on the first deploy that shipped this, and
// every background job then died on its own timeout waiting to acquire one.
// Fetch first, lock only to write.
//
// Deliberately never starts remote work and never records a failure: this
// is opportunistic catch-up, and a provider that cannot answer right now
// still has its ordinary scheduled run ahead of it.
func (s *RankingSync) RefreshFromCache(ctx context.Context) {
	cached, ok := s.Provider.(enrichment.CachedRankingProvider)
	if !ok {
		return
	}
	ranked, err := cached.FetchLatestCached(ctx)
	if err != nil {
		s.Log.Warn("startup ranking refresh failed, leaving the scheduled sync to it", "source", s.Source, "error", err)
		return
	}
	if len(ranked) == 0 {
		return
	}
	_, err = s.Lock.Execute(ctx, s.lockKey()+":startup-refresh", func(ctx context.Context) error {
		fresher, err := s.newerThanRecorded(ctx, ranked)
		if err != nil || !fresher {
			return err
		}
		s.apply(ctx, ranked, "rankings refreshed from the provider's last finished run")
		return nil
	})
	if err != nil {
		s.Log.Error("startup ranking refresh failed", "source", s.Source, "error", err)
	}
}

// newerThanRecorded reports whether the cached feed post-dates this
// source's last successful sync. Compared on the ranking's own published
// date, not on when it was read: re-applying a snapshot we already hold
// would be write traffic for nothing.
func (s *RankingSync) newerThanRecorded(ctx context.Context, ranked []enrichment.RankedTeam) (bool, error) {
	var published time.Time
	for _, rt := range ranked {
		if rt.PublishedAt.After(published) {
			published = rt.PublishedAt
		}
	}
	if published.IsZero() {
		return false, nil
	}
	state, err := s.State.State(ctx, s.Source)
	if err != nil {
		return false, err
	}
	if state == nil || state.LastSuccessAt == nil {
		return true, nil
	}
	return published.After(*state.LastSuccessAt), nil
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
	// Only the games this feed actually ranks: see enrichment.RanksGame
	// for what goes wrong when a ranking is matched against a team from a
	// game it knows nothing about.
	teams, err := s.Teams.ListTeams(ctx, enrichment.RankedGames(s.Source)...)
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
