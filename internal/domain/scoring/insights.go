package scoring

import (
	"context"
	"sort"
	"time"

	"cs2predictor/internal/platform/common"
)

// UserPrediction is one settled vote: which team the person backed, when
// the match was played, and whether they were right. It is the raw
// material for every personal insight below — a flat list rather than
// pre-aggregated numbers, so the interesting parts (streaks, trends,
// which team they read well) stay pure Go that can be tested without a
// database.
//
// TeamID is the grouping key for teamAccuracy — TeamName is display-only.
// Grouping by name would silently merge two different teams that happen to
// share one (not rare among lower-tier orgs) and split one team's history
// across two group entries the moment it's renamed; the id is stable
// identity, the name is just what to print next to it.
type UserPrediction struct {
	PlayedAt time.Time
	TeamID   common.TeamID
	TeamName string
	Correct  bool
}

// TeamAccuracy is how well someone reads one particular team.
type TeamAccuracy struct {
	TeamID      common.TeamID
	TeamName    string
	Correct     int
	Predictions int
}

func (t TeamAccuracy) AccuracyPercent() int {
	return accuracyPercent(t.Correct, t.Predictions)
}

// PeriodSummary is one window's worth of results, used in pairs to show
// whether someone is improving.
type PeriodSummary struct {
	Correct     int
	Predictions int
}

func (p PeriodSummary) AccuracyPercent() int {
	return accuracyPercent(p.Correct, p.Predictions)
}

// PersonalInsights is the "how am I actually doing?" view: the plain
// totals people already get elsewhere answer "how much", these answer
// "how well, lately, and at what".
type PersonalInsights struct {
	// CurrentStreak counts consecutive correct predictions ending at the
	// most recent one; zero when the latest prediction was wrong.
	CurrentStreak int
	LongestStreak int
	// RecentForm is the last InsightsFormSize results, oldest first, so it
	// renders left-to-right the way a form guide reads.
	RecentForm []bool
	// Teams is every team backed at least InsightsTeamMinPredictions
	// times, most accurate first. Below that threshold a percentage says
	// more about luck than about reading the team.
	Teams []TeamAccuracy
	// Recent and Previous are two equal, adjacent windows ending now —
	// the comparison behind the trend arrow. Previous is empty for
	// somebody who only started recently, which is not a decline.
	Recent   PeriodSummary
	Previous PeriodSummary
}

const (
	// InsightsWindow is how far back "recently" reaches; Previous covers
	// the same span immediately before it. A month is long enough to
	// contain a tournament run and short enough to still be news.
	InsightsWindow = 30 * 24 * time.Hour
	// InsightsFormSize bounds the form guide to what reads at a glance.
	InsightsFormSize = 10
	// InsightsTeamMinPredictions is the smallest sample worth quoting a
	// percentage for.
	InsightsTeamMinPredictions = 3
	// InsightsTeamLimit bounds how many teams the screen names.
	InsightsTeamLimit = 5
	// InsightsMaxPredictions bounds how much history is read to build
	// these — enough for a long streak and a full form guide without
	// pulling a heavy user's entire career into memory.
	InsightsMaxPredictions = 500
)

// HasData reports whether there is anything worth rendering. Everything
// below degrades to zero values on an empty history, so this is what
// callers use to show an "no predictions yet" screen instead of a screen
// full of zeroes.
func (i PersonalInsights) HasData() bool { return len(i.RecentForm) > 0 }

// Trend reports the change in accuracy between the two windows, in
// percentage points, and whether the comparison is meaningful at all.
// It isn't, for someone with no predictions in either window or no
// earlier window to compare against.
func (i PersonalInsights) Trend() (deltaPercentagePoints int, comparable bool) {
	if i.Recent.Predictions == 0 || i.Previous.Predictions == 0 {
		return 0, false
	}
	return i.Recent.AccuracyPercent() - i.Previous.AccuracyPercent(), true
}

// BuildPersonalInsights derives every personal insight from one person's
// settled predictions. Input order does not matter: it sorts
// chronologically itself, since "streak" and "recent" are only meaningful
// against a known order and callers shouldn't have to guarantee one.
func BuildPersonalInsights(predictions []UserPrediction, now time.Time) PersonalInsights {
	if len(predictions) == 0 {
		return PersonalInsights{}
	}
	ordered := make([]UserPrediction, len(predictions))
	copy(ordered, predictions)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].PlayedAt.Before(ordered[b].PlayedAt) })

	insights := PersonalInsights{
		CurrentStreak: currentStreak(ordered),
		LongestStreak: longestStreak(ordered),
		RecentForm:    recentForm(ordered),
		Teams:         teamAccuracy(ordered),
	}
	recentFrom := now.Add(-InsightsWindow)
	previousFrom := now.Add(-2 * InsightsWindow)
	for _, p := range ordered {
		switch {
		case !p.PlayedAt.Before(recentFrom):
			insights.Recent.Predictions++
			if p.Correct {
				insights.Recent.Correct++
			}
		case !p.PlayedAt.Before(previousFrom):
			insights.Previous.Predictions++
			if p.Correct {
				insights.Previous.Correct++
			}
		}
	}
	return insights
}

func currentStreak(ordered []UserPrediction) int {
	streak := 0
	for i := len(ordered) - 1; i >= 0 && ordered[i].Correct; i-- {
		streak++
	}
	return streak
}

func longestStreak(ordered []UserPrediction) int {
	longest, running := 0, 0
	for _, p := range ordered {
		if !p.Correct {
			running = 0
			continue
		}
		running++
		if running > longest {
			longest = running
		}
	}
	return longest
}

func recentForm(ordered []UserPrediction) []bool {
	start := max(len(ordered)-InsightsFormSize, 0)
	form := make([]bool, 0, len(ordered)-start)
	for _, p := range ordered[start:] {
		form = append(form, p.Correct)
	}
	return form
}

// teamAccuracy ranks teams by how well the person reads them. Grouped by
// TeamID, not TeamName (see UserPrediction's doc comment) — ties break on
// sample size and then on name, so the same history always renders the
// same order — a list that reshuffles between views reads as broken.
func teamAccuracy(ordered []UserPrediction) []TeamAccuracy {
	totals := map[common.TeamID]*TeamAccuracy{}
	for _, p := range ordered {
		if p.TeamName == "" {
			continue
		}
		entry, ok := totals[p.TeamID]
		if !ok {
			entry = &TeamAccuracy{TeamID: p.TeamID, TeamName: p.TeamName}
			totals[p.TeamID] = entry
		}
		entry.Predictions++
		if p.Correct {
			entry.Correct++
		}
	}

	teams := make([]TeamAccuracy, 0, len(totals))
	for _, entry := range totals {
		if entry.Predictions >= InsightsTeamMinPredictions {
			teams = append(teams, *entry)
		}
	}
	sort.Slice(teams, func(a, b int) bool {
		if pa, pb := teams[a].AccuracyPercent(), teams[b].AccuracyPercent(); pa != pb {
			return pa > pb
		}
		if teams[a].Predictions != teams[b].Predictions {
			return teams[a].Predictions > teams[b].Predictions
		}
		return teams[a].TeamName < teams[b].TeamName
	})
	if len(teams) > InsightsTeamLimit {
		teams = teams[:InsightsTeamLimit]
	}
	return teams
}

// PersonalInsightsRepository reads the vote-level history the insights are
// built from. Separate from PersonalRepository because it is the only port
// that returns individual votes rather than aggregates, and only the DM
// insights screen needs it.
type PersonalInsightsRepository interface {
	// UserPredictions returns the person's most recent settled
	// predictions across every chat, at most limit of them.
	UserPredictions(ctx context.Context, userID common.UserID, limit int) ([]UserPrediction, error)
}
