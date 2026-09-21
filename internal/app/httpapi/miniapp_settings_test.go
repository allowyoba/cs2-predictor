package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

type stubSettings struct {
	settings map[int64]chat.Settings
	managed  []chat.Settings
	locale   *common.LocaleCode
	zone     *string
	nickname *string
	games    map[int64][]competition.GameCode
	auto     map[[2]string]bool
}

func newStubSettings() *stubSettings {
	return &stubSettings{
		settings: map[int64]chat.Settings{},
		games:    map[int64][]competition.GameCode{},
		auto:     map[[2]string]bool{},
	}
}

func (s *stubSettings) Find(_ context.Context, chatID common.ChatID) (*chat.Settings, error) {
	found, ok := s.settings[chatID.Value]
	if !ok {
		return nil, nil
	}
	return &found, nil
}

func (s *stubSettings) Save(_ context.Context, settings chat.Settings) (chat.Settings, error) {
	s.settings[settings.ChatID.Value] = settings
	return settings, nil
}

func (s *stubSettings) ManagedChats(context.Context, common.UserID) ([]chat.Settings, error) {
	return s.managed, nil
}

func (s *stubSettings) SetEnabledGames(_ context.Context, chatID common.ChatID, games []competition.GameCode) error {
	s.games[chatID.Value] = games
	return nil
}

func (s *stubSettings) SetAutoSubscribeGame(_ context.Context, chatID common.ChatID, game competition.GameCode, on bool) error {
	s.auto[[2]string{chatID.String(), string(game)}] = on
	return nil
}

func (s *stubSettings) UserLocale(context.Context, common.UserID) (*common.LocaleCode, error) {
	return s.locale, nil
}

func (s *stubSettings) SetUserLocale(_ context.Context, _ common.UserID, locale common.LocaleCode) error {
	s.locale = &locale
	return nil
}

func (s *stubSettings) UserTimezone(context.Context, common.UserID) (*string, error) {
	return s.zone, nil
}

func (s *stubSettings) SetUserTimezone(_ context.Context, _ common.UserID, zone string) error {
	s.zone = &zone
	return nil
}

func (s *stubSettings) Nickname(context.Context, common.UserID) (*string, error) {
	return s.nickname, nil
}

func (s *stubSettings) SetNickname(_ context.Context, _ common.UserID, name string) error {
	s.nickname = &name
	return nil
}

type stubAuthz struct {
	allow  bool
	asked  common.ChatID
	askedP chat.Permission
}

func (a *stubAuthz) HasPermission(_ context.Context, chatID common.ChatID, _ common.UserID, p chat.Permission) (bool, error) {
	a.asked, a.askedP = chatID, p
	return a.allow, nil
}

func settingsRequest(t *testing.T, method, path, initData string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if initData != "" {
		req.Header.Set("Authorization", "tma "+initData)
	}
	return req
}

func grantedDeps() MiniAppDeps {
	return miniAppTestDeps(&stubAccess{access: map[int64]chat.MiniAppStatus{42: chat.MiniAppGranted}}, &stubMiniAppStats{})
}

// A person's own settings and a chat's are two different things with two
// different owners, and the payload keeps them apart — which is what stops
// somebody muting a chat when they meant to mute themselves.
func TestMiniappSettings_SeparatesWhatIsYoursFromWhatIsTheChats(t *testing.T) {
	store := newStubSettings()
	quietFrom, quietTo := 60, 480
	store.managed = []chat.Settings{{
		ChatID: common.ChatID{Value: -100}, Title: "Прогнозы", Locale: common.LocaleRU,
		Timezone: chat.DefaultTimezone, EnabledGames: []competition.GameCode{competition.GameCS2},
		QuietFromMinute: &quietFrom, QuietToMinute: &quietTo,
	}}
	switches := &fakeSwitchboard{}
	handler := settingsHandler(grantedDeps(), store, switches)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, settingsRequest(t, http.MethodGet, "/api/miniapp/v1/me/settings",
		signInitData(t, `{"id":42,"first_name":"Аня"}`, time.Now()), nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	var body settingsDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Personal.Notify) != len(common.PersonalNotificationKinds) {
		t.Fatalf("personal switches = %d, want the whole catalogue so anything off can be turned on", len(body.Personal.Notify))
	}
	for _, entry := range body.Personal.Notify {
		if entry.On {
			t.Fatalf("%s is on for somebody who never asked", entry.Kind)
		}
	}
	if len(body.Chats) != 1 || body.Chats[0].ID != -100 {
		t.Fatalf("chats = %+v, want the one this person manages", body.Chats)
	}
	if body.Chats[0].QuietFrom != 60 || body.Chats[0].QuietTo != 480 {
		t.Fatalf("quiet hours = %d..%d, want them carried across", body.Chats[0].QuietFrom, body.Chats[0].QuietTo)
	}
	// The whole catalogue, not only what is on: a list that hides what is
	// off cannot be used to turn anything on.
	if len(body.Chats[0].Games) != len(competition.Games) {
		t.Fatalf("games = %+v, want every game with a flag", body.Chats[0].Games)
	}
}

// Rights are re-checked on the write, never carried over from whatever the
// list said when the screen was opened: they can be taken away while
// somebody has the app in front of them.
func TestMiniappSettings_RefusesAChatThisPersonDoesNotManage(t *testing.T) {
	store := newStubSettings()
	store.settings[-100] = chat.Settings{ChatID: common.ChatID{Value: -100}, Locale: common.LocaleRU}
	authz := &stubAuthz{allow: false}
	handler := patchChatSettingsHandler(grantedDeps(), store, &fakeSwitchboard{}, authz)

	mux := http.NewServeMux()
	mux.Handle("PATCH /api/miniapp/v1/chats/{id}/settings", handler)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, settingsRequest(t, http.MethodPatch, "/api/miniapp/v1/chats/-100/settings",
		signInitData(t, `{"id":42,"first_name":"Аня"}`, time.Now()), map[string]any{"top_tier_only": true}))

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if authz.askedP != chat.PermissionManageGroupSettings {
		t.Fatalf("checked %q, want the settings permission", authz.askedP)
	}
	if settings := store.settings[-100]; settings.DefaultTopTierOnly {
		t.Fatal("the refused write was applied anyway")
	}
}

// A patch says what changed and stays silent about the rest, so two
// screens open at once cannot overwrite each other's untouched fields.
func TestMiniappSettings_PatchTouchesOnlyWhatItNames(t *testing.T) {
	store := newStubSettings()
	store.settings[-100] = chat.Settings{
		ChatID: common.ChatID{Value: -100}, Title: "Прогнозы", Locale: common.LocaleRU,
		Timezone: chat.DefaultTimezone, DefaultTopTierOnly: true,
	}
	handler := patchChatSettingsHandler(grantedDeps(), store, &fakeSwitchboard{}, &stubAuthz{allow: true})

	mux := http.NewServeMux()
	mux.Handle("PATCH /api/miniapp/v1/chats/{id}/settings", handler)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, settingsRequest(t, http.MethodPatch, "/api/miniapp/v1/chats/-100/settings",
		signInitData(t, `{"id":42,"first_name":"Аня"}`, time.Now()), map[string]any{"prefer_hltv_flags": true}))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	saved := store.settings[-100]
	if !saved.PreferHLTVFlags {
		t.Fatal("the named field was not saved")
	}
	if !saved.DefaultTopTierOnly || saved.Timezone != chat.DefaultTimezone {
		t.Fatalf("an unnamed field was overwritten: %+v", saved)
	}
}

// A value the server does not recognise is refused, never stored and never
// quietly ignored: a screen showing something the server did not keep is
// worse than an error.
func TestMiniappSettings_RefusesValuesItDoesNotUnderstand(t *testing.T) {
	store := newStubSettings()
	handler := patchSettingsHandler(grantedDeps(), store, &fakeSwitchboard{})
	initData := signInitData(t, `{"id":42,"first_name":"Аня"}`, time.Now())

	for name, body := range map[string]any{
		"a language that does not exist": map[string]any{"locale": "KL"},
		"a timezone that does not load":  map[string]any{"timezone": "Middle/Earth"},
		"an unknown notification":        map[string]any{"notify": map[string]any{"kind": "everything", "on": true}},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, settingsRequest(t, http.MethodPatch, "/api/miniapp/v1/me/settings", initData, body))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
		})
	}

	// An empty timezone is not a bad value: it is how somebody goes back
	// to following the chat's zone.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, settingsRequest(t, http.MethodPatch, "/api/miniapp/v1/me/settings",
		initData, map[string]any{"timezone": ""}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("clearing the timezone = %d, want 200: %s", recorder.Code, recorder.Body)
	}
}

type fakeSwitchboard struct{ set map[string]bool }

func (f *fakeSwitchboard) NotifyEnabled(context.Context, common.NotifyScope, int64, string) (bool, error) {
	return false, nil
}

func (f *fakeSwitchboard) NotifySettings(context.Context, common.NotifyScope, int64) (map[string]bool, error) {
	return f.set, nil
}

func (f *fakeSwitchboard) SetNotifyEnabled(_ context.Context, _ common.NotifyScope, _ int64, kind string, on bool) error {
	if f.set == nil {
		f.set = map[string]bool{}
	}
	f.set[kind] = on
	return nil
}

func (f *fakeSwitchboard) NotifySubjects(_ context.Context, _ common.NotifyScope, _ string, c []int64) ([]int64, error) {
	return c, nil
}
