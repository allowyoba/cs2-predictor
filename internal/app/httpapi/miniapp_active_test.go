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

// The two screens that are not about the past: what is running right now,
// and what has been won — the second of which belongs to a chat, because
// first place among four friends and among forty are different things.

type stubActive struct {
	active    []scoring.ActivePrediction
	medals    []scoring.MedalTally
	standings []scoring.UserChatStanding
}

func (s *stubActive) ActivePredictions(context.Context, common.UserID, int) ([]scoring.ActivePrediction, error) {
	return s.active, nil
}
func (s *stubActive) UserMedals(context.Context, common.UserID) ([]scoring.MedalTally, error) {
	return s.medals, nil
}
func (s *stubActive) UserChatStats(context.Context, common.UserID) ([]scoring.UserChatStanding, error) {
	return s.standings, nil
}

func TestMiniappActive_ShowsWhatIsStillRunningWithItsBroadcast(t *testing.T) {
	now := time.Now()
	soon := now.Add(2 * time.Hour)
	started := now.Add(-30 * time.Minute)
	active := &stubActive{active: []scoring.ActivePrediction{
		{
			ChatTitle: "Прогнозы", Game: competition.GameCS2, EventName: "IEM",
			ScheduledAt: &soon, ClosesAt: soon, FirstTeamName: "G2", SecondTeamName: "NAVI",
			PredictedScore: competition.MatchScore{First: 2, Second: 0},
			StreamURL:      "https://twitch.tv/esl_csgo",
		},
		{
			ChatTitle: "Прогнозы", Game: competition.GameDota2, EventName: "TI",
			ScheduledAt: &started, ClosesAt: started, FirstTeamName: "Spirit", SecondTeamName: "Tundra",
			PredictedScore: competition.MatchScore{First: 2, Second: 1},
		},
	}}
	handler := activeHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), active)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/active",
		signInitData(t, `{"id":7,"first_name":"Root"}`, now)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	var body activeDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 {
		t.Fatalf("count = %d, want both", body.Count)
	}
	if body.Entries[0].Stream != "https://twitch.tv/esl_csgo" {
		t.Fatalf("expected the broadcast on the upcoming match, got %+v", body.Entries[0])
	}
	if body.Entries[0].Live {
		t.Fatal("a match two hours away is not under way")
	}
	// A match already started: the poll is closed and there is nothing to
	// do but watch, which the screen says rather than implying it is still
	// open.
	if !body.Entries[1].Live {
		t.Fatalf("expected the started match to be marked live, got %+v", body.Entries[1])
	}
	if body.Entries[1].Stream != "" {
		t.Fatal("no broadcast published means no link, not an empty one")
	}
}

func TestMiniappChats_ReportsMedalsWhereTheyWereWon(t *testing.T) {
	now := time.Now()
	first, second := common.ChatID{Value: -1}, common.ChatID{Value: -2}
	active := &stubActive{
		standings: []scoring.UserChatStanding{
			{ChatID: first, ChatTitle: "Друзья", Points: 40, CorrectPredictions: 8, Predictions: 10, Tournaments: 2},
			{ChatID: second, ChatTitle: "Работа", Points: 5, CorrectPredictions: 1, Predictions: 4, Tournaments: 1},
		},
		medals: []scoring.MedalTally{{ChatID: first, ChatTitle: "Друзья", Medals: scoring.MedalCount{Gold: 2, Bronze: 1}}},
	}
	handler := chatsHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), active)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/chats",
		signInitData(t, `{"id":7,"first_name":"Root"}`, now)))

	var body chatsDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 {
		t.Fatalf("count = %d, want both chats", body.Count)
	}
	if body.Chats[0].Gold != 2 || body.Chats[0].Bronze != 1 || body.Chats[0].Accuracy != 80 {
		t.Fatalf("chat = %+v, want its own medals and accuracy", body.Chats[0])
	}
	// A chat where nothing was won still appears: the record is the point,
	// and an empty cabinet is an honest answer.
	if body.Chats[1].Gold != 0 || body.Chats[1].Predictions != 4 {
		t.Fatalf("chat = %+v, want it listed without medals", body.Chats[1])
	}
}

// Both are personal, so both sit behind the same two gates.
func TestMiniappActiveAndChats_RequireASignedLaunchAndAccess(t *testing.T) {
	deps := miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{})
	for name, handler := range map[string]http.Handler{
		"active": activeHandler(deps, &stubActive{}),
		"chats":  chatsHandler(deps, &stubActive{}),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/miniapp/v1/me/"+name, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", name, recorder.Code)
		}
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/"+name,
			signInitData(t, `{"id":42,"first_name":"A"}`, time.Now())))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", name, recorder.Code)
		}
	}
}
