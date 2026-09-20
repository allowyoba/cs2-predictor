package httpapi

import (
	"context"
	"net/http"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// Two reads that are not about the past.
//
// Everything else the app shows is settled history. These are the parts
// somebody opens the app for on a match day — what they have riding right
// now — and the record of what they have won, which belongs to a chat
// rather than to a person in the abstract: finishing first among four
// friends and first among forty are different achievements, and merging
// them into one number tells you neither.

// MiniAppActive is the read behind both.
type MiniAppActive interface {
	ActivePredictions(ctx context.Context, userID common.UserID, limit int) ([]scoring.ActivePrediction, error)
	UserMedals(ctx context.Context, userID common.UserID) ([]scoring.MedalTally, error)
	UserChatStats(ctx context.Context, userID common.UserID) ([]scoring.UserChatStanding, error)
}

type activeEntryDTO struct {
	Game      string     `json:"game"`
	Event     string     `json:"event"`
	Chat      string     `json:"chat"`
	First     string     `json:"first_team"`
	Second    string     `json:"second_team"`
	Predicted string     `json:"predicted"`
	StartsAt  *time.Time `json:"starts_at,omitempty"`
	ClosesAt  time.Time  `json:"closes_at"`
	// Stream is the broadcast to watch it on, absent when none is
	// published — which is most matches until shortly before they start.
	Stream string `json:"stream,omitempty"`
	// Live marks a match already under way: the poll is closed, the result
	// is not in yet, and there is nothing left to do but watch.
	Live bool `json:"live"`
}

type activeDTO struct {
	Count   int              `json:"count"`
	Entries []activeEntryDTO `json:"entries"`
}

// chatStandingDTO is one chat's record and medals.
type chatStandingDTO struct {
	Chat        string `json:"chat"`
	Predictions int    `json:"predictions"`
	Accuracy    int    `json:"accuracy"`
	Points      int    `json:"points"`
	Tournaments int    `json:"tournaments"`
	Gold        int    `json:"gold"`
	Silver      int    `json:"silver"`
	Bronze      int    `json:"bronze"`
}

type chatsDTO struct {
	Count int               `json:"count"`
	Chats []chatStandingDTO `json:"chats"`
}

// activeHandler serves GET /api/miniapp/v1/me/active.
func activeHandler(deps MiniAppDeps, active MiniAppActive) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if active == nil {
			http.Error(w, "active predictions are not configured", http.StatusServiceUnavailable)
			return
		}
		rows, err := active.ActivePredictions(r.Context(), user.ID, scoring.ActivePredictionsMax)
		if err != nil {
			miniAppError(w, deps.Log, "active predictions unavailable", err)
			return
		}
		now := deps.Clock.Now()
		body := activeDTO{Entries: make([]activeEntryDTO, 0, len(rows))}
		for _, row := range rows {
			body.Entries = append(body.Entries, activeEntryDTO{
				Game: string(row.Game), Event: row.EventName, Chat: row.ChatTitle,
				First: row.FirstTeamName, Second: row.SecondTeamName,
				Predicted: row.PredictedScore.String(), StartsAt: row.ScheduledAt,
				ClosesAt: row.ClosesAt, Stream: row.StreamURL,
				Live: row.ScheduledAt != nil && !row.ScheduledAt.After(now),
			})
		}
		body.Count = len(body.Entries)
		writeJSON(w, body)
	})
}

// chatsHandler serves GET /api/miniapp/v1/me/chats: the per-chat record,
// with the medals that were actually awarded there.
func chatsHandler(deps MiniAppDeps, active MiniAppActive) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if active == nil {
			http.Error(w, "chat statistics are not configured", http.StatusServiceUnavailable)
			return
		}
		standings, err := active.UserChatStats(r.Context(), user.ID)
		if err != nil {
			miniAppError(w, deps.Log, "chat statistics unavailable", err)
			return
		}
		medals, err := active.UserMedals(r.Context(), user.ID)
		if err != nil {
			miniAppError(w, deps.Log, "medals unavailable", err)
			return
		}
		byChat := make(map[common.ChatID]scoring.MedalCount, len(medals))
		for _, tally := range medals {
			byChat[tally.ChatID] = tally.Medals
		}

		body := chatsDTO{Chats: make([]chatStandingDTO, 0, len(standings))}
		for _, standing := range standings {
			medal := byChat[standing.ChatID]
			body.Chats = append(body.Chats, chatStandingDTO{
				Chat: standing.ChatTitle, Predictions: standing.Predictions,
				Accuracy: standing.AccuracyPercent(), Points: standing.Points,
				Tournaments: standing.Tournaments,
				Gold:        medal.Gold, Silver: medal.Silver, Bronze: medal.Bronze,
			})
		}
		body.Count = len(body.Chats)
		writeJSON(w, body)
	})
}
