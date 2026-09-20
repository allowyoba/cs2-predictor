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

// MiniAppNames resolves the name this person is known by inside the bot.
//
// The Mini App sits next to leaderboards that call somebody by their chosen
// nickname; greeting them by their Telegram first name instead makes the
// app look like it is about somebody else. Optional: without it the app
// falls back to the name Telegram sent with the launch.
type MiniAppNames interface {
	Nickname(ctx context.Context, userID common.UserID) (*string, error)
	UserProfile(ctx context.Context, userID common.UserID) (*chat.UserProfile, error)
}

// MiniAppPrefs reads the personal settings the app honours. Only the crest
// source so far, and it is a person's choice rather than a chat's because
// the app is a private surface — see chat.Repository.PrefersHLTVLogos.
type MiniAppPrefs interface {
	PrefersHLTVLogos(ctx context.Context, userID common.UserID) (bool, error)
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
	// Names resolves the display name the rest of the bot uses.
	Names MiniAppNames
	// Prefs is this person's own settings; nil leaves the defaults.
	Prefs MiniAppPrefs
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

// chatRefDTO is one chat the person plays in, for the filter bar. Carries
// its id because the filter travels back as a query parameter, and a title
// is not a key — two chats may well be called the same thing.
type chatRefDTO struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Predictions int    `json:"predictions"`
}

type dashboardDTO struct {
	User struct {
		ID          int64  `json:"id"`
		DisplayName string `json:"display_name"`
		PhotoURL    string `json:"photo_url,omitempty"`
		// LogoSource is "provider" or "hltv": which crests this person
		// asked for. The app sends it back when it fetches the team list,
		// which keeps that endpoint public and cacheable while the
		// preference still belongs to the person.
		LogoSource string `json:"logo_source"`
	} `json:"user"`
	Summary summaryDTO `json:"summary"`
	Form    formDTO    `json:"form"`
	Trend   *trendDTO  `json:"trend,omitempty"`
	// Games and Chats are deliberately NOT narrowed by the current scope:
	// they are what the filter bar is built from, and a filter that
	// removes its own options as soon as you use one is a trap.
	Games []gameDTO    `json:"games"`
	Chats []chatRefDTO `json:"chats"`
	// Best and Worst are the two ends of the same ranking, both inside the
	// current scope. Naming only the teams somebody reads well answers
	// half a question: the useful half is usually the other one.
	Best  []teamDTO `json:"best_teams"`
	Worst []teamDTO `json:"worst_teams"`
}

// accessDTO is what the app is told about its own right to be open.
type accessDTO struct {
	Status string `json:"status"`
	// Operator is true for the root administrators, who never need a
	// grant. The app uses it to hide the "request access" affordance.
	Operator bool `json:"operator"`
}

// dashboardHandler serves GET /api/miniapp/v1/me/dashboard.
//
// Every figure here answers the same question as every other figure here,
// because they are all built from the one scope the request carried. The
// rail's own options are the exception, and say so.
func dashboardHandler(deps MiniAppDeps, active MiniAppActive) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		all, err := deps.Stats.UserPredictions(r.Context(), user.ID, scoring.InsightsMaxPredictions)
		if err != nil {
			miniAppError(w, deps.Log, "prediction history unavailable", err)
			return
		}
		overall, err := deps.Stats.UserStats(r.Context(), user.ID, selected.period(scoring.AllTime()))
		if err != nil {
			miniAppError(w, deps.Log, "statistics unavailable", err)
			return
		}

		now := deps.Clock.Now()
		scoped := selected.filterPredictions(all)
		insights := scoring.BuildPersonalInsights(scoped, now)

		var body dashboardDTO
		body.User.ID = user.ID.Value
		body.User.DisplayName = deps.displayName(r.Context(), user)
		body.User.PhotoURL = user.Profile.PhotoURL
		body.User.LogoSource = "provider"
		if deps.Prefs != nil {
			if prefer, err := deps.Prefs.PrefersHLTVLogos(r.Context(), user.ID); err == nil && prefer {
				body.User.LogoSource = "hltv"
			}
		}
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
		body.Games = gameBreakdown(all, selected, now)
		body.Best, body.Worst = teamExtremes(scoped)
		if chats, err := chatRefs(r.Context(), active, user.ID); err == nil {
			body.Chats = chats
		} else {
			// The filter bar losing its chat list is not worth failing the
			// whole screen over; the rest of the dashboard is intact.
			deps.Log.Error("chat list unavailable", "error", err)
		}
		writeJSON(w, body)
	})
}

// gameBreakdown is the rail: every discipline this person plays, whatever
// the current filter says, but narrowed by the chat filter — a chat that
// only follows one game genuinely has only one discipline to offer.
func gameBreakdown(all []scoring.UserPrediction, selected scope, now time.Time) []gameDTO {
	byChat := scope{ChatID: selected.ChatID}.filterPredictions(all)
	out := make([]gameDTO, 0, 4)
	for _, entry := range scoring.SplitByGame(byChat, now) {
		game := gameDTO{
			Game:          string(entry.Game),
			CurrentStreak: entry.Insights.CurrentStreak,
			Trend:         trendOf(entry.Insights),
		}
		game.Predictions, game.Accuracy = recordOf(entry.Insights)
		out = append(out, game)
	}
	return out
}

// teamExtremes names the teams somebody reads best and worst, from the
// same ranking. The worst end is read off the bottom rather than resorted,
// so a team cannot appear in both lists with different numbers.
func teamExtremes(scoped []scoring.UserPrediction) (best, worst []teamDTO) {
	ranked := scoring.TeamAccuracyOf(scoped)
	render := func(teams []scoring.TeamAccuracy) []teamDTO {
		out := make([]teamDTO, 0, len(teams))
		for _, team := range teams {
			out = append(out, teamDTO{
				Team: team.TeamName, Accuracy: team.AccuracyPercent(), Predictions: team.Predictions,
			})
		}
		return out
	}
	// With too few teams to have two ends, there is one list and it is the
	// ranking: splitting three teams into "best" and "worst" invents a
	// verdict the sample cannot support.
	if len(ranked) < 2*scoring.InsightsTeamLimit {
		half := len(ranked) / 2
		return render(ranked[:half]), reverse(render(ranked[half:]))
	}
	return render(ranked[:scoring.InsightsTeamLimit]), reverse(render(ranked[len(ranked)-scoring.InsightsTeamLimit:]))
}

// reverse puts the worst-read team first in its own list, so both tables
// read top-down as "most notable first".
func reverse(teams []teamDTO) []teamDTO {
	for i, j := 0, len(teams)-1; i < j; i, j = i+1, j-1 {
		teams[i], teams[j] = teams[j], teams[i]
	}
	return teams
}

// chatRefs lists the chats the filter bar can choose between.
func chatRefs(ctx context.Context, active MiniAppActive, userID common.UserID) ([]chatRefDTO, error) {
	if active == nil {
		return nil, nil
	}
	standings, err := active.UserChatStats(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]chatRefDTO, 0, len(standings))
	for _, standing := range standings {
		out = append(out, chatRefDTO{
			ID: standing.ChatID.Value, Title: standing.ChatTitle, Predictions: standing.Predictions,
		})
	}
	return out, nil
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

// displayName is the same name the leaderboards use: the nickname
// somebody chose, then whatever the bot stored for them, and only then the
// name Telegram sent with this launch. A person who renamed themselves in
// the bot and is greeted by their Telegram name in the app reasonably
// concludes the app is showing somebody else's numbers.
func (d MiniAppDeps) displayName(ctx context.Context, user authenticatedUser) string {
	if d.Names != nil {
		if nickname, err := d.Names.Nickname(ctx, user.ID); err == nil && nickname != nil && *nickname != "" {
			return *nickname
		}
		if profile, err := d.Names.UserProfile(ctx, user.ID); err == nil && profile != nil && profile.DisplayName != "" {
			return profile.DisplayName
		}
	}
	return displayNameOf(user.Profile)
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
