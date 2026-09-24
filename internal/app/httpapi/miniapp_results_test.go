package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// Same gate as every other authenticated endpoint: no signature, no data.
func TestMiniappResults_RefusesAnUnsignedLaunch(t *testing.T) {
	stats := &stubMiniAppStats{}
	handler := resultsHandler(miniAppTestDeps(&stubAccess{}, stats))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/results", ""))
	if recorder.Code != 401 {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if stats.askedFor.Value != 0 {
		t.Fatal("the database must not be touched for an unverified caller")
	}
}

// A month is round-tripped as the same key the client sent, so the picker
// can tell which button is active, and its label reads in Russian like the
// rest of the app.
func TestMiniappResults_RendersTheRequestedMonth(t *testing.T) {
	stats := &stubMiniAppStats{
		standing: &scoring.UserStanding{Points: 12, Predictions: 5, CorrectPredictions: 4},
		months:   []scoring.StatsMonth{{Year: 2026, Month: time.March}, {Year: 2025, Month: time.December}},
	}
	access := &stubAccess{access: map[int64]chat.MiniAppStatus{42: chat.MiniAppGranted}}
	handler := resultsHandler(miniAppTestDeps(access, stats))
	initData := signInitData(t, `{"id":42,"first_name":"A"}`, time.Now())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/results?period=month:2026-03", initData))
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}

	var body resultsDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Period.Key != "month:2026-03" {
		t.Fatalf("period key = %q, want month:2026-03", body.Period.Key)
	}
	if body.Period.Label != "Март 2026" {
		t.Fatalf("period label = %q, want Март 2026", body.Period.Label)
	}
	if body.Summary.Points != 12 || body.Summary.Predictions != 5 {
		t.Fatalf("summary = %+v, want the signed user's own standing", body.Summary)
	}
	if len(body.AvailableMonths) != 2 || len(body.AvailableYears) != 2 {
		t.Fatalf("available periods = %+v / %+v, want 2 months and 2 distinct years", body.AvailableMonths, body.AvailableYears)
	}
	if stats.askedFor.Value != 42 {
		t.Fatalf("read statistics for %d, want the signed user 42", stats.askedFor.Value)
	}
}

// An unknown period is refused rather than silently read as all-time — a
// screen showing the wrong window with no sign it happened is worse than
// one that fails visibly.
func TestMiniappResults_RefusesAnUnparsablePeriod(t *testing.T) {
	stats := &stubMiniAppStats{}
	access := &stubAccess{access: map[int64]chat.MiniAppStatus{42: chat.MiniAppGranted}}
	handler := resultsHandler(miniAppTestDeps(access, stats))
	initData := signInitData(t, `{"id":42,"first_name":"A"}`, time.Now())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/results?period=whenever", initData))
	if recorder.Code != 400 {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// A chat with nothing in the selected window is left out rather than shown
// at zero, and the ones that do have activity are ranked by points.
func TestMiniappResults_ChatsAreNarrowedToTheSelectedWindow(t *testing.T) {
	stats := &stubMiniAppStats{
		chatStats: []scoring.UserChatStanding{
			{ChatID: common.ChatID{Value: 1}, ChatTitle: "Alpha"},
			{ChatID: common.ChatID{Value: 2}, ChatTitle: "Beta"},
		},
		statsFor: func(p scoring.StatsPeriod) *scoring.UserStanding {
			if p.ChatID != nil && p.ChatID.Value == 1 {
				return &scoring.UserStanding{Points: 3, Predictions: 2, CorrectPredictions: 1}
			}
			// chat 2 (and the overall, chat-less read): nothing in this window.
			return &scoring.UserStanding{}
		},
	}
	access := &stubAccess{access: map[int64]chat.MiniAppStatus{42: chat.MiniAppGranted}}
	handler := resultsHandler(miniAppTestDeps(access, stats))
	initData := signInitData(t, `{"id":42,"first_name":"A"}`, time.Now())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/results", initData))
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	var body resultsDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Chats) != 1 || body.Chats[0].Title != "Alpha" {
		t.Fatalf("chats = %+v, want only Alpha (Beta has nothing in this window)", body.Chats)
	}
}
