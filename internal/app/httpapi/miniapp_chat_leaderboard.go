package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cs2predictor/internal/domain/scoring"
)

// The Mini App's cross-chat leaderboard: which chat is collectively best at
// predicting, not who inside it. Like the DM screen, it only ever carries
// chat-level totals (scoring.ChatStanding) — no member list — which is what
// makes it safe to serve to any authenticated app user regardless of which
// chats they themselves are in.

// MiniAppChatLeaderboard reads the cross-chat aggregate.
type MiniAppChatLeaderboard interface {
	ChatLeaderboard(ctx context.Context, period scoring.StatsPeriod) ([]scoring.ChatStanding, error)
}

type chatStandingRowDTO struct {
	ID           int64  `json:"id"`
	Chat         string `json:"chat"`
	Rank         int    `json:"rank"`
	Points       int    `json:"points"`
	Accuracy     int    `json:"accuracy"`
	Predictions  int    `json:"predictions"`
	Participants int    `json:"participants"`
}

type chatLeaderboardDTO struct {
	Period string               `json:"period"`
	Game   string               `json:"game,omitempty"`
	Count  int                  `json:"count"`
	Chats  []chatStandingRowDTO `json:"chats"`
}

// chatLeaderboardPeriodFromQuery maps the "period" query parameter to a
// scoring.StatsPeriod, mirroring the bot DM screen's own three cuts
// (all-time/year/month) — there is no single chat's calendar to anchor a
// finer picker on across every chat at once.
func chatLeaderboardPeriodFromQuery(r *http.Request, now time.Time) (scoring.StatsPeriod, string) {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("period"))) {
	case "year":
		return scoring.ForYear(now.Year()), "year"
	case "month":
		return scoring.ForMonth(now.Year(), now.Month()), "month"
	default:
		return scoring.AllTime(), "all"
	}
}

// chatLeaderboardHandler serves GET /api/miniapp/v1/chats/leaderboard.
func chatLeaderboardHandler(deps MiniAppDeps, repo MiniAppChatLeaderboard) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, _ authenticatedUser) {
		if repo == nil {
			http.Error(w, "chat leaderboard is not configured", http.StatusServiceUnavailable)
			return
		}
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		period, periodLabel := chatLeaderboardPeriodFromQuery(r, deps.Clock.Now())
		period = period.ForGame(selected.Game)

		standings, err := repo.ChatLeaderboard(r.Context(), period)
		if err != nil {
			miniAppError(w, deps.Log, "chat leaderboard unavailable", err)
			return
		}

		body := chatLeaderboardDTO{Period: periodLabel, Game: string(selected.Game), Chats: make([]chatStandingRowDTO, 0, len(standings))}
		for _, s := range standings {
			body.Chats = append(body.Chats, chatStandingRowDTO{
				ID: s.ChatID.Value, Chat: s.ChatTitle, Rank: s.Rank, Points: s.Points,
				Accuracy: s.AccuracyPercent(), Predictions: s.Predictions, Participants: s.Participants,
			})
		}
		body.Count = len(body.Chats)
		writeJSON(w, body)
	})
}
