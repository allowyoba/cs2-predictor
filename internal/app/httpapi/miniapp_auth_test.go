package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// A Mini App runs in a WebView nobody here controls, so everything it
// sends is attacker-controlled except Telegram's own signature. These are
// the tests for that boundary; each one is a way the check could look
// right and not be.

const testBotToken = "123456:test-bot-token"

// signInitData builds a launch Telegram itself would have signed.
func signInitData(t *testing.T, userJSON string, authDate time.Time) string {
	t.Helper()
	values := url.Values{}
	values.Set("user", userJSON)
	values.Set("auth_date", strconv.FormatInt(authDate.Unix(), 10))
	values.Set("query_id", "AAE")

	pairs := make([]string, 0, len(values))
	for key, list := range values {
		pairs = append(pairs, key+"="+list[0])
	}
	sort.Strings(pairs)

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(testBotToken))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(pairs, "\n")))
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return values.Encode()
}

func TestVerifyInitData_AcceptsWhatTelegramSigned(t *testing.T) {
	now := time.Now()
	raw := signInitData(t, `{"id":42,"first_name":"Аня","username":"anya"}`, now.Add(-time.Minute))

	user, err := VerifyInitData(raw, testBotToken, now)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 42 || user.FirstName != "Аня" {
		t.Fatalf("user = %+v, want the signed payload", user)
	}
}

func TestVerifyInitData_RefusesEverythingElse(t *testing.T) {
	now := time.Now()
	valid := signInitData(t, `{"id":42,"first_name":"Аня"}`, now)

	cases := map[string]string{
		"empty":            "",
		"no hash":          "user=%7B%22id%22%3A42%7D&auth_date=" + strconv.FormatInt(now.Unix(), 10),
		"tampered hash":    strings.Replace(valid, "hash=", "hash=00", 1),
		"tampered payload": strings.Replace(valid, "%22id%22%3A42", "%22id%22%3A43", 1),
		// The classic mistake: signing the launch and then trusting an
		// unsigned parameter beside it.
		"extra unsigned user": valid + "&user_id=999",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyInitData(raw, testBotToken, now); err == nil {
				t.Fatal("expected the launch to be refused")
			}
		})
	}

	// Another bot's token must not open this bot's app.
	if _, err := VerifyInitData(valid, "999:someone-elses-token", now); err == nil {
		t.Fatal("a signature from a different bot must not verify")
	}
}

// A signature with no freshness bound is a credential that never expires:
// initData copied out of a client once would work forever.
func TestVerifyInitData_RefusesStaleAndFutureLaunches(t *testing.T) {
	now := time.Now()

	stale := signInitData(t, `{"id":42,"first_name":"A"}`, now.Add(-InitDataMaxAge-time.Minute))
	if _, err := VerifyInitData(stale, testBotToken, now); err == nil {
		t.Fatal("a launch older than the window must be refused")
	}
	future := signInitData(t, `{"id":42,"first_name":"A"}`, now.Add(InitDataMaxAge+time.Minute))
	if _, err := VerifyInitData(future, testBotToken, now); err == nil {
		t.Fatal("a launch from the future is somebody else's clock")
	}
}

// --- the gate in front of the endpoints ---

type stubMiniAppStats struct {
	standing    *scoring.UserStanding
	predictions []scoring.UserPrediction
	bias        []scoring.TeamBias
	months      []scoring.StatsMonth
	chatStats   []scoring.UserChatStanding
	askedFor    common.UserID
	// statsFor, when set, overrides standing per call — used to give
	// different chats different figures for the same request.
	statsFor func(scoring.StatsPeriod) *scoring.UserStanding
}

func (s *stubMiniAppStats) UserStats(_ context.Context, userID common.UserID, period scoring.StatsPeriod) (*scoring.UserStanding, error) {
	s.askedFor = userID
	if s.statsFor != nil {
		return s.statsFor(period), nil
	}
	return s.standing, nil
}
func (s *stubMiniAppStats) UserPredictions(_ context.Context, userID common.UserID, _ int) ([]scoring.UserPrediction, error) {
	s.askedFor = userID
	return s.predictions, nil
}
func (s *stubMiniAppStats) AvailableUserMonths(_ context.Context, userID common.UserID) ([]scoring.StatsMonth, error) {
	s.askedFor = userID
	return s.months, nil
}
func (s *stubMiniAppStats) UserChatStats(_ context.Context, userID common.UserID) ([]scoring.UserChatStanding, error) {
	s.askedFor = userID
	return s.chatStats, nil
}

type stubAccess struct {
	access    map[int64]chat.MiniAppStatus
	requested []int64
}

func (s *stubAccess) MiniAppAccess(_ context.Context, userID common.UserID) (*chat.MiniAppAccess, error) {
	status, ok := s.access[userID.Value]
	if !ok {
		return nil, nil
	}
	return &chat.MiniAppAccess{UserID: userID, Status: status}, nil
}
func (s *stubAccess) RequestMiniAppAccess(_ context.Context, userID common.UserID, _ time.Time) error {
	s.requested = append(s.requested, userID.Value)
	return nil
}

func miniAppTestDeps(access *stubAccess, stats *stubMiniAppStats, operators ...int64) MiniAppDeps {
	return MiniAppDeps{
		BotToken: testBotToken, Stats: stats, Access: access,
		Operators: operators, Clock: common.SystemUTCClock(),
	}
}

func miniAppRequest(t *testing.T, path, initData string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if initData != "" {
		req.Header.Set("Authorization", "tma "+initData)
	}
	return req
}

// The dashboard is one person's history. An unverified caller is told
// nothing at all — not even whether that person exists.
func TestMiniappDashboard_RefusesAnUnsignedLaunch(t *testing.T) {
	stats := &stubMiniAppStats{}
	handler := dashboardHandler(miniAppTestDeps(&stubAccess{}, stats), nil)

	for _, initData := range []string{"", "user=%7B%22id%22%3A42%7D&hash=deadbeef"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/dashboard", initData))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "42") {
			t.Fatalf("a refused caller must learn nothing: %q", recorder.Body.String())
		}
	}
	if stats.askedFor.Value != 0 {
		t.Fatal("the database must not be touched for an unverified caller")
	}
}

// Signed, but not granted: the person has proven who they are, so they are
// owed the reason — and still no data.
func TestMiniappDashboard_RefusesSomebodyWithoutAccess(t *testing.T) {
	stats := &stubMiniAppStats{}
	handler := dashboardHandler(miniAppTestDeps(&stubAccess{}, stats), nil)
	initData := signInitData(t, `{"id":42,"first_name":"A"}`, time.Now())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/dashboard", initData))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if stats.askedFor.Value != 0 {
		t.Fatal("no history is read for somebody who may not see it")
	}
}

// The data served is the caller's own, taken from the signed payload —
// never from a parameter the caller chose.
func TestMiniappDashboard_ServesTheSignedUsersOwnStatistics(t *testing.T) {
	stats := &stubMiniAppStats{
		standing: &scoring.UserStanding{Points: 42, Predictions: 10, CorrectPredictions: 7, ExactPredictions: 3, Tournaments: 2},
	}
	access := &stubAccess{access: map[int64]chat.MiniAppStatus{42: chat.MiniAppGranted}}
	handler := dashboardHandler(miniAppTestDeps(access, stats), nil)
	initData := signInitData(t, `{"id":42,"first_name":"Аня"}`, time.Now())

	recorder := httptest.NewRecorder()
	// The forged parameter names somebody else; it must be ignored.
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/dashboard?user_id=9999", initData))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if stats.askedFor.Value != 42 {
		t.Fatalf("read statistics for %d, want the signed user 42", stats.askedFor.Value)
	}
	if cache := recorder.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store on personal data", cache)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"points":42`) || !strings.Contains(body, `"accuracy":70`) {
		t.Fatalf("expected the real figures in the body, got %s", body)
	}
}

// Root operators are exempt: they are who grants access to everybody else.
func TestMiniappDashboard_LetsOperatorsInWithoutAGrant(t *testing.T) {
	stats := &stubMiniAppStats{standing: &scoring.UserStanding{Predictions: 1}}
	handler := dashboardHandler(miniAppTestDeps(&stubAccess{}, stats, 7), nil)
	initData := signInitData(t, `{"id":7,"first_name":"Root"}`, time.Now())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/dashboard", initData))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an operator", recorder.Code)
	}
}

// Asking for access is the one thing somebody without it may do.
func TestMiniappAccess_CanBeAskedForWithoutHavingIt(t *testing.T) {
	access := &stubAccess{}
	handler := accessHandler(miniAppTestDeps(access, &stubMiniAppStats{}))
	initData := signInitData(t, `{"id":42,"first_name":"A"}`, time.Now())

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/miniapp/v1/me/access", nil)
	request.Header.Set("Authorization", "tma "+initData)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if len(access.requested) != 1 || access.requested[0] != 42 {
		t.Fatalf("expected the signed user's request to be recorded, got %v", access.requested)
	}
	// Still unsigned callers get nothing here either.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/miniapp/v1/me/access", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

// stubNames is the bot's own idea of what to call somebody.
type stubNames struct {
	nickname string
	stored   string
}

func (s stubNames) Nickname(context.Context, common.UserID) (*string, error) {
	if s.nickname == "" {
		return nil, nil
	}
	return &s.nickname, nil
}
func (s stubNames) UserProfile(_ context.Context, userID common.UserID) (*chat.UserProfile, error) {
	if s.stored == "" {
		return nil, nil
	}
	return &chat.UserProfile{UserID: userID, DisplayName: s.stored}, nil
}

// The app sits next to leaderboards that call somebody by the name they
// chose. Greeting them by their Telegram first name instead makes the app
// look like it is about somebody else.
func TestMiniappDashboard_UsesTheNameTheBotShowsEverywhereElse(t *testing.T) {
	now := time.Now()
	initData := signInitData(t, `{"id":42,"first_name":"Telegram","last_name":"Name"}`, now)
	access := &stubAccess{access: map[int64]chat.MiniAppStatus{42: chat.MiniAppGranted}}

	nameFrom := func(names MiniAppNames) string {
		deps := miniAppTestDeps(access, &stubMiniAppStats{standing: &scoring.UserStanding{}})
		deps.Names = names
		recorder := httptest.NewRecorder()
		dashboardHandler(deps, nil).ServeHTTP(recorder, miniAppRequest(t, "/api/miniapp/v1/me/dashboard", initData))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
		}
		var body dashboardDTO
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.User.DisplayName
	}

	if got := nameFrom(stubNames{nickname: "Аня", stored: "Anna S"}); got != "Аня" {
		t.Fatalf("display name = %q, want the chosen nickname", got)
	}
	if got := nameFrom(stubNames{stored: "Anna S"}); got != "Anna S" {
		t.Fatalf("display name = %q, want the stored name when no nickname was chosen", got)
	}
	// Nothing stored at all — a first launch — still greets them properly.
	if got := nameFrom(stubNames{}); got != "Telegram Name" {
		t.Fatalf("display name = %q, want the Telegram profile as the last resort", got)
	}
	if got := nameFrom(nil); got != "Telegram Name" {
		t.Fatalf("display name = %q, want the launch profile when no name store is wired", got)
	}
}

// The bias read is one extra query behind one block; these tests are about
// authentication, so it answers nothing and the dashboard still renders.
func (s *stubMiniAppStats) UserTeamBias(context.Context, common.UserID, int) ([]scoring.TeamBias, error) {
	return s.bias, nil
}
