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
	if !slices.Contains(cds, "pstats:bets:0") {
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
			PlayedAt: now.Add(-2 * time.Hour), ChatID: common.ChatID{Value: -1002}, ChatTitle: "Friends",
			FirstTeamName: "Vitality", SecondTeamName: "FaZe",
			PredictedScore: competition.MatchScore{First: 2, Second: 1}, ActualScore: competition.MatchScore{First: 1, Second: 2},
			Correct: false, Points: 0,
		},
	}

	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:bets:0")); err != nil {
		t.Fatal(err)
	}
	if personal.betsChatID != nil {
		t.Fatalf("expected an all-chats lookup (nil chat filter), got %v", personal.betsChatID)
	}
	text := lastText(*calls)
	for _, want := range []string{"NAVI", "G2", "Office CS2", "Vitality", "FaZe", "Friends", "2:0", "2:1", "1:2", "✅", "❌"} {
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

	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:bets:0")); err != nil {
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

	data := "pstats:bets:c:-1001:0:0"
	if err := handler.handlePrivateCallback(context.Background(), betsCB(data)); err != nil {
		t.Fatal(err)
	}
	if personal.betsChatID == nil || personal.betsChatID.Value != -1001 {
		t.Fatalf("expected the chat filter to be -1001, got %v", personal.betsChatID)
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

	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:bets:0")); err != nil {
		t.Fatal(err)
	}
	text := lastText(*calls)
	if !strings.Contains(text, "TeamA") || strings.Contains(text, "Team"+string(rune('A'+privateBetsPageSize))) {
		t.Fatalf("expected only the first page's bets to render, got %q", text)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "pstats:bets:1") {
		t.Fatalf("expected a next-page button, got %v", cds)
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
	if !slices.Contains(cds, "pstats:bets:c:-1001:0:0") {
		t.Fatalf("expected a bets button scoped to this chat, got %v", cds)
	}
}
