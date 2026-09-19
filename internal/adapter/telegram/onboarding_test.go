package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// A chat that has picked no game is not a chat with nothing happening in
// it — it is a chat one tap away from everything working. These cover the
// screens where that tap has to be findable.

// The menu is the first screen anybody sees, so it is where the missing
// step belongs — and only while it is missing.
func TestMenu_PointsAFreshChatAtPickingItsGames(t *testing.T) {
	render := func(t *testing.T, games []competition.GameCode, dmContext bool) map[string]any {
		t.Helper()
		server, calls := newRecordingServer(t)
		defer server.Close()
		h, _ := newTestHandler(t, server)
		settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, EnabledGames: games}
		if err := h.menu(context.Background(), sendTarget(settings.ChatID, nil), settings, dmContext); err != nil {
			t.Fatal(err)
		}
		return (*calls)[len(*calls)-1]
	}

	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	hint := texts.Get("menu.setup_games", common.LocaleRU)

	fresh := render(t, nil, true)
	text, _ := fresh["text"].(string)
	if !strings.Contains(text, hint) {
		t.Fatalf("expected the menu to name the missing step, got %q", text)
	}
	markup, _ := json.Marshal(fresh["reply_markup"])
	if !strings.Contains(string(markup), "settings:games") {
		t.Fatalf("expected a direct route to the games screen, got %s", markup)
	}

	// Once a game is on, the hint has done its job and must get out of the
	// way — a permanent banner is just noise on every later visit.
	configured := render(t, []competition.GameCode{competition.GameCS2}, true)
	if text, _ := configured["text"].(string); strings.Contains(text, hint) {
		t.Fatalf("expected no setup hint once a game is enabled, got %q", text)
	}
}

// The subscription list is where somebody goes looking for tournaments, so
// an empty one has to say what to do — and in a group, who can do it.
func TestSubscribedEvents_EmptyStateOffersTheNextStep(t *testing.T) {
	render := func(t *testing.T, games []competition.GameCode, dmContext bool) map[string]any {
		t.Helper()
		server, calls := newRecordingServer(t)
		defer server.Close()
		h, _ := newTestHandler(t, server)
		settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, EnabledGames: games}
		if err := h.subscribedEvents(context.Background(), sendTarget(settings.ChatID, nil), settings, dmContext); err != nil {
			t.Fatal(err)
		}
		return (*calls)[len(*calls)-1]
	}
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}

	withGames := render(t, []competition.GameCode{competition.GameCS2}, true)
	text, _ := withGames["text"].(string)
	if !strings.Contains(text, texts.Get("events.empty", common.LocaleRU)) {
		t.Fatalf("expected the DM panel's empty state, got %q", text)
	}
	markup, _ := json.Marshal(withGames["reply_markup"])
	if !strings.Contains(string(markup), "events:add") {
		t.Fatalf("expected the add-tournament button on an empty list, got %s", markup)
	}

	// No games picked: searching cannot help yet, so the screen says the
	// real reason instead of "nothing found".
	noGames := render(t, nil, true)
	if text, _ := noGames["text"].(string); !strings.Contains(text, texts.Get("events.empty_no_games", common.LocaleRU)) {
		t.Fatalf("expected the no-games explanation, got %q", text)
	}

	// In the group itself the action is not available to everyone, so the
	// text points at who can do it rather than offering a button that
	// would be refused.
	inGroup := render(t, []competition.GameCode{competition.GameCS2}, false)
	if text, _ := inGroup["text"].(string); !strings.Contains(text, texts.Get("events.empty_group", common.LocaleRU)) {
		t.Fatalf("expected the group wording, got %q", text)
	}
	groupMarkup, _ := json.Marshal(inGroup["reply_markup"])
	if strings.Contains(string(groupMarkup), "events:add") {
		t.Fatalf("the add button belongs to the DM panel only, got %s", groupMarkup)
	}
}
