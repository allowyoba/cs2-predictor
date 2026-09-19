package scoring

import (
	"context"
	"sort"

	"cs2predictor/internal/platform/common"
)

// Nomination keys. Stable: they travel through the outbox payload and the
// publisher maps them to localized titles.
const (
	// AwardUnderdog goes to whoever most often backed the side the rest of
	// the chat did not — and was right.
	AwardUnderdog = "underdog"
	// AwardLoneVoice is the rarer cousin of AwardUnderdog: polls where
	// exactly one person called it correctly, and it was them.
	AwardLoneVoice = "lone_voice"
	// AwardStreak is the longest unbroken run of correct calls.
	AwardStreak = "streak"
	// AwardExact is the most exact scorelines.
	AwardExact = "exact"
	// AwardFlawless is a perfect record over a meaningful sample.
	AwardFlawless = "flawless"
	// AwardSniper is the best accuracy that is not perfect.
	AwardSniper = "sniper"
	// AwardIronman is voting on every single match of the tournament.
	AwardIronman = "ironman"
	// AwardVolume is the most predictions made — the filler that keeps a
	// quiet tournament from producing an empty section.
	AwardVolume = "volume"
)

// Minimum samples. A nomination handed out on one lucky pick is not an
// accolade, it is noise — each threshold is the point at which the number
// starts to mean something.
const (
	MinUnderdogWins    = 2
	MinLoneVoice       = 1
	MinAwardStreak     = 3
	MinExactScores     = 2
	MinFlawlessSample  = 4
	MinSniperSample    = 5
	MinIronmanPolls    = 5
	MinVolumeSample    = 5
	DefaultAwardsShown = 3
)

// EventAward is one nomination: who won it and the number that earned it.
type EventAward struct {
	Kind        string
	UserID      common.UserID
	DisplayName string
	Value       int
	// Detail is the second number a nomination needs — the sample behind
	// an accuracy, the poll count behind a perfect attendance. Zero when
	// the title needs no context.
	Detail int
}

// EventUserSpecials holds the per-user numbers a tournament recap needs
// that a plain leaderboard aggregate cannot answer: they need the chat's
// other votes on the same poll, or the chronology of one person's calls.
type EventUserSpecials struct {
	UserID      common.UserID
	DisplayName string
	// ContrarianWins counts correct calls that went against the chat's
	// majority on that poll.
	ContrarianWins int
	// LoneCorrect counts polls where this person was the only one right.
	LoneCorrect int
	// VotedPolls is how many of the tournament's polls they voted in.
	VotedPolls int
	// LongestStreak is their longest run of consecutive correct calls
	// within the tournament.
	LongestStreak int
}

// EventSpecials is the whole tournament's worth of the above, plus the
// poll count that makes "voted in every match" answerable.
type EventSpecials struct {
	TotalPolls int
	Users      []EventUserSpecials
}

// EventSpecialsRepository computes EventSpecials for one chat's run of one
// tournament.
type EventSpecialsRepository interface {
	EventSpecials(ctx context.Context, chatID common.ChatID, eventID common.EventID) (EventSpecials, error)
}

// PickEventAwards chooses which nominations a tournament actually gets.
//
// The pool is deliberately larger than the number shown: a tournament where
// somebody went 9-for-9 deserves a different headline from one decided by a
// single contrarian call, and handing out the same three titles every time
// is how a recap stops being read. Each candidate carries a strength — how
// unusual the achievement is, not just how big the number — and the
// strongest few win, with the pool's own order breaking ties so the result
// is deterministic.
func PickEventAwards(standings []UserStanding, specials EventSpecials, limit int) []EventAward {
	if limit <= 0 {
		limit = DefaultAwardsShown
	}
	type candidate struct {
		award    EventAward
		strength int
		priority int
	}
	// Strength is "how unusual is this", on one shared scale, so the
	// nominations can be compared against each other at all: being the only
	// person to call a match outranks a good accuracy, and a good accuracy
	// outranks simply having predicted a lot.
	var candidates []candidate
	add := func(priority, strength int, award EventAward) {
		if award.DisplayName == "" || strength <= 0 {
			return
		}
		candidates = append(candidates, candidate{award: award, strength: strength, priority: priority})
	}

	collectCrowdAwards(specials, add)
	collectStandingAwards(standings, add)

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].strength != candidates[j].strength {
			return candidates[i].strength > candidates[j].strength
		}
		return candidates[i].priority < candidates[j].priority
	})

	// One person is allowed at most one title: three nominations for the
	// same runaway winner reads as padding, not as a recap.
	var out []EventAward
	seen := map[common.UserID]bool{}
	for _, c := range candidates {
		if seen[c.award.UserID] {
			continue
		}
		seen[c.award.UserID] = true
		out = append(out, c.award)
		if len(out) == limit {
			break
		}
	}
	return out
}

// awardCollector receives one candidate nomination: its position in the
// pool (the tie-break), how unusual it is, and the award itself.
type awardCollector func(priority, strength int, award EventAward)

// best returns the strongest row for one nomination: the first row that
// qualifies, then whatever beats it. Every nomination is that same shape,
// so it is written once rather than eight times.
func best[T any](rows []T, eligible func(T) bool, better func(candidate, current T) bool) *T {
	var out *T
	for i := range rows {
		if !eligible(rows[i]) {
			continue
		}
		if out == nil || better(rows[i], *out) {
			out = &rows[i]
		}
	}
	return out
}

// collectCrowdAwards covers the nominations that exist only relative to the
// rest of the chat, or to the chronology of somebody's own calls.
func collectCrowdAwards(specials EventSpecials, add awardCollector) {
	users := specials.Users
	if u := best(users,
		func(u EventUserSpecials) bool { return u.ContrarianWins >= MinUnderdogWins },
		func(c, cur EventUserSpecials) bool { return c.ContrarianWins > cur.ContrarianWins }); u != nil {
		add(0, u.ContrarianWins*2, EventAward{
			Kind: AwardUnderdog, UserID: u.UserID, DisplayName: u.DisplayName, Value: u.ContrarianWins,
		})
	}
	// Weighted highest: being the only one who saw it coming is the rarest
	// thing that happens in a prediction chat.
	if u := best(users,
		func(u EventUserSpecials) bool { return u.LoneCorrect >= MinLoneVoice },
		func(c, cur EventUserSpecials) bool { return c.LoneCorrect > cur.LoneCorrect }); u != nil {
		add(1, u.LoneCorrect*5, EventAward{
			Kind: AwardLoneVoice, UserID: u.UserID, DisplayName: u.DisplayName, Value: u.LoneCorrect,
		})
	}
	if u := best(users,
		func(u EventUserSpecials) bool { return u.LongestStreak >= MinAwardStreak },
		func(c, cur EventUserSpecials) bool { return c.LongestStreak > cur.LongestStreak }); u != nil {
		add(2, u.LongestStreak*3/2, EventAward{
			Kind: AwardStreak, UserID: u.UserID, DisplayName: u.DisplayName, Value: u.LongestStreak,
		})
	}
	if u := best(users,
		func(u EventUserSpecials) bool {
			return specials.TotalPolls >= MinIronmanPolls && u.VotedPolls == specials.TotalPolls
		},
		func(c, cur EventUserSpecials) bool { return c.VotedPolls > cur.VotedPolls }); u != nil {
		add(6, specials.TotalPolls/3, EventAward{
			Kind: AwardIronman, UserID: u.UserID, DisplayName: u.DisplayName, Value: u.VotedPolls,
		})
	}
}

// collectStandingAwards covers the nominations a plain tournament
// leaderboard already answers.
func collectStandingAwards(standings []UserStanding, add awardCollector) {
	if row := best(standings,
		func(r UserStanding) bool { return r.ExactPredictions >= MinExactScores },
		func(c, cur UserStanding) bool { return c.ExactPredictions > cur.ExactPredictions }); row != nil {
		add(3, row.ExactPredictions*2, EventAward{
			Kind: AwardExact, UserID: row.UserID, DisplayName: row.DisplayName, Value: row.ExactPredictions,
		})
	}
	if row := best(standings,
		func(r UserStanding) bool {
			return r.Predictions >= MinFlawlessSample && r.CorrectPredictions == r.Predictions
		},
		func(c, cur UserStanding) bool { return c.Predictions > cur.Predictions }); row != nil {
		add(4, row.Predictions+2, EventAward{
			Kind: AwardFlawless, UserID: row.UserID, DisplayName: row.DisplayName, Value: row.Predictions,
		})
	}
	// A perfect record is its own nomination above, so the accuracy title
	// goes to the best imperfect one. Measured from a coin flip rather than
	// from zero: 55% is not an achievement, 90% is.
	if row := best(standings,
		func(r UserStanding) bool {
			return r.Predictions >= MinSniperSample && r.CorrectPredictions < r.Predictions
		},
		betterEventAccuracy); row != nil {
		add(5, (row.AccuracyPercent()-50)/5, EventAward{
			Kind: AwardSniper, UserID: row.UserID, DisplayName: row.DisplayName,
			Value: row.AccuracyPercent(), Detail: row.Predictions,
		})
	}
	if row := best(standings,
		func(r UserStanding) bool { return r.Predictions >= MinVolumeSample },
		func(c, cur UserStanding) bool { return c.Predictions > cur.Predictions }); row != nil {
		add(7, row.Predictions/4, EventAward{
			Kind: AwardVolume, UserID: row.UserID, DisplayName: row.DisplayName, Value: row.Predictions,
		})
	}
}

// betterEventAccuracy prefers the higher accuracy and, at equal accuracy,
// the larger sample — the same ordering the annual digest applies.
func betterEventAccuracy(a, b UserStanding) bool {
	if a.AccuracyPercent() != b.AccuracyPercent() {
		return a.AccuracyPercent() > b.AccuracyPercent()
	}
	return a.Predictions > b.Predictions
}
