package app

import (
	"context"
	"log/slog"
	"time"

	"cs2predictor/internal/domain/competition"
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

// apifyUpdateWeekday/apifyEndOfDayHour describe HLTV's own weekly update
// cadence — confirmed to publish a fresh ranking every Monday — and the
// point in that day this gate treats as "safe to assume today's number is
// final" (HLTV gives no exact publish time). apifyTournamentLookahead is
// how far ahead of a top-tier event's start this gate will fetch early,
// off the Monday schedule, so a poll for a match in an imminent tournament
// isn't stuck with last week's numbers for days.
const (
	apifyUpdateWeekday       = time.Monday
	apifyEndOfDayHour        = 23 // UTC
	apifyTournamentLookahead = 7 * 24 * time.Hour
)

// ApifyRankingGate caps a paid, Apify-backed RankingSync to at most once a
// calendar week (Monday 00:00 UTC through the following Sunday), firing
// only when at least one of these holds:
//   - it's HLTV's own update day (Monday), at or after the end-of-day
//     cutoff above — the plain weekly schedule; or
//   - a top-tier tournament is currently running; or
//   - a top-tier tournament starts within apifyTournamentLookahead.
//
// The last two let a fetch happen early, ahead of the Monday schedule, so
// a tournament starting mid-week (or already running) is never judged
// against a stale ranking — matching the original "before/during a
// tournament" requirement — while the once-a-week cap (checked first,
// before either condition) keeps a run of tournaments from paying for a
// fresh Apify fetch more than once in the same week.
type ApifyRankingGate struct {
	State   enrichment.SyncStateRepository
	Catalog competition.Catalog
	Clock   common.Clock
	Log     *slog.Logger
}

func (g *ApifyRankingGate) ShouldRun(ctx context.Context, source enrichment.Source) (bool, error) {
	now := g.Clock.Now().UTC()
	st, err := g.State.State(ctx, source)
	if err != nil {
		return false, err
	}
	if st.LastSuccessAt != nil && !st.LastSuccessAt.UTC().Before(startOfWeekUTC(now)) {
		return false, nil // already fetched this week
	}
	if now.Weekday() == apifyUpdateWeekday && now.Hour() >= apifyEndOfDayHour {
		return true, nil
	}
	active, err := g.tournamentActive(ctx, now)
	if err != nil {
		return false, err
	}
	return active, nil
}

// tournamentActive reports whether any top-tier event is running, or due
// to start within apifyTournamentLookahead. Mirrors the same
// topTierOnly/UPCOMING+RUNNING filter CompetitionSynchronization's own
// announceBigEvent significance check uses, so "a tournament that matters"
// means the same thing in both places.
func (g *ApifyRankingGate) tournamentActive(ctx context.Context, now time.Time) (bool, error) {
	events, err := g.Catalog.SearchEvents(ctx, "", 1000, true)
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.Status == competition.EventRunning {
			return true, nil
		}
		if e.StartsAt != nil && e.StartsAt.After(now) && e.StartsAt.Before(now.Add(apifyTournamentLookahead)) {
			return true, nil
		}
	}
	return false, nil
}

// startOfWeekUTC returns 00:00 UTC of t's calendar week's Monday — t is
// assumed already UTC. Used to test "already ran this week" against a
// week defined the same way HLTV's own Monday cadence is.
func startOfWeekUTC(t time.Time) time.Time {
	day := t.Truncate(24 * time.Hour)
	daysSinceMonday := (int(day.Weekday()) - int(time.Monday) + 7) % 7
	return day.AddDate(0, 0, -daysSinceMonday)
}
