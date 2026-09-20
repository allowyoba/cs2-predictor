package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// One navigation contract, checked against the screens themselves rather
// than trusted to review: every screen that is not a root menu ends with a
// back button, alone on the last row, and that button leads one level up
// — not to whichever menu the code happened to name.
//
// This is what makes the bot feel like one product instead of a pile of
// screens: wherever somebody is, the way out is in the same place and does
// the same thing.

// screenUnderTest is one renderable screen plus where its back button must
// lead.
type screenUnderTest struct {
	name    string
	back    string
	render  func(t *testing.T, h *UpdateHandler, settings chat.Settings, target replyTarget) error
	private bool
}

func navigationScreens() []screenUnderTest {
	return []screenUnderTest{
		{
			name: "group help", back: "menu:main",
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.helpView(context.Background(), target, s.Locale, "menu:main", false)
			},
		},
		{
			name: "private help", back: "pstats:menu", private: true,
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.helpView(context.Background(), target, s.Locale, "pstats:menu", true)
			},
		},
		{
			name: "chat settings", back: "menu:main",
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.settingsView(context.Background(), target, s, false)
			},
		},
		{
			name: "games", back: "menu:settings",
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.gamesView(context.Background(), target, s)
			},
		},
		{
			name: "chat timezone", back: "menu:settings",
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.timezoneView(context.Background(), target, s)
			},
		},
		{
			name: "quiet hours", back: "menu:settings",
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.quietHoursView(context.Background(), target, s)
			},
		},
		{
			name: "personal timezone", back: "pstats:settings", private: true,
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.userTimezoneView(context.Background(), target, common.UserID{Value: 7}, s.Locale)
			},
		},
		{
			name: "tournaments", back: "menu:main",
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.eventMenu(context.Background(), target, s)
			},
		},
		{
			name: "add tournament", back: "menu:events",
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.eventAddMenu(context.Background(), target, s)
			},
		},
		{
			name: "personal form", back: "pstats:menu", private: true,
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				h.Scoring = stubScoringWithInsights{}
				return h.renderPersonalInsights(context.Background(), target, common.UserID{Value: 7}, s.Locale, "")
			},
		},
		{
			// Opened from the system panel, so it returns there — this one
			// used to drop the reader into the personal dashboard, a
			// section they were never in.
			name: "team match review", back: "hub:system", private: true,
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				h.TeamMatchOperatorChatIDs = []int64{7}
				h.TeamMatches = emptyTeamMatches{}
				return h.teamMatchQueueMenu(context.Background(), target, common.UserID{Value: 7}, s.Locale, 0)
			},
		},
		{
			name: "ideas", back: "pstats:settings", private: true,
			render: func(t *testing.T, h *UpdateHandler, s chat.Settings, target replyTarget) error {
				return h.suggestionMenu(context.Background(), target, s.Locale)
			},
		},
	}
}

func TestNavigation_EveryScreenEndsWithOneBackButtonOneLevelUp(t *testing.T) {
	for _, screen := range navigationScreens() {
		t.Run(screen.name, func(t *testing.T) {
			server, calls := newRecordingServer(t)
			defer server.Close()
			h, chats := newTestHandler(t, server)
			settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
			if _, err := chats.Save(context.Background(), settings); err != nil {
				t.Fatal(err)
			}
			target := sendTarget(settings.ChatID, nil)
			if screen.private {
				target = sendTarget(common.ChatID{Value: 7}, nil)
			}

			if err := screen.render(t, h, settings, target); err != nil {
				t.Fatal(err)
			}

			var rendered map[string]any
			for _, c := range *calls {
				if _, ok := c["reply_markup"]; ok {
					rendered = c
				}
			}
			if rendered == nil {
				t.Fatal("the screen rendered no keyboard at all")
			}
			kb, _ := rendered["reply_markup"].(map[string]any)
			rows, _ := kb["inline_keyboard"].([]any)
			if len(rows) == 0 {
				t.Fatal("the screen rendered an empty keyboard")
			}
			last, _ := rows[len(rows)-1].([]any)
			if len(last) != 1 {
				t.Fatalf("the back button must sit alone on the last row, got %d buttons there", len(last))
			}
			btn, _ := last[0].(map[string]any)
			label, _ := btn["text"].(string)
			if !strings.Contains(label, ru(t, "nav.back")) {
				t.Fatalf("the last row is %q, not the back button", label)
			}
			if got := btn["callback_data"]; got != screen.back {
				t.Fatalf("back leads to %v, want %q — one level up, not wherever the code happened to name", got, screen.back)
			}
		})
	}
}

// emptyTeamMatches is a review queue with nothing in it — enough for the
// navigation contract, which is about the way out of a screen rather than
// what it lists.
type emptyTeamMatches struct{ enrichment.TeamMatchRepository }

func (emptyTeamMatches) ListPending(context.Context, int) ([]enrichment.TeamMatchRequest, error) {
	return nil, nil
}
