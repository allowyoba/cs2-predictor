package chat

import (
	"cs2predictor/internal/platform/common"
)

// Moderator/ModeratorInfo and the Permission model — an internally flagged
// user, granted a subset of the four capabilities below, who can act on a
// chat without being a Telegram admin themselves. See AuthorizationService
// (authorization.go) for how a permission is actually checked.

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
