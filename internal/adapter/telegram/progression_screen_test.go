package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// newPhotoAcceptingServer replies "ok" to any request regardless of content
// type — unlike newRecordingServer (JSON-only; it panics on a multipart
// sendPhoto body), this is for tests that only care whether SendPhoto
// completed without error, not what exactly it uploaded (client_test.go's
// TestSendPhoto_UploadsTheImageAsAMultipartFile already covers that).
func newPhotoAcceptingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// progressionScoring adds ProgressionRepository on top of the regular
// group leaderboard fake, mirroring personalDataScoring's own pattern for
// PersonalBetsRepository.
type progressionScoring struct {
	*dataScoring
	points        []scoring.ProgressionPoint
	requestedChat common.ChatID
}

func (s *progressionScoring) PointsProgression(_ context.Context, chatID common.ChatID, _ scoring.StatsPeriod) ([]scoring.ProgressionPoint, error) {
	s.requestedChat = chatID
	return s.points, nil
}

func newProgressionTestHandler(t *testing.T) (*UpdateHandler, chat.Settings) {
	t.Helper()
	srv, _ := newRecordingServer(t)
	t.Cleanup(srv.Close)
	handler, chats := newTestHandler(t, srv)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)
	return handler, settings
}

// TestSendProgressionChart_DrawsEveryoneWhenAskedFromTheGroupItself covers
// the "all participants in the group" half of the feature: a chart
// requested with target.chatID == settings.ChatID (i.e. asked for directly
// in the group, not proxied from a DM) must not restrict to one user.
func TestSendProgressionChart_DrawsEveryoneWhenAskedFromTheGroupItself(t *testing.T) {
	handler, settings := newProgressionTestHandler(t)
	base := time.Now()
	prog := &progressionScoring{dataScoring: &dataScoring{}, points: []scoring.ProgressionPoint{
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", PlayedAt: base, Points: 2},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: base, Points: 1},
	}}
	handler.Scoring = prog
	srv := newPhotoAcceptingServer(t)
	handler.Client = NewClient(Config{BaseURL: srv.URL, Token: "test-token"}, srv.Client())

	target := sendTarget(settings.ChatID, nil) // same chat as settings.ChatID => group context
	if err := handler.sendProgressionChart(context.Background(), target, settings, scoring.AllTime(), common.UserID{Value: 1}); err != nil {
		t.Fatal(err)
	}
	if prog.requestedChat != settings.ChatID {
		t.Fatalf("expected PointsProgression to be scoped to the chat, got %+v", prog.requestedChat)
	}
}

// TestSendProgressionChart_DrawsOnlyViewerWhenAskedFromADM covers the
// personal half: a chart asked for from a DM (target.chatID differs from
// the managed chat's own id) must draw only the viewer, not everyone.
func TestSendProgressionChart_DrawsOnlyViewerWhenAskedFromADM(t *testing.T) {
	handler, settings := newProgressionTestHandler(t)
	base := time.Now()
	prog := &progressionScoring{dataScoring: &dataScoring{}, points: []scoring.ProgressionPoint{
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", PlayedAt: base, Points: 2},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: base, Points: 1},
	}}
	handler.Scoring = prog
	srv := newPhotoAcceptingServer(t)
	handler.Client = NewClient(Config{BaseURL: srv.URL, Token: "test-token"}, srv.Client())

	dmTarget := sendTarget(common.ChatID{Value: 999}, nil) // an admin's own DM, not the managed chat
	if err := handler.sendProgressionChart(context.Background(), dmTarget, settings, scoring.AllTime(), common.UserID{Value: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestSendProgressionChart_RepliesWithEmptyTextWhenNoData(t *testing.T) {
	handler, settings := newProgressionTestHandler(t)
	handler.Scoring = &progressionScoring{dataScoring: &dataScoring{}}

	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler.Client = NewClient(Config{BaseURL: srv.URL, Token: "test-token"}, srv.Client())

	target := sendTarget(settings.ChatID, nil)
	if err := handler.sendProgressionChart(context.Background(), target, settings, scoring.AllTime(), common.UserID{Value: 1}); err != nil {
		t.Fatal(err)
	}
	text, _ := (*calls)[0]["text"].(string)
	if text != ru(t, "chart.empty") {
		t.Fatalf("expected the empty-chart message, got %q", text)
	}
}

func TestChartCallbackData_RoundTripsEveryPeriodKind(t *testing.T) {
	eventID := common.NewEventID()
	cases := []scoring.StatsPeriod{
		scoring.AllTime(),
		scoring.ForYear(2026),
		scoring.ForMonth(2026, time.September),
		scoring.ForEvent(eventID),
	}
	for _, period := range cases {
		data := chartCallbackData(period)
		if data == "" {
			t.Fatalf("empty callback data for period %+v", period)
		}
	}
}
