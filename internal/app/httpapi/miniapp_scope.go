package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// One filter, applied once, for the whole app.
//
// Before this, each screen narrowed its own data: the history page had its
// own discipline chips beside the app's, analytics recomputed some figures
// per game and left others alone, and awards and the profile ignored the
// choice entirely. The result was a control that meant something different
// depending on where you were standing, which is worse than no control.
//
// So the scope lives here, at the API's edge. Every endpoint parses it the
// same way and every screen sends the same two parameters, which makes the
// app's one filter bar the only place a filter exists. A screen cannot
// disagree with another screen about what "CS2" means, because neither of
// them decides.

// scope is what the filter bar selects: a discipline, a chat, or neither.
type scope struct {
	// Game is empty for "all disciplines".
	Game competition.GameCode
	// ChatID is nil for "all chats".
	ChatID *common.ChatID
}

// scopeFrom reads the filter off the query string. An unparsable chat is
// an error rather than a silently ignored parameter: a screen that shows
// every chat while the bar says otherwise is a lie, and the app has no way
// to notice it happened.
func scopeFrom(r *http.Request) (scope, error) {
	var s scope
	if raw := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("game"))); raw != "" {
		s.Game = competition.GameCode(raw)
	}
	raw := strings.TrimSpace(r.URL.Query().Get("chat"))
	if raw == "" {
		return s, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return scope{}, errInvalidChatFilter
	}
	s.ChatID = &common.ChatID{Value: id}
	return s, nil
}

// period narrows a stats period to this scope — the same narrowing the
// database does for the aggregate figures.
func (s scope) period(p scoring.StatsPeriod) scoring.StatsPeriod {
	return p.ForGame(s.Game).ForChat(s.ChatID)
}

// keepsGame reports whether a row's discipline is in scope.
func (s scope) keepsGame(game competition.GameCode) bool {
	return s.Game == "" || game == s.Game
}

// keepsChat reports whether a row's chat is in scope.
func (s scope) keepsChat(chatID common.ChatID) bool {
	return s.ChatID == nil || chatID == *s.ChatID
}

func (s scope) keepsBet(bet scoring.UserBet) bool {
	return s.keepsGame(bet.Game) && s.keepsChat(bet.ChatID)
}

func (s scope) keepsPrediction(p scoring.UserPrediction) bool {
	return s.keepsGame(p.Game) && s.keepsChat(p.ChatID)
}

func (s scope) keepsActive(a scoring.ActivePrediction) bool {
	return s.keepsGame(a.Game) && s.keepsChat(a.ChatID)
}

func (s scope) keepsMedal(m scoring.EventMedal) bool {
	return s.keepsGame(m.Game) && s.keepsChat(m.ChatID)
}

// filterPredictions narrows the list every personal-form figure is built
// from, so form, streaks, trend and the team tables all answer the same
// question.
func (s scope) filterPredictions(all []scoring.UserPrediction) []scoring.UserPrediction {
	if s.Game == "" && s.ChatID == nil {
		return all
	}
	out := make([]scoring.UserPrediction, 0, len(all))
	for _, p := range all {
		if s.keepsPrediction(p) {
			out = append(out, p)
		}
	}
	return out
}

var errInvalidChatFilter = errors.New("invalid chat filter")
