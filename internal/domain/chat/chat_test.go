package chat

import (
	"context"
	"testing"

	"cs2predictor/internal/platform/common"
)

type fakeMembership struct{ role MemberRole }

func (f fakeMembership) Role(context.Context, common.ChatID, common.UserID) (MemberRole, error) {
	return f.role, nil
}

type fakeRepo struct{ moderator bool }

func (f fakeRepo) Find(context.Context, common.ChatID) (*Settings, error) { return nil, nil }
func (f fakeRepo) Save(_ context.Context, s Settings) (Settings, error)   { return s, nil }
func (f fakeRepo) IsModerator(context.Context, common.ChatID, common.UserID) (bool, error) {
	return f.moderator, nil
}
func (f fakeRepo) AddModerator(context.Context, Moderator) error                       { return nil }
func (f fakeRepo) RemoveModerator(context.Context, common.ChatID, common.UserID) error { return nil }
func (f fakeRepo) EventTopic(context.Context, common.ChatID, common.EventID) (*int64, error) {
	return nil, nil
}
func (f fakeRepo) SaveEventTopic(context.Context, EventTopic) error                     { return nil }
func (f fakeRepo) ClearEventTopic(context.Context, common.ChatID, common.EventID) error { return nil }

// The remainder of Repository is unused by these authorization tests but
// required by the interface — kept as explicit no-ops rather than embedding
// a shared stub, so a future method addition surfaces here as a compile
// error worth a second look.
func (f fakeRepo) ListModerators(context.Context, common.ChatID) ([]ModeratorInfo, error) {
	return nil, nil
}
func (f fakeRepo) RecordManaged(context.Context, common.ChatID, common.UserID) error { return nil }
func (f fakeRepo) ManagedChats(context.Context, common.UserID) ([]Settings, error)   { return nil, nil }
func (f fakeRepo) SetDMSession(context.Context, common.UserID, common.ChatID) error  { return nil }
func (f fakeRepo) DMSession(context.Context, common.UserID) (*common.ChatID, error)  { return nil, nil }
func (f fakeRepo) ClearDMSession(context.Context, common.UserID) error               { return nil }
func (f fakeRepo) UserLocale(context.Context, common.UserID) (*common.LocaleCode, error) {
	return nil, nil
}
func (f fakeRepo) SetUserLocale(context.Context, common.UserID, common.LocaleCode) error { return nil }

// An admin bypasses the moderator check entirely.
func TestCanManage_AdminBypassesModeratorFlag(t *testing.T) {
	svc := NewAuthorizationService(fakeRepo{moderator: false}, fakeMembership{role: RoleAdministrator})
	ok, err := svc.CanManage(context.Background(), common.ChatID{Value: -1}, common.UserID{Value: 1})
	if err != nil || !ok {
		t.Fatalf("expected admin to manage regardless of moderator flag, got ok=%v err=%v", ok, err)
	}
}

// Ground truth: moderator flag alone is insufficient without current
// Telegram membership — a user who left is never allowed even if still
// flagged as moderator in the DB.
func TestCanManage_ModeratorFlagInsufficientWithoutMembership(t *testing.T) {
	svc := NewAuthorizationService(fakeRepo{moderator: true}, fakeMembership{role: RoleLeft})
	ok, err := svc.CanManage(context.Background(), common.ChatID{Value: -1}, common.UserID{Value: 1})
	if err != nil || ok {
		t.Fatalf("expected LEFT member with moderator flag to be denied, got ok=%v err=%v", ok, err)
	}
}

func TestCanManage_MemberWithModeratorFlagAllowed(t *testing.T) {
	svc := NewAuthorizationService(fakeRepo{moderator: true}, fakeMembership{role: RoleMember})
	ok, err := svc.CanManage(context.Background(), common.ChatID{Value: -1}, common.UserID{Value: 1})
	if err != nil || !ok {
		t.Fatalf("expected MEMBER+moderator to be allowed, got ok=%v err=%v", ok, err)
	}
}

func TestCanManage_PlainMemberDenied(t *testing.T) {
	svc := NewAuthorizationService(fakeRepo{moderator: false}, fakeMembership{role: RoleMember})
	ok, err := svc.CanManage(context.Background(), common.ChatID{Value: -1}, common.UserID{Value: 1})
	if err != nil || ok {
		t.Fatalf("expected plain MEMBER to be denied, got ok=%v err=%v", ok, err)
	}
}

func TestRequireManager_ReturnsErrAccessDenied(t *testing.T) {
	svc := NewAuthorizationService(fakeRepo{moderator: false}, fakeMembership{role: RoleKicked})
	if err := svc.RequireManager(context.Background(), common.ChatID{Value: -1}, common.UserID{Value: 1}); err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

// multiRoleMembership implements MembershipGateway and the optional
// AdministratorLister with a per-user role map (default MEMBER) — needed
// for OtherManagers, which distinguishes several different users' roles at
// once, unlike fakeMembership's single shared role.
type multiRoleMembership struct {
	roles  map[int64]MemberRole
	admins []common.UserID
}

func (m multiRoleMembership) Role(_ context.Context, _ common.ChatID, userID common.UserID) (MemberRole, error) {
	if r, ok := m.roles[userID.Value]; ok {
		return r, nil
	}
	return RoleMember, nil
}
func (m multiRoleMembership) Administrators(context.Context, common.ChatID) ([]common.UserID, error) {
	return m.admins, nil
}

// moderatorRepo extends fakeRepo with ListModerators/a per-user
// IsModerator, for OtherManagers' moderator-enumeration branch.
type moderatorRepo struct {
	fakeRepo
	mods []ModeratorInfo
}

func (r moderatorRepo) ListModerators(context.Context, common.ChatID) ([]ModeratorInfo, error) {
	return r.mods, nil
}
func (r moderatorRepo) IsModerator(_ context.Context, _ common.ChatID, userID common.UserID) (bool, error) {
	for _, m := range r.mods {
		if m.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

func TestOtherManagers_CombinesAdminsAndModeratorsDeduplicated(t *testing.T) {
	actor := common.UserID{Value: 1}
	admin := common.UserID{Value: 2}
	modOnly := common.UserID{Value: 3}
	adminAlsoListedAsMod := common.UserID{Value: 2} // same as admin — must not be duplicated

	telegram := multiRoleMembership{
		admins: []common.UserID{actor, admin},
		roles:  map[int64]MemberRole{actor.Value: RoleAdministrator, admin.Value: RoleAdministrator, modOnly.Value: RoleMember},
	}
	repo := moderatorRepo{mods: []ModeratorInfo{{UserID: modOnly}, {UserID: adminAlsoListedAsMod}}}
	svc := NewAuthorizationService(repo, telegram)

	got, err := svc.OtherManagers(context.Background(), common.ChatID{Value: -1}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("OtherManagers = %v, want exactly [admin, modOnly] (2 entries, deduplicated)", got)
	}
	seen := map[common.UserID]bool{}
	for _, id := range got {
		seen[id] = true
	}
	if !seen[admin] || !seen[modOnly] || seen[actor] {
		t.Fatalf("OtherManagers = %v, want {admin, modOnly} excluding actor", got)
	}
}

func TestOtherManagers_ExcludesModeratorWhoNoLongerPassesCanManage(t *testing.T) {
	actor := common.UserID{Value: 1}
	staleModerator := common.UserID{Value: 2} // flagged moderator, but left the chat on Telegram

	telegram := multiRoleMembership{roles: map[int64]MemberRole{staleModerator.Value: RoleLeft}}
	repo := moderatorRepo{mods: []ModeratorInfo{{UserID: staleModerator}}}
	svc := NewAuthorizationService(repo, telegram)

	got, err := svc.OtherManagers(context.Background(), common.ChatID{Value: -1}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("OtherManagers = %v, want empty: a moderator flag alone is insufficient once they've left", got)
	}
}

func TestOtherManagers_EmptyWhenActorIsSoleManager(t *testing.T) {
	actor := common.UserID{Value: 1}
	telegram := multiRoleMembership{admins: []common.UserID{actor}}
	svc := NewAuthorizationService(fakeRepo{}, telegram)

	got, err := svc.OtherManagers(context.Background(), common.ChatID{Value: -1}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("OtherManagers = %v, want empty when the actor is the only manager", got)
	}
}

func TestHasOtherManager_AgreesWithOtherManagers(t *testing.T) {
	actor := common.UserID{Value: 1}
	admin := common.UserID{Value: 2}
	telegram := multiRoleMembership{admins: []common.UserID{actor, admin}, roles: map[int64]MemberRole{actor.Value: RoleAdministrator, admin.Value: RoleAdministrator}}
	svc := NewAuthorizationService(fakeRepo{}, telegram)

	hasOther, err := svc.HasOtherManager(context.Background(), common.ChatID{Value: -1}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if !hasOther {
		t.Fatal("expected HasOtherManager=true to agree with a non-empty OtherManagers")
	}
}

func (f fakeRepo) SetDMReachable(context.Context, common.UserID, bool) error { return nil }
func (f fakeRepo) FilterDMReachable(context.Context, []common.UserID) ([]common.UserID, error) {
	return nil, nil
}
func (f fakeRepo) Nickname(context.Context, common.UserID) (*string, error) { return nil, nil }
func (f fakeRepo) SetNickname(context.Context, common.UserID, string) error { return nil }

func TestIsTelegramAdmin(t *testing.T) {
	cases := []struct {
		role MemberRole
		want bool
	}{
		{RoleOwner, true},
		{RoleAdministrator, true},
		{RoleMember, false},
		{RoleRestricted, false},
		{RoleLeft, false},
		{RoleKicked, false},
	}
	for _, tc := range cases {
		svc := NewAuthorizationService(fakeRepo{}, fakeMembership{role: tc.role})
		got, err := svc.IsTelegramAdmin(context.Background(), common.ChatID{Value: -1}, common.UserID{Value: 1})
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("IsTelegramAdmin(%s) = %v, want %v", tc.role, got, tc.want)
		}
	}
}

func TestRequireTelegramAdmin(t *testing.T) {
	svc := NewAuthorizationService(fakeRepo{}, fakeMembership{role: RoleAdministrator})
	if err := svc.RequireTelegramAdmin(context.Background(), common.ChatID{Value: -1}, common.UserID{Value: 1}); err != nil {
		t.Fatalf("expected an administrator to pass, got %v", err)
	}

	svc = NewAuthorizationService(fakeRepo{}, fakeMembership{role: RoleMember})
	if err := svc.RequireTelegramAdmin(context.Background(), common.ChatID{Value: -1}, common.UserID{Value: 1}); err != ErrAccessDenied {
		t.Fatalf("expected a plain member to be denied even with no moderator flag involved, got %v", err)
	}
}

func TestHasOtherManager_FalseWhenActorIsSoleManager(t *testing.T) {
	actor := common.UserID{Value: 1}
	telegram := multiRoleMembership{admins: []common.UserID{actor}}
	svc := NewAuthorizationService(fakeRepo{}, telegram)

	hasOther, err := svc.HasOtherManager(context.Background(), common.ChatID{Value: -1}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if hasOther {
		t.Fatal("expected HasOtherManager=false when the actor is the only manager")
	}
}

func TestZoneOrDefault(t *testing.T) {
	if got := ZoneOrDefault("Europe/Berlin"); got.String() != "Europe/Berlin" {
		t.Fatalf("ZoneOrDefault(valid zone) = %v, want Europe/Berlin", got)
	}
	if got := ZoneOrDefault(""); got.String() != DefaultTimezone {
		t.Fatalf("ZoneOrDefault(empty) = %v, want the default %s", got, DefaultTimezone)
	}
	if got := ZoneOrDefault("Not/A/Real/Zone"); got.String() != DefaultTimezone {
		t.Fatalf("ZoneOrDefault(garbage) = %v, want the default %s", got, DefaultTimezone)
	}
}
