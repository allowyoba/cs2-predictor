// Package chat holds chat settings, per-event topic overrides, moderator
// management, and the Telegram-membership-aware authorization rules.
package chat

import (
	"context"
	"errors"
	"time"

	"cs2predictor/internal/platform/common"
)

// ErrAccessDenied is returned by ChatAuthorizationService when the caller
// isn't allowed to manage the chat.
var ErrAccessDenied = errors.New("user is not allowed to manage this chat")

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

type Moderator struct {
	ChatID      common.ChatID
	UserID      common.UserID
	AppointedBy common.UserID
	Username    string
	DisplayName string
	// Permissions granted at appointment time. A nil/empty slice means the
	// moderator has no granted permissions yet (a state that only happens
	// transiently — every UI path that appoints a moderator picks a preset
	// or manual set before saving).
	Permissions []Permission
}

type ModeratorInfo struct {
	UserID      common.UserID
	Username    string
	DisplayName string
	AppointedBy common.UserID
	Permissions []Permission
}

// Permission is one granular capability a moderator can be granted,
// independent of the others — a moderator with only ViewStats cannot touch
// events, and vice versa. Telegram owner/admin bypass this entirely (see
// AuthorizationService.HasPermission).
type Permission string

const (
	PermissionManageEvents        Permission = "manage_events"
	PermissionManageMatches       Permission = "manage_matches"
	PermissionViewStats           Permission = "view_stats"
	PermissionManageGroupSettings Permission = "manage_group_settings"
)

// AllPermissions lists every known permission, in the fixed order used to
// render toggle screens and to encode/decode the manual-selection bitmask
// (see the telegram adapter's permission wizard).
func AllPermissions() []Permission {
	return []Permission{PermissionManageEvents, PermissionManageMatches, PermissionViewStats, PermissionManageGroupSettings}
}

// ValidPermission reports whether p is one of the known permissions —
// guards against persisting or matching against a typo'd or stale slug.
func ValidPermission(p Permission) bool {
	for _, known := range AllPermissions() {
		if p == known {
			return true
		}
	}
	return false
}

// PresetFullAccess/PresetContent/PresetStatsOnly are the three canned
// permission sets offered on the assignment/edit screens, alongside a
// fourth "configure manually" option that starts from an empty set.
func PresetFullAccess() []Permission { return AllPermissions() }
func PresetContent() []Permission {
	return []Permission{PermissionManageEvents, PermissionManageMatches}
}
func PresetStatsOnly() []Permission { return []Permission{PermissionViewStats} }

// HasPermission reports whether perms includes p.
func HasPermission(perms []Permission, p Permission) bool {
	for _, has := range perms {
		if has == p {
			return true
		}
	}
	return false
}

// ActiveChatLister is the narrow view scheduled chat-wide reports need:
// just ListActive. DigestScheduler and CompetitionSynchronization declare
// it as their field type instead of depending on the full Repository.
type ActiveChatLister interface {
	ListActive(ctx context.Context) ([]Settings, error)
}

type MemberRole string

const (
	RoleOwner         MemberRole = "OWNER"
	RoleAdministrator MemberRole = "ADMINISTRATOR"
	RoleMember        MemberRole = "MEMBER"
	RoleRestricted    MemberRole = "RESTRICTED"
	RoleLeft          MemberRole = "LEFT"
	RoleKicked        MemberRole = "KICKED"
)

// MembershipGateway fetches a user's live Telegram role for a chat — never
// cached, so every authorization check hits the Bot API.
type MembershipGateway interface {
	Role(ctx context.Context, chatID common.ChatID, userID common.UserID) (MemberRole, error)
}

type AdministratorLister interface {
	Administrators(ctx context.Context, chatID common.ChatID) ([]common.UserID, error)
}

// Repository is the chat-settings persistence port. Everything the admin
// surface actually depends on lives here rather than in optional
// extensions asserted at runtime: a missing method is then a compile
// error, not a screen that silently renders empty.
type Repository interface {
	Find(ctx context.Context, chatID common.ChatID) (*Settings, error)
	Save(ctx context.Context, settings Settings) (Settings, error)

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

// AuthorizationService implements the exact canManage rule: Telegram
// owner/admin always passes; a MEMBER passes only if also flagged as an
// internal moderator (moderator flag alone is insufficient without current
// Telegram membership — RESTRICTED/LEFT/KICKED never pass even if flagged).
type AuthorizationService struct {
	chats    Repository
	telegram MembershipGateway
}

func NewAuthorizationService(chats Repository, telegram MembershipGateway) *AuthorizationService {
	return &AuthorizationService{chats: chats, telegram: telegram}
}

func (s *AuthorizationService) IsTelegramAdmin(ctx context.Context, chatID common.ChatID, userID common.UserID) (bool, error) {
	role, err := s.telegram.Role(ctx, chatID, userID)
	if err != nil {
		return false, err
	}
	return role == RoleOwner || role == RoleAdministrator, nil
}

func (s *AuthorizationService) CanManage(ctx context.Context, chatID common.ChatID, userID common.UserID) (bool, error) {
	role, err := s.telegram.Role(ctx, chatID, userID)
	if err != nil {
		return false, err
	}
	if role == RoleOwner || role == RoleAdministrator {
		return true, nil
	}
	if role == RoleMember {
		return s.chats.IsModerator(ctx, chatID, userID)
	}
	return false, nil
}

// HasOtherManager reports whether OtherManagers would return anything,
// without paying for building the full deduplicated list — cheaper for
// callers (e.g. picking which explanation text to show) that only need the
// yes/no answer.
func (s *AuthorizationService) HasOtherManager(ctx context.Context, chatID common.ChatID, actor common.UserID) (bool, error) {
	if admins, ok := s.telegram.(AdministratorLister); ok {
		ids, err := admins.Administrators(ctx, chatID)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if id != actor {
				return true, nil
			}
		}
	}
	items, err := s.chats.ListModerators(ctx, chatID)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.UserID == actor {
			continue
		}
		ok, err := s.CanManage(ctx, chatID, item.UserID)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// OtherManagers returns every qualifying manager of chatID besides actor:
// the deduplicated union of live Telegram admins/owners and flagged
// moderators who currently pass CanManage. Used to fan a DM confirmation
// request out to everyone who could approve it (see
// PendingUnsubscribeRepository). Order is not significant.
func (s *AuthorizationService) OtherManagers(ctx context.Context, chatID common.ChatID, actor common.UserID) ([]common.UserID, error) {
	seen := map[common.UserID]bool{actor: true}
	var out []common.UserID
	add := func(id common.UserID) {
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}

	if admins, ok := s.telegram.(AdministratorLister); ok {
		ids, err := admins.Administrators(ctx, chatID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			add(id)
		}
	}
	items, err := s.chats.ListModerators(ctx, chatID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if seen[item.UserID] {
			continue
		}
		ok, err := s.CanManage(ctx, chatID, item.UserID)
		if err != nil {
			return nil, err
		}
		if ok {
			add(item.UserID)
		}
	}
	return out, nil
}

func (s *AuthorizationService) RequireManager(ctx context.Context, chatID common.ChatID, userID common.UserID) error {
	ok, err := s.CanManage(ctx, chatID, userID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAccessDenied
	}
	return nil
}

func (s *AuthorizationService) RequireTelegramAdmin(ctx context.Context, chatID common.ChatID, userID common.UserID) error {
	ok, err := s.IsTelegramAdmin(ctx, chatID, userID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAccessDenied
	}
	return nil
}

// HasPermission is CanManage's granular sibling: Telegram owner/admin always
// pass, unconditionally; a plain MEMBER passes only if flagged as a
// moderator AND holding the specific permission asked about. RESTRICTED,
// LEFT and KICKED never pass, matching CanManage.
func (s *AuthorizationService) HasPermission(ctx context.Context, chatID common.ChatID, userID common.UserID, permission Permission) (bool, error) {
	role, err := s.telegram.Role(ctx, chatID, userID)
	if err != nil {
		return false, err
	}
	if role == RoleOwner || role == RoleAdministrator {
		return true, nil
	}
	if role != RoleMember {
		return false, nil
	}
	perms, err := s.chats.ModeratorPermissions(ctx, chatID, userID)
	if err != nil {
		return false, err
	}
	return HasPermission(perms, permission), nil
}

// RequirePermission is HasPermission's error-returning form, mirroring
// RequireManager.
func (s *AuthorizationService) RequirePermission(ctx context.Context, chatID common.ChatID, userID common.UserID, permission Permission) error {
	ok, err := s.HasPermission(ctx, chatID, userID, permission)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAccessDenied
	}
	return nil
}

// IsMember reports whether userID currently belongs to chatID at all
// (any role except LEFT/KICKED) — the membership check the moderator
// invitation flow runs before granting access, independent of whether the
// user is already trusted with anything.
func (s *AuthorizationService) IsMember(ctx context.Context, chatID common.ChatID, userID common.UserID) (bool, error) {
	role, err := s.telegram.Role(ctx, chatID, userID)
	if err != nil {
		return false, err
	}
	return role != "" && role != RoleLeft && role != RoleKicked, nil
}
