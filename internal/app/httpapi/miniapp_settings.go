package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// The settings the bot offers in a private conversation, offered here too.
//
// Two owners, never mixed on one screen: what a person sets for themselves,
// and what a manager sets for a chat. That separation is the whole reason
// the bot's own settings are two different menus, and copying the menus
// without copying the separation would be the fastest way to have somebody
// mute a chat when they meant to mute themselves.
//
// What deliberately stays in the bot: appointing a moderator, which is done
// by replying to that person's message and has no meaning outside a
// conversation; and the change log, which is a record rather than a
// setting. Both are linked to from here rather than reimplemented.

// MiniAppSettings is everything these endpoints read and write.
type MiniAppSettings interface {
	Find(ctx context.Context, chatID common.ChatID) (*chat.Settings, error)
	Save(ctx context.Context, settings chat.Settings) (chat.Settings, error)
	ManagedChats(ctx context.Context, userID common.UserID) ([]chat.Settings, error)
	SetEnabledGames(ctx context.Context, chatID common.ChatID, games []competition.GameCode) error
	SetAutoSubscribeGame(ctx context.Context, chatID common.ChatID, game competition.GameCode, on bool) error

	UserLocale(ctx context.Context, userID common.UserID) (*common.LocaleCode, error)
	SetUserLocale(ctx context.Context, userID common.UserID, locale common.LocaleCode) error
	UserTimezone(ctx context.Context, userID common.UserID) (*string, error)
	SetUserTimezone(ctx context.Context, userID common.UserID, timezone string) error
	Nickname(ctx context.Context, userID common.UserID) (*string, error)
	SetNickname(ctx context.Context, userID common.UserID, nickname string) error
	PrefersHLTVLogos(ctx context.Context, userID common.UserID) (bool, error)
	SetPrefersHLTVLogos(ctx context.Context, userID common.UserID, prefer bool) error
}

// MiniAppAuthorizer answers whether this person may change that chat.
type MiniAppAuthorizer interface {
	HasPermission(ctx context.Context, chatID common.ChatID, userID common.UserID, permission chat.Permission) (bool, error)
}

// switchDTO is one boolean setting as the app renders it: a label key the
// page already knows, and the state. Sent as a list rather than as named
// fields so a new switch on the server appears in the app without the page
// having to be taught about it.
type switchDTO struct {
	Kind string `json:"kind"`
	On   bool   `json:"on"`
}

// personalSettingsDTO is what somebody sets for themselves.
type personalSettingsDTO struct {
	Locale string `json:"locale"`
	// Timezone is empty when they have never chosen one, in which case
	// every private screen falls back to the chat's — which is a real
	// state and the screen says so rather than inventing UTC.
	Timezone string `json:"timezone"`
	// Nickname is the name leaderboards use; empty means their Telegram
	// name is used as-is.
	Nickname   string      `json:"nickname"`
	LogoSource string      `json:"logo_source"`
	Notify     []switchDTO `json:"notify"`
}

// chatSettingsDTO is what a manager sets for one chat.
type chatSettingsDTO struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Locale         string `json:"locale"`
	Timezone       string `json:"timezone"`
	StreamLanguage string `json:"stream_language"`
	TopTierOnly    bool   `json:"top_tier_only"`
	PreferHLTVFlag bool   `json:"prefer_hltv_flags"`
	// QuietFrom/QuietTo are minutes since local midnight, -1 when the chat
	// has no quiet window at all.
	QuietFrom int `json:"quiet_from"`
	QuietTo   int `json:"quiet_to"`
	// Games and AutoSubscribe are the full catalogue with a flag each, not
	// only the enabled ones: a list that hides what is off cannot be used
	// to turn anything on.
	Games         []gameToggleDTO `json:"games"`
	Notify        []switchDTO     `json:"notify"`
	CanManage     bool            `json:"can_manage"`
	ModeratorsURL string          `json:"moderators_hint,omitempty"`
}

type gameToggleDTO struct {
	Game          string `json:"game"`
	Enabled       bool   `json:"enabled"`
	AutoSubscribe bool   `json:"auto_subscribe"`
}

type settingsDTO struct {
	Personal personalSettingsDTO `json:"personal"`
	Chats    []chatSettingsDTO   `json:"chats"`
}

// settingsHandler serves GET /api/miniapp/v1/me/settings.
func settingsHandler(deps MiniAppDeps, store MiniAppSettings, switches common.NotifySwitchboard) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if store == nil {
			http.Error(w, "settings are not configured", http.StatusServiceUnavailable)
			return
		}
		body, err := readSettings(r.Context(), store, switches, user.ID)
		if err != nil {
			miniAppError(w, deps.Log, "settings unavailable", err)
			return
		}
		writeJSON(w, body)
	})
}

func readSettings(ctx context.Context, store MiniAppSettings, switches common.NotifySwitchboard, userID common.UserID) (settingsDTO, error) {
	var body settingsDTO
	body.Personal.Locale = string(common.LocaleRU)
	if locale, err := store.UserLocale(ctx, userID); err == nil && locale != nil {
		body.Personal.Locale = string(*locale)
	}
	if zone, err := store.UserTimezone(ctx, userID); err == nil && zone != nil {
		body.Personal.Timezone = *zone
	}
	if nickname, err := store.Nickname(ctx, userID); err == nil && nickname != nil {
		body.Personal.Nickname = *nickname
	}
	body.Personal.LogoSource = "provider"
	if prefer, err := store.PrefersHLTVLogos(ctx, userID); err == nil && prefer {
		body.Personal.LogoSource = "hltv"
	}
	body.Personal.Notify = readSwitches(ctx, switches, common.ScopeUser, userID.Value, personalNotifyKinds())

	chats, err := store.ManagedChats(ctx, userID)
	if err != nil {
		return settingsDTO{}, err
	}
	body.Chats = make([]chatSettingsDTO, 0, len(chats))
	for _, settings := range chats {
		body.Chats = append(body.Chats, chatSettingsView(ctx, switches, settings))
	}
	return body, nil
}

func chatSettingsView(ctx context.Context, switches common.NotifySwitchboard, settings chat.Settings) chatSettingsDTO {
	view := chatSettingsDTO{
		ID: settings.ChatID.Value, Title: settings.Title,
		Locale: string(settings.Locale), Timezone: settings.Timezone,
		StreamLanguage: string(settings.StreamLocale()),
		TopTierOnly:    settings.DefaultTopTierOnly,
		PreferHLTVFlag: settings.PreferHLTVFlags,
		QuietFrom:      -1, QuietTo: -1,
		CanManage: true,
	}
	if settings.QuietHoursSet() {
		view.QuietFrom, view.QuietTo = *settings.QuietFromMinute, *settings.QuietToMinute
	}
	for _, game := range competition.Games {
		view.Games = append(view.Games, gameToggleDTO{
			Game: string(game), Enabled: settings.GameEnabled(game),
			AutoSubscribe: settings.AutoSubscribesTo(game),
		})
	}
	view.Notify = readSwitches(ctx, switches, common.ScopeChat, settings.ChatID.Value, chatNotifyKinds())
	return view
}

func personalNotifyKinds() []string {
	out := make([]string, 0, len(common.PersonalNotificationKinds))
	for _, kind := range common.PersonalNotificationKinds {
		out = append(out, string(kind))
	}
	return out
}

func chatNotifyKinds() []string {
	out := make([]string, 0, len(common.ChatNotificationKinds))
	for _, kind := range common.ChatNotificationKinds {
		out = append(out, string(kind))
	}
	return out
}

// readSwitches renders a switchboard scope in the catalogue's own order, so
// the switches never swap places between two views of the same screen.
func readSwitches(ctx context.Context, switches common.NotifySwitchboard, scope common.NotifyScope, subject int64, kinds []string) []switchDTO {
	out := make([]switchDTO, 0, len(kinds))
	stored := map[string]bool{}
	if switches != nil {
		if found, err := switches.NotifySettings(ctx, scope, subject); err == nil {
			stored = found
		}
	}
	for _, kind := range kinds {
		out = append(out, switchDTO{Kind: kind, On: stored[kind]})
	}
	return out
}

// personalPatch is the writable half of a person's own settings. Every
// field is a pointer: a PATCH says what changed and stays silent about
// everything else, so two screens open at once cannot overwrite each
// other's untouched fields.
type personalPatch struct {
	Locale     *string `json:"locale"`
	Timezone   *string `json:"timezone"`
	Nickname   *string `json:"nickname"`
	LogoSource *string `json:"logo_source"`
	Notify     *struct {
		Kind string `json:"kind"`
		On   bool   `json:"on"`
	} `json:"notify"`
}

// patchSettingsHandler serves PATCH /api/miniapp/v1/me/settings.
func patchSettingsHandler(deps MiniAppDeps, store MiniAppSettings, switches common.NotifySwitchboard) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if store == nil {
			http.Error(w, "settings are not configured", http.StatusServiceUnavailable)
			return
		}
		var patch personalPatch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, settingsPatchMaxBytes)).Decode(&patch); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := applyPersonalPatch(r.Context(), store, switches, user.ID, patch); err != nil {
			if err == errInvalidSetting {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			miniAppError(w, deps.Log, "could not save the setting", err)
			return
		}
		body, err := readSettings(r.Context(), store, switches, user.ID)
		if err != nil {
			miniAppError(w, deps.Log, "settings unavailable", err)
			return
		}
		writeJSON(w, body)
	})
}

func applyPersonalPatch(ctx context.Context, store MiniAppSettings, switches common.NotifySwitchboard, userID common.UserID, patch personalPatch) error {
	if err := applyPersonalIdentity(ctx, store, userID, patch); err != nil {
		return err
	}
	if patch.LogoSource != nil {
		if *patch.LogoSource != "provider" && *patch.LogoSource != "hltv" {
			return errInvalidSetting
		}
		if err := store.SetPrefersHLTVLogos(ctx, userID, *patch.LogoSource == "hltv"); err != nil {
			return err
		}
	}
	if patch.Notify != nil {
		kind := common.NotificationKind(patch.Notify.Kind)
		if !common.KnownNotificationKind(kind) || switches == nil {
			return errInvalidSetting
		}
		if err := switches.SetNotifyEnabled(ctx, common.ScopeUser, userID.Value, patch.Notify.Kind, patch.Notify.On); err != nil {
			return err
		}
	}
	return nil
}

// applyPersonalIdentity is the half of the patch about who somebody is and
// where they are: language, timezone, the name leaderboards use.
func applyPersonalIdentity(ctx context.Context, store MiniAppSettings, userID common.UserID, patch personalPatch) error {
	if patch.Locale != nil {
		locale, ok := parseLocale(*patch.Locale)
		if !ok {
			return errInvalidSetting
		}
		if err := store.SetUserLocale(ctx, userID, locale); err != nil {
			return err
		}
	}
	if patch.Timezone != nil {
		// An empty string clears it, which is how somebody goes back to
		// following the chat's zone.
		zone := strings.TrimSpace(*patch.Timezone)
		if zone != "" && !validTimezone(zone) {
			return errInvalidSetting
		}
		if err := store.SetUserTimezone(ctx, userID, zone); err != nil {
			return err
		}
	}
	if patch.Nickname != nil {
		name := strings.TrimSpace(*patch.Nickname)
		if len([]rune(name)) > nicknameMaxRunes {
			return errInvalidSetting
		}
		if err := store.SetNickname(ctx, userID, name); err != nil {
			return err
		}
	}
	return nil
}

// chatPatch is the writable half of a chat's settings.
type chatPatch struct {
	Locale         *string `json:"locale"`
	Timezone       *string `json:"timezone"`
	StreamLanguage *string `json:"stream_language"`
	TopTierOnly    *bool   `json:"top_tier_only"`
	PreferHLTVFlag *bool   `json:"prefer_hltv_flags"`
	QuietFrom      *int    `json:"quiet_from"`
	QuietTo        *int    `json:"quiet_to"`
	Game           *struct {
		Game          string `json:"game"`
		Enabled       *bool  `json:"enabled"`
		AutoSubscribe *bool  `json:"auto_subscribe"`
	} `json:"game"`
	Notify *struct {
		Kind string `json:"kind"`
		On   bool   `json:"on"`
	} `json:"notify"`
}

// patchChatSettingsHandler serves PATCH /api/miniapp/v1/chats/{id}/settings.
//
// The permission is re-checked here on every write, never carried over
// from whatever the list said when the screen was opened: rights can be
// taken away while somebody has the app in front of them, and a screen is
// not an authorization.
func patchChatSettingsHandler(deps MiniAppDeps, store MiniAppSettings, switches common.NotifySwitchboard, authz MiniAppAuthorizer) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if store == nil || authz == nil {
			http.Error(w, "settings are not configured", http.StatusServiceUnavailable)
			return
		}
		chatID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid chat id", http.StatusBadRequest)
			return
		}
		id := common.ChatID{Value: chatID}

		allowed, err := authz.HasPermission(r.Context(), id, user.ID, chat.PermissionManageGroupSettings)
		if err != nil {
			miniAppError(w, deps.Log, "permission check failed", err)
			return
		}
		if !allowed {
			http.Error(w, "not a manager of this chat", http.StatusForbidden)
			return
		}

		var patch chatPatch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, settingsPatchMaxBytes)).Decode(&patch); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		settings, err := store.Find(r.Context(), id)
		if err != nil || settings == nil {
			http.Error(w, "chat not found", http.StatusNotFound)
			return
		}
		if err := applyChatPatch(r.Context(), store, switches, *settings, patch); err != nil {
			if err == errInvalidSetting {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			miniAppError(w, deps.Log, "could not save the setting", err)
			return
		}
		updated, err := store.Find(r.Context(), id)
		if err != nil || updated == nil {
			miniAppError(w, deps.Log, "settings unavailable", err)
			return
		}
		writeJSON(w, chatSettingsView(r.Context(), switches, *updated))
	})
}

//nolint:gocyclo // a patch of N independent optional fields is N independent branches; splitting it would only move the same count into helpers that each read worse
func applyChatPatch(ctx context.Context, store MiniAppSettings, switches common.NotifySwitchboard, settings chat.Settings, patch chatPatch) error {
	// The satellite tables first: they are their own writes and do not
	// travel through Save.
	if patch.Game != nil {
		game := competition.GameCode(strings.ToUpper(patch.Game.Game))
		if !knownGame(game) {
			return errInvalidSetting
		}
		if patch.Game.Enabled != nil {
			games := withGame(settings.EnabledGames, game, *patch.Game.Enabled)
			if err := store.SetEnabledGames(ctx, settings.ChatID, games); err != nil {
				return err
			}
		}
		if patch.Game.AutoSubscribe != nil {
			if err := store.SetAutoSubscribeGame(ctx, settings.ChatID, game, *patch.Game.AutoSubscribe); err != nil {
				return err
			}
		}
	}
	if patch.Notify != nil {
		kind := common.ChatNotificationKind(patch.Notify.Kind)
		if !common.KnownChatNotification(kind) || switches == nil {
			return errInvalidSetting
		}
		if err := switches.SetNotifyEnabled(ctx, common.ScopeChat, settings.ChatID.Value, patch.Notify.Kind, patch.Notify.On); err != nil {
			return err
		}
	}

	changed := false
	if patch.Locale != nil {
		locale, ok := parseLocale(*patch.Locale)
		if !ok {
			return errInvalidSetting
		}
		settings.Locale, changed = locale, true
	}
	if patch.StreamLanguage != nil {
		locale, ok := parseLocale(*patch.StreamLanguage)
		if !ok {
			return errInvalidSetting
		}
		settings.StreamLanguage, changed = locale, true
	}
	if patch.Timezone != nil {
		if !validTimezone(*patch.Timezone) {
			return errInvalidSetting
		}
		settings.Timezone, changed = *patch.Timezone, true
	}
	if patch.TopTierOnly != nil {
		settings.DefaultTopTierOnly, changed = *patch.TopTierOnly, true
	}
	if patch.PreferHLTVFlag != nil {
		settings.PreferHLTVFlags, changed = *patch.PreferHLTVFlag, true
	}
	if patch.QuietFrom != nil || patch.QuietTo != nil {
		if err := applyQuietHours(&settings, patch); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	_, err := store.Save(ctx, settings)
	return err
}

// applyQuietHours sets or clears the window. Either bound at -1 clears it,
// which is how a chat goes back to being reachable at any hour.
func applyQuietHours(settings *chat.Settings, patch chatPatch) error {
	from, to := -1, -1
	if settings.QuietHoursSet() {
		from, to = *settings.QuietFromMinute, *settings.QuietToMinute
	}
	if patch.QuietFrom != nil {
		from = *patch.QuietFrom
	}
	if patch.QuietTo != nil {
		to = *patch.QuietTo
	}
	if from < 0 || to < 0 {
		settings.QuietFromMinute, settings.QuietToMinute = nil, nil
		return nil
	}
	if from >= minutesPerDay || to >= minutesPerDay {
		return errInvalidSetting
	}
	settings.QuietFromMinute, settings.QuietToMinute = &from, &to
	return nil
}

func withGame(games []competition.GameCode, game competition.GameCode, on bool) []competition.GameCode {
	out := make([]competition.GameCode, 0, len(games)+1)
	for _, existing := range games {
		if existing != game {
			out = append(out, existing)
		}
	}
	if on {
		out = append(out, game)
	}
	return out
}

func knownGame(game competition.GameCode) bool {
	for _, known := range competition.Games {
		if known == game {
			return true
		}
	}
	return false
}

const (
	// settingsPatchMaxBytes bounds a settings body. These carry one field
	// each; anything larger is not a setting.
	settingsPatchMaxBytes = 4 << 10
	// nicknameMaxRunes mirrors the bot's own limit for the same field, so
	// a name accepted here is a name the leaderboards can render.
	nicknameMaxRunes = 40
	minutesPerDay    = 24 * 60
)

// errInvalidSetting is a refusal the caller turns into a 400. A setting
// the server does not recognise is never quietly ignored: a screen that
// shows a value the server did not store is worse than an error.
var errInvalidSetting = errors.New("invalid setting")

// parseLocale refuses a language the bot does not speak.
//
// Checked on the raw string rather than through common.LocaleFrom, which
// answers Russian for anything it does not recognise — a sensible default
// when reading Telegram's own language hint, and silent data loss when
// reading a value somebody sent us on purpose.
func parseLocale(value string) (common.LocaleCode, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(common.LocaleRU):
		return common.LocaleRU, true
	case string(common.LocaleEN):
		return common.LocaleEN, true
	default:
		return "", false
	}
}

// validTimezone accepts only a zone the runtime can actually load, which
// is the same check the bot's /timezone command makes.
func validTimezone(name string) bool {
	_, err := time.LoadLocation(name)
	return err == nil
}
