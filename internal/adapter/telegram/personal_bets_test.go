package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

func betsCB(data string) *CallbackQuery {
	return &CallbackQuery{ID: "cb", From: User{ID: 42, FirstName: "Alex"},
		Message: &Message{MessageID: 3, Chat: Chat{ID: 42, Type: "private"}}, Data: &data}
}

func TestPrivateStatsMenu_OffersBets(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	personal.months = []scoring.StatsMonth{{Year: 2026, Month: time.September}}

	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:menu")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, betsCallback(betsFilter{}, 0, 0)) {
		t.Fatalf("expected a bets button on the personal stats menu, got %v", cds)
	}
}

func TestPrivateBets_ListsEverySettledBetAcrossChats(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	personal.bets = []scoring.UserBet{
		{
			PlayedAt: now.Add(-time.Hour), ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2",
			FirstTeamName: "NAVI", SecondTeamName: "G2",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 0},
			Correct: true, Points: 3,
		},
		{
			// Read the winner, missed the scoreline: worth a point, and
			// worth its own glyph.
			PlayedAt: now.Add(-90 * time.Minute), ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2",
			FirstTeamName: "Spirit", SecondTeamName: "MOUZ",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 1},
			Correct: true, Points: 1,
		},
		{
			PlayedAt: now.Add(-2 * time.Hour), ChatID: common.ChatID{Value: -1002}, ChatTitle: "Friends",
			FirstTeamName: "Vitality", SecondTeamName: "FaZe",
			PredictedScore: competition.MatchScore{First: 2, Second: 1}, ActualScore: competition.MatchScore{First: 1, Second: 2},
			Correct: false, Points: 0,
		},
	}

	if err := handler.handlePrivateCallback(context.Background(), betsCB(betsCallback(betsFilter{}, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if personal.betsChatID != nil {
		t.Fatalf("expected an unscoped read (chat filtering happens in memory), got %v", personal.betsChatID)
	}
	text := lastText(*calls)
	// Three outcomes, three markers: an exact scoreline is worth more
	// points than reading the winner, and a single tick for both hid the
	// harder result on the screen that lists it.
	for _, want := range []string{"NAVI", "G2", "Office CS2", "Vitality", "FaZe", "Friends", "2:0", "2:1", "1:2", "🎯", "✅", "❌"} {
		if !strings.Contains(text, want) {
			t.Fatalf("bets screen %q is missing %q", text, want)
		}
	}
	if !strings.Contains(text, ru(t, "bets.line_points", 3)) {
		t.Fatalf("expected the correct bet's points to be shown, got %q", text)
	}
}

func TestPrivateBets_NoSettledBetsSaysSo(t *testing.T) {
	handler, _, calls := setupInsightsTest(t, nil)

	if err := handler.handlePrivateCallback(context.Background(), betsCB(betsCallback(betsFilter{}, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if text := lastText(*calls); !strings.Contains(text, ru(t, "bets.empty")) {
		t.Fatalf("expected the empty-bets screen, got %q", text)
	}
}

// A page of settled bets from one specific chat must not repeat that chat's
// own name on every row (it's implied by the screen you navigated in from),
// and must not include bets placed in a different chat.
func TestPrivateBets_ChatScopedListOmitsTheChatNameAndFiltersOutOtherChats(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	personal.bets = []scoring.UserBet{
		{
			PlayedAt: now, ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2",
			FirstTeamName: "NAVI", SecondTeamName: "G2",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 0},
			Correct: true, Points: 3,
		},
		{
			PlayedAt: now, ChatID: common.ChatID{Value: -1002}, ChatTitle: "Friends",
			FirstTeamName: "Vitality", SecondTeamName: "FaZe",
			PredictedScore: competition.MatchScore{First: 2, Second: 1}, ActualScore: competition.MatchScore{First: 1, Second: 2},
			Correct: false, Points: 0,
		},
	}

	chatID := common.ChatID{Value: -1001}
	data := betsCallback(betsFilter{ChatID: &chatID}, 0, 0)
	if err := handler.handlePrivateCallback(context.Background(), betsCB(data)); err != nil {
		t.Fatal(err)
	}
	if personal.betsChatID != nil {
		t.Fatalf("expected an unscoped read (chat filtering happens in memory), got %v", personal.betsChatID)
	}
	text := lastText(*calls)
	if !strings.Contains(text, "NAVI") || !strings.Contains(text, "G2") {
		t.Fatalf("expected the chat's own bet, got %q", text)
	}
	if strings.Contains(text, "Vitality") || strings.Contains(text, "Office CS2") {
		t.Fatalf("expected no other chat's bet and no repeated chat name, got %q", text)
	}

	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "pstats:chat:-1001:0") {
		t.Fatalf("expected the back button to return to the chat-detail screen, got %v", cds)
	}
	// The chat filter row must show which chat is active.
	if !slices.Contains(cds, betsCallback(betsFilter{ChatID: &chatID}, 0, 0)) {
		t.Fatalf("expected the selected chat's own chip callback to still be offered, got %v", cds)
	}
}

// More bets than fit on one page must paginate: a nav row shows up, and the
// first page shows only the newest privateBetsPageSize of them.
func TestPrivateBets_PaginatesOverflow(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	var bets []scoring.UserBet
	for i := 0; i < privateBetsPageSize+2; i++ {
		bets = append(bets, scoring.UserBet{
			PlayedAt: now.Add(-time.Duration(i) * time.Hour), ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2",
			FirstTeamName: "Team" + string(rune('A'+i)), SecondTeamName: "Rival",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 0},
			Correct: true, Points: 1,
		})
	}
	personal.bets = bets

	if err := handler.handlePrivateCallback(context.Background(), betsCB(betsCallback(betsFilter{}, 0, 0))); err != nil {
		t.Fatal(err)
	}
	text := lastText(*calls)
	if !strings.Contains(text, "TeamA") || strings.Contains(text, "Team"+string(rune('A'+privateBetsPageSize))) {
		t.Fatalf("expected only the first page's bets to render, got %q", text)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, betsCallback(betsFilter{}, 0, 1)) {
		t.Fatalf("expected a next-page button, got %v", cds)
	}
}

// The game and result filter rows only appear once there is more than one
// value to choose between, and only combine — narrowing by game must not
// also clear the chat or result the person already picked.
func TestPrivateBets_GameAndResultFiltersCombineWithChat(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	chatA := common.ChatID{Value: -1001}
	chatB := common.ChatID{Value: -1002}
	personal.bets = []scoring.UserBet{
		{
			PlayedAt: now, ChatID: chatA, ChatTitle: "Office CS2", Game: competition.GameCS2,
			FirstTeamName: "NAVI", SecondTeamName: "G2",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 0},
			Correct: true, Points: 3,
		},
		{
			PlayedAt: now, ChatID: chatA, ChatTitle: "Office CS2", Game: competition.GameDota2,
			FirstTeamName: "Spirit", SecondTeamName: "OG",
			PredictedScore: competition.MatchScore{First: 2, Second: 1}, ActualScore: competition.MatchScore{First: 0, Second: 2},
			Correct: false, Points: 0,
		},
		{
			PlayedAt: now, ChatID: chatB, ChatTitle: "Friends", Game: competition.GameCS2,
			FirstTeamName: "Vitality", SecondTeamName: "FaZe",
			PredictedScore: competition.MatchScore{First: 2, Second: 1}, ActualScore: competition.MatchScore{First: 1, Second: 2},
			Correct: false, Points: 0,
		},
	}

	filter := betsFilter{ChatID: &chatA, Game: competition.GameCS2, Result: "correct"}
	if err := handler.handlePrivateCallback(context.Background(), betsCB(betsCallback(filter, 0, 0))); err != nil {
		t.Fatal(err)
	}
	text := lastText(*calls)
	if !strings.Contains(text, "NAVI") {
		t.Fatalf("expected the one bet matching every filter, got %q", text)
	}
	if strings.Contains(text, "Spirit") || strings.Contains(text, "Vitality") {
		t.Fatalf("expected bets outside the combined filter to be excluded, got %q", text)
	}

	cds, _ := findKeyboardButtons(*calls)
	// Every active chip carries its own selection forward: switching game
	// must not drop the chat or result filter, and vice versa.
	dota := betsCallback(betsFilter{ChatID: &chatA, Game: competition.GameDota2, Result: "correct"}, 0, 0)
	wrong := betsCallback(betsFilter{ChatID: &chatA, Game: competition.GameCS2, Result: "wrong"}, 0, 0)
	if !slices.Contains(cds, dota) {
		t.Fatalf("expected switching game to keep the chat and result filters, got %v", cds)
	}
	if !slices.Contains(cds, wrong) {
		t.Fatalf("expected switching result to keep the chat and game filters, got %v", cds)
	}
	// The active game/result chip must be marked, the inactive ones not.
	if !slices.Contains(cds, betsCallback(betsFilter{ChatID: &chatA, Result: "correct"}, 0, 0)) {
		t.Fatalf("expected an 'all games' chip preserving the other filters, got %v", cds)
	}
}

// A filter combination that matches nothing must say so plainly, while
// keeping the filter chips on screen so the person can loosen them rather
// than being stuck on a dead end.
func TestPrivateBets_NoMatchesForFilterCombinationSaysSo(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	chatA := common.ChatID{Value: -1001}
	chatB := common.ChatID{Value: -1002}
	personal.bets = []scoring.UserBet{
		{
			PlayedAt: now, ChatID: chatA, ChatTitle: "Office CS2", Game: competition.GameCS2,
			FirstTeamName: "NAVI", SecondTeamName: "G2",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 0},
			Correct: true, Points: 3,
		},
		{
			PlayedAt: now, ChatID: chatB, ChatTitle: "Friends", Game: competition.GameDota2,
			FirstTeamName: "Spirit", SecondTeamName: "OG",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 0, Second: 2},
			Correct: false, Points: 0,
		},
	}

	// A legitimate combination — both the chat and the game exist on their
	// own, just never together.
	filter := betsFilter{ChatID: &chatA, Game: competition.GameDota2}
	if err := handler.handlePrivateCallback(context.Background(), betsCB(betsCallback(filter, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if text := lastText(*calls); !strings.Contains(text, ru(t, "bets.empty")) {
		t.Fatalf("expected the empty-bets screen for a filter matching nothing, got %q", text)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, betsCallback(betsFilter{ChatID: &chatA}, 0, 0)) {
		t.Fatalf("expected the 'all games' chip to still be offered so the filter can be loosened, got %v", cds)
	}
}

// A stale filter (a chat the person is no longer in, a game they never
// predicted) must degrade to "all" rather than a screen stuck on an
// impossible slice forever.
func TestPrivateBets_StaleFilterDegradesToAll(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	personal.bets = []scoring.UserBet{
		{
			PlayedAt: now, ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2", Game: competition.GameCS2,
			FirstTeamName: "NAVI", SecondTeamName: "G2",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 0},
			Correct: true, Points: 3,
		},
	}

	staleChat := common.ChatID{Value: -9999}
	filter := betsFilter{ChatID: &staleChat, Game: competition.GameDota2}
	if err := handler.handlePrivateCallback(context.Background(), betsCB(betsCallback(filter, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastText(*calls), "NAVI") {
		t.Fatalf("expected the stale filter to fall back to the full list, got %q", lastText(*calls))
	}
}

func TestRenderPrivateChatStats_OffersBetsForThatChat(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	personal.chats = []scoring.UserChatStanding{
		{ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2", Points: 10, CorrectPredictions: 3, Predictions: 5, Tournaments: 2},
	}

	data := "pstats:chat:-1001:0"
	if err := handler.handlePrivateCallback(context.Background(), betsCB(data)); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	chatID := common.ChatID{Value: -1001}
	if !slices.Contains(cds, betsCallback(betsFilter{ChatID: &chatID}, 0, 0)) {
		t.Fatalf("expected a bets button scoped to this chat, got %v", cds)
	}
}
