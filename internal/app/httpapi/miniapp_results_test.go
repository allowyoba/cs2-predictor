package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The group leaderboard: the same read the bot's own /leaderboard command
// uses (ScoringRepository.Leaderboard), sliced by chat and by period.

type stubResults struct {
	rows   []scoring.UserStanding
	err    error
	chatID common.ChatID
	period scoring.StatsPeriod
}

func (s *stubResults) Leaderboard(_ context.Context, chatID common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	s.chatID, s.period = chatID, period
	return s.rows, s.err
}

func TestMiniappResults_RequiresAChat(t *testing.T) {
	results := &stubResults{}
	handler := resultsHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), results)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/results",
		signInitData(t, `{"id":7,"first_name":"Root"}`, time.Now())))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 without a selected chat: %s", recorder.Code, recorder.Body)
	}
}

func TestMiniappResults_RanksTheChatAndMarksTheViewer(t *testing.T) {
	now := time.Now()
	results := &stubResults{rows: []scoring.UserStanding{
		{UserID: common.UserID{Value: 7}, DisplayName: "Root", Rank: 1, Points: 12, CorrectPredictions: 6, Predictions: 8, ExactPredictions: 3, Tournaments: 2},
		{UserID: common.UserID{Value: 9}, DisplayName: "Ann", Rank: 2, Points: 5, CorrectPredictions: 2, Predictions: 4, Tournaments: 1},
	}}
	handler := resultsHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), results)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/results?chat=-100",
		signInitData(t, `{"id":7,"first_name":"Root"}`, now)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if results.chatID.Value != -100 {
		t.Fatalf("chat = %d, want -100", results.chatID.Value)
	}
	if results.period.Kind != scoring.PeriodAllTime {
		t.Fatalf("period = %v, want all-time by default", results.period.Kind)
	}

	var body resultsDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Participants != 2 || body.Predictions != 12 {
		t.Fatalf("participants/predictions = %d/%d, want 2/12", body.Participants, body.Predictions)
	}
	if body.YourRank != 1 || body.YourPoints != 12 {
		t.Fatalf("your rank/points = %d/%d, want 1/12", body.YourRank, body.YourPoints)
	}
	if !body.Rows[0].You || body.Rows[1].You {
		t.Fatal("only the viewer's own row should be marked")
	}
}

func TestMiniappResults_ParsesYearAndMonthPeriods(t *testing.T) {
	results := &stubResults{}
	handler := resultsHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), results)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/results?chat=-100&period=month&year=2025&month=6",
		signInitData(t, `{"id":7,"first_name":"Root"}`, time.Now())))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if results.period.Kind != scoring.PeriodMonth || results.period.Year != 2025 || results.period.Month != time.June {
		t.Fatalf("period = %+v, want June 2025", results.period)
	}
}

func TestMiniappResults_RejectsAnUnknownPeriod(t *testing.T) {
	results := &stubResults{}
	handler := resultsHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), results)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/results?chat=-100&period=decade",
		signInitData(t, `{"id":7,"first_name":"Root"}`, time.Now())))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown period: %s", recorder.Code, recorder.Body)
	}
}
