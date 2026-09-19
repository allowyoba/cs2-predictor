package chat

import (
	"context"
	"errors"

	"cs2predictor/internal/platform/common"
)

// ErrAccessDenied is returned by AuthorizationService when the caller
// isn't allowed to manage the chat.
var ErrAccessDenied = errors.New("user is not allowed to manage this chat")

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
// PendingApprovalRepository). Order is not significant.
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
