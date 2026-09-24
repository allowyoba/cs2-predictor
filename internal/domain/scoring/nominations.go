package scoring

import (
	"sort"
	"time"

	"cs2predictor/internal/platform/common"
)

// Requirement (d): a small set of derived "nominations" over a historical
// period, built only from data already tracked elsewhere in this package
// (UserPrediction) plus two thin extra facts a caller already has lying
// around (chat activity counts, and an upset's underdog odds) — nothing
// here needs new tracking of its own.

// ChatActivity is one chat's raw prediction volume within a period — the
// input to MostActiveChat. Built by the caller from whatever repository
// already counts predictions per chat (e.g. scoring.Repository.Leaderboard
// summed, or a dedicated count query); kept a plain struct here so this
// package stays free of any repository dependency.
type ChatActivity struct {
	ChatID      common.ChatID
	ChatTitle   string
	Predictions int
}

// MostActiveChat picks the chat with the most settled predictions in the
// period — "which room actually plays" is the simplest and most durable
// activity signal already on hand, needing nothing beyond a prediction
// count per chat. Ties break on chat id so the same input always picks the
// same winner. Returns false for an empty period.
func MostActiveChat(activity []ChatActivity) (ChatActivity, bool) {
	if len(activity) == 0 {
		return ChatActivity{}, false
	}
	best := activity[0]
	for _, a := range activity[1:] {
		if a.Predictions > best.Predictions || (a.Predictions == best.Predictions && a.ChatID.Value < best.ChatID.Value) {
			best = a
		}
	}
	if best.Predictions == 0 {
		return ChatActivity{}, false
	}
	return best, true
}

// BestTeamWinRate finds whoever read one specific team best within a
// period — "best win-rate on a subscribed team's matches this month" from
// the brief. It is TeamAccuracyOf (already built for personal insights)
// filtered to one team and re-ranked by accuracy alone, since a
// nomination cares about who read the team best, not who bet on it most.
// InsightsTeamMinPredictions still applies: a 1-for-1 record is luck, not
// a nomination.
func BestTeamWinRate(predictions []UserPrediction, teamID common.TeamID) (TeamAccuracy, bool) {
	var forTeam []UserPrediction
	for _, p := range predictions {
		if p.TeamID == teamID {
			forTeam = append(forTeam, p)
		}
	}
	best := TeamAccuracy{}
	found := false
	for _, entry := range TeamAccuracyOf(forTeam) {
		if entry.TeamID != teamID {
			continue
		}
		if !found || entry.AccuracyPercent() > best.AccuracyPercent() {
			best = entry
			found = true
		}
	}
	return best, found
}

// LongestCorrectStreakAward is who is currently riding the longest active
// correct-prediction streak, and how long it is — the leaderboard-wide
// counterpart of PersonalInsights.CurrentStreak, which only ever looks at
// one person.
type LongestCorrectStreakAward struct {
	UserID      common.UserID
	DisplayName string
	Streak      int
}

// LongestCorrectStreak scores everyone's currently-live streak (from
// BuildPersonalInsights.CurrentStreak, i.e. "correct predictions in a row
// ending right now", not a past streak that has since been broken) and
// names whoever's is longest. now is passed through unchanged so the
// window this runs over matches whatever period the caller already
// filtered predictions to.
func LongestCorrectStreak(byUser map[common.UserID]struct {
	DisplayName string
	Predictions []UserPrediction
}, now time.Time) (LongestCorrectStreakAward, bool) {
	var best LongestCorrectStreakAward
	found := false
	// Deterministic order: Go map iteration is randomized, and a
	// nomination that could name a different tied winner on every call
	// would be useless for a "screenshot this" moment.
	ids := make([]common.UserID, 0, len(byUser))
	for id := range byUser {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Value < ids[j].Value })
	for _, id := range ids {
		entry := byUser[id]
		streak := currentStreak(sortedByPlayedAt(entry.Predictions))
		if streak == 0 {
			continue
		}
		if !found || streak > best.Streak {
			best = LongestCorrectStreakAward{UserID: id, DisplayName: entry.DisplayName, Streak: streak}
			found = true
		}
	}
	return best, found
}

func sortedByPlayedAt(predictions []UserPrediction) []UserPrediction {
	ordered := make([]UserPrediction, len(predictions))
	copy(ordered, predictions)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].PlayedAt.Before(ordered[b].PlayedAt) })
	return ordered
}

// UpsetCall is one correctly-predicted match where the backed team was the
// underdog — the raw material for BiggestUpsetCalled. UnderdogRankGap is
// how many ranking places worse the backed team was (favorite's rank minus
// underdog's rank); the caller computes it from whatever ranking feed it
// already has, since this package tracks neither team rankings nor odds.
type UpsetCall struct {
	UserID           common.UserID
	DisplayName      string
	MatchDescription string
	UnderdogRankGap  int
	PlayedAt         time.Time
}

// BiggestUpsetCalled picks the largest rank gap among correctly-called
// underdogs in the period — "biggest upset correctly called" from the
// brief. Ties break on the earlier call, so an upset is credited to
// whoever actually called it first rather than to a later coincidence of
// the same gap.
func BiggestUpsetCalled(calls []UpsetCall) (UpsetCall, bool) {
	if len(calls) == 0 {
		return UpsetCall{}, false
	}
	best := calls[0]
	for _, c := range calls[1:] {
		if c.UnderdogRankGap > best.UnderdogRankGap ||
			(c.UnderdogRankGap == best.UnderdogRankGap && c.PlayedAt.Before(best.PlayedAt)) {
			best = c
		}
	}
	if best.UnderdogRankGap <= 0 {
		return UpsetCall{}, false
	}
	return best, true
}
