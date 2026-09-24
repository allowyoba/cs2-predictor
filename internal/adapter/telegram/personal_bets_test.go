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

	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:bets:0")); err != nil {
		t.Fatal(err)
	}
	if personal.betsChatID != nil {
		t.Fatalf("expected an all-chats lookup (nil chat filter), got %v", personal.betsChatID)
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
	if !slices.Contains(cds, "pstats:bets:f::::1") {
		t.Fatalf("expected a next-page button, got %v", cds)
	}
}

// A three-way mixed history: two games, two chats, and all three result
// kinds represented, so each filter dimension (and their combination) has
// something to actually narrow down.
func mixedBetsFixture(now time.Time) []scoring.UserBet {
	return []scoring.UserBet{
		{
			PlayedAt: now.Add(-1 * time.Hour), ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2",
			FirstTeamName: "NAVI", SecondTeamName: "G2", Game: "cs2",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 0},
			Correct: true, Points: 3,
		},
		{
			PlayedAt: now.Add(-2 * time.Hour), ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2",
			FirstTeamName: "Spirit", SecondTeamName: "MOUZ", Game: "cs2",
			PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 1},
			Correct: true, Points: 1,
		},
		{
			PlayedAt: now.Add(-3 * time.Hour), ChatID: common.ChatID{Value: -1002}, ChatTitle: "Friends",
			FirstTeamName: "Vitality", SecondTeamName: "FaZe", Game: "cs2",
			PredictedScore: competition.MatchScore{First: 2, Second: 1}, ActualScore: competition.MatchScore{First: 1, Second: 2},
			Correct: false, Points: 0,
		},
		{
			PlayedAt: now.Add(-4 * time.Hour), ChatID: common.ChatID{Value: -1002}, ChatTitle: "Friends",
			FirstTeamName: "Team Liquid", SecondTeamName: "OG", Game: "dota2",
			PredictedScore: competition.MatchScore{First: 2, Second: 1}, ActualScore: competition.MatchScore{First: 2, Second: 1},
			Correct: true, Points: 3,
		},
	}
}

// Each filter dimension narrows the list on its own, and all three combine
// (an AND, not an OR): game+chat+kind together must leave exactly the one
// row that matches every axis.
func TestPrivateBets_FiltersCombine(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	personal.bets = mixedBetsFixture(now)

	cases := []struct {
		name    string
		data    string
		wantIn  []string
		wantOut []string
	}{
		{
			name:    "game only",
			data:    "pstats:bets:f:dota2:::0",
			wantIn:  []string{"Team Liquid", "OG"},
			wantOut: []string{"NAVI", "Vitality"},
		},
		{
			name:    "chat only",
			data:    "pstats:bets:f::-1001::0",
			wantIn:  []string{"NAVI", "Spirit"},
			wantOut: []string{"Vitality", "Team Liquid"},
		},
		{
			name:    "result kind only",
			data:    "pstats:bets:f:::miss:0",
			wantIn:  []string{"Vitality", "FaZe"},
			wantOut: []string{"NAVI", "Team Liquid"},
		},
		{
			name:    "game and chat and kind combined",
			data:    "pstats:bets:f:cs2:-1001:winner:0",
			wantIn:  []string{"Spirit", "MOUZ"},
			wantOut: []string{"NAVI", "Vitality", "Team Liquid"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			*calls = nil
			if err := handler.handlePrivateCallback(context.Background(), betsCB(tc.data)); err != nil {
				t.Fatal(err)
			}
			text := lastText(*calls)
			for _, want := range tc.wantIn {
				if !strings.Contains(text, want) {
					t.Fatalf("expected %q in filtered screen, got %q", want, text)
				}
			}
			for _, unwanted := range tc.wantOut {
				if strings.Contains(text, unwanted) {
					t.Fatalf("expected %q to be filtered out, got %q", unwanted, text)
				}
			}
		})
	}
}

// A filter combination that matches nothing must say so clearly rather than
// rendering a blank list — and must still offer the filter picker so the
// person can pick a different combination.
func TestPrivateBets_EmptyFilterCombinationShowsClearMessage(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	personal.bets = mixedBetsFixture(now)

	// dota2 + miss: no such row in the fixture.
	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:bets:f:dota2::miss:0")); err != nil {
		t.Fatal(err)
	}
	text := lastText(*calls)
	if !strings.Contains(text, ru(t, "bets.empty_filtered")) {
		t.Fatalf("expected the filtered-empty message, got %q", text)
	}
	if strings.Contains(text, ru(t, "bets.empty")) && !strings.Contains(text, ru(t, "bets.empty_filtered")) {
		t.Fatalf("expected the filtered-empty message, not the no-history one, got %q", text)
	}
}

// With no bets at all, the plain "no bets yet" message shows instead of the
// filtered-empty one — there's no filter in play to blame.
func TestPrivateBets_NoBetsAtAllUsesPlainEmptyMessage(t *testing.T) {
	handler, _, calls := setupInsightsTest(t, nil)
	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:bets:f::::0")); err != nil {
		t.Fatal(err)
	}
	text := lastText(*calls)
	if !strings.Contains(text, ru(t, "bets.empty")) {
		t.Fatalf("expected the plain empty message, got %q", text)
	}
}

// The filter callback round-trips through build/parse for every combination
// of set/unset segments, and rejects malformed data.
func TestBetsFilterCallback_RoundTrips(t *testing.T) {
	chatID := common.ChatID{Value: -1001}
	cases := []struct {
		name   string
		game   competition.GameCode
		chatID *common.ChatID
		kind   scoring.BetResultKind
		page   int
	}{
		{name: "all unset", page: 2},
		{name: "game set", game: "cs2", page: 0},
		{name: "chat set", chatID: &chatID, page: 1},
		{name: "kind set", kind: scoring.BetResultExact, page: 0},
		{name: "all set", game: "dota2", chatID: &chatID, kind: scoring.BetResultMiss, page: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := betsFilterCallback(tc.game, tc.chatID, tc.kind, tc.page)
			gotGame, gotChat, gotKind, gotPage, err := parseBetsFilterCallback(strings.TrimPrefix(data, "pstats:bets:f:"))
			if err != nil {
				t.Fatalf("unexpected parse error for %q: %v", data, err)
			}
			if gotGame != tc.game || gotKind != tc.kind || gotPage != tc.page {
				t.Fatalf("round-trip mismatch: got game=%q chat=%v kind=%q page=%d", gotGame, gotChat, gotKind, gotPage)
			}
			if (tc.chatID == nil) != (gotChat == nil) || (tc.chatID != nil && *tc.chatID != *gotChat) {
				t.Fatalf("chat round-trip mismatch: want %v got %v", tc.chatID, gotChat)
			}
		})
	}

	if _, _, _, _, err := parseBetsFilterCallback("cs2:-1001:winner"); err == nil {
		t.Fatal("expected an error for a callback missing its page segment")
	}
	if _, _, _, _, err := parseBetsFilterCallback("cs2:not-a-number:winner:0"); err == nil {
		t.Fatal("expected an error for a non-numeric chat id")
	}
}

// The old bare "pstats:bets:<page>" and chat-scoped "pstats:bets:c:..."
// callback formats sent on already-delivered messages must keep routing
// after this change, unfiltered, exactly as before.
func TestPrivateBets_LegacyCallbacksStillRoute(t *testing.T) {
	handler, personal, calls := setupInsightsTest(t, nil)
	now := time.Now()
	personal.bets = mixedBetsFixture(now)

	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:bets:0")); err != nil {
		t.Fatal(err)
	}
	if personal.betsChatID != nil {
		t.Fatalf("expected the legacy bare callback to still mean all chats, got %v", personal.betsChatID)
	}
	text := lastText(*calls)
	for _, want := range []string{"NAVI", "Vitality", "Team Liquid"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected the legacy all-chats callback to still show every bet, got %q", text)
		}
	}

	*calls = nil
	if err := handler.handlePrivateCallback(context.Background(), betsCB("pstats:bets:c:-1001:0:0")); err != nil {
		t.Fatal(err)
	}
	if personal.betsChatID == nil || personal.betsChatID.Value != -1001 {
		t.Fatalf("expected the legacy chat-scoped callback to still scope the repository lookup, got %v", personal.betsChatID)
	}
	text = lastText(*calls)
	if strings.Contains(text, "Vitality") {
		t.Fatalf("expected the legacy chat-scoped callback to still filter out other chats, got %q", text)
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
