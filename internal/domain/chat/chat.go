// Package chat holds chat settings, per-event topic overrides, moderator
// management, and the Telegram-membership-aware authorization rules. Split
// across three files by responsibility: chat.go (this file) has settings
// persistence and the shared Repository port, moderator.go has the
// moderator/permission model, and authorization.go has the
// Telegram-membership-aware AuthorizationService.
package chat

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// Settings mirrors ChatSettings: locale defaults to RU, timezone defaults to
// "Europe/Moscow" (not UTC) — that default drives leaderboard period
// bucketing whenever a chat hasn't set its own timezone.
type Settings struct {
	ChatID         common.ChatID
	Title          string
	Locale         common.LocaleCode
	Timezone       string // IANA zone name, e.g. "Europe/Moscow"
	DefaultTopicID *int64
	Active         bool
	// DefaultTopTierOnly makes "/events <query>" behave as if "top "/"топ "
	// had been typed, without needing to type it every time — a manager
	// toggles it from the settings menu. An explicit "top"/"топ" or
	// "all"/"все" prefix on a given search still overrides it for that one
	// search.
	DefaultTopTierOnly bool
	// AutoSubscribeTopTier, when set, subscribes this chat to a newly
	// discovered S/A tier tournament immediately instead of only offering
	// it via the proactive big-event-discovered notification — see
	// CompetitionSynchronization.announceBigEvent.
	AutoSubscribeTopTier bool
}

// DefaultTimezone is the fallback IANA zone for a chat that hasn't set one.
const DefaultTimezone = "Europe/Moscow"

// ZoneOrDefault resolves an IANA zone name to a *time.Location, falling back
// to DefaultTimezone (and finally UTC) if name is empty or doesn't parse —
// the one shared implementation of that fallback chain, used everywhere a
// chat's stored timezone string needs to become a usable *time.Location.
//
// name is checked for emptiness explicitly rather than left to
// time.LoadLocation: an empty string is a documented special case there
// that resolves straight to UTC, which would skip DefaultTimezone
// entirely for a chat that has no timezone set yet.
func ZoneOrDefault(name string) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	if loc, err := time.LoadLocation(DefaultTimezone); err == nil {
		return loc
	}
	return time.UTC
}

type EventTopic struct {
	ChatID  common.ChatID
	EventID common.EventID
	TopicID int64
}

// ActiveChatLister is the narrow view scheduled chat-wide reports need:
// just ListActive. DigestScheduler and CompetitionSynchronization declare
// it as their field type instead of depending on the full Repository.
type ActiveChatLister interface {
	ListActive(ctx context.Context) ([]Settings, error)
}

// Repository is the chat-settings persistence port. Everything the admin
// surface actually depends on lives here rather than in optional
// extensions asserted at runtime: a missing method is then a compile
// error, not a screen that silently renders empty.
type Repository interface {
	Find(ctx context.Context, chatID common.ChatID) (*Settings, error)
	Save(ctx context.Context, settings Settings) (Settings, error)
	// MigrateChatID renames a chat's id everywhere at once (via ON UPDATE
	// CASCADE on every foreign key referencing it) — Telegram permanently
	// renumbers a group's id when it's upgraded to a supergroup, announced
	// by a "migrate_to_chat_id" service message. Without this, the bot
	// would create a second, parallel row for the new id instead of
	// continuing the same chat's history, and the same group would appear
	// twice in the "chats you manage" list. A no-op if oldID has no row.
	MigrateChatID(ctx context.Context, oldID, newID common.ChatID) error

	IsModerator(ctx context.Context, chatID common.ChatID, userID common.UserID) (bool, error)
	AddModerator(ctx context.Context, moderator Moderator) error
	RemoveModerator(ctx context.Context, chatID common.ChatID, userID common.UserID) error
	ListModerators(ctx context.Context, chatID common.ChatID) ([]ModeratorInfo, error)
	// ModeratorPermissions returns the permissions granted to userID in
	// chatID, or nil if they aren't a moderator there at all — the read
	// side AuthorizationService.HasPermission checks on every gated action.
	ModeratorPermissions(ctx context.Context, chatID common.ChatID, userID common.UserID) ([]Permission, error)
	// SetModeratorPermissions replaces the full permission set for an
	// already-appointed moderator. Calling it for someone who isn't a
	// moderator is a no-op.
	SetModeratorPermissions(ctx context.Context, chatID common.ChatID, userID common.UserID, permissions []Permission) error
	// UserProfile resolves a display name/username pair for userID from the
	// shared telegram_user table, used to label assignment candidates and
	// invitation acceptors without a full ModeratorInfo. Returns nil if the
	// bot has never seen this user.
	UserProfile(ctx context.Context, userID common.UserID) (*UserProfile, error)
	// UserProfiles is UserProfile's batched sibling — one round trip to
	// label a whole list of candidates (e.g. a chat's recent poll
	// participants) instead of one query per person. A userID with no
	// cached profile is simply absent from the result map.
	UserProfiles(ctx context.Context, userIDs []common.UserID) (map[common.UserID]UserProfile, error)

	EventTopic(ctx context.Context, chatID common.ChatID, eventID common.EventID) (*int64, error)
	SaveEventTopic(ctx context.Context, topic EventTopic) error
	ClearEventTopic(ctx context.Context, chatID common.ChatID, eventID common.EventID) error

	// RecordManaged best-effort notes that a user just passed a
	// manager/admin check for a chat; ManagedChats reads it back, most
	// recently confirmed first, to populate the DM chat picker. Entries can
	// be stale (rights may have been revoked since), so callers still
	// re-verify live via AuthorizationService before acting on a choice.
	RecordManaged(ctx context.Context, chatID common.ChatID, userID common.UserID) error
	ManagedChats(ctx context.Context, userID common.UserID) ([]Settings, error)

	// The DM session is the fallback "which chat am I managing" for DM text
	// commands. Button-driven actions carry their chat in the callback data
	// itself and don't consult it — see targetManagedChat.
	SetDMSession(ctx context.Context, userID common.UserID, chatID common.ChatID) error
	// DMSession returns (nil, nil) when no session is set.
	DMSession(ctx context.Context, userID common.UserID) (*common.ChatID, error)
	ClearDMSession(ctx context.Context, userID common.UserID) error

	// UserLocale returns the user's own UI language for private chats, or
	// nil if they've never chosen one (group chats keep their own,
	// independent setting on Settings).
	UserLocale(ctx context.Context, userID common.UserID) (*common.LocaleCode, error)
	SetUserLocale(ctx context.Context, userID common.UserID, locale common.LocaleCode) error

	// SetDMReachable records whether the bot may message this user
	// privately. Telegram forbids a bot's first message to someone who has
	// never started a chat with it, so this is how the confirmation fan-out
	// knows in advance who it can actually reach; FilterDMReachable narrows
	// a candidate list to those users in one round trip.
	SetDMReachable(ctx context.Context, userID common.UserID, reachable bool) error
	FilterDMReachable(ctx context.Context, userIDs []common.UserID) ([]common.UserID, error)

	// Nickname returns the name a user has chosen for themselves on
	// leaderboards and result lists, or nil if they have never set one;
	// callers then fall back to the Telegram-sourced display_name. It is
	// a separate column from display_name because that column is
	// refreshed from the live Telegram profile on every vote (see
	// PredictionRepository.SaveVote), and a nickname needs to survive
	// that refresh.
	Nickname(ctx context.Context, userID common.UserID) (*string, error)
	// SetNickname stores the chosen name; an empty string clears it back
	// to the Telegram-sourced default.
	SetNickname(ctx context.Context, userID common.UserID, nickname string) error
}

// UserProfile is the minimal, best-effort identity the bot has cached for a
// Telegram user from prior interactions.
type UserProfile struct {
	UserID      common.UserID
	Username    string
	DisplayName string
}

// NotificationPrefs is one person's own opt-ins for the private nudges
// the bot can send them. Both default to false: unsolicited private
// messages from a bot are spam, so nothing is sent until the person turns
// it on from their own DM settings.
type NotificationPrefs struct {
	ResultRecaps  bool
	PollReminders bool
}

// NotificationPrefsRepository reads and writes those opt-ins. Kept
// separate from Repository so the DM settings screen is the only consumer
// that has to know about them; the jobs that send the nudges use the
// narrower common.NotificationAudience instead.
type NotificationPrefsRepository interface {
	NotificationPrefs(ctx context.Context, userID common.UserID) (NotificationPrefs, error)
	SetNotificationPref(ctx context.Context, userID common.UserID, kind common.NotificationKind, on bool) error
}
