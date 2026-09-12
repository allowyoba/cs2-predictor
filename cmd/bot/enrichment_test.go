package main

import (
	"log/slog"
	"testing"

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
	b := buildEnrichment(app.EnrichmentConfig{}, newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
	if len(b.Sources) != 0 || len(b.TeamMatchSources) != 0 || len(b.Jobs) != 0 {
		t.Fatalf("expected an empty build, got %+v", b)
	}
}

func TestBuildEnrichment_ValveVRSAloneRegistersOneJobAndSource(t *testing.T) {
	cfg := app.EnrichmentConfig{ValveVRSEnabled: true, ValveVRSSyncInterval: 6}
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
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
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
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
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
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
	b := buildEnrichment(cfg, newTestEnrichmentRepo(), nil, nil, nil, common.SystemUTCClock(), nil, slog.Default())
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
