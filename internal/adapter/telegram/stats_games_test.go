package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// A chat following two games runs two contests at once, and one merged
// table hides which one somebody is actually good at. These cover the
// filter that separates them — and, just as importantly, its absence where
// it would be furniture.

func statsSettings(games ...competition.GameCode) chat.Settings {
	return chat.Settings{
		ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU,
		Timezone: chat.DefaultTimezone, Active: true, EnabledGames: games,
	}
}

func TestStatsGameFilter_AppearsOnlyForAChatFollowingSeveralGames(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	callback := func(p scoring.StatsPeriod) string { return leaderboardPageData(p, 0, "menu:stats") }

	if row := h.statsGameFilterRow(statsSettings(competition.GameCS2), scoring.AllTime(), callback); row != nil {
		t.Fatalf("one game means nothing to choose between, got %+v", row)
	}
	row := h.statsGameFilterRow(statsSettings(competition.GameCS2, competition.GameDota2), scoring.AllTime(), callback)
	if len(row) != 3 {
		t.Fatalf("expected 'all' plus one button per game, got %+v", row)
	}
	// An event is one game by definition; a filter there would be a
	// control that cannot change anything.
	if row := h.statsGameFilterRow(statsSettings(competition.GameCS2, competition.GameDota2),
		scoring.ForEvent(common.NewEventID()), callback); row != nil {
		t.Fatalf("expected no filter on an event board, got %+v", row)
	}
}

// The chosen game survives every other control on the screen: changing the
// period, paging, opening a chart. A filter that silently resets is a
// filter nobody trusts.
func TestStatsGameFilter_SurvivesPeriodChangesAndPaging(t *testing.T) {
	dota := scoring.AllTime().ForGame(competition.GameDota2)

	page := leaderboardPageData(dota, 2, "menu:stats")
	if !strings.HasSuffix(page, ":d") {
		t.Fatalf("paging lost the game: %q", page)
	}
	rest, game := splitGame(page)
	if game != competition.GameDota2 || strings.HasSuffix(rest, ":d") {
		t.Fatalf("splitGame(%q) = %q, %q", page, rest, game)
	}
	if got := chartCallbackData(dota); !strings.HasSuffix(got, ":d") {
		t.Fatalf("the chart must open on the same slice: %q", got)
	}
	if got := rankChartCallbackData(dota); !strings.HasSuffix(got, ":d") {
		t.Fatalf("the rank chart must open on the same slice: %q", got)
	}
	// All games is the widest view and carries no token at all, exactly
	// like every callback did before the filter existed.
	if got := leaderboardPageData(scoring.AllTime(), 0, "menu:stats"); strings.HasSuffix(got, ":d") || strings.HasSuffix(got, ":c") {
		t.Fatalf("the unfiltered view carries no game token: %q", got)
	}
}

// A stale button from an old message must degrade to the widest view
// rather than be refused.
func TestSplitGame_UnknownTokenMeansAllGames(t *testing.T) {
	rest, game := splitGame("stats:p:a:0")
	if game != "" || rest != "stats:p:a:0" {
		t.Fatalf("splitGame = %q, %q; want the data untouched", rest, game)
	}
}

// End to end through the router: tapping Dota 2 renders a board that says
// so, and the buttons on it keep the choice.
func TestStatsPage_RendersTheChosenGameAndKeepsIt(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, chats := newTestHandler(t, server)
	settings := statsSettings(competition.GameCS2, competition.GameDota2)
	if _, err := chats.Save(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(context.Background(), settings.ChatID, settings.EnabledGames); err != nil {
		t.Fatal(err)
	}
	data := leaderboardPageData(scoring.AllTime().ForGame(competition.GameDota2), 0, "menu:stats")
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}

	if err := h.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	text := lastText(*calls)
	if !strings.Contains(text, ru(t, "game.dota2_short")) {
		t.Fatalf("expected the board to name the game it is about, got %q", text)
	}
	var withMarkup map[string]any
	for i := len(*calls) - 1; i >= 0; i-- {
		if _, ok := (*calls)[i]["reply_markup"]; ok {
			withMarkup = (*calls)[i]
			break
		}
	}
	markup, _ := json.Marshal(withMarkup["reply_markup"])
	if !strings.Contains(string(markup), `:d"`) {
		t.Fatalf("expected the screen's own buttons to keep the game, got %s", markup)
	}
}
