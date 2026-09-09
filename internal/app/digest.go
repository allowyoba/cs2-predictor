package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

const (
	monthlyReportType = "MONTHLY"
	annualReportType  = "ANNUAL"
)

// ScheduledReportStore persists report idempotency markers. Claim must use
// the transaction carried in ctx when one exists.
type ScheduledReportStore interface {
	// Claimed is a cheap, non-transactional pre-check so callers can skip
	// expensive report computation once a period is already claimed,
	// instead of only discovering that after doing all the work. It is
	// purely an optimization: Claim itself is still the atomic source of
	// truth, so a race between Claimed and Claim can at worst cause one
	// redundant computation, never a duplicate report.
	Claimed(ctx context.Context, chatID common.ChatID, reportType, periodKey string) (bool, error)
	Claim(ctx context.Context, chatID common.ChatID, reportType, periodKey string) (bool, error)
}

// DigestScheduler creates automatic leaderboard digests. Delivery itself goes
// through the transactional outbox, so a Telegram outage cannot lose a report.
type DigestScheduler struct {
	Chats    chat.ActiveChatLister
	Scoring  scoring.Repository
	Insights scoring.AnnualInsightsRepository
	Store    ScheduledReportStore
	Outbox   common.Outbox
	Lock     common.ClusterLock
	Clock    common.Clock
	RunTx    TxRunner
	Log      *slog.Logger
}

func (d *DigestScheduler) Dispatch(ctx context.Context) {
	_, err := d.Lock.Execute(ctx, "cs2predictor:scheduled-digests", func(ctx context.Context) error {
		return d.dispatch(ctx)
	})
	if err != nil {
		d.Log.Error("scheduled digest dispatch failed", "error", err)
	}
}

func (d *DigestScheduler) dispatch(ctx context.Context) error {
	chats, err := d.Chats.ListActive(ctx)
	if err != nil {
		return err
	}

	for _, settings := range chats {
		loc := chat.ZoneOrDefault(settings.Timezone)
		localNow := d.Clock.Now().In(loc)

		annualYear, annualDue := dueAnnualDigest(localNow)
		monthlyYear, monthlyMonth, monthlyDue := dueMonthlyDigest(localNow)

		// December's monthly Top-3 is embedded into the annual Dec-31 digest,
		// so do not send two automatic messages at the same instant.
		if monthlyDue && (!annualDue || monthlyYear != annualYear || monthlyMonth != time.December) {
			if err := d.enqueueMonthly(ctx, settings, monthlyYear, monthlyMonth); err != nil {
				d.Log.Error("monthly digest failed", "chatId", settings.ChatID.Value, "year", monthlyYear, "month", int(monthlyMonth), "error", err)
			}
		}
		if annualDue {
			if err := d.enqueueAnnual(ctx, settings, annualYear); err != nil {
				d.Log.Error("annual digest failed", "chatId", settings.ChatID.Value, "year", annualYear, "error", err)
			}
		}
	}
	return nil
}

// Monthly reports are due from 20:00 local time on the month's last day.
// A six-hour grace window after midnight lets a short deployment/restart
// recover the report without backfilling arbitrarily old months.
func dueMonthlyDigest(local time.Time) (int, time.Month, bool) {
	lastDay := time.Date(local.Year(), local.Month()+1, 0, 0, 0, 0, 0, local.Location()).Day()
	if local.Day() == lastDay && local.Hour() >= 20 {
		return local.Year(), local.Month(), true
	}
	if local.Day() == 1 && local.Hour() < 6 {
		prev := local.AddDate(0, -1, 0)
		return prev.Year(), prev.Month(), true
	}
	return 0, 0, false
}

// The annual report is due at 20:00 local time on Dec 31, with the same
// short recovery window into Jan 1.
func dueAnnualDigest(local time.Time) (int, bool) {
	if local.Month() == time.December && local.Day() == 31 && local.Hour() >= 20 {
		return local.Year(), true
	}
	if local.Month() == time.January && local.Day() == 1 && local.Hour() < 6 {
		return local.Year() - 1, true
	}
	return 0, false
}

// digestTopN is how many standings a digest ever shows — every call site
// wants the top 3, so this stays a constant rather than a parameter until an
// actual second caller needs a different cutoff.
const digestTopN = 3

func digestTop(rows []scoring.UserStanding) []common.DigestStanding {
	n := digestTopN
	if len(rows) < n {
		n = len(rows)
	}
	out := make([]common.DigestStanding, 0, n)
	for _, row := range rows[:n] {
		out = append(out, common.DigestStanding{
			DisplayName: row.DisplayName,
			Rank:        row.Rank,
			Points:      row.Points,
			Predictions: row.Predictions,
			Accuracy:    row.AccuracyPercent(),
		})
	}
	return out
}

func betterAccuracy(a, b scoring.UserStanding) bool {
	// Compare ratios without floating point so ties are deterministic and
	// rounding to a display percentage cannot accidentally change the winner.
	lhs := int64(a.CorrectPredictions) * int64(b.Predictions)
	rhs := int64(b.CorrectPredictions) * int64(a.Predictions)
	if lhs != rhs {
		return lhs > rhs
	}
	if a.CorrectPredictions != b.CorrectPredictions {
		return a.CorrectPredictions > b.CorrectPredictions
	}
	if a.Predictions != b.Predictions {
		return a.Predictions > b.Predictions
	}
	if a.Points != b.Points {
		return a.Points > b.Points
	}
	return a.UserID.Value < b.UserID.Value
}

func buildAnnualHighlights(rows []scoring.UserStanding, specials scoring.AnnualSpecials) common.AnnualHighlightsNotification {
	var out common.AnnualHighlightsNotification
	if len(rows) == 0 {
		return out
	}

	finalByUser := make(map[int64]scoring.UserStanding, len(rows))
	for _, row := range rows {
		finalByUser[row.UserID.Value] = row
	}

	// Comeback: biggest positive climb after the user's first representative
	// sample. Users with fewer than AnnualComebackMinPredictions are already
	// excluded by the repository query.
	bestImprovement := 0
	var bestComeback *scoring.ComebackBaseline
	var bestComebackFinal scoring.UserStanding
	for i := range specials.ComebackBaselines {
		baseline := specials.ComebackBaselines[i]
		final, ok := finalByUser[baseline.UserID.Value]
		if !ok {
			continue
		}
		improvement := baseline.Rank - final.Rank
		if improvement <= 0 {
			continue
		}
		if bestComeback == nil || improvement > bestImprovement ||
			(improvement == bestImprovement && (final.Rank < bestComebackFinal.Rank ||
				(final.Rank == bestComebackFinal.Rank && final.Points > bestComebackFinal.Points) ||
				(final.Rank == bestComebackFinal.Rank && final.Points == bestComebackFinal.Points && final.UserID.Value < bestComebackFinal.UserID.Value))) {
			copyBaseline := baseline
			bestComeback = &copyBaseline
			bestComebackFinal = final
			bestImprovement = improvement
		}
	}
	if bestComeback != nil {
		out.Comeback = &common.AnnualComebackNotification{
			DisplayName: bestComeback.DisplayName,
			StartRank:   bestComeback.Rank,
			FinalRank:   bestComebackFinal.Rank,
		}
	}

	// Sniper: best correctness rate, but only after a meaningful sample.
	var sniper *scoring.UserStanding
	for i := range rows {
		row := rows[i]
		if row.Predictions < scoring.AnnualSniperMinPredictions {
			continue
		}
		if sniper == nil || betterAccuracy(row, *sniper) {
			copyRow := row
			sniper = &copyRow
		}
	}
	if sniper != nil {
		out.Sniper = &common.AnnualUserMetricNotification{
			DisplayName: sniper.DisplayName,
			Value:       sniper.AccuracyPercent(),
			Predictions: sniper.Predictions,
			Accuracy:    sniper.AccuracyPercent(),
		}
	}

	// Expert: the largest absolute number of correctly predicted outcomes.
	var expert *scoring.UserStanding
	for i := range rows {
		row := rows[i]
		if row.CorrectPredictions <= 0 {
			continue
		}
		if expert == nil || row.CorrectPredictions > expert.CorrectPredictions ||
			(row.CorrectPredictions == expert.CorrectPredictions && betterAccuracy(row, *expert)) {
			copyRow := row
			expert = &copyRow
		}
	}
	if expert != nil {
		out.Expert = &common.AnnualUserMetricNotification{
			DisplayName: expert.DisplayName,
			Value:       expert.CorrectPredictions,
			Predictions: expert.Predictions,
			Accuracy:    expert.AccuracyPercent(),
		}
	}

	// Exact: most exact-score predictions. Overall accuracy is a tie-breaker,
	// never the primary criterion for this nomination.
	var exact *scoring.UserStanding
	for i := range rows {
		row := rows[i]
		if row.ExactPredictions <= 0 {
			continue
		}
		if exact == nil || row.ExactPredictions > exact.ExactPredictions ||
			(row.ExactPredictions == exact.ExactPredictions && betterAccuracy(row, *exact)) {
			copyRow := row
			exact = &copyRow
		}
	}
	if exact != nil {
		out.Exact = &common.AnnualUserMetricNotification{
			DisplayName: exact.DisplayName,
			Value:       exact.ExactPredictions,
			Predictions: exact.Predictions,
		}
	}

	if specials.LongestStreak != nil && specials.LongestStreak.Streak > 0 {
		out.Streak = &common.AnnualUserMetricNotification{
			DisplayName: specials.LongestStreak.DisplayName,
			Value:       specials.LongestStreak.Streak,
		}
	}
	if specials.BestTeamSynergy != nil && specials.BestTeamSynergy.Predictions >= scoring.AnnualTeamMinPredictions {
		out.TeamSynergy = &common.AnnualTeamSynergyNotification{
			DisplayName: specials.BestTeamSynergy.DisplayName,
			TeamName:    specials.BestTeamSynergy.TeamName,
			Correct:     specials.BestTeamSynergy.Correct,
			Predictions: specials.BestTeamSynergy.Predictions,
			Accuracy:    specials.BestTeamSynergy.AccuracyPercent(),
		}
	}
	return out
}

func (d *DigestScheduler) enqueueMonthly(ctx context.Context, settings chat.Settings, year int, month time.Month) error {
	periodKey := fmt.Sprintf("%04d-%02d", year, month)
	if claimed, err := d.Store.Claimed(ctx, settings.ChatID, monthlyReportType, periodKey); err != nil || claimed {
		return err
	}
	monthRows, err := d.Scoring.Leaderboard(ctx, settings.ChatID, scoring.ForMonth(year, month))
	if err != nil {
		return err
	}
	// No activity in the month means no unsolicited empty message. We still
	// claim the period below so the scheduler does not reconsider it every minute.
	if len(monthRows) == 0 {
		return d.claim(ctx, settings.ChatID, monthlyReportType, periodKey, "", "")
	}
	yearRows, err := d.Scoring.Leaderboard(ctx, settings.ChatID, scoring.ForYear(year))
	if err != nil {
		return err
	}

	n := common.MonthlyDigestNotification{
		ChatID: settings.ChatID.Value, TopicID: settings.DefaultTopicID,
		Year: year, Month: int(month), MonthStandings: digestTop(monthRows), YearStandings: digestTop(yearRows),
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return d.claim(ctx, settings.ChatID, monthlyReportType, periodKey, "telegram.monthly-digest", string(payload))
}

func (d *DigestScheduler) enqueueAnnual(ctx context.Context, settings chat.Settings, year int) error {
	periodKey := fmt.Sprintf("%04d", year)
	if claimed, err := d.Store.Claimed(ctx, settings.ChatID, annualReportType, periodKey); err != nil || claimed {
		return err
	}
	yearRows, err := d.Scoring.Leaderboard(ctx, settings.ChatID, scoring.ForYear(year))
	if err != nil {
		return err
	}
	if len(yearRows) == 0 {
		return d.claim(ctx, settings.ChatID, annualReportType, periodKey, "", "")
	}
	decemberRows, err := d.Scoring.Leaderboard(ctx, settings.ChatID, scoring.ForMonth(year, time.December))
	if err != nil {
		return err
	}

	predictions := 0
	for _, row := range yearRows {
		predictions += row.Predictions
	}
	var specials scoring.AnnualSpecials
	if d.Insights != nil {
		specials, err = d.Insights.AnnualSpecials(ctx, settings.ChatID, year)
		if err != nil {
			return err
		}
	}
	n := common.AnnualDigestNotification{
		ChatID: settings.ChatID.Value, TopicID: settings.DefaultTopicID, Year: year,
		DecemberStandings: digestTop(decemberRows), YearStandings: digestTop(yearRows),
		Participants: len(yearRows), Predictions: predictions,
		Highlights: buildAnnualHighlights(yearRows, specials),
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return d.claim(ctx, settings.ChatID, annualReportType, periodKey, "telegram.annual-digest", string(payload))
}

func (d *DigestScheduler) claim(ctx context.Context, chatID common.ChatID, reportType, periodKey, eventType, payload string) error {
	return d.RunTx(ctx, func(txCtx context.Context) error {
		claimed, err := d.Store.Claim(txCtx, chatID, reportType, periodKey)
		if err != nil || !claimed {
			return err
		}
		if eventType == "" {
			return nil
		}
		_, err = d.Outbox.Enqueue(txCtx, "TELEGRAM_CHAT", fmt.Sprintf("%d:%s:%s", chatID.Value, reportType, periodKey), eventType, payload)
		return err
	})
}
