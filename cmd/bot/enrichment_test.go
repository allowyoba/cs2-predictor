package main

import (
	"log/slog"
	"testing"
	"time"

	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// buildEnrichment only wires configuration together — it never calls a
// method on the repository/catalog/subscriptions it's given — so a
// zero-value repo and nil catalog/subscriptions/lock are safe stand-ins
// for a unit test that only checks which sources/jobs get registered.
func newTestEnrichmentRepo() *pg.EnrichmentRepository {
	return pg.NewEnrichmentRepository(nil)
}

func TestBuildEnrichment_NothingEnabledProducesNothing(t *testing.T) {
	b := buildEnrichment(app.EnrichmentConfig{}, newTestEnrichmentRepo(), newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
	if len(b.Sources) != 0 || len(b.TeamMatchSources) != 0 || len(b.Jobs) != 0 {
		t.Fatalf("expected an empty build, got %+v", b)
	}
}

func TestBuildEnrichment_ValveVRSAloneRegistersOneJobAndSource(t *testing.T) {
	cfg := app.EnrichmentConfig{ValveVRSEnabled: true, ValveVRSSyncInterval: 6}
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
	if len(b.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(b.Jobs))
	}
	if len(b.Sources) != 1 || b.Sources[0] != enrichment.SourceValveVRS {
		t.Fatalf("expected [VALVE_VRS] sources, got %+v", b.Sources)
	}
	if len(b.TeamMatchSources) != 1 || b.TeamMatchSources[0] != enrichment.SourceValveVRS {
		t.Fatalf("expected [VALVE_VRS] team-match sources, got %+v", b.TeamMatchSources)
	}
}

// HLTV enabled registers a *paired* weekly fetch: both an HLTV job and a
// second, independently-locked VALVE_VRS job — even with the free VRS sync
// disabled, VALVE_VRS must still be registered as a source since the paid
// job alone can now produce that data.
func TestBuildEnrichment_HLTVAloneRegistersPairedJobsAndBothSources(t *testing.T) {
	cfg := app.EnrichmentConfig{HLTVEnabled: true, HLTVAPIToken: "tok", ApifyMaxTeams: 100, ApifyRankingCheckInterval: 1}
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
	if len(b.Jobs) != 2 {
		t.Fatalf("expected 2 jobs (HLTV + Apify-VRS), got %d", len(b.Jobs))
	}
	wantSources := map[enrichment.Source]bool{enrichment.SourceValveVRS: true, enrichment.SourceHLTV: true}
	if len(b.Sources) != 2 || !wantSources[b.Sources[0]] || !wantSources[b.Sources[1]] {
		t.Fatalf("expected both VALVE_VRS and HLTV sources, got %+v", b.Sources)
	}
}

func TestBuildEnrichment_BothValveVRSAndHLTVRegisterValveVRSSourceOnlyOnce(t *testing.T) {
	cfg := app.EnrichmentConfig{
		ValveVRSEnabled: true, ValveVRSSyncInterval: 6,
		HLTVEnabled: true, HLTVAPIToken: "tok", ApifyMaxTeams: 100, ApifyRankingCheckInterval: 1,
	}
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
	count := 0
	for _, s := range b.Sources {
		if s == enrichment.SourceValveVRS {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected VALVE_VRS registered exactly once, got %d times in %+v", count, b.Sources)
	}
	if len(b.Jobs) != 3 {
		t.Fatalf("expected 3 jobs (free VRS + HLTV + Apify-VRS), got %d", len(b.Jobs))
	}
}

func TestBuildEnrichment_GRIDAndLiquipediaRegisterIndependently(t *testing.T) {
	cfg := app.EnrichmentConfig{
		GRIDEnabled: true, GRIDAPIKey: "key", GRIDSyncInterval: 3,
		LiquipediaEnabled: true, LiquipediaAPIKey: "key", LiquipediaSyncInterval: 24,
	}
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
	if len(b.Jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(b.Jobs))
	}
	wantSources := map[enrichment.Source]bool{enrichment.SourceGRID: true, enrichment.SourceLiquipedia: true}
	if len(b.Sources) != 2 || !wantSources[b.Sources[0]] || !wantSources[b.Sources[1]] {
		t.Fatalf("expected both GRID and LIQUIPEDIA sources, got %+v", b.Sources)
	}
	// Neither GRID nor Liquipedia feeds TeamMatchService — only ranking
	// sources (VRS/HLTV) resolve team identity.
	if len(b.TeamMatchSources) != 0 {
		t.Fatalf("expected no team-match sources from GRID/Liquipedia, got %+v", b.TeamMatchSources)
	}
}

// The freshness a status screen judges an Apify-gated source against is the
// gate's cadence, never the interval its job is ticked at. ApifyRankingGate
// lets at most one fetch per calendar week through, so reporting the check
// interval (an hour) made a perfectly healthy HLTV read as "overdue by 6
// days" for six days out of every seven.
func TestBuildEnrichment_GatedSourcesReportTheWeeklyCadenceNotTheTick(t *testing.T) {
	cfg := app.EnrichmentConfig{
		HLTVEnabled: true, HLTVAPIToken: "tok", ApifyMaxTeams: 100,
		ApifyRankingCheckInterval: time.Hour,
	}
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())

	if got := b.Intervals[enrichment.SourceHLTV]; got != app.ApifyRankingPeriod {
		t.Fatalf("HLTV cadence = %s, want the gate's %s", got, app.ApifyRankingPeriod)
	}
	// The paid Valve feed is behind the same gate, so with the free one off
	// it is weekly too.
	if got := b.Intervals[enrichment.SourceValveVRS]; got != app.ApifyRankingPeriod {
		t.Fatalf("Apify-only VALVE_VRS cadence = %s, want the gate's %s", got, app.ApifyRankingPeriod)
	}

	// With the free feed also running, VALVE_VRS is as fresh as that one —
	// the faster of the two is what its timestamp should be read against.
	cfg.ValveVRSEnabled, cfg.ValveVRSSyncInterval = true, 15*time.Minute
	withFree := buildEnrichment(cfg, newTestEnrichmentRepo(), newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
	if got := withFree.Intervals[enrichment.SourceValveVRS]; got != 15*time.Minute {
		t.Fatalf("VALVE_VRS cadence with the free feed on = %s, want 15m", got)
	}
	// HLTV has no free counterpart, so it stays weekly either way.
	if got := withFree.Intervals[enrichment.SourceHLTV]; got != app.ApifyRankingPeriod {
		t.Fatalf("HLTV cadence = %s, want the gate's %s", got, app.ApifyRankingPeriod)
	}
}
