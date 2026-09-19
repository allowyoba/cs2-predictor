package app

import (
	"context"
	"time"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// RankingSyncGate decides whether a RankingSync's Dispatch should actually
// fetch on this tick — see RankingSync.Gate. Ticking more often than the
// gate allows is fine (and expected): the gate, not the schedule, decides
// when a paid source is worth calling.
type RankingSyncGate interface {
	ShouldRun(ctx context.Context, source enrichment.Source) (bool, error)
}

// apifyEndOfDayHour is how far into HLTV's own update day (Monday) this
// gate waits before treating that day's ranking as final — HLTV gives no
// exact publish time.
const apifyEndOfDayHour = 23 // UTC

// ApifyRankingGate caps a paid, Apify-backed RankingSync to at most one
// fetch per calendar week: it opens at the Monday end-of-day cutoff above
// and stays open for the rest of that week until this job's own run has
// actually been collected. Deliberately simple and tournament-independent —
// HLTV only republishes weekly regardless, so a mid-week fetch could still
// only ever return the same Monday numbers.
//
// The window is the whole rest of the week rather than Monday's last hour
// alone because a single failed attempt used to cost seven days of stale
// rankings: the hourly tick that hit the cutoff was, in practice, the only
// attempt the week ever got. Staying open costs nothing extra — the run
// itself is what's billed, and the provider refuses to start a second one
// for work already done or under way (see enrichment.ProviderRun).
//
// "Already ran this week" is answered from that same ProviderRun and not
// from SyncState, because a Source can have two jobs writing it: the paid
// Apify Valve feed shares enrichment.SourceValveVRS with the free, frequent
// valvevrs one, whose successes kept this gate shut every week of its
// existence — the paid Valve ranking was never once fetched.
type ApifyRankingGate struct {
	Runs enrichment.ProviderRunRepository
	// Key is the actor's ranking mode ("hltv"/"valve"), matching the Key
	// the provider records its run under.
	Key   string
	Clock common.Clock
}

func (g *ApifyRankingGate) ShouldRun(ctx context.Context, source enrichment.Source) (bool, error) {
	now := g.Clock.Now().UTC()
	weekStart := common.StartOfWeekUTC(now)
	run, err := g.Runs.Run(ctx, source, g.Key)
	if err != nil {
		return false, err
	}
	if run != nil && run.Status == enrichment.RunStatusCollected && !run.PeriodStart.Before(weekStart) {
		return false, nil // this week's ranking is already in
	}
	return !now.Before(weekStart.Add(apifyEndOfDayHour * time.Hour)), nil
}
