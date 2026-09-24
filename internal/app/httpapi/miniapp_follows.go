package httpapi

import (
	"net/http"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// Team/player cuts of the person's own record, and period nominations over
// the chats they play in.

type followCutDTO struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	ChatID      int64  `json:"chat_id"`
	ChatTitle   string `json:"chat_title"`
	Points      int    `json:"points"`
	Correct     int    `json:"correct"`
	Predictions int    `json:"predictions"`
	Accuracy    int    `json:"accuracy"`
}

type followsDTO struct {
	Follows []followCutDTO `json:"follows"`
}

type nominationsDTO struct {
	Nominations []nominationDTO `json:"nominations"`
	// Predictions is how many settled calls the nominations rest on.
	Predictions int `json:"predictions"`
}

// followsHandler serves GET /api/miniapp/v1/me/follows: for every team or
// player a chat of theirs follows, this person's record on those matches.
func followsHandler(deps MiniAppDeps, active MiniAppActive, targets subscription.TargetRepository) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if targets == nil || deps.Stats == nil {
			http.Error(w, "follows are not configured", http.StatusServiceUnavailable)
			return
		}
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		chats, err := chatRefs(r.Context(), active, user.ID)
		if err != nil {
			miniAppError(w, deps.Log, "follows unavailable", err)
			return
		}
		out := []followCutDTO{}
		for _, ref := range chats {
			chatID := common.ChatID{Value: ref.ID}
			if !selected.keepsChat(chatID) {
				continue
			}
			list, err := targets.Targets(r.Context(), chatID)
			if err != nil {
				miniAppError(w, deps.Log, "follows unavailable", err)
				return
			}
			resolved, err := targets.ResolveTeams(r.Context(), list)
			if err != nil {
				miniAppError(w, deps.Log, "follows unavailable", err)
				return
			}
			for _, t := range list {
				if t.Kind == subscription.TargetTournament {
					continue
				}
				teams := subscription.TeamsOf([]subscription.Target{t}, resolved)
				period := scoring.AllTime().ForGame(selected.Game).ForChat(&chatID).ForTeams(teams)
				standing, err := deps.Stats.UserStats(r.Context(), user.ID, period)
				if err != nil {
					miniAppError(w, deps.Log, "follows unavailable", err)
					return
				}
				cut := followCutDTO{Kind: string(t.Kind), Label: t.Label, ChatID: ref.ID, ChatTitle: ref.Title}
				if standing != nil {
					cut.Points, cut.Correct, cut.Predictions = standing.Points, standing.CorrectPredictions, standing.Predictions
					cut.Accuracy = standing.AccuracyPercent()
				}
				out = append(out, cut)
			}
		}
		writeJSON(w, followsDTO{Follows: out})
	})
}

type nominationDTO struct {
	Kind      string `json:"kind"`
	UserName  string `json:"user_name,omitempty"`
	IsYou     bool   `json:"is_you"`
	ChatTitle string `json:"chat_title,omitempty"`
	Value     int    `json:"value"`
	Sample    int    `json:"sample"`
	Picked    string `json:"picked,omitempty"`
	Opponent  string `json:"opponent,omitempty"`
}

// nominationSince maps the period picker to its start; "all" is zero.
func nominationSince(raw string, now time.Time) (time.Time, bool) {
	switch raw {
	case "", "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), true
	case "year":
		return time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC), true
	case "all":
		return time.Time{}, true
	}
	return time.Time{}, false
}

// nominationsHandler serves GET /api/miniapp/v1/me/nominations.
func nominationsHandler(deps MiniAppDeps, active MiniAppActive, facts scoring.ChatFactsRepository) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if facts == nil {
			http.Error(w, "nominations are not configured", http.StatusServiceUnavailable)
			return
		}
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		since, ok := nominationSince(r.URL.Query().Get("period"), deps.Clock.Now().UTC())
		if !ok {
			http.Error(w, "invalid period", http.StatusBadRequest)
			return
		}
		chats, err := chatRefs(r.Context(), active, user.ID)
		if err != nil {
			miniAppError(w, deps.Log, "nominations unavailable", err)
			return
		}
		titles := map[int64]string{}
		var ids []common.ChatID
		for _, ref := range chats {
			if selected.keepsChat(common.ChatID{Value: ref.ID}) {
				ids = append(ids, common.ChatID{Value: ref.ID})
				titles[ref.ID] = ref.Title
			}
		}
		rows, err := facts.ChatPredictionFacts(r.Context(), ids, since, scoring.NominationFactsMax)
		if err != nil {
			miniAppError(w, deps.Log, "nominations unavailable", err)
			return
		}
		scoped := rows[:0]
		for _, f := range rows {
			if selected.keepsGame(f.Game) {
				scoped = append(scoped, f)
			}
		}
		out := []nominationDTO{}
		for _, n := range scoring.Nominate(scoped) {
			dto := nominationDTO{Kind: string(n.Kind), Value: n.Value, Sample: n.Sample, Picked: n.Picked, Opponent: n.Opponent}
			if n.Kind == scoring.NominationActiveChat {
				dto.ChatTitle = titles[n.ChatID.Value]
			} else {
				dto.UserName, dto.IsYou = n.UserName, n.UserID == user.ID
			}
			out = append(out, dto)
		}
		writeJSON(w, nominationsDTO{Nominations: out, Predictions: len(scoped)})
	})
}
