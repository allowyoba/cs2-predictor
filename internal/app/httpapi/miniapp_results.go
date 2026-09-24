package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The Mini App's period/chat results screen: the same question the bot's
// own "Результаты" DM menu answers (how did I do this month, this year, or
// ever, and in which chat), but with a real delta against the previous
// window and a per-chat breakdown for that same window — neither of which
// a Telegram DM message has room for.
//
// Every figure here is scoring.UserStats or scoring.UserChatStats read
// again with a different scoring.StatsPeriod; nothing is estimated, and a
// chat with no activity in the chosen window is simply absent from the
// list rather than shown at zero.

// periodOptionDTO is one entry the picker can choose: a month, or a bare
// year (derived from the same months). Kept in the order
// AvailableUserMonths already returns them in — most recent first.
type periodOptionDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// resultsChatDTO is one chat's own record for the selected period —
// UserChatStanding narrowed to a period rather than all-time, which is why
// it is not the same type the achievements screen uses.
type resultsChatDTO struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Predictions int    `json:"predictions"`
	Accuracy    int    `json:"accuracy"`
	Points      int    `json:"points"`
}

type resultsDTO struct {
	Period struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	} `json:"period"`
	Summary summaryDTO `json:"summary"`
	// Previous is the immediately preceding window of the same length (the
	// prior month for a month, the prior year for a year), and is absent
	// for all-time — there is no window before "ever" — and for a period
	// the person had no earlier one to compare against.
	Previous *summaryDTO `json:"previous,omitempty"`
	DeltaPP  *int        `json:"delta_pp,omitempty"`
	// Chats is this same period narrowed to each chat the person plays in,
	// one call to UserStats per chat — cheap, since nobody plays in more
	// than a handful. A chat absent here had no activity in this window.
	Chats []resultsChatDTO `json:"chats"`
	// AvailablePeriods lists every month with at least one vote, most
	// recent first, plus the distinct years derived from them — the same
	// pair of granularities the bot's own period menu offers.
	AvailableMonths []periodOptionDTO `json:"available_months"`
	AvailableYears  []periodOptionDTO `json:"available_years"`
}

// resultsHandler serves GET /api/miniapp/v1/me/results?period=...&chat=...
func resultsHandler(deps MiniAppDeps) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		period, err := periodFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		stats, err := deps.Stats.UserStats(r.Context(), user.ID, selected.period(period))
		if err != nil {
			miniAppError(w, deps.Log, "statistics unavailable", err)
			return
		}

		var body resultsDTO
		body.Period.Key, body.Period.Label = periodKeyAndLabel(period)
		if stats != nil {
			body.Summary = summaryDTO{
				Predictions: stats.Predictions, Correct: stats.CorrectPredictions,
				Exact: stats.ExactPredictions, Accuracy: stats.AccuracyPercent(),
				Points: stats.Points, Tournaments: stats.Tournaments,
			}
		}
		if previous, ok := previousPeriod(period); ok {
			if prevStats, err := deps.Stats.UserStats(r.Context(), user.ID, selected.period(previous)); err == nil && prevStats != nil && prevStats.Predictions > 0 {
				summary := summaryDTO{
					Predictions: prevStats.Predictions, Correct: prevStats.CorrectPredictions,
					Exact: prevStats.ExactPredictions, Accuracy: prevStats.AccuracyPercent(),
					Points: prevStats.Points, Tournaments: prevStats.Tournaments,
				}
				body.Previous = &summary
				delta := body.Summary.Accuracy - summary.Accuracy
				body.DeltaPP = &delta
			}
		}
		body.Chats = resultsChatBreakdown(r.Context(), deps, user.ID, selected, period)
		months, err := deps.Stats.AvailableUserMonths(r.Context(), user.ID)
		if err == nil {
			body.AvailableMonths, body.AvailableYears = availablePeriods(months)
		} else {
			deps.Log.Error("available months unavailable", "error", err)
		}
		writeJSON(w, body)
	})
}

// resultsChatBreakdown reads every chat the person plays in and narrows
// each to the selected period, dropping any chat with nothing in it — a
// chat is only ever shown for a window it actually has data in.
func resultsChatBreakdown(ctx context.Context, deps MiniAppDeps, userID common.UserID, selected scope, period scoring.StatsPeriod) []resultsChatDTO {
	chats, err := deps.Stats.UserChatStats(ctx, userID)
	if err != nil {
		if deps.Log != nil {
			deps.Log.Warn("chat breakdown unavailable", "error", err)
		}
		return nil
	}
	out := make([]resultsChatDTO, 0, len(chats))
	for _, c := range chats {
		if !selected.keepsChat(c.ChatID) {
			continue
		}
		stats, err := deps.Stats.UserStats(ctx, userID, selected.period(period).ForChat(&c.ChatID))
		if err != nil || stats == nil || stats.Predictions == 0 {
			continue
		}
		out = append(out, resultsChatDTO{
			ID: c.ChatID.Value, Title: c.ChatTitle,
			Predictions: stats.Predictions, Accuracy: stats.AccuracyPercent(), Points: stats.Points,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Points > out[j].Points })
	return out
}

// previousPeriod is the immediately preceding window of the same shape.
// ok=false for all-time (nothing comes before it) and for anything that
// is not a month or a year — this screen only ever offers those two.
func previousPeriod(p scoring.StatsPeriod) (scoring.StatsPeriod, bool) {
	switch p.Kind {
	case scoring.PeriodMonth:
		year, month := p.Year, p.Month-1
		if month < 1 {
			month = 12
			year--
		}
		return scoring.ForMonth(year, month), true
	case scoring.PeriodYear:
		return scoring.ForYear(p.Year - 1), true
	default:
		return scoring.StatsPeriod{}, false
	}
}

// periodKeyAndLabel renders the same key the client sent (round-tripped so
// the UI can tell which button is active) and a human label for the
// period's own heading.
func periodKeyAndLabel(p scoring.StatsPeriod) (key, label string) {
	switch p.Kind {
	case scoring.PeriodMonth:
		return fmt.Sprintf("month:%04d-%02d", p.Year, int(p.Month)), fmt.Sprintf("%s %d", ruMonthName(p.Month), p.Year)
	case scoring.PeriodYear:
		return fmt.Sprintf("year:%d", p.Year), fmt.Sprintf("%d", p.Year)
	default:
		return "all", "За всё время"
	}
}

// availablePeriods turns the flat list of months with data into the two
// pickers the screen offers: every month, and the distinct years behind
// them, both most-recent-first (AvailableUserMonths' own order).
func availablePeriods(months []scoring.StatsMonth) (byMonth, byYear []periodOptionDTO) {
	seenYear := map[int]bool{}
	byMonth = make([]periodOptionDTO, 0, len(months))
	for _, m := range months {
		byMonth = append(byMonth, periodOptionDTO{
			Key: fmt.Sprintf("month:%04d-%02d", m.Year, int(m.Month)), Label: fmt.Sprintf("%s %d", ruMonthName(m.Month), m.Year),
		})
		if !seenYear[m.Year] {
			seenYear[m.Year] = true
			byYear = append(byYear, periodOptionDTO{Key: fmt.Sprintf("year:%d", m.Year), Label: fmt.Sprintf("%d", m.Year)})
		}
	}
	return byMonth, byYear
}

var ruMonths = [...]string{"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
	"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}

// ruMonthName gives a Russian month name — the whole app is Russian
// labelled, matching the bot's own DM text, unlike time.Month's English
// String().
func ruMonthName(m time.Month) string {
	if m < 1 || int(m) > len(ruMonths) {
		return m.String()
	}
	return ruMonths[m-1]
}
