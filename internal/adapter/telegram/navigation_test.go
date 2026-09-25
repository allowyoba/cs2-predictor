package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// TestMenuNavigation_EditsInPlaceAndOffersABackButton exercises a full
// tap-through: /menu (sends), tap "Статистика" (must edit, not send, and
// must carry a back button to the main menu), tap that back button
// (must edit back to the main menu, not accumulate a 3rd message).
func TestMenuNavigation_EditsInPlaceAndOffersABackButton(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

	text := "/menu"
	msg := &Message{MessageID: 1, Chat: Chat{ID: -1, Type: "group"}, Text: &text}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0]["__method"] != "sendMessage" {
		t.Fatalf("expected /menu to send exactly one new message, got %+v", *calls)
	}
	menuMessageID := int64(1) // the recording server always answers with message_id:1

	data := "menu:stats"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "A"}, Message: &Message{MessageID: menuMessageID, Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 { // the earlier /menu send + this tap's (edit + answer)
		t.Fatalf("expected exactly 3 calls total after the tap, got %d: %+v", len(*calls), *calls)
	}
	statsCall := (*calls)[1]
	if statsCall["__method"] != "editMessageText" {
		t.Fatalf("expected menu:stats to edit the existing message in place, got method %v", statsCall["__method"])
	}
	if statsCall["message_id"] != float64(menuMessageID) {
		t.Fatalf("expected the edit to target message_id %d, got %v", menuMessageID, statsCall["message_id"])
	}
	statsLabels := buttonLabels(t, statsCall)
	if !containsAll(statsLabels, ru(t, "nav.back")) {
		t.Fatalf("expected a back button on the stats menu, got %v", statsLabels)
	}

	// Now tap the back button.
	backData := "menu:main"
	backCB := &CallbackQuery{ID: "cb2", From: User{ID: 1, FirstName: "A"}, Message: &Message{MessageID: menuMessageID, Chat: Chat{ID: -1, Type: "group"}}, Data: &backData}
	if err := handler.handleCallback(context.Background(), backCB); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 5 { // the previous 3 + this tap's (edit + answer)
		t.Fatalf("expected 5 calls total after the back tap, got %d: %+v", len(*calls), *calls)
	}
	backCall := (*calls)[3]
	if backCall["__method"] != "editMessageText" {
		t.Fatalf("expected menu:main to edit the existing message in place, got method %v", backCall["__method"])
	}
}

// TestSettingsToggle_AnswersOnceViaToastNotTwice guards against a real
// class of bug: Telegram accepts exactly one answerCallbackQuery per
// callback query, so a branch that already toasted must tell handleCallback
// not to answer again. A second answer call fails, and a handler that
// swallows that failure would leave the toggle silently broken.
func TestSettingsToggle_AnswersOnceViaToastNotTwice(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	data := "settings:top_tier"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 5, Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	var answerCalls int
	var sawEdit bool
	for _, c := range *calls {
		switch c["__method"] {
		case "answerCallbackQuery":
			answerCalls++
			if text, _ := c["text"].(string); text == "" {
				t.Fatalf("expected the toggle's answerCallbackQuery to carry a confirmation text, got empty")
			}
		case "editMessageText":
			sawEdit = true
		}
	}
	if answerCalls != 1 {
		t.Fatalf("expected exactly 1 answerCallbackQuery call, got %d: %+v", answerCalls, *calls)
	}
	if !sawEdit {
		t.Fatalf("expected the settings view to be re-rendered in place, got %+v", *calls)
	}
}

func TestLeaderboardRows_AreCompactAndDoNotUseSpacePaddedColumns(t *testing.T) {
	rows := leaderboardRows([]scoring.UserStanding{
		{Rank: 1, DisplayName: "Очень длинное имя игрока", Points: 123},
		{Rank: 4, DisplayName: "Alex", Points: 9, ExactPredictions: 2, CorrectPredictions: 5, Predictions: 8},
	}, common.UserID{})
	if strings.Contains(rows, "                        ") {
		t.Fatalf("leaderboard must not use fixed-width space padding: %q", rows)
	}
	if !strings.Contains(rows, "🥇 <b>") || !strings.Contains(rows, "4. <b>Alex</b> <code>(2-3-3)</code> · <code>9</code>") {
		t.Fatalf("unexpected compact leaderboard format: %q", rows)
	}
}

func TestLeaderboardRows_ShowsTournamentWinsOnlyWhenPositive(t *testing.T) {
	rows := leaderboardRows([]scoring.UserStanding{
		{Rank: 1, DisplayName: "Alex", Points: 9, TournamentWins: 3},
		{Rank: 2, DisplayName: "Sam", Points: 5},
	}, common.UserID{})
	if !strings.Contains(rows, "⭐3") {
		t.Fatalf("expected the tournament-win count for Alex, got %q", rows)
	}
	if strings.Count(rows, "⭐") != 1 {
		t.Fatalf("Sam has no tournament wins and must not show a star: %q", rows)
	}
}

func TestLeaderboardRows_ShowsMovementWhenAvailable(t *testing.T) {
	prev := 6
	rows := leaderboardRows([]scoring.UserStanding{{Rank: 4, PreviousRank: &prev, DisplayName: "Alex", Points: 9}}, common.UserID{})
	if !strings.Contains(rows, "↑2") {
		t.Fatalf("expected rank movement, got %q", rows)
	}
}

func TestStatsBreakdown_SplitsExactOutcomeAndWrong(t *testing.T) {
	cases := []struct {
		exact, correct, total int
		want                  string
	}{
		{0, 0, 0, "(0-0-0)"},
		{2, 5, 8, "(2-3-3)"}, // 2 exact, 3 outcome-only, 3 wrong
		{4, 4, 4, "(4-0-0)"}, // every prediction exact
		{0, 0, 6, "(0-0-6)"}, // every prediction wrong
	}
	for _, c := range cases {
		if got := statsBreakdown(c.exact, c.correct, c.total); got != c.want {
			t.Fatalf("statsBreakdown(%d,%d,%d) = %q, want %q", c.exact, c.correct, c.total, got, c.want)
		}
	}
}

func TestStatsFilterLabel_MarksActiveChoice(t *testing.T) {
	if got := statsFilterLabel(true, "Month"); got != "✓ Month" {
		t.Fatalf("active label = %q", got)
	}
	if got := statsFilterLabel(false, "Month"); got != "Month" {
		t.Fatalf("inactive label = %q", got)
	}
}

func TestMatchStatusIcon_CoversImportantStates(t *testing.T) {
	cases := map[competition.MatchStatus]string{
		competition.MatchNotStarted: "🕒",
		competition.MatchRunning:    "🔴",
		competition.MatchFinished:   "✅",
		competition.MatchCancelled:  "❌",
		competition.MatchPostponed:  "⏸",
	}
	for status, want := range cases {
		if got := matchStatusIcon(status); got != want {
			t.Fatalf("matchStatusIcon(%q) = %q, want %q", status, got, want)
		}
	}
}
