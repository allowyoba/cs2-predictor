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
	UserMedals(ctx context.Context, userID common.UserID) ([]scoring.EventMedal, error)
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

// chatStandingDTO is one chat's record and medal count.
type chatStandingDTO struct {
	ID          int64  `json:"id"`
	Chat        string `json:"chat"`
	Predictions int    `json:"predictions"`
	Accuracy    int    `json:"accuracy"`
	Points      int    `json:"points"`
	Tournaments int    `json:"tournaments"`
	Gold        int    `json:"gold"`
	Silver      int    `json:"silver"`
	Bronze      int    `json:"bronze"`
}

// medalDTO is one medal, said out loud: which place, in which tournament,
// in which chat, and when. A trophy shelf of unlabelled discs is a number
// with decoration on it.
type medalDTO struct {
	Place     int       `json:"place"`
	Event     string    `json:"event"`
	Chat      string    `json:"chat"`
	Game      string    `json:"game"`
	AwardedAt time.Time `json:"awarded_at"`
}

type chatsDTO struct {
	Count  int               `json:"count"`
	Chats  []chatStandingDTO `json:"chats"`
	Medals []medalDTO        `json:"medals"`
}

// activeHandler serves GET /api/miniapp/v1/me/active.
func activeHandler(deps MiniAppDeps, active MiniAppActive) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if active == nil {
			http.Error(w, "active predictions are not configured", http.StatusServiceUnavailable)
			return
		}
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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
			if !selected.keepsActive(row) {
				continue
			}
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
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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

		// Counted from the medals themselves rather than read from a
		// second query, so the tally above a chat and the list below it
		// can never disagree — and both obey the discipline filter.
		body := chatsDTO{
			Chats:  make([]chatStandingDTO, 0, len(standings)),
			Medals: make([]medalDTO, 0, len(medals)),
		}
		byChat := map[common.ChatID]*scoring.MedalCount{}
		for _, medal := range medals {
			if !selected.keepsMedal(medal) {
				continue
			}
			count, ok := byChat[medal.ChatID]
			if !ok {
				count = &scoring.MedalCount{}
				byChat[medal.ChatID] = count
			}
			switch medal.Place {
			case 1:
				count.Gold++
			case 2:
				count.Silver++
			case 3:
				count.Bronze++
			}
			body.Medals = append(body.Medals, medalDTO{
				Place: medal.Place, Event: medal.EventName, Chat: medal.ChatTitle,
				Game: string(medal.Game), AwardedAt: medal.AwardedAt,
			})
		}

		for _, standing := range standings {
			if !selected.keepsChat(standing.ChatID) {
				continue
			}
			var medal scoring.MedalCount
			if count, ok := byChat[standing.ChatID]; ok {
				medal = *count
			}
			body.Chats = append(body.Chats, chatStandingDTO{
				ID: standing.ChatID.Value, Chat: standing.ChatTitle, Predictions: standing.Predictions,
				Accuracy: standing.AccuracyPercent(), Points: standing.Points,
				Tournaments: standing.Tournaments,
				Gold:        medal.Gold, Silver: medal.Silver, Bronze: medal.Bronze,
			})
		}
		body.Count = len(body.Chats)
		writeJSON(w, body)
	})
}
