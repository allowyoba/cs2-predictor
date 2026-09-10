package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

func TestPermMaskRoundTrip(t *testing.T) {
	for _, perms := range [][]chat.Permission{
		nil,
		chat.PresetFullAccess(),
		chat.PresetContent(),
		chat.PresetStatsOnly(),
		{chat.PermissionManageGroupSettings},
	} {
		mask := permMask(perms)
		got := permsFromMask(mask)
		if len(got) != len(perms) {
			t.Fatalf("round trip of %v produced %v", perms, got)
		}
		for _, p := range perms {
			if !chat.HasPermission(got, p) {
				t.Fatalf("round trip of %v lost permission %v (mask %d)", perms, p, mask)
			}
		}
	}
}

func TestParsePermWizardData(t *testing.T) {
	if purpose, subject, rest, ok := parsePermWizardData("e:1a:f"); !ok || purpose != permPurposeEditModerator || subject != "1a" || len(rest) != 1 || rest[0] != "f" {
		t.Fatalf("edit parse = %v %v %v %v", purpose, subject, rest, ok)
	}
	if purpose, subject, rest, ok := parsePermWizardData("i:"); !ok || purpose != permPurposeInvitation || subject != "" || len(rest) != 0 {
		t.Fatalf("invitation parse = %v %v %v %v", purpose, subject, rest, ok)
	}
	if _, _, _, ok := parsePermWizardData("x:1a"); ok {
		t.Fatal("expected an unknown purpose to be rejected")
	}
	if _, _, _, ok := parsePermWizardData("e"); ok {
		t.Fatal("expected a payload with no subject field to be rejected")
	}
}

// fakeInvitations is a minimal in-memory chat.ModeratorInvitationRepository.
type fakeInvitations struct {
	mu    sync.Mutex
	items map[string]chat.ModeratorInvitation
}

func newFakeInvitations() *fakeInvitations {
	return &fakeInvitations{items: map[string]chat.ModeratorInvitation{}}
}
func (f *fakeInvitations) CreateInvitation(_ context.Context, inv chat.ModeratorInvitation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[inv.Token] = inv
	return nil
}
func (f *fakeInvitations) Invitation(_ context.Context, token string) (*chat.ModeratorInvitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.items[token]
	if !ok {
		return nil, nil
	}
	return &inv, nil
}
func (f *fakeInvitations) UseInvitation(_ context.Context, token string, usedBy common.UserID, usedAt time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.items[token]
	if !ok || inv.UsedAt != nil || inv.RevokedAt != nil {
		return false, nil
	}
	inv.UsedAt = &usedAt
	inv.UsedBy = &usedBy
	f.items[token] = inv
	return true, nil
}
func (f *fakeInvitations) RevokeInvitation(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.items[token]
	if !ok {
		return nil
	}
	now := time.Now()
	inv.RevokedAt = &now
	f.items[token] = inv
	return nil
}

// recordingAdminActions is a minimal in-memory chat.AdminActionLog, kept
// here so permission-wizard tests can assert on what got audited.
type recordingAdminActions struct {
	mu      sync.Mutex
	actions []chat.AdminAction
}

func (r *recordingAdminActions) Record(_ context.Context, action chat.AdminAction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action)
	return nil
}
func (r *recordingAdminActions) Recent(_ context.Context, chatID common.ChatID, limit int) ([]chat.AdminAction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []chat.AdminAction
	for i := len(r.actions) - 1; i >= 0 && len(out) < limit; i-- {
		if r.actions[i].ChatID == chatID {
			out = append(out, r.actions[i])
		}
	}
	return out, nil
}

// TestModeratorsAdd_DeniedForPlainModerator is the spec regression check:
// appointing/removing moderators or editing their permissions is
// Telegram-admin-only — a moderator, even one with every content
// permission, must never be able to manage other moderators.
func TestModeratorsAdd_DeniedForPlainModerator(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	// newTestHandler wires a MEMBER-role membership by default; the actor
	// is flagged as a full-access moderator but is not a Telegram admin.
	_ = chats.AddModerator(context.Background(), chat.Moderator{ChatID: chatID, UserID: common.UserID{Value: 1}, AppointedBy: common.UserID{Value: 999}, Permissions: chat.PresetFullAccess()})

	data := "moderators:add"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Mod"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, ru(t, "error.forbidden")) {
		t.Fatalf("expected the forbidden reply for a non-admin moderator, got %q", body)
	}
}

// TestModeratorPermissions_AdminEditsExistingModeratorViaPresets exercises
// the full wizard for an already-appointed moderator: preset tap -> confirm
// -> apply, ending with SetModeratorPermissions holding exactly the preset's
// permissions and an audit entry recorded.
func TestModeratorPermissions_AdminEditsExistingModeratorViaPresets(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	target := common.UserID{Value: 5}
	_ = chats.AddModerator(context.Background(), chat.Moderator{ChatID: chatID, UserID: target, AppointedBy: common.UserID{Value: 1}, Permissions: chat.PresetStatsOnly()})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	adminActions := &recordingAdminActions{}
	handler.AdminActions = adminActions

	subject := permWizardSubject(permPurposeEditModerator, target)
	data := cbPermPreset(permPurposeEditModerator, subject, "content")
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	applyData := cbPermApply(permPurposeEditModerator, subject, permMask(chat.PresetContent()))
	cb2 := &CallbackQuery{ID: "cb2", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &applyData}
	if err := handler.handleCallback(context.Background(), cb2); err != nil {
		t.Fatal(err)
	}

	got, err := chats.ModeratorPermissions(context.Background(), chatID, target)
	if err != nil {
		t.Fatal(err)
	}
	if !chat.HasPermission(got, chat.PermissionManageEvents) || !chat.HasPermission(got, chat.PermissionManageMatches) || chat.HasPermission(got, chat.PermissionViewStats) {
		t.Fatalf("expected exactly the content preset after editing, got %v", got)
	}
	if len(adminActions.actions) == 0 || adminActions.actions[len(adminActions.actions)-1].Kind != "moderator_permissions_changed" {
		t.Fatalf("expected a moderator_permissions_changed audit entry, got %+v", adminActions.actions)
	}
}

// TestModeratorAssign_FromParticipants appoints a brand-new moderator
// reached through the "👥 From participants" list, verifying the chosen
// preset's permissions land on the new moderator row.
func TestModeratorAssign_FromParticipants(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	candidate := common.UserID{Value: 77}
	preds := &inMemoryPredictions{participants: []common.UserID{candidate}}
	handler.Predictions = prediction.NewService(preds, nil, handler.Clock)

	subject := permWizardSubject(permPurposeAssignNew, candidate)
	applyData := cbPermApply(permPurposeAssignNew, subject, permMask(chat.PresetFullAccess()))
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &applyData}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	isMod, err := chats.IsModerator(context.Background(), chatID, candidate)
	if err != nil || !isMod {
		t.Fatalf("expected candidate to be appointed, isMod=%v err=%v", isMod, err)
	}
	perms, _ := chats.ModeratorPermissions(context.Background(), chatID, candidate)
	if len(perms) != len(chat.AllPermissions()) {
		t.Fatalf("expected full access, got %v", perms)
	}
}

// TestGranularPermission_StatsOnlyModeratorCannotChangeLocale is the
// negative half of the manage_group_settings/view_stats wiring: a moderator
// holding only view_stats can open the change history but must still be
// denied a locale change.
func TestGranularPermission_StatsOnlyModeratorCannotChangeLocale(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	original := chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), original)
	_ = chats.AddModerator(context.Background(), chat.Moderator{ChatID: chatID, UserID: common.UserID{Value: 1}, AppointedBy: common.UserID{Value: 999}, Permissions: chat.PresetStatsOnly()})

	data := "settings:locale"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "StatsMod"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	updated, _ := chats.Find(context.Background(), chatID)
	if updated.Locale != common.LocaleRU {
		t.Fatalf("locale = %s, want unchanged RU (view_stats must not grant settings access)", updated.Locale)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, ru(t, "error.forbidden")) {
		t.Fatalf("expected the forbidden reply, got %q", body)
	}
}

// TestGranularPermission_GroupSettingsModeratorCanChangeLocale is the
// positive half: manage_group_settings alone (without full access) is
// enough to pass the same gate.
func TestGranularPermission_GroupSettingsModeratorCanChangeLocale(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	original := chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), original)
	_ = chats.AddModerator(context.Background(), chat.Moderator{ChatID: chatID, UserID: common.UserID{Value: 1}, AppointedBy: common.UserID{Value: 999}, Permissions: []chat.Permission{chat.PermissionManageGroupSettings}})

	data := "settings:locale"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "SettingsMod"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	updated, _ := chats.Find(context.Background(), chatID)
	if updated.Locale != common.LocaleEN {
		t.Fatalf("locale = %s, want EN after toggle by a manage_group_settings moderator", updated.Locale)
	}
}

// TestInvitation_CreateAndAccept exercises the whole invitation lifecycle:
// an admin creates a link with a chosen permission set, and a different
// user who is a member of the chat accepts it, ending up appointed with
// exactly those permissions — the one assignment path that needs no native
// Telegram user picker.
func TestInvitation_CreateAndAccept(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	handler.BotUsername = "cs2predictor_bot"
	invitations := newFakeInvitations()
	handler.Invitations = invitations

	// Admin creates the invitation via the wizard's "i" purpose.
	applyData := cbPermApply(permPurposeInvitation, "", permMask(chat.PresetStatsOnly()))
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &applyData}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if len(invitations.items) != 1 {
		t.Fatalf("expected exactly one invitation to be created, got %d", len(invitations.items))
	}
	var token string
	for tok := range invitations.items {
		token = tok
	}

	// A different member accepts it from their own DM.
	acceptor := common.UserID{Value: 42}
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleMember})
	acceptData := cbInvitationAccept(token)
	acceptCB := &CallbackQuery{ID: "cb2", From: User{ID: acceptor.Value, FirstName: "Newbie"}, Message: &Message{MessageID: 5, Chat: Chat{ID: acceptor.Value, Type: "private"}}, Data: &acceptData}
	if err := handler.handleCallback(context.Background(), acceptCB); err != nil {
		t.Fatal(err)
	}

	isMod, err := chats.IsModerator(context.Background(), chatID, acceptor)
	if err != nil || !isMod {
		t.Fatalf("expected acceptor to be appointed, isMod=%v err=%v", isMod, err)
	}
	perms, _ := chats.ModeratorPermissions(context.Background(), chatID, acceptor)
	if len(perms) != 1 || perms[0] != chat.PermissionViewStats {
		t.Fatalf("expected exactly view_stats, got %v", perms)
	}

	inv, err := invitations.Invitation(context.Background(), token)
	if err != nil || inv == nil || inv.UsedAt == nil {
		t.Fatalf("expected the invitation to be marked used, got %+v err=%v", inv, err)
	}

	// A second accept attempt must not double-spend the invitation.
	acceptCB2 := &CallbackQuery{ID: "cb3", From: User{ID: 999, FirstName: "Other"}, Message: &Message{MessageID: 6, Chat: Chat{ID: 999, Type: "private"}}, Data: &acceptData}
	if err := handler.handleCallback(context.Background(), acceptCB2); err != nil {
		t.Fatal(err)
	}
	isMod2, _ := chats.IsModerator(context.Background(), chatID, common.UserID{Value: 999})
	if isMod2 {
		t.Fatal("expected the second acceptor to NOT be appointed — the invitation was already used")
	}
}
