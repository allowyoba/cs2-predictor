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

// The history screen: every settled prediction this person has made, newest
// first, across every chat they play in.
//
// The bot's own bets screen is scoped to one chat and can leave the
// tournament implicit. This one cannot: a cross-chat list of team names
// with no discipline and no tournament reads as a pile of near-duplicates,
// which is why UserBet carries both.

// MiniAppHistory is the read behind it.
type MiniAppHistory interface {
	UserBets(ctx context.Context, userID common.UserID, chatID *common.ChatID, limit int) ([]scoring.UserBet, error)
}

// historyEntryDTO is one row as the app renders it.
type historyEntryDTO struct {
	PlayedAt  time.Time `json:"played_at"`
	Game      string    `json:"game"`
	Event     string    `json:"event"`
	Chat      string    `json:"chat"`
	First     string    `json:"first_team"`
	Second    string    `json:"second_team"`
	Predicted string    `json:"predicted"`
	Actual    string    `json:"actual"`
	Correct   bool      `json:"correct"`
	Points    int       `json:"points"`
}

type historyDTO struct {
	Count   int               `json:"count"`
	Entries []historyEntryDTO `json:"entries"`
}

// miniappHistoryLimit bounds one response, for the same reason the team
// list is bounded: a screen shows a feed, not an archive.
const miniappHistoryLimit = 100

// historyHandler serves GET /api/miniapp/v1/me/history.
//
// Filtering happens here rather than in SQL because the underlying read is
// already capped and already sorted: narrowing 300 rows in memory costs
// nothing, while a filtered query per combination would multiply the
// indexes this table needs for a screen somebody scrolls twice.
func historyHandler(deps MiniAppDeps, history MiniAppHistory) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if history == nil {
			http.Error(w, "history is not configured", http.StatusServiceUnavailable)
			return
		}
		filter, err := parseHistoryFilter(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bets, err := history.UserBets(r.Context(), user.ID, nil, scoring.UserBetsMaxRows)
		if err != nil {
			miniAppError(w, deps.Log, "history unavailable", err)
			return
		}
		writeJSON(w, buildHistory(bets, filter))
	})
}

// historyFilter is what the screen's chips and sheet narrow the feed by.
type historyFilter struct {
	game   string
	result string
	limit  int
}

func parseHistoryFilter(r *http.Request) (historyFilter, error) {
	filter := historyFilter{
		game:   strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("game"))),
		result: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("result"))),
		limit:  miniappHistoryLimit,
	}
	if filter.result != "" && filter.result != "correct" && filter.result != "wrong" {
		// Refused rather than ignored: a screen showing everything while a
		// chip claims otherwise is worse than an error.
		return historyFilter{}, errInvalidResultFilter
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return historyFilter{}, errInvalidLimit
		}
		filter.limit = min(parsed, miniappHistoryLimit)
	}
	return filter, nil
}

// buildHistory narrows and renders. In memory rather than in SQL because
// the read is already capped and sorted: filtering 300 rows costs nothing,
// while a query per combination would multiply the indexes this table
// needs for a screen somebody scrolls twice.
func buildHistory(bets []scoring.UserBet, filter historyFilter) historyDTO {
	body := historyDTO{Entries: make([]historyEntryDTO, 0, min(filter.limit, len(bets)))}
	for _, bet := range bets {
		if !filter.matches(bet) {
			continue
		}
		if len(body.Entries) == filter.limit {
			break
		}
		body.Entries = append(body.Entries, historyEntryDTO{
			PlayedAt: bet.PlayedAt, Game: string(bet.Game), Event: bet.EventName, Chat: bet.ChatTitle,
			First: bet.FirstTeamName, Second: bet.SecondTeamName,
			Predicted: bet.PredictedScore.String(), Actual: bet.ActualScore.String(),
			Correct: bet.Correct, Points: bet.Points,
		})
	}
	body.Count = len(body.Entries)
	return body
}

func (f historyFilter) matches(bet scoring.UserBet) bool {
	if f.game != "" && string(bet.Game) != f.game {
		return false
	}
	switch f.result {
	case "correct":
		return bet.Correct
	case "wrong":
		return !bet.Correct
	default:
		return true
	}
}

var (
	errInvalidResultFilter = errors.New("invalid result filter")
	errInvalidLimit        = errors.New("invalid limit")
)
