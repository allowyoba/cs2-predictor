package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

type stubChatLeaderboard struct {
	standings []scoring.ChatStanding
	gotPeriod scoring.StatsPeriod
}

func (s *stubChatLeaderboard) ChatLeaderboard(_ context.Context, period scoring.StatsPeriod) ([]scoring.ChatStanding, error) {
	s.gotPeriod = period
	return s.standings, nil
}

func TestChatLeaderboardHandler_ListsChatsRankedBestFirst(t *testing.T) {
	repo := &stubChatLeaderboard{standings: []scoring.ChatStanding{
		{ChatID: common.ChatID{Value: 1}, ChatTitle: "Alpha", Points: 40, CorrectPredictions: 8, Predictions: 10, Rank: 1},
		{ChatID: common.ChatID{Value: 2}, ChatTitle: "Beta", Points: 20, CorrectPredictions: 4, Predictions: 10, Rank: 2},
	}}
	handler := chatLeaderboardHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), repo)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/chats/leaderboard",
		signInitData(t, `{"id":7,"first_name":"Root"}`, common.SystemUTCClock().Now())))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	var body chatLeaderboardDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 || body.Chats[0].Chat != "Alpha" || body.Chats[0].Accuracy != 80 {
		t.Fatalf("unexpected body: %+v", body)
	}
	// No per-member data ever leaves this endpoint — only chat-level totals.
	if body.Chats[0].ID == 0 {
		t.Fatalf("expected the chat id to be carried, got %+v", body.Chats[0])
	}
}

func TestChatLeaderboardHandler_AppliesGameAndPeriodFilters(t *testing.T) {
	repo := &stubChatLeaderboard{}
	handler := chatLeaderboardHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), repo)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/chats/leaderboard?period=year&game=cs2",
		signInitData(t, `{"id":7,"first_name":"Root"}`, common.SystemUTCClock().Now())))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if repo.gotPeriod.Kind != scoring.PeriodYear {
		t.Fatalf("period = %+v, want a year period", repo.gotPeriod)
	}
	if repo.gotPeriod.Game != "CS2" {
		t.Fatalf("game = %q, want CS2", repo.gotPeriod.Game)
	}
}

func TestChatLeaderboardHandler_UnconfiguredAnswers503(t *testing.T) {
	handler := chatLeaderboardHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/chats/leaderboard",
		signInitData(t, `{"id":7,"first_name":"Root"}`, common.SystemUTCClock().Now())))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}
