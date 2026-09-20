package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The history screen is a cross-chat feed, so every row has to carry what
// distinguishes it: the discipline and the tournament. Without those, two
// rows of team names read as duplicates.

type stubHistory struct {
	bets     []scoring.UserBet
	askedFor common.UserID
}

func (s *stubHistory) UserBets(_ context.Context, userID common.UserID, _ *common.ChatID, _ int) ([]scoring.UserBet, error) {
	s.askedFor = userID
	return s.bets, nil
}

func historyBet(game competition.GameCode, event string, correct bool, at time.Time) scoring.UserBet {
	predicted := competition.MatchScore{First: 2, Second: 0}
	actual := competition.MatchScore{First: 2, Second: 1}
	if !correct {
		actual = competition.MatchScore{First: 0, Second: 2}
	}
	return scoring.UserBet{
		PlayedAt: at, ChatTitle: "Прогнозы", FirstTeamName: "G2", SecondTeamName: "NAVI",
		PredictedScore: predicted, ActualScore: actual, Correct: correct, Points: 1,
		Game: game, EventName: event,
	}
}

func TestMiniappHistory_ServesTheSignedUsersOwnFeedWithContext(t *testing.T) {
	now := time.Now()
	history := &stubHistory{bets: []scoring.UserBet{
		historyBet(competition.GameCS2, "IEM Katowice", true, now),
		historyBet(competition.GameDota2, "The International", false, now.Add(-time.Hour)),
	}}
	handler := historyHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), history)
	initData := signInitData(t, `{"id":7,"first_name":"Root"}`, now)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/history", initData))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if history.askedFor.Value != 7 {
		t.Fatalf("read history for %d, want the signed user", history.askedFor.Value)
	}
	var body historyDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 {
		t.Fatalf("count = %d, want both entries", body.Count)
	}
	first := body.Entries[0]
	if first.Game != "CS2" || first.Event != "IEM Katowice" || first.Chat != "Прогнозы" {
		t.Fatalf("entry = %+v, want the discipline and tournament on the row", first)
	}
	if first.Predicted != "2:0" || first.Actual != "2:1" {
		t.Fatalf("entry = %+v, want both scorelines", first)
	}
}

func TestMiniappHistory_FiltersByGameAndResult(t *testing.T) {
	now := time.Now()
	history := &stubHistory{bets: []scoring.UserBet{
		historyBet(competition.GameCS2, "IEM", true, now),
		historyBet(competition.GameCS2, "IEM", false, now),
		historyBet(competition.GameDota2, "TI", true, now),
	}}
	handler := historyHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), history)
	initData := signInitData(t, `{"id":7,"first_name":"Root"}`, now)

	decode := func(query string) historyDTO {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/history"+query, initData))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", query, recorder.Code)
		}
		var body historyDTO
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	if got := decode("?game=cs2").Count; got != 2 {
		t.Fatalf("CS2 entries = %d, want 2", got)
	}
	if got := decode("?result=wrong").Count; got != 1 {
		t.Fatalf("wrong entries = %d, want 1", got)
	}
	if got := decode("?game=DOTA2&result=correct").Count; got != 1 {
		t.Fatalf("filters must compose, got %d", got)
	}

	// A filter the API does not know is refused rather than silently
	// ignored: a screen showing everything while a chip says otherwise is
	// worse than an error.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/history?result=maybe", initData))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// The feed is personal, so the same two gates apply as everywhere else.
func TestMiniappHistory_RequiresASignedLaunchAndAccess(t *testing.T) {
	history := &stubHistory{}
	handler := historyHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}), history)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/me/history", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/history",
		signInitData(t, `{"id":42,"first_name":"A"}`, time.Now())))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if history.askedFor.Value != 0 {
		t.Fatal("no history is read for somebody who may not see it")
	}
}
