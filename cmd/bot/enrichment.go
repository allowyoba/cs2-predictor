package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"cs2predictor/internal/adapter/apifyhltv"
	"cs2predictor/internal/adapter/grid"
	"cs2predictor/internal/adapter/liquipedia"
	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/adapter/valvevrs"
	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// backgroundJob pairs a scheduled sync job's Dispatch method with the
// interval it should be ticked at — see runBackground in main.go.
type backgroundJob struct {
	interval time.Duration
	dispatch func(ctx context.Context)
}

// enrichmentBuild is everything run() needs from the optional enrichment
// providers: which sources are actually producing data (for /healthz and
// TeamMatchService) and which background jobs to register. Pulled out of
// run() itself because this is the one part of composition-root wiring
// with real decisions to get right (which slices to append to, whether to
// share a lock key) rather than a flat, linear list of constructor calls.
type enrichmentBuild struct {
	// StartupTasks run once when the process comes up, before the
	// schedulers settle into their intervals.
	StartupTasks     []func(ctx context.Context)
	Sources          []enrichment.Source
	TeamMatchSources []enrichment.Source
	Jobs             []backgroundJob
}

// buildEnrichment wires every optional, independently-toggleable
// enrichment provider (Valve VRS, HLTV/Apify, GRID, Liquipedia) against
// the shared EnrichmentRepository. None of this affects PandaScore's role
// as the sole source of truth for events/matches/results — it only ever
// adds cached, best-effort context to a poll.
// state is repo's sync-state port wrapped so provider outages reach the
// administrators (see app.ObservedSyncState); every job takes it instead of
// repo directly, which is what makes the alerting impossible to forget when
// a new provider is added.
func buildEnrichment(
	cfg app.EnrichmentConfig, repo *pg.EnrichmentRepository, state enrichment.SyncStateRepository,
	catalog competition.Catalog, subscriptions subscription.Repository,
	httpClient *http.Client, clock common.Clock, lock common.ClusterLock, log *slog.Logger,
) enrichmentBuild {
	var b enrichmentBuild

	if cfg.ValveVRSEnabled {
		sync := &app.RankingSync{
			Source: enrichment.SourceValveVRS, Provider: valvevrs.NewProvider(valvevrs.DefaultConfig(), httpClient),
			Teams: repo, Rankings: repo, Identity: repo, State: state, Snapshots: repo,
			Lock: lock, Log: log,
		}
		b.Jobs = append(b.Jobs, backgroundJob{cfg.ValveVRSSyncInterval, sync.Dispatch})
	}

	// hltvSync and vrsApifySync are a paired weekly fetch — both hit the
	// same Apify actor, just in its two different ranking modes (see
	// apifyhltv.DefaultConfig/DefaultValveConfig) — and share one
	// ApifyRankingGate so both fire (or both stay quiet) together, at most
	// once a calendar week, on HLTV's own Monday update day. vrsApifySync
	// writes to the exact same enrichment.SourceValveVRS as the free
	// GitHub-based sync above (if enabled); that one keeps running on its
	// own frequent schedule as the fallback for the rest of the week. It
	// gets its own LockKey (see RankingSync.LockKey's doc comment) rather
	// than sharing the free sync's Source-derived one: with no jitter and
	// an interval that's an exact multiple of this job's check interval, a
	// shared lock name would let the two silently and repeatedly starve
	// each other.
	if cfg.HLTVEnabled {
		newSync := func(source enrichment.Source, providerConfig apifyhltv.Config, lockKey string) *app.RankingSync {
			providerConfig.MaxTeams = cfg.ApifyMaxTeams
			// One gate per ranking mode, keyed the same way the provider
			// records its run — a gate shared across both modes cannot tell
			// whose week is already done.
			return &app.RankingSync{
				Source: source, Provider: apifyhltv.NewProvider(providerConfig, httpClient, repo, clock),
				Teams: repo, Rankings: repo, Identity: repo, State: state, Snapshots: repo,
				Gate:    &app.ApifyRankingGate{Runs: repo, Key: providerConfig.RankingType, Clock: clock},
				LockKey: lockKey, Lock: lock, Log: log,
			}
		}
		hltvSync := newSync(enrichment.SourceHLTV, apifyhltv.DefaultConfig(cfg.HLTVAPIToken), "")
		vrsApifySync := newSync(enrichment.SourceValveVRS, apifyhltv.DefaultValveConfig(cfg.HLTVAPIToken), "cs2predictor:ranking-sync:VALVE_VRS_APIFY")
		b.Jobs = append(b.Jobs,
			backgroundJob{cfg.ApifyRankingCheckInterval, hltvSync.Dispatch},
			backgroundJob{cfg.ApifyRankingCheckInterval, vrsApifySync.Dispatch},
		)
		// A run can finish while the bot is down — during the deploy that
		// restarts it, most of all. Reading that finished run costs
		// nothing, so it happens at startup rather than waiting for the
		// weekly window to come round again.
		b.StartupTasks = append(b.StartupTasks, hltvSync.RefreshFromCache, vrsApifySync.RefreshFromCache)
	}

	// SourceValveVRS is registered once here, covering either or both of
	// the free and paid syncs above being the one(s) actually running —
	// team-matching against VALVE_VRS only needs at least one of them
	// producing data, not both.
	if cfg.ValveVRSEnabled || cfg.HLTVEnabled {
		b.Sources = append(b.Sources, enrichment.SourceValveVRS)
		b.TeamMatchSources = append(b.TeamMatchSources, enrichment.SourceValveVRS)
	}
	if cfg.HLTVEnabled {
		b.Sources = append(b.Sources, enrichment.SourceHLTV)
		b.TeamMatchSources = append(b.TeamMatchSources, enrichment.SourceHLTV)
	}

	if cfg.GRIDEnabled {
		gridProvider := grid.NewProvider(grid.DefaultConfig(cfg.GRIDAPIKey), httpClient)
		sync := &app.TeamStatsSync{
			TeamStats: gridProvider, MatchStats: gridProvider,
			Catalog: catalog, Subscriptions: subscriptions,
			Form: repo, H2H: repo, State: state, Lock: lock, Log: log,
		}
		b.Jobs = append(b.Jobs, backgroundJob{cfg.GRIDSyncInterval, sync.Dispatch})
		b.Sources = append(b.Sources, enrichment.SourceGRID)
	}

	if cfg.LiquipediaEnabled {
		sync := &app.TournamentMetadataSync{
			Provider: liquipedia.NewProvider(liquipedia.DefaultConfig(cfg.LiquipediaAPIKey), httpClient),
			Catalog:  catalog, Subscriptions: subscriptions,
			Metadata: repo, State: state, Lock: lock, Log: log,
		}
		b.Jobs = append(b.Jobs, backgroundJob{cfg.LiquipediaSyncInterval, sync.Dispatch})
		b.Sources = append(b.Sources, enrichment.SourceLiquipedia)
	}

	return b
}
