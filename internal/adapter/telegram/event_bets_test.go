package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

func newEventBetsTestHandler(t *testing.T) (*UpdateHandler, *[]map[string]any, chat.Settings, common.EventID) {
	t.Helper()
	srv, calls := newRecordingServer(t)
	t.Cleanup(srv.Close)
	handler, chats := newTestHandler(t, srv)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)
	eventID := common.NewEventID()
	return handler, calls, settings, eventID
}

// TestRenderEventBets_ShowsOwnMatchesScopedToTheTournament covers the "my
// results in this tournament" half of the feature: it must call
// UserBetsForEvent (not the all-chats/all-time UserBets) and render the
// per-match lines it returns.
func TestRenderEventBets_ShowsOwnMatchesScopedToTheTournament(t *testing.T) {
	handler, calls, settings, eventID := newEventBetsTestHandler(t)
	viewer := common.UserID{Value: 42}
	personal := &personalDataScoring{
		dataScoring: &dataScoring{},
		bets: []scoring.UserBet{
			{
				PlayedAt: time.Now(), ChatID: settings.ChatID, ChatTitle: "Office CS2",
				FirstTeamName: "Spirit", SecondTeamName: "NAVI",
				PredictedScore: competition.MatchScore{First: 2, Second: 0}, ActualScore: competition.MatchScore{First: 2, Second: 0},
				Correct: true, Points: 2,
			},
		},
	}
	handler.Scoring = personal

	target := sendTarget(settings.ChatID, nil)
	if err := handler.renderEventBets(context.Background(), target, settings, eventID, viewer, "", viewer, "stats:event:x"); err != nil {
		t.Fatal(err)
	}
	if personal.betsChatID == nil || *personal.betsChatID != settings.ChatID {
		t.Fatalf("expected UserBetsForEvent to be scoped to the chat, got %+v", personal.betsChatID)
	}
	body, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(body, "Spirit") || !strings.Contains(body, "NAVI") {
		t.Fatalf("expected the match to be rendered, got %q", body)
	}
	if !strings.Contains(body, ru(t, "evbets.title_mine")) {
		t.Fatalf("expected the 'my matches' title, got %q", body)
	}
}

// TestRenderEventBets_ShowsAnotherParticipantsMatchesWithTheirName covers
// the "table for a chosen participant" half — the title must name the
// subject, not read as if it were the viewer's own results.
func TestRenderEventBets_ShowsAnotherParticipantsMatchesWithTheirName(t *testing.T) {
	handler, calls, settings, eventID := newEventBetsTestHandler(t)
	viewer := common.UserID{Value: 42}
	subject := common.UserID{Value: 99}
	personal := &personalDataScoring{
		dataScoring: &dataScoring{},
		bets: []scoring.UserBet{
			{PlayedAt: time.Now(), ChatID: settings.ChatID, FirstTeamName: "FaZe", SecondTeamName: "G2",
				PredictedScore: competition.MatchScore{First: 1, Second: 2}, ActualScore: competition.MatchScore{First: 1, Second: 2}, Points: 2},
		},
	}
	handler.Scoring = personal

	target := sendTarget(settings.ChatID, nil)
	if err := handler.renderEventBets(context.Background(), target, settings, eventID, subject, "Другой участник", viewer, "stats:evpick:x:0"); err != nil {
		t.Fatal(err)
	}
	body, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(body, "Другой участник") {
		t.Fatalf("expected the subject's name in the title, got %q", body)
	}
	if strings.Contains(body, ru(t, "evbets.title_mine")) {
		t.Fatalf("must not use the 'my matches' title for someone else's results, got %q", body)
	}
}

// TestRenderEventParticipantPicker_ListsEveryLeaderboardEntryAsAButton
// covers the participant picker: every standing becomes its own button
// carrying that participant's id, so tapping it opens their table.
func TestRenderEventParticipantPicker_ListsEveryLeaderboardEntryAsAButton(t *testing.T) {
	handler, calls, settings, eventID := newEventBetsTestHandler(t)
	handler.Scoring = &dataScoring{leaderboard: []scoring.UserStanding{
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", Points: 9, Rank: 1},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", Points: 5, Rank: 2},
	}}

	target := sendTarget(settings.ChatID, nil)
	if err := handler.renderEventParticipantPicker(context.Background(), target, settings, eventID, 0, "stats:event:x"); err != nil {
		t.Fatal(err)
	}
	labels := buttonLabels(t, (*calls)[0])
	if !containsAll(labels, "Alex", "Sam") {
		t.Fatalf("expected a button per participant, got %v", labels)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !containsPrefix(cds, eventParticipantCallback(eventID, common.UserID{Value: 1})) {
		t.Fatalf("expected a callback targeting participant 1, got %v", cds)
	}
}
