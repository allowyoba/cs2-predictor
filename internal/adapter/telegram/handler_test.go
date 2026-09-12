package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// --- fakes ---

type fakeDedup struct{ claimed map[int64]bool }

func newFakeDedup() *fakeDedup { return &fakeDedup{claimed: map[int64]bool{}} }
func (d *fakeDedup) Claim(_ context.Context, id int64) (bool, error) {
	if d.claimed[id] {
		return false, nil
	}
	d.claimed[id] = true
	return true, nil
}
func (d *fakeDedup) Release(_ context.Context, id int64) error { delete(d.claimed, id); return nil }

type fakeChats struct {
	settings      map[int64]chat.Settings
	moderators    map[[2]int64]bool
	permissions   map[[2]int64][]chat.Permission
	moderatorMeta map[[2]int64]chat.ModeratorInfo
	managed       map[[2]int64]bool
	dmSessions    map[int64]int64
	locales       map[int64]common.LocaleCode
	reachable     map[int64]bool
	notifyPrefs   map[int64]chat.NotificationPrefs
	nicknames     map[int64]string
	profiles      map[int64]chat.UserProfile
}

func newFakeChats() *fakeChats {
	return &fakeChats{
		settings: map[int64]chat.Settings{}, moderators: map[[2]int64]bool{},
		permissions:   map[[2]int64][]chat.Permission{},
		moderatorMeta: map[[2]int64]chat.ModeratorInfo{},
		managed:       map[[2]int64]bool{}, dmSessions: map[int64]int64{},
		locales: map[int64]common.LocaleCode{}, reachable: map[int64]bool{},
		notifyPrefs: map[int64]chat.NotificationPrefs{},
		nicknames:   map[int64]string{},
		profiles:    map[int64]chat.UserProfile{},
	}
}

var _ chat.Repository = (*fakeChats)(nil)

func (f *fakeChats) RecordManaged(_ context.Context, chatID common.ChatID, userID common.UserID) error {
	f.managed[[2]int64{chatID.Value, userID.Value}] = true
	return nil
}
func (f *fakeChats) ManagedChats(_ context.Context, userID common.UserID) ([]chat.Settings, error) {
	var out []chat.Settings
	for key := range f.managed {
		if key[1] != userID.Value {
			continue
		}
		if s, ok := f.settings[key[0]]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeChats) NotificationPrefs(_ context.Context, userID common.UserID) (chat.NotificationPrefs, error) {
	return f.notifyPrefs[userID.Value], nil
}
func (f *fakeChats) SetNotificationPref(_ context.Context, userID common.UserID, kind common.NotificationKind, on bool) error {
	prefs := f.notifyPrefs[userID.Value]
	switch kind {
	case common.NotifyResultRecaps:
		prefs.ResultRecaps = on
	case common.NotifyPollReminders:
		prefs.PollReminders = on
	default:
		return fmt.Errorf("unknown notification kind %q", kind)
	}
	f.notifyPrefs[userID.Value] = prefs
	return nil
}

// Nickname/SetNickname mirror the postgres implementation's semantics: an
// empty string clears the entry back to "unset" rather than storing "".
func (f *fakeChats) Nickname(_ context.Context, userID common.UserID) (*string, error) {
	name, ok := f.nicknames[userID.Value]
	if !ok {
		return nil, nil
	}
	return &name, nil
}
func (f *fakeChats) SetNickname(_ context.Context, userID common.UserID, nickname string) error {
	if nickname == "" {
		delete(f.nicknames, userID.Value)
		return nil
	}
	f.nicknames[userID.Value] = nickname
	return nil
}

func (f *fakeChats) SetDMSession(_ context.Context, userID common.UserID, chatID common.ChatID) error {
	f.dmSessions[userID.Value] = chatID.Value
	return nil
}
func (f *fakeChats) DMSession(_ context.Context, userID common.UserID) (*common.ChatID, error) {
	id, ok := f.dmSessions[userID.Value]
	if !ok {
		return nil, nil
	}
	return &common.ChatID{Value: id}, nil
}
func (f *fakeChats) ClearDMSession(_ context.Context, userID common.UserID) error {
	delete(f.dmSessions, userID.Value)
	return nil
}
func (f *fakeChats) ListModerators(_ context.Context, chatID common.ChatID) ([]chat.ModeratorInfo, error) {
	var out []chat.ModeratorInfo
	for key := range f.moderators {
		if key[0] == chatID.Value {
			info := chat.ModeratorInfo{UserID: common.UserID{Value: key[1]}, DisplayName: "Moderator", Permissions: f.permissions[key]}
			if meta, ok := f.moderatorMeta[key]; ok {
				info.Username, info.DisplayName, info.AppointedBy = meta.Username, meta.DisplayName, meta.AppointedBy
			}
			out = append(out, info)
		}
	}
	return out, nil
}
func (f *fakeChats) UserLocale(_ context.Context, userID common.UserID) (*common.LocaleCode, error) {
	if l, ok := f.locales[userID.Value]; ok {
		return &l, nil
	}
	return nil, nil
}
func (f *fakeChats) SetUserLocale(_ context.Context, userID common.UserID, locale common.LocaleCode) error {
	f.locales[userID.Value] = locale
	return nil
}
func (f *fakeChats) Find(_ context.Context, id common.ChatID) (*chat.Settings, error) {
	if s, ok := f.settings[id.Value]; ok {
		return &s, nil
	}
	return nil, nil
}
func (f *fakeChats) Save(_ context.Context, s chat.Settings) (chat.Settings, error) {
	f.settings[s.ChatID.Value] = s
	return s, nil
}
func (f *fakeChats) IsModerator(_ context.Context, chatID common.ChatID, userID common.UserID) (bool, error) {
	return f.moderators[[2]int64{chatID.Value, userID.Value}], nil
}
func (f *fakeChats) AddModerator(_ context.Context, m chat.Moderator) error {
	key := [2]int64{m.ChatID.Value, m.UserID.Value}
	f.moderators[key] = true
	f.permissions[key] = m.Permissions
	f.moderatorMeta[key] = chat.ModeratorInfo{UserID: m.UserID, Username: m.Username, DisplayName: m.DisplayName, AppointedBy: m.AppointedBy}
	return nil
}
func (f *fakeChats) RemoveModerator(_ context.Context, chatID common.ChatID, userID common.UserID) error {
	key := [2]int64{chatID.Value, userID.Value}
	delete(f.moderators, key)
	delete(f.permissions, key)
	delete(f.moderatorMeta, key)
	return nil
}
func (f *fakeChats) ModeratorPermissions(_ context.Context, chatID common.ChatID, userID common.UserID) ([]chat.Permission, error) {
	return f.permissions[[2]int64{chatID.Value, userID.Value}], nil
}
func (f *fakeChats) SetModeratorPermissions(_ context.Context, chatID common.ChatID, userID common.UserID, permissions []chat.Permission) error {
	f.permissions[[2]int64{chatID.Value, userID.Value}] = permissions
	return nil
}
func (f *fakeChats) UserProfile(_ context.Context, userID common.UserID) (*chat.UserProfile, error) {
	if p, ok := f.profiles[userID.Value]; ok {
		return &p, nil
	}
	return nil, nil
}
func (f *fakeChats) UserProfiles(_ context.Context, userIDs []common.UserID) (map[common.UserID]chat.UserProfile, error) {
	out := make(map[common.UserID]chat.UserProfile, len(userIDs))
	for _, id := range userIDs {
		if p, ok := f.profiles[id.Value]; ok {
			out[id] = p
		}
	}
	return out, nil
}
func (f *fakeChats) EventTopic(context.Context, common.ChatID, common.EventID) (*int64, error) {
	return nil, nil
}
func (f *fakeChats) MigrateChatID(_ context.Context, oldID, newID common.ChatID) error {
	if s, ok := f.settings[oldID.Value]; ok {
		delete(f.settings, oldID.Value)
		s.ChatID = newID
		f.settings[newID.Value] = s
	}
	for key := range f.managed {
		if key[0] == oldID.Value {
			delete(f.managed, key)
			f.managed[[2]int64{newID.Value, key[1]}] = true
		}
	}
	return nil
}
func (f *fakeChats) SaveEventTopic(context.Context, chat.EventTopic) error                { return nil }
func (f *fakeChats) ClearEventTopic(context.Context, common.ChatID, common.EventID) error { return nil }

type fakeMembership struct{ role chat.MemberRole }

func (m fakeMembership) Role(context.Context, common.ChatID, common.UserID) (chat.MemberRole, error) {
	return m.role, nil
}

type fakeCatalog struct{}

func (fakeCatalog) SearchEvents(context.Context, string, int, bool) ([]competition.Event, error) {
	return nil, nil
}
func (fakeCatalog) FindEvent(context.Context, common.EventID) (*competition.Event, error) {
	return nil, nil
}
func (fakeCatalog) FindEvents(context.Context, []common.EventID) ([]competition.Event, error) {
	return nil, nil
}
func (fakeCatalog) FindMatch(context.Context, common.MatchID) (*competition.Match, error) {
	return nil, nil
}
func (fakeCatalog) FindUnstartedMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (fakeCatalog) FindUnstartedMatchesForEvents(context.Context, []common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (fakeCatalog) FindMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (fakeCatalog) SaveEvent(_ context.Context, e competition.Event) (competition.Event, error) {
	return e, nil
}
func (fakeCatalog) SaveMatch(_ context.Context, m competition.Match) (competition.Match, error) {
	return m, nil
}

type fakeSubs struct{}

func (fakeSubs) Subscribe(_ context.Context, s subscription.EventSubscription) (subscription.EventSubscription, error) {
	return s, nil
}
func (fakeSubs) Unsubscribe(context.Context, common.ChatID, common.EventID) error { return nil }
func (fakeSubs) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (fakeSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) { return nil, nil }
func (fakeSubs) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return nil, nil
}

type fakeScoring struct{}

func (fakeScoring) AvailableMonths(context.Context, common.ChatID) ([]scoring.StatsMonth, error) {
	return nil, nil
}
func (fakeScoring) AvailableEventIDs(context.Context, common.ChatID) ([]common.EventID, error) {
	return nil, nil
}
func (fakeScoring) ReplaceAwards(context.Context, common.PollID, []scoring.Award) error { return nil }
func (fakeScoring) Leaderboard(context.Context, common.ChatID, scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	return nil, nil
}
func (fakeScoring) MedalCounts(context.Context, common.ChatID) (map[common.UserID]scoring.MedalCount, error) {
	return nil, nil
}
func (fakeScoring) AwardMedals(context.Context, common.ChatID, common.EventID, []scoring.UserStanding, time.Time) error {
	return nil
}
func (fakeScoring) EventCompletionHash(context.Context, common.ChatID, common.EventID) (string, bool, error) {
	return "", false, nil
}
func (fakeScoring) MarkEventCompleted(context.Context, common.ChatID, common.EventID, string, time.Time) error {
	return nil
}
func (fakeScoring) LockEventCompletion(context.Context, common.ChatID, common.EventID) error {
	return nil
}
func (fakeScoring) AvailableUserMonths(context.Context, common.UserID) ([]scoring.StatsMonth, error) {
	return nil, nil
}
func (fakeScoring) UserStats(context.Context, common.UserID, scoring.StatsPeriod) (*scoring.UserStanding, error) {
	return nil, nil
}
func (fakeScoring) UserChatStats(context.Context, common.UserID) ([]scoring.UserChatStanding, error) {
	return nil, nil
}

// A genuinely unexpected error (not chat.ErrAccessDenied, not a
// validationError — a DB outage, say) must still reach the user as a
// generic reply instead of leaving a tapped button/typed command looking
// like it silently did nothing, but the original error must still
// propagate afterward so the caller's retry/dedup-release logic still
// fires.
func TestHandleCommandError_UnexpectedErrorGetsAGenericReplyButStillPropagates(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU}

	original := errors.New("database is on fire")
	err := handler.handleCommandError(context.Background(), settings, nil, "/whatever", original)

	if !errors.Is(err, original) {
		t.Fatalf("expected the original error to still propagate, got %v", err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "error.generic")) {
		t.Fatalf("expected a generic error reply sent to the user, got %q", lastText(*calls))
	}
}

// --- test setup ---

func newTestHandler(t *testing.T, server *httptest.Server) (*UpdateHandler, *fakeChats) {
	t.Helper()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	authz := chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleMember})

	return &UpdateHandler{
		Dedup:         newFakeDedup(),
		Chats:         chats,
		Authorization: authz,
		Catalog:       fakeCatalog{},
		Subscriptions: fakeSubs{},
		Scoring:       fakeScoring{},
		Texts:         texts,
		Client:        client,
		Clock:         common.SystemUTCClock(),
		Log:           slog.Default(),
	}, chats
}

func newRecordingServer(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var calls []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["__method"] = strings.TrimPrefix(r.URL.Path, "/bottest-token/")
		calls = append(calls, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	return server, &calls
}

func TestWebhook_RejectsMissingOrWrongSecret(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	webhook := NewWebhookHandler(Config{WebhookSecret: "correct-secret-value"}, handler)

	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(`{"update_id":1}`))
	rec := httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing secret: status = %d, want 401", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(`{"update_id":1}`))
	req2.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong")
	rec2 := httptest.NewRecorder()
	webhook.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: status = %d, want 401", rec2.Code)
	}
}

func TestWebhook_AcceptsCorrectSecret(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	webhook := NewWebhookHandler(Config{WebhookSecret: "correct-secret-value"}, handler)

	body := `{"update_id":1,"message":{"message_id":1,"chat":{"id":-100,"type":"group"},"from":{"id":7,"first_name":"A"},"text":"/menu"}}`
	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "correct-secret-value")
	rec := httptest.NewRecorder()
	webhook.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(*calls) == 0 {
		t.Fatal("expected the update to trigger a Bot API call (the /menu reply)")
	}
}

func TestHandleMessage_PrivateChatRepliesAndDoesNotPersistChat(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)

	text := "/menu"
	msg := &Message{MessageID: 1, Chat: Chat{ID: 555, Type: "private"}, From: &User{ID: 555, FirstName: "Alex"}, Text: &text}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if _, ok := chats.settings[555]; ok {
		t.Fatal("expected no chat settings to be persisted for a private chat")
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one sendMessage call, got %d", len(*calls))
	}
	// Locale is hardcoded to RU for the private statistics UI.
	if text, _ := (*calls)[0]["text"].(string); !strings.Contains(text, ru(t, "private.stats_empty")) {
		t.Fatalf("unexpected private stats reply = %q", text)
	}
}

func TestHandleMessage_MenuCommandSendsMenuAndPersistsChat(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)

	text := "/menu"
	title := "My Chat"
	msg := &Message{MessageID: 1, Chat: Chat{ID: -100, Type: "group", Title: &title}, Text: &text}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if s, ok := chats.settings[-100]; !ok || s.Title != "My Chat" {
		t.Fatalf("expected chat settings to be persisted with title 'My Chat', got %+v", s)
	}
	if len(*calls) != 1 || (*calls)[0]["reply_markup"] == nil {
		t.Fatalf("expected one sendMessage call with a keyboard, got %+v", *calls)
	}
}

func TestHandleMessage_EventsCommandRequiresTwoCharacters(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	text := "/events a"
	msg := &Message{MessageID: 1, Chat: Chat{ID: -100, Type: "group"}, From: &User{ID: 7, FirstName: "Admin"}, Text: &text}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one reply, got %d", len(*calls))
	}
	// New chats default to RU; too-short a query reuses the search hint text.
	body, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(body, ru(t, "events.search_hint")) {
		t.Fatalf("reply text = %q", body)
	}
}

func TestHandleMessage_ModeratorAddRequiresReply(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	// This handler's fakeMembership always returns MEMBER, so
	// RequireTelegramAdmin fails first — swap in an admin membership to
	// reach the "must reply" validation branch.
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	text := "/moderator add"
	from := User{ID: 1, FirstName: "Admin"}
	msg := &Message{MessageID: 1, Chat: Chat{ID: -100, Type: "group"}, Text: &text, From: &from}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	body, _ := (*calls)[len(*calls)-1]["text"].(string)
	if !strings.Contains(body, ru(t, "error.generic")) {
		t.Fatalf("expected the generic error reply for a missing reply-target, got %q", body)
	}
}

func TestHandleCallback_UnknownDataRepliesExpiredAndAnswers(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: -100}, Title: "C", Locale: common.LocaleEN, Timezone: chat.DefaultTimezone, Active: true})

	data := "unknown:thing"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "A"}, Message: &Message{Chat: Chat{ID: -100, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	// An unknown/stale callback gets a toast (answerCallbackQuery with
	// text), not a new chat message — the origin message could be anything
	// (including an old notification), so editing or messaging it isn't
	// safe, but a toast is always a harmless no-op.
	if len(*calls) != 1 || (*calls)[0]["__method"] != "answerCallbackQuery" {
		t.Fatalf("expected exactly one answerCallbackQuery call, got %+v", *calls)
	}
	if text, _ := (*calls)[0]["text"].(string); !strings.Contains(text, "expired") {
		t.Fatalf("expected the expired-callback toast text, got %q", text)
	}
}

func containsAll(haystack []string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestDedup_SkipsDuplicateUpdates(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	text := "/menu"
	update := Update{UpdateID: 42, Message: &Message{MessageID: 1, Chat: Chat{ID: -100, Type: "group"}, Text: &text}}
	if err := handler.Handle(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	firstCallCount := len(*calls)
	if err := handler.Handle(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != firstCallCount {
		t.Fatalf("expected duplicate update to be skipped, calls went from %d to %d", firstCallCount, len(*calls))
	}
}

// TestHandle_StampsLogsWithTelegramUpdateID verifies every log line emitted
// while processing an update — including ones from deep inside a command
// handler — carries "telegramUpdateId", so a chain of log lines from one
// incoming webhook request can be correlated in production.
func TestHandle_StampsLogsWithTelegramUpdateID(t *testing.T) {
	server, _ := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	var buf bytes.Buffer
	handler.Log = slog.New(slog.NewJSONHandler(&buf, nil))

	// /moderator add without a reply-target triggers handleCommandError's
	// warning log — a deep, non-obvious call site to verify gets stamped.
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	text := "/moderator add"
	from := User{ID: 1, FirstName: "Admin"}
	update := Update{UpdateID: 777, Message: &Message{MessageID: 1, Chat: Chat{ID: -100, Type: "group"}, Text: &text, From: &from}}

	if err := handler.Handle(context.Background(), update); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), `"telegramUpdateId":777`) {
		t.Fatalf("expected every log line to carry telegramUpdateId=777, got:\n%s", buf.String())
	}
}

func (f *fakeChats) SetDMReachable(_ context.Context, userID common.UserID, reachable bool) error {
	f.reachable[userID.Value] = reachable
	return nil
}
func (f *fakeChats) FilterDMReachable(_ context.Context, userIDs []common.UserID) ([]common.UserID, error) {
	var out []common.UserID
	for _, id := range userIDs {
		if f.reachable[id.Value] {
			out = append(out, id)
		}
	}
	return out, nil
}
