package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

func TestSanitizeNickname_CollapsesWhitespaceAndBoundsLength(t *testing.T) {
	got, err := sanitizeNickname("  Alex \n \t The  Great\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Alex The Great" {
		t.Fatalf("got %q, want internal whitespace collapsed to single spaces", got)
	}

	if _, err := sanitizeNickname("   \n\t  "); err == nil {
		t.Fatal("a blank reply must be rejected rather than silently treated as a reset")
	}

	if _, err := sanitizeNickname(strings.Repeat("a", nicknameMaxRunes+1)); err == nil {
		t.Fatalf("a name over %d characters must be rejected", nicknameMaxRunes)
	}
	if _, err := sanitizeNickname(strings.Repeat("a", nicknameMaxRunes)); err != nil {
		t.Fatalf("exactly %d characters should be accepted: %v", nicknameMaxRunes, err)
	}
}

func TestPrivateStatsMenu_OffersRename(t *testing.T) {
	handler, _, calls := setupInsightsTest(t, nil)

	data := "pstats:menu"
	cb := &CallbackQuery{ID: "cb", From: User{ID: 42, FirstName: "Alex"},
		Message: &Message{MessageID: 3, Chat: Chat{ID: 42, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	found := false
	for _, cd := range cds {
		if cd == "pstats:rename" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a rename button on the personal stats menu, got %v", cds)
	}
}

func TestRenameMenu_ShowsUnsetUntilOneIsChosen(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	data := "pstats:rename"
	cb := &CallbackQuery{ID: "cb", From: User{ID: 42, FirstName: "Alex"},
		Message: &Message{MessageID: 3, Chat: Chat{ID: 42, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	text := lastText(*calls)
	if !strings.Contains(text, ru(t, "dm.rename_unset")) {
		t.Fatalf("expected the unset screen, got %q", text)
	}
	cds, _ := findKeyboardButtons(*calls)
	for _, cd := range cds {
		if cd == cbRenameReset() {
			t.Fatal("a reset button must not appear before any nickname is set")
		}
	}
}

func TestRename_PromptSetSaveAndReset_RoundTrips(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	userID := common.UserID{Value: 42}
	ctx := context.Background()

	// 1. Ask for the prompt.
	data := cbRenameAsk()
	cb := &CallbackQuery{ID: "cb", From: User{ID: 42, FirstName: "Alex"},
		Message: &Message{MessageID: 3, Chat: Chat{ID: 42, Type: "private"}}, Data: &data}
	if err := handler.handlePrivateCallback(ctx, cb); err != nil {
		t.Fatal(err)
	}
	prompt := lastText(*calls)
	if !strings.Contains(prompt, ru(t, "dm.rename_prompt")) {
		t.Fatalf("expected the rename prompt, got %q", prompt)
	}

	// 2. Reply to that exact prompt with a new name.
	*calls = nil
	promptText := ru(t, "dm.rename_prompt")
	reply := "  Captain   Clutch  "
	msg := &Message{
		MessageID: 4, Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42, FirstName: "Alex"},
		Text: &reply, ReplyToMessage: &Message{Text: &promptText},
	}
	if err := handler.handlePrivateMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	saved, err := chats.Nickname(ctx, userID)
	if err != nil || saved == nil || *saved != "Captain Clutch" {
		t.Fatalf("Nickname() = %v, %v; want \"Captain Clutch\"", saved, err)
	}
	confirmation := lastText(*calls)
	if !strings.Contains(confirmation, "Captain Clutch") {
		t.Fatalf("expected a confirmation naming the new nickname, got %q", confirmation)
	}

	// 3. It now shows on the leaderboard in place of the Telegram name —
	// this is a unit test of the rendering path only: the SQL-level
	// COALESCE(nickname, display_name) that makes this true for real data
	// is covered by TestScoringRepository_LeaderboardPrefersTheChosenNickname.
	handler.Scoring = &dataScoring{leaderboard: []scoring.UserStanding{
		{UserID: userID, DisplayName: "Captain Clutch", Rank: 1, Points: 10, Predictions: 3, CorrectPredictions: 2},
	}}
	*calls = nil
	groupSettings := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	if err := handler.renderLeaderboard(ctx, sendTarget(groupSettings.ChatID, nil), groupSettings, scoring.AllTime()); err != nil {
		t.Fatal(err)
	}
	if text := lastText(*calls); !strings.Contains(text, "Captain Clutch") {
		t.Fatalf("leaderboard does not show the chosen name: %q", text)
	}

	// 4. Reset returns to the DM menu wording.
	*calls = nil
	data = cbRenameReset()
	cb.Data = &data
	if err := handler.handlePrivateCallback(ctx, cb); err != nil {
		t.Fatal(err)
	}
	if saved, err := chats.Nickname(ctx, userID); err != nil || saved != nil {
		t.Fatalf("Nickname() after reset = %v, %v; want nil", saved, err)
	}
	if text := lastText(*calls); !strings.Contains(text, ru(t, "dm.rename_unset")) {
		t.Fatalf("expected the unset screen after reset, got %q", text)
	}
}

// Renaming has no chat context, unlike the events-search reply, so it
// must work for a plain player with no managed-chat DM session at all.
func TestRename_WorksWithoutAManagedChatSession(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)

	promptText := ru(t, "dm.rename_prompt")
	reply := "Regular Player"
	msg := &Message{
		MessageID: 4, Chat: Chat{ID: 99, Type: "private"}, From: &User{ID: 99, FirstName: "Regular"},
		Text: &reply, ReplyToMessage: &Message{Text: &promptText},
	}
	if err := handler.handlePrivateMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	saved, err := chats.Nickname(context.Background(), common.UserID{Value: 99})
	if err != nil || saved == nil || *saved != "Regular Player" {
		t.Fatalf("Nickname() = %v, %v; want \"Regular Player\" even with no managed chat", saved, err)
	}
	if len(*calls) == 0 {
		t.Fatal("expected a confirmation message")
	}
}

func TestRename_RejectsAnEmptyReplyWithoutClearingTheExistingName(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	ctx := context.Background()
	userID := common.UserID{Value: 42}
	if err := chats.SetNickname(ctx, userID, "Existing Name"); err != nil {
		t.Fatal(err)
	}

	promptText := ru(t, "dm.rename_prompt")
	reply := "   "
	msg := &Message{
		MessageID: 4, Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42, FirstName: "Alex"},
		Text: &reply, ReplyToMessage: &Message{Text: &promptText},
	}
	if err := handler.handlePrivateMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	saved, err := chats.Nickname(ctx, userID)
	if err != nil || saved == nil || *saved != "Existing Name" {
		t.Fatalf("Nickname() = %v, %v; a rejected blank reply must not touch the existing name", saved, err)
	}
	if text := lastText(*calls); !strings.Contains(text, ru(t, "error.generic")) {
		t.Fatalf("expected the generic error screen, got %q", text)
	}
}
