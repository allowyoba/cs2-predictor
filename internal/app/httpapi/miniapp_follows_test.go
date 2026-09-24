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

type stubChatFacts struct {
	facts    []scoring.PredictionFact
	askedFor []common.ChatID
}

func (s *stubChatFacts) ChatPredictionFacts(_ context.Context, chatIDs []common.ChatID, _ time.Time, _ int) ([]scoring.PredictionFact, error) {
	s.askedFor = chatIDs
	return s.facts, nil
}

func TestMiniappNominations_ReadsOnlyTheViewersChatsAndMarksThem(t *testing.T) {
	now := time.Now()
	var facts []scoring.PredictionFact
	for i := 0; i < 12; i++ {
		facts = append(facts, scoring.PredictionFact{UserID: common.UserID{Value: 7}, UserName: "Me",
			ChatID: common.ChatID{Value: -1}, PlayedAt: now.Add(time.Duration(-i) * time.Hour), Correct: true})
	}
	store := &stubChatFacts{facts: facts}
	active := &stubActive{standings: []scoring.UserChatStanding{{ChatID: common.ChatID{Value: -1}, ChatTitle: "Friends"}}}
	handler := nominationsHandler(miniAppTestDeps(&stubAccess{}, &stubMiniAppStats{}, 7), active, store)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/nominations?period=year",
		signInitData(t, `{"id":7,"first_name":"Me"}`, now)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if len(store.askedFor) != 1 || store.askedFor[0].Value != -1 {
		t.Fatalf("asked for chats %v, want only the viewer's", store.askedFor)
	}
	var body struct {
		Nominations []nominationDTO `json:"nominations"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	found := map[string]nominationDTO{}
	for _, n := range body.Nominations {
		found[n.Kind] = n
	}
	if n := found["best_accuracy"]; !n.IsYou || n.Value != 100 {
		t.Fatalf("accuracy nomination = %+v", n)
	}
	if n := found["most_active_chat"]; n.ChatTitle != "Friends" {
		t.Fatalf("chat nomination = %+v", n)
	}

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, miniAppRequest(t, "/api/miniapp/v1/me/nominations?period=decade",
		signInitData(t, `{"id":7,"first_name":"Me"}`, now)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown period status = %d", bad.Code)
	}
}
