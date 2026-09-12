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

// apifyUpdateWeekday/apifyEndOfDayHour describe HLTV's own weekly update
// cadence — confirmed to publish a fresh ranking every Monday — and the
// point in that day this gate treats as "safe to assume today's number is
// final" (HLTV gives no exact publish time).
const (
	apifyUpdateWeekday = time.Monday
	apifyEndOfDayHour  = 23 // UTC
)

// ApifyRankingGate caps a paid, Apify-backed RankingSync to at most once a
// calendar week (Monday 00:00 UTC through the following Sunday): it fires
// only on HLTV's own update day (Monday), at or after the end-of-day cutoff
// above, and only if this source hasn't already succeeded since that
// week began. Deliberately simple and tournament-independent — an earlier
// version also fetched early for an imminent/running tournament, but that
// doubled the number of trigger conditions to reason about for a benefit
// that wasn't worth the complexity: HLTV only republishes weekly regardless,
// so a mid-week fetch could still only ever return the same Monday numbers.
type ApifyRankingGate struct {
	State enrichment.SyncStateRepository
	Clock common.Clock
}

func (g *ApifyRankingGate) ShouldRun(ctx context.Context, source enrichment.Source) (bool, error) {
	now := g.Clock.Now().UTC()
	st, err := g.State.State(ctx, source)
	if err != nil {
		return false, err
	}
	if st.LastSuccessAt != nil && !st.LastSuccessAt.UTC().Before(startOfWeekUTC(now)) {
		return false, nil // already fetched this week — covers the restart/redeploy case too, since Dispatch runs immediately on startup
	}
	return now.Weekday() == apifyUpdateWeekday && now.Hour() >= apifyEndOfDayHour, nil
}

// startOfWeekUTC returns 00:00 UTC of t's calendar week's Monday — t is
// assumed already UTC. Used to test "already ran this week" against a
// week defined the same way HLTV's own Monday cadence is.
func startOfWeekUTC(t time.Time) time.Time {
	day := t.Truncate(24 * time.Hour)
	daysSinceMonday := (int(day.Weekday()) - int(time.Monday) + 7) % 7
	return day.AddDate(0, 0, -daysSinceMonday)
}
