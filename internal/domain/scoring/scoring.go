// Package scoring holds award calculation, dense ranking, and the
// leaderboard/medal ports.
package scoring

import (
	"context"
	"sort"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

type AwardKind string

const (
	AwardExactScore AwardKind = "EXACT_SCORE"
	AwardOutcome    AwardKind = "OUTCOME"
)

type Award struct {
	ChatID         common.ChatID
	EventID        common.EventID
	MatchID        common.MatchID
	PollID         common.PollID
	UserID         common.UserID
	Points         int
	Kind           AwardKind
	MatchStartedAt time.Time
	AwardedAt      time.Time
}

// StatsPeriod selects the leaderboard time window. Exactly one field
// besides Kind is meaningful, depending on which Kind it is.
type PeriodKind string

const (
	PeriodAllTime PeriodKind = "ALL_TIME"
	PeriodYear    PeriodKind = "YEAR"
	PeriodMonth   PeriodKind = "MONTH"
	PeriodEvent   PeriodKind = "EVENT"
	PeriodDay     PeriodKind = "DAY"
)

type StatsPeriod struct {
	Kind    PeriodKind
	Year    int            // PeriodYear
	Month   time.Month     // PeriodMonth (with Year)
	EventID common.EventID // PeriodEvent
	Day     time.Time      // PeriodDay (date components only)
}

func AllTime() StatsPeriod      { return StatsPeriod{Kind: PeriodAllTime} }
func ForYear(y int) StatsPeriod { return StatsPeriod{Kind: PeriodYear, Year: y} }
func ForMonth(y int, m time.Month) StatsPeriod {
	return StatsPeriod{Kind: PeriodMonth, Year: y, Month: m}
}
func ForEvent(id common.EventID) StatsPeriod { return StatsPeriod{Kind: PeriodEvent, EventID: id} }
func ForDay(d time.Time) StatsPeriod         { return StatsPeriod{Kind: PeriodDay, Day: d} }

type StatsMonth struct {
	Year  int
	Month time.Month
}

type UserStanding struct {
	UserID             common.UserID
	DisplayName        string
	Points             int
	ExactPredictions   int
	CorrectPredictions int
	Predictions        int
	Tournaments        int
	Rank               int
	PreviousRank       *int // not set by domain code, filled in ad hoc at call sites (ResultSettlementService)
	PointsDelta        int  // same as above
}

// accuracyPercent rounds correct/total to the nearest whole percent for a
// compact Telegram UI, returning 0 for an empty sample rather than dividing
// by zero. Every *.AccuracyPercent() method in this package is a thin
// wrapper around this one formula, so a future rounding-rule change only
// has to happen here.
func accuracyPercent(correct, total int) int {
	if total <= 0 {
		return 0
	}
	return (correct*100 + total/2) / total
}

// AccuracyPercent is the share of finished polls in which the user predicted
// the correct match outcome (exact score or just the winner/draw). It is
// intentionally independent of the points awarded for an exact score.
func (s UserStanding) AccuracyPercent() int {
	return accuracyPercent(s.CorrectPredictions, s.Predictions)
}

type MedalCount struct {
	Gold, Silver, Bronze int
}

// UserChatStanding is a compact per-chat summary for the private bot UI.
// Predictions are counted per Telegram poll, so the same person voting in
// different chats contributes one participation in each chat. Tournaments are
// distinct event IDs within that chat.
// Annual insight thresholds are product rules, not persistence details.
// They keep "100% from 1 prediction" and similar tiny-sample anomalies out
// of year-end awards while still letting active community members qualify.
const (
	AnnualSniperMinPredictions   = 20
	AnnualComebackBaselineVotes  = 5
	AnnualComebackMinPredictions = 20
	AnnualTeamMinPredictions     = 5
)

// ComebackBaseline is the user's dense leaderboard rank after their first
// representative sample (AnnualComebackBaselineVotes finished predictions).
// The final year rank is joined in the application layer from Leaderboard.
type ComebackBaseline struct {
	UserID      common.UserID
	DisplayName string
	Rank        int
}

type CorrectStreakInsight struct {
	UserID      common.UserID
	DisplayName string
	Streak      int
}

type TeamSynergyInsight struct {
	UserID      common.UserID
	DisplayName string
	TeamName    string
	Correct     int
	Predictions int
}

func (s TeamSynergyInsight) AccuracyPercent() int {
	return accuracyPercent(s.Correct, s.Predictions)
}

// AnnualSpecials contains year-end metrics that need vote chronology or team
// joins and therefore cannot be derived from a plain leaderboard aggregate.
type AnnualSpecials struct {
	ComebackBaselines []ComebackBaseline
	LongestStreak     *CorrectStreakInsight
	BestTeamSynergy   *TeamSynergyInsight
}

type AnnualInsightsRepository interface {
	AnnualSpecials(ctx context.Context, chatID common.ChatID, year int) (AnnualSpecials, error)
}

type UserChatStanding struct {
	ChatID             common.ChatID
	ChatTitle          string
	Points             int
	CorrectPredictions int
	Predictions        int
	Tournaments        int
}

func (s UserChatStanding) AccuracyPercent() int {
	return accuracyPercent(s.CorrectPredictions, s.Predictions)
}

// PersonalRepository exposes statistics that are scoped by Telegram user
// rather than by a single group chat. It is intentionally separate from
// Repository so existing scoring consumers only depend on the group-oriented
// leaderboard API they need.
type PersonalRepository interface {
	AvailableUserMonths(ctx context.Context, userID common.UserID) ([]StatsMonth, error)
	UserStats(ctx context.Context, userID common.UserID, period StatsPeriod) (*UserStanding, error)
	UserChatStats(ctx context.Context, userID common.UserID) ([]UserChatStanding, error)
}

// LeaderboardMaxParticipants bounds how many distinct participants a
// Leaderboard read returns, ranked best-first, so an old, very large chat's
// all-time leaderboard can't grow into an unbounded aggregate scan re-run on
// every view. Set far above any realistic Telegram group's actual voter
// count (bots/members who never predicted don't count at all) so it only
// ever guards against a pathological case, never clips a real leaderboard —
// unlike InsightsMaxPredictions/UserBetsMaxRows, which bound a single
// person's own history and so can use a much tighter cap.
const LeaderboardMaxParticipants = 5000

// Repository is the scoring/leaderboard persistence port.
type Repository interface {
	// AvailableMonths returns the local calendar months for which this chat has
	// at least one finished match with a recorded prediction. Results are
	// ordered newest first and power the stats period picker.
	AvailableMonths(ctx context.Context, chatID common.ChatID) ([]StatsMonth, error)
	// AvailableEventIDs returns events that actually have finished predictions
	// in this chat, newest first. It is deliberately independent of current
	// subscriptions so historical tournament statistics remain accessible.
	AvailableEventIDs(ctx context.Context, chatID common.ChatID) ([]common.EventID, error)
	ReplaceAwards(ctx context.Context, pollID common.PollID, awards []Award) error
	// Leaderboard returns every participant ranked best-first, up to
	// LeaderboardMaxParticipants of them.
	Leaderboard(ctx context.Context, chatID common.ChatID, period StatsPeriod) ([]UserStanding, error)
	MedalCounts(ctx context.Context, chatID common.ChatID) (map[common.UserID]MedalCount, error)
	AwardMedals(ctx context.Context, chatID common.ChatID, eventID common.EventID, standings []UserStanding, at time.Time) error
	EventCompletionHash(ctx context.Context, chatID common.ChatID, eventID common.EventID) (string, bool, error)
	MarkEventCompleted(ctx context.Context, chatID common.ChatID, eventID common.EventID, resultHash string, at time.Time) error
	// LockEventCompletion serializes concurrent event-completion attempts
	// for the same (chatID, eventID) pair via a transaction-scoped
	// database lock: it must be called as the very first statement inside
	// a transaction (see app.TxRunner), and blocks until any other
	// transaction currently holding the same (chatID, eventID) lock has
	// committed or rolled back. Two different scheduled jobs
	// (DiscoverEvents and SynchronizeMatches) can both reach event
	// completion for the same event, guarded by different cluster locks —
	// without this, both could pass the EventCompletionHash idempotency
	// check before either commits, double-awarding medals and enqueuing
	// two "event finished" notifications.
	LockEventCompletion(ctx context.Context, chatID common.ChatID, eventID common.EventID) error
}

// SettlementRepository is the per-poll settlement idempotency port.
type SettlementRepository interface {
	ResultHash(ctx context.Context, pollID common.PollID) (string, bool, error)
	MarkSettled(ctx context.Context, pollID common.PollID, resultHash string, at time.Time) error
}

// DenseRank sorts rows by points desc, exactPredictions desc, predictions
// desc, userID asc, then assigns a 1-based dense rank: the rank only
// advances when points differ between consecutive rows in that sorted
// order, so equal-points rows always share a rank even though the
// tie-breakers still fix their relative order deterministically.
func DenseRank(rows []UserStanding) []UserStanding {
	sorted := make([]UserStanding, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Points != b.Points {
			return a.Points > b.Points
		}
		if a.ExactPredictions != b.ExactPredictions {
			return a.ExactPredictions > b.ExactPredictions
		}
		if a.Predictions != b.Predictions {
			return a.Predictions > b.Predictions
		}
		return a.UserID.Value < b.UserID.Value
	})

	rank := 0
	var previousPoints *int
	for i := range sorted {
		if previousPoints == nil || sorted[i].Points != *previousPoints {
			rank++
			p := sorted[i].Points
			previousPoints = &p
		}
		sorted[i].Rank = rank
	}
	return sorted
}

// Calculate returns the (points, kind) award for a predicted vs. actual
// score under the given format, or (0, "", false) if the outcome was wrong
// (no award row at all — the vote is simply excluded from scoring).
//
// Test vectors: BO5 exact=3, correct-winner-wrong-score=1,
// wrong-winner=none; FIXED_MAPS(2) exact draw=2;
// FIXED_MAPS(4) predicted 1:1 actual 2:2 (both draws, not exact) = 1.
func Calculate(predicted, actual competition.MatchScore, format competition.SeriesFormat) (points int, kind AwardKind, ok bool) {
	if predicted == actual {
		return format.ExactPoints(), AwardExactScore, true
	}
	if predicted.Outcome() == actual.Outcome() {
		return 1, AwardOutcome, true
	}
	return 0, "", false
}
