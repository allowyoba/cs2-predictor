package telegram

import (
	"strings"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
)

// The game filter on the statistics screens.
//
// A chat following two games is really running two contests at once.
// Somebody who predicts every Dota 2 match and never touches CS2 is not
// mid-table — they are first at the thing they actually play — and one
// merged leaderboard is precisely what hides that. So every period-based
// screen can be narrowed to one game, and the filter only appears where it
// means something: a chat following a single game has nothing to choose
// between, and an event-scoped view is already one game by definition.

// gameTokens maps a game to the single character its callbacks carry.
// Callback data is capped at 64 bytes and already carries a period, a page
// and a back target, so the game gets one character rather than "DOTA2".
var gameTokens = map[competition.GameCode]string{
	competition.GameCS2:   "c",
	competition.GameDota2: "d",
}

// gameToken renders the suffix a callback carries for this period's game —
// empty for "all games", which is what every callback looked like before
// the filter existed and still does for a single-game chat.
func gameToken(game competition.GameCode) string {
	return gameTokens[game]
}

// parseGameToken reads that character back. An unknown token means all
// games rather than an error: a stale button from an old message must
// degrade to the widest view, not to a refusal.
func parseGameToken(token string) competition.GameCode {
	for game, t := range gameTokens {
		if t == token {
			return game
		}
	}
	return ""
}

// appendGame adds the token to a callback when there is one to add.
func appendGame(data string, game competition.GameCode) string {
	if token := gameToken(game); token != "" {
		return data + ":" + token
	}
	return data
}

// splitGame pulls a trailing game token off callback data, returning the
// rest unchanged when there is none. Every stats route runs its data
// through this, so one convention covers all of them.
func splitGame(data string) (string, competition.GameCode) {
	idx := strings.LastIndex(data, ":")
	if idx < 0 {
		return data, ""
	}
	if game := parseGameToken(data[idx+1:]); game != "" {
		return data[:idx], game
	}
	return data, ""
}

// statsGameFilterRow renders the filter itself: one button per game the
// chat follows, plus "all". Returns nil for a chat with fewer than two
// games — a filter with one option is furniture, not a choice.
func (h *UpdateHandler) statsGameFilterRow(settings chat.Settings, period scoring.StatsPeriod,
	callback func(scoring.StatsPeriod) string) []InlineButton {
	if len(settings.EnabledGames) < 2 || period.Kind == scoring.PeriodEvent {
		return nil
	}
	row := []InlineButton{button(
		statsFilterLabel(period.Game == "", h.Texts.Get("stats.all_games", settings.Locale)),
		callback(period.ForGame("")),
	)}
	for _, game := range settings.EnabledGames {
		row = append(row, button(
			statsFilterLabel(period.Game == game, h.Texts.Get(gameShortLabelKey(game), settings.Locale)),
			callback(period.ForGame(game)),
		))
	}
	return row
}

// statsGameSuffix names the game in a screen's title, so a narrowed board
// never reads as the whole chat's.
func (h *UpdateHandler) statsGameSuffix(settings chat.Settings, period scoring.StatsPeriod) string {
	if period.Game == "" {
		return ""
	}
	return " · " + h.Texts.Get(gameShortLabelKey(period.Game), settings.Locale)
}
