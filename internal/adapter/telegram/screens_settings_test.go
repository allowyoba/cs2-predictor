package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// settingsRoutes is every destination the settings screen is responsible for
// offering. Named here rather than derived from the screen itself, so
// regrouping the keyboard cannot quietly drop one: a setting that stops being
// rendered is unreachable, and nothing else in the bot links to it.
var settingsRoutes = []string{
	"settings:games", "settings:top_tier", "settings:auto_subscribe",
	"settings:notify", "settings:quiet", "settings:timezone", "settings:stream_language",
	"settings:locale", "settings:flags",
	"settings:moderators", "settings:history",
}

// Every setting stays reachable, and exactly once — a duplicate is a second
// way to reach the same screen from the same screen, which is how a keyboard
// stops being readable.
func TestSettings_OffersEverySettingExactlyOnce(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})
	settings.EnabledGames = []competition.GameCode{competition.GameCS2}

	if err := handler.settingsView(context.Background(),
		sendTarget(settings.ChatID, nil), settings, false); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)

	for _, route := range settingsRoutes {
		got := 0
		for _, cd := range cds {
			if cd == route {
				got++
			}
		}
		if got != 1 {
			t.Fatalf("%s appears %d times on the settings screen, want exactly 1: %v", route, got, cds)
		}
	}
	if !slices.Contains(cds, "menu:main") {
		t.Fatalf("the settings screen has no way back: %v", cds)
	}
}

// The groups are what make eleven settings findable rather than a wall, so
// each heading has to actually be rendered.
func TestSettings_GroupsTheSettingsUnderHeadings(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})
	settings.EnabledGames = []competition.GameCode{competition.GameCS2}

	if err := handler.settingsView(context.Background(),
		sendTarget(settings.ChatID, nil), settings, false); err != nil {
		t.Fatal(err)
	}
	labels := allButtonLabels(*calls)
	var headings []string
	for cd, label := range labels {
		if cd == "noop" {
			headings = append(headings, label)
		}
	}
	// allButtonLabels keys by callback data, so every heading collapses onto
	// "noop" — enough to prove they are rendered, and the text of the last
	// one tells us they carry the section form.
	if len(headings) == 0 {
		t.Fatalf("no section headings on the settings screen: %v", labels)
	}
	for _, key := range []string{
		"settings.section_tournaments", "settings.section_messages",
		"settings.section_appearance", "settings.section_access",
	} {
		want := handler.Texts.Get(key, settings.Locale)
		var found bool
		for _, c := range *calls {
			if markup, ok := c["reply_markup"].(map[string]any); ok {
				if strings.Contains(renderKeyboard(markup), want) {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("the %q section is missing from the settings screen", want)
		}
	}
}

// A chat that does not follow Counter-Strike is not offered a choice between
// HLTV's flags and the provider's, because HLTV has nothing to say about any
// other game.
func TestSettings_HidesTheFlagSourceWithoutCounterStrike(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})
	settings.EnabledGames = []competition.GameCode{competition.GameDota2}

	if err := handler.settingsView(context.Background(),
		sendTarget(settings.ChatID, nil), settings, false); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if slices.Contains(cds, "settings:flags") {
		t.Fatalf("a Dota-only chat must not be offered the HLTV flag source: %v", cds)
	}
	// Everything else is still there.
	for _, route := range settingsRoutes {
		if route == "settings:flags" {
			continue
		}
		if !slices.Contains(cds, route) {
			t.Fatalf("%s disappeared along with the flag source: %v", route, cds)
		}
	}
}

// renderKeyboard flattens a keyboard to its text, for asserting on labels
// that are not tied to a unique callback.
func renderKeyboard(markup map[string]any) string {
	var b strings.Builder
	rows, _ := markup["inline_keyboard"].([]any)
	for _, row := range rows {
		for _, btn := range row.([]any) {
			if m, ok := btn.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					b.WriteString(text + "\n")
				}
			}
		}
	}
	return b.String()
}
