package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The Mini App's dashboard: one person's own numbers, and nothing that is
// not already in this bot's database.
//
// Every figure here comes from the same tables the Telegram screens read,
// so the app and the bot can never disagree about how somebody is doing.
// Nothing is modelled, estimated or filled in: a figure the database
// cannot produce is absent from the response rather than approximated,
// which is the Mini App spec's own first rule.

// MiniAppStats is the read side of the dashboard.
type MiniAppStats interface {
	UserStats(ctx context.Context, userID common.UserID, period scoring.StatsPeriod) (*scoring.UserStanding, error)
	UserPredictions(ctx context.Context, userID common.UserID, limit int) ([]scoring.UserPrediction, error)
}

// MiniAppAccessChecker answers whether this person may open the app at
// all.
type MiniAppAccessChecker interface {
	MiniAppAccess(ctx context.Context, userID common.UserID) (*chat.MiniAppAccess, error)
	RequestMiniAppAccess(ctx context.Context, userID common.UserID, at time.Time) error
}

// MiniAppDeps is everything the authenticated half of the Mini App API
// needs. Any of it missing turns those endpoints off rather than serving
// half an answer.
type MiniAppDeps struct {
	BotToken string
	Stats    MiniAppStats
	Access   MiniAppAccessChecker
	// Operators are the root administrators (DEPLOY_NOTIFY_CHAT_IDS).
	// They are exempt from the access grant: they are who approves it.
	Operators []int64
	Clock     common.Clock
	Log       *slog.Logger
}

// summaryDTO is the headline block: what somebody has done overall.
type summaryDTO struct {
	Predictions int `json:"predictions"`
	Correct     int `json:"correct"`
	Exact       int `json:"exact"`
	Accuracy    int `json:"accuracy"`
	Points      int `json:"points"`
	Tournaments int `json:"tournaments"`
}

// formDTO is the recent-results strip and the streaks around it.
type formDTO struct {
	CurrentStreak int    `json:"current_streak"`
	BestStreak    int    `json:"best_streak"`
	Recent        []bool `json:"recent"`
}

// trendDTO compares two adjacent windows. Absent — not zeroed — when there
// is no earlier window to compare against, because "no change" and "no
// history" are different answers and only one of them is true.
type trendDTO struct {
	CurrentAccuracy  int `json:"current_accuracy"`
	PreviousAccuracy int `json:"previous_accuracy"`
	DeltaPP          int `json:"delta_pp"`
	CurrentSample    int `json:"current_sample"`
	PreviousSample   int `json:"previous_sample"`
}

// gameDTO is one game's own record, for somebody who plays more than one.
type gameDTO struct {
	Game          string    `json:"game"`
	Predictions   int       `json:"predictions"`
	Accuracy      int       `json:"accuracy"`
	CurrentStreak int       `json:"current_streak"`
	Trend         *trendDTO `json:"trend,omitempty"`
}

// teamDTO is how well somebody reads one team.
type teamDTO struct {
	Team        string `json:"team"`
	Accuracy    int    `json:"accuracy"`
	Predictions int    `json:"predictions"`
}

type dashboardDTO struct {
	User struct {
		ID          int64  `json:"id"`
		DisplayName string `json:"display_name"`
		PhotoURL    string `json:"photo_url,omitempty"`
	} `json:"user"`
	Summary summaryDTO `json:"summary"`
	Form    formDTO    `json:"form"`
	Trend   *trendDTO  `json:"trend,omitempty"`
	Games   []gameDTO  `json:"games"`
	Teams   []teamDTO  `json:"teams"`
}

// accessDTO is what the app is told about its own right to be open.
type accessDTO struct {
	Status string `json:"status"`
	// Operator is true for the root administrators, who never need a
	// grant. The app uses it to hide the "request access" affordance.
	Operator bool `json:"operator"`
}

// dashboardHandler serves GET /api/miniapp/v1/me/dashboard.
func dashboardHandler(deps MiniAppDeps) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		insightsLimit := scoring.InsightsMaxPredictions
		predictions, err := deps.Stats.UserPredictions(r.Context(), user.ID, insightsLimit)
		if err != nil {
			miniAppError(w, deps.Log, "prediction history unavailable", err)
			return
		}
		overall, err := deps.Stats.UserStats(r.Context(), user.ID, scoring.AllTime())
		if err != nil {
			miniAppError(w, deps.Log, "statistics unavailable", err)
			return
		}

		now := deps.Clock.Now()
		insights := scoring.BuildPersonalInsights(predictions, now)
		var body dashboardDTO
		body.User.ID = user.ID.Value
		body.User.DisplayName = displayNameOf(user.Profile)
		body.User.PhotoURL = user.Profile.PhotoURL
		if overall != nil {
			body.Summary = summaryDTO{
				Predictions: overall.Predictions, Correct: overall.CorrectPredictions,
				Exact: overall.ExactPredictions, Accuracy: overall.AccuracyPercent(),
				Points: overall.Points, Tournaments: overall.Tournaments,
			}
		}
		body.Form = formDTO{
			CurrentStreak: insights.CurrentStreak, BestStreak: insights.LongestStreak,
			Recent: insights.RecentForm,
		}
		body.Trend = trendOf(insights)
		for _, entry := range scoring.SplitByGame(predictions, now) {
			game := gameDTO{
				Game:          string(entry.Game),
				CurrentStreak: entry.Insights.CurrentStreak,
				Trend:         trendOf(entry.Insights),
			}
			game.Predictions, game.Accuracy = recordOf(entry.Insights)
			body.Games = append(body.Games, game)
		}
		for _, team := range insights.Teams {
			body.Teams = append(body.Teams, teamDTO{
				Team: team.TeamName, Accuracy: team.AccuracyPercent(), Predictions: team.Predictions,
			})
		}
		writeJSON(w, body)
	})
}

// accessHandler serves GET /api/miniapp/v1/me/access and POST .../request:
// the app's own "may I be here" question, and the way to ask.
func accessHandler(deps MiniAppDeps) http.Handler {
	return withMiniAppAuthAllowingClosed(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if r.Method == http.MethodPost {
			if err := deps.Access.RequestMiniAppAccess(r.Context(), user.ID, deps.Clock.Now()); err != nil {
				miniAppError(w, deps.Log, "could not record the request", err)
				return
			}
		}
		status, operator, err := miniAppStatus(r.Context(), deps, user.ID)
		if err != nil {
			miniAppError(w, deps.Log, "access lookup failed", err)
			return
		}
		writeJSON(w, accessDTO{Status: string(status), Operator: operator})
	})
}

// trendOf renders the comparison only when there is an earlier window to
// compare against — see PersonalInsights.Trend.
func trendOf(insights scoring.PersonalInsights) *trendDTO {
	delta, ok := insights.Trend()
	if !ok {
		return nil
	}
	return &trendDTO{
		CurrentAccuracy: insights.Recent.AccuracyPercent(), PreviousAccuracy: insights.Previous.AccuracyPercent(),
		DeltaPP: delta, CurrentSample: insights.Recent.Predictions, PreviousSample: insights.Previous.Predictions,
	}
}

// recordOf sums the two windows into one record, which is what a per-game
// row shows.
func recordOf(insights scoring.PersonalInsights) (predictions, accuracy int) {
	predictions = insights.Recent.Predictions + insights.Previous.Predictions
	correct := insights.Recent.Correct + insights.Previous.Correct
	return predictions, scoring.PeriodSummary{Correct: correct, Predictions: predictions}.AccuracyPercent()
}

func displayNameOf(profile InitDataUser) string {
	name := profile.FirstName
	if profile.LastName != "" {
		name += " " + profile.LastName
	}
	if name == "" {
		name = profile.Username
	}
	return name
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Personal data: never cached by anything between here and the phone.
	w.Header().Set("Cache-Control", "no-store")
	writeJSONBody(w, body)
}

// writeJSONBody writes the body with the headers already decided by the
// caller — used where the status code is set first.
func writeJSONBody(w http.ResponseWriter, body any) {
	_ = json.NewEncoder(w).Encode(body)
}

// miniAppError logs the cause and tells the client nothing about it.
func miniAppError(w http.ResponseWriter, log *slog.Logger, message string, err error) {
	if log != nil {
		log.Error("mini app request failed", "reason", message, "error", err)
	}
	http.Error(w, message, http.StatusInternalServerError)
}

// miniAppStatus resolves one person's standing: operators are always in.
func miniAppStatus(ctx context.Context, deps MiniAppDeps, userID common.UserID) (chat.MiniAppStatus, bool, error) {
	for _, operator := range deps.Operators {
		if operator == userID.Value {
			return chat.MiniAppGranted, true, nil
		}
	}
	access, err := deps.Access.MiniAppAccess(ctx, userID)
	if err != nil {
		return "", false, err
	}
	if access == nil {
		return "", false, nil
	}
	return access.Status, false, nil
}

// errMiniAppUnavailable marks a deployment that did not wire these
// endpoints at all.
var errMiniAppUnavailable = errors.New("mini app API is not configured")
