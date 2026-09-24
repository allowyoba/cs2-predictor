package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// errInvalidPeriod marks a period selector the app should never have sent
// — this file's own equivalent of errInvalidChatFilter in miniapp_scope.go.
var errInvalidPeriod = errors.New("invalid period")

// The group leaderboard: who ranks where in one chat, for one period.
//
// This is deliberately not another cut of the dashboard or the profile —
// both of those are one person's own record. A leaderboard only means
// anything against a specific field, which is why (unlike every other
// Mini App screen) it requires a chat rather than defaulting to "all of
// them": ranking somebody against the union of every chat they are in
// measures them against people they never actually compete with.
//
// The ranking itself is the same query the bot's own /leaderboard command
// reads (ScoringRepository.Leaderboard) — never duplicated here, so the
// app and the bot can never disagree about who is first.

// MiniAppResults is the read behind the results screen.
type MiniAppResults interface {
	Leaderboard(ctx context.Context, chatID common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error)
}

// resultRowDTO is one participant's place in the table.
type resultRowDTO struct {
	Rank        int    `json:"rank"`
	DisplayName string `json:"display_name"`
	Points      int    `json:"points"`
	Accuracy    int    `json:"accuracy"`
	Exact       int    `json:"exact"`
	Predictions int    `json:"predictions"`
	Tournaments int    `json:"tournaments"`
	// You marks the row belonging to whoever asked, so the app can pin or
	// highlight it without doing its own name matching against a display
	// name — which is not even unique.
	You bool `json:"you,omitempty"`
}

// resultsDTO is the whole leaderboard for one chat and one period.
type resultsDTO struct {
	Period       string `json:"period"`
	Year         int    `json:"year,omitempty"`
	Month        int    `json:"month,omitempty"`
	Chat         int64  `json:"chat"`
	Participants int    `json:"participants"`
	// Predictions is every row's own count added up — the total sample
	// this table is built from, not a second query answering a question
	// the rows already answer.
	Predictions int            `json:"predictions"`
	YourRank    int            `json:"your_rank,omitempty"`
	YourPoints  int            `json:"your_points,omitempty"`
	Rows        []resultRowDTO `json:"rows"`
}

// resultsHandler serves GET /api/miniapp/v1/me/results.
//
// A chat is mandatory: the query string always carries the app's one
// filter bar (scopeFrom), but here an absent chat is a 400 rather than
// "every chat merged into one table", because that table would not
// describe any actual competition.
func resultsHandler(deps MiniAppDeps, results MiniAppResults) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if results == nil {
			http.Error(w, "results are not configured", http.StatusServiceUnavailable)
			return
		}
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if selected.ChatID == nil {
			http.Error(w, "a chat must be selected", http.StatusBadRequest)
			return
		}
		period, err := periodFrom(r, deps.Clock.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		period = period.ForGame(selected.Game)

		standings, err := results.Leaderboard(r.Context(), *selected.ChatID, period)
		if err != nil {
			miniAppError(w, deps.Log, "leaderboard unavailable", err)
			return
		}

		body := resultsDTO{
			Period: string(period.Kind), Chat: selected.ChatID.Value,
			Rows: make([]resultRowDTO, 0, len(standings)),
		}
		if period.Kind == scoring.PeriodYear || period.Kind == scoring.PeriodMonth {
			body.Year = period.Year
		}
		if period.Kind == scoring.PeriodMonth {
			body.Month = int(period.Month)
		}
		for _, standing := range standings {
			row := resultRowDTO{
				Rank: standing.Rank, DisplayName: standing.DisplayName, Points: standing.Points,
				Accuracy: standing.AccuracyPercent(), Exact: standing.ExactPredictions,
				Predictions: standing.Predictions, Tournaments: standing.Tournaments,
			}
			if standing.UserID == user.ID {
				row.You = true
				body.YourRank = standing.Rank
				body.YourPoints = standing.Points
			}
			body.Predictions += standing.Predictions
			body.Rows = append(body.Rows, row)
		}
		body.Participants = len(body.Rows)
		writeJSON(w, body)
	})
}

// periodFrom reads the period selector: all-time (the default), this
// year, or this month, matching scoring.AllTime/ForYear/ForMonth exactly
// — the same three windows the bot's own leaderboard screen offers.
func periodFrom(r *http.Request, now time.Time) (scoring.StatsPeriod, error) {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("period"))) {
	case "", "all":
		return scoring.AllTime(), nil
	case "year":
		year, err := intParam(r, "year", now.Year())
		if err != nil {
			return scoring.StatsPeriod{}, err
		}
		return scoring.ForYear(year), nil
	case "month":
		year, err := intParam(r, "year", now.Year())
		if err != nil {
			return scoring.StatsPeriod{}, err
		}
		month, err := intParam(r, "month", int(now.Month()))
		if err != nil {
			return scoring.StatsPeriod{}, err
		}
		if month < 1 || month > 12 {
			return scoring.StatsPeriod{}, errInvalidPeriod
		}
		return scoring.ForMonth(year, time.Month(month)), nil
	default:
		return scoring.StatsPeriod{}, errInvalidPeriod
	}
}

func intParam(r *http.Request, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errInvalidPeriod
	}
	return value, nil
}
