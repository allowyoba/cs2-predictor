package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// "My form" is one person's record across every chat they play in, but a
// person in several chats reasonably wants to know how they do in each one
// separately — this is the same per-chat breakdown idea as SplitByGame, one
// axis over.

func chatInsightPredictions(chatID common.ChatID, n, correct int, from time.Time) []scoring.UserPrediction {
	out := make([]scoring.UserPrediction, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, scoring.UserPrediction{
			PlayedAt: from.Add(-time.Duration(i) * time.Hour),
			TeamID:   common.NewTeamID(), TeamName: "team",
			Correct: i < correct, ChatID: chatID,
		})
	}
	return out
}

func renderInsightsWithChat(t *testing.T, predictions []scoring.UserPrediction, chatID *common.ChatID) (string, string) {
	t.Helper()
	server, calls := newRecordingServer(t)
	t.Cleanup(server.Close)
	h, _ := newTestHandler(t, server)
	h.Scoring = stubScoringWithInsights{predictions: predictions}
	if err := h.renderPersonalInsights(context.Background(), sendTarget(common.ChatID{Value: 7}, nil),
		common.UserID{Value: 7}, common.LocaleRU, "", chatID); err != nil {
		t.Fatal(err)
	}
	var markup []byte
	for i := len(*calls) - 1; i >= 0; i-- {
		if _, ok := (*calls)[i]["reply_markup"]; ok {
			markup, _ = json.Marshal((*calls)[i]["reply_markup"])
			break
		}
	}
	return lastText(*calls), string(markup)
}

func TestPersonalInsights_SplitsByChatOnlyWhenThereIsMoreThanOne(t *testing.T) {
	now := time.Now()
	oneChat := chatInsightPredictions(common.ChatID{Value: 1}, 12, 9, now)

	_, markup := renderInsightsWithChat(t, oneChat, nil)
	if strings.Contains(markup, "pstats:insights::") {
		t.Fatalf("one chat is one record — no chat picker belongs here: %s", markup)
	}

	twoChats := append(oneChat, chatInsightPredictions(common.ChatID{Value: 2}, 8, 2, now)...)
	_, markup = renderInsightsWithChat(t, twoChats, nil)
	if !strings.Contains(markup, "pstats:insights::1") || !strings.Contains(markup, "pstats:insights::2") {
		t.Fatalf("expected a button per chat, got %s", markup)
	}
}

func TestPersonalInsights_ChatFilterNarrowsTheCard(t *testing.T) {
	now := time.Now()
	chatA, chatB := common.ChatID{Value: 1}, common.ChatID{Value: 2}
	history := append(chatInsightPredictions(chatA, 10, 10, now),
		chatInsightPredictions(chatB, 4, 0, now)...)

	text, _ := renderInsightsWithChat(t, history, &chatB)
	if !strings.Contains(text, "0%") {
		t.Fatalf("expected chat B's own 0%% record, not the blended one: %q", text)
	}
}

// A button naming a chat the person has no history in any more must open
// the combined view, not an empty card — same fallback as a stale game.
func TestPersonalInsights_StaleChatFallsBackToTheCombinedView(t *testing.T) {
	stale := common.ChatID{Value: 999}
	text, _ := renderInsightsWithChat(t, chatInsightPredictions(common.ChatID{Value: 1}, 6, 3, time.Now()), &stale)

	if strings.Contains(text, ru(t, "insights.empty")) {
		t.Fatalf("expected the combined view rather than an empty card: %q", text)
	}
}
