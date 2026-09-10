package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// The moderator management surface: the moderator list, a per-moderator
// card, the shared permission wizard (preset -> optional manual toggle ->
// confirm -> apply) used both to edit an existing moderator and to appoint a
// new one, participant picking, and one-time invitation links. See
// callbackdata.go for the payload grammar these screens and callbacks.go's
// routing both speak.

// participantPickPageSize caps how many recent-participant candidates show
// per page of the "👥 From participants" assignment screen.
const participantPickPageSize = 8

// participantWindow bounds "recent" for ChatParticipants — Telegram gives a
// bot no member list, so a moderator candidate is someone who has actually
// voted here lately, not everyone who ever has.
const participantWindow = 90 * 24 * time.Hour

// userLabel resolves a display name for userID from the shared user table,
// falling back to their numeric id when the bot has never seen them (or the
// lookup fails) — a best-effort label, same spirit as moderatorName.
func (h *UpdateHandler) userLabel(ctx context.Context, userID common.UserID) string {
	profile, err := h.Chats.UserProfile(ctx, userID)
	if err != nil || profile == nil || strings.TrimSpace(profile.DisplayName) == "" {
		return userID.String()
	}
	return profile.DisplayName
}

// permissionLabelKey composes a permission's i18n key at runtime, the same
// way historyKindKey does for admin-action kinds — kept as a named function
// (rather than an inline "moderators.perm."+string(p)) so the i18n parity
// test's static scanner doesn't mistake the literal prefix for a key of its
// own; see TestI18n_EveryPermissionHasALabel for the dedicated check this
// pattern needs instead.
func permissionLabelKey(p chat.Permission) string { return "moderators.perm." + string(p) }

// permissionListText renders perms as a bulleted, localized list in
// AllPermissions() order, or a "none granted" placeholder when empty.
func (h *UpdateHandler) permissionListText(perms []chat.Permission, locale common.LocaleCode) string {
	if len(perms) == 0 {
		return h.Texts.Get("moderators.none", locale)
	}
	var lines []string
	for _, p := range chat.AllPermissions() {
		if chat.HasPermission(perms, p) {
			lines = append(lines, "• "+h.Texts.Get(permissionLabelKey(p), locale))
		}
	}
	return strings.Join(lines, "\n")
}

func (h *UpdateHandler) moderatorsView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	mods, err := h.Chats.ListModerators(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	text := h.Texts.Get("moderators.title", settings.Locale)
	var rows [][]InlineButton
	if len(mods) == 0 {
		text += "\n\n" + h.Texts.Get("moderators.empty", settings.Locale)
	} else {
		var lines []string
		for _, mod := range mods {
			name := strings.TrimSpace(mod.DisplayName)
			if name == "" {
				name = "Telegram user " + mod.UserID.String()
			}
			handle := ""
			if mod.Username != "" {
				handle = " @" + escapeHTML(mod.Username)
			}
			lines = append(lines, "• "+bold(escapeHTML(name))+handle)
			rows = append(rows, []InlineButton{button("👤 "+truncate(name, 30), cbModeratorCard(mod.UserID))})
		}
		text += "\n\n" + strings.Join(lines, "\n")
	}
	rows = append(rows, []InlineButton{button(h.Texts.Get("moderators.add", settings.Locale), "moderators:add")})
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:settings")})
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) moderatorCardView(ctx context.Context, target replyTarget, settings chat.Settings, userID common.UserID) error {
	mods, err := h.Chats.ListModerators(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	var mod *chat.ModeratorInfo
	for i := range mods {
		if mods[i].UserID == userID {
			mod = &mods[i]
			break
		}
	}
	back := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "settings:moderators")}}}
	if mod == nil {
		return h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("moderators.empty", settings.Locale)), back)
	}
	name := strings.TrimSpace(mod.DisplayName)
	if name == "" {
		name = "Telegram user " + mod.UserID.String()
	}
	text := h.Texts.Get("moderators.card_title", settings.Locale, escapeHTML(name))
	if mod.Username != "" {
		text += " @" + escapeHTML(mod.Username)
	}
	text += "\n\n" + h.Texts.Get("moderators.card_permissions", settings.Locale) + "\n" + h.permissionListText(mod.Permissions, settings.Locale)
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("moderators.card_edit", settings.Locale), cbPermStart(permPurposeEditModerator, permWizardSubject(permPurposeEditModerator, userID)))},
		{button(h.Texts.Get("moderators.card_remove", settings.Locale), cbModeratorRemove(userID))},
		{h.backButton(settings.Locale, "settings:moderators")},
	}}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), kb)
}

func (h *UpdateHandler) assignMenuView(ctx context.Context, target replyTarget, settings chat.Settings) error {
	text := h.Texts.Get("moderators.assign_title", settings.Locale)
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("moderators.assign_invite", settings.Locale), cbPermStart(permPurposeInvitation, ""))},
		{button(h.Texts.Get("moderators.assign_participants", settings.Locale), cbModeratorPick(0))},
		{h.backButton(settings.Locale, "settings:moderators")},
	}}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), kb)
}

// participantPickView lists recent, not-already-moderator participants as
// assignment candidates. h.Predictions == nil (only happens in tests that
// don't wire it up) degrades to an empty list rather than panicking.
func (h *UpdateHandler) participantPickView(ctx context.Context, target replyTarget, settings chat.Settings, page int) error {
	var candidates []common.UserID
	if h.Predictions != nil {
		ids, err := h.Predictions.ChatParticipants(ctx, settings.ChatID, h.Clock.Now().Add(-participantWindow))
		if err != nil {
			return err
		}
		candidates = ids
	}
	mods, err := h.Chats.ListModerators(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	existing := map[common.UserID]bool{}
	for _, m := range mods {
		existing[m.UserID] = true
	}
	var filtered []common.UserID
	for _, id := range candidates {
		if !existing[id] {
			filtered = append(filtered, id)
		}
	}
	back := []InlineButton{h.backButton(settings.Locale, "moderators:add")}
	text := h.Texts.Get("moderators.pick_title", settings.Locale)
	if len(filtered) == 0 {
		text += "\n\n" + h.Texts.Get("moderators.pick_empty", settings.Locale)
		return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: [][]InlineButton{back}})
	}
	totalPages := (len(filtered) + participantPickPageSize - 1) / participantPickPageSize
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}
	start := page * participantPickPageSize
	end := start + participantPickPageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	var rows [][]InlineButton
	for _, id := range filtered[start:end] {
		label := h.userLabel(ctx, id)
		rows = append(rows, []InlineButton{button("👤 "+truncate(label, 30), cbPermStart(permPurposeAssignNew, permWizardSubject(permPurposeAssignNew, id)))})
	}
	counter := fmt.Sprintf("%d / %d", page+1, totalPages)
	if nav := paginationRow(page, totalPages, counter, func(p int) string { return cbModeratorPick(p) }); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, back)
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// permSubjectLabel resolves the human label shown on the wizard screens:
// empty for an invitation (no target user yet), otherwise the candidate's
// or existing moderator's display name.
func (h *UpdateHandler) permSubjectLabel(ctx context.Context, purpose permWizardPurpose, subject string) string {
	if purpose == permPurposeInvitation {
		return ""
	}
	userID, err := parseBase36UserID(subject)
	if err != nil {
		return subject
	}
	return h.userLabel(ctx, userID)
}

// permWizardBackTarget is where "← Back" on the preset/toggle screens goes:
// the moderator's own card when editing, otherwise the assignment menu.
func permWizardBackTarget(purpose permWizardPurpose, subject string) string {
	if purpose == permPurposeEditModerator {
		return "moderators:card:" + subject
	}
	return "moderators:add"
}

func (h *UpdateHandler) permPresetView(ctx context.Context, target replyTarget, settings chat.Settings, purpose permWizardPurpose, subject string) error {
	titleKey := "moderators.preset_title_invite"
	switch purpose {
	case permPurposeEditModerator:
		titleKey = "moderators.preset_title_edit"
	case permPurposeAssignNew:
		titleKey = "moderators.preset_title_assign"
	}
	var text string
	if purpose == permPurposeInvitation {
		text = h.Texts.Get(titleKey, settings.Locale)
	} else {
		text = h.Texts.Get(titleKey, settings.Locale, escapeHTML(h.permSubjectLabel(ctx, purpose, subject)))
	}
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("moderators.preset_full", settings.Locale), cbPermPreset(purpose, subject, "full"))},
		{button(h.Texts.Get("moderators.preset_content", settings.Locale), cbPermPreset(purpose, subject, "content"))},
		{button(h.Texts.Get("moderators.preset_stats", settings.Locale), cbPermPreset(purpose, subject, "stats"))},
		{button(h.Texts.Get("moderators.preset_manual", settings.Locale), cbPermPreset(purpose, subject, "manual"))},
		{h.backButton(settings.Locale, permWizardBackTarget(purpose, subject))},
	}}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), kb)
}

func (h *UpdateHandler) permToggleView(ctx context.Context, target replyTarget, settings chat.Settings, purpose permWizardPurpose, subject string, mask int) error {
	text := h.Texts.Get("moderators.toggle_title", settings.Locale)
	var rows [][]InlineButton
	for i, p := range chat.AllPermissions() {
		mark := "⬜ "
		if mask&(1<<i) != 0 {
			mark = "✅ "
		}
		newMask := mask ^ (1 << i)
		rows = append(rows, []InlineButton{button(mark+h.Texts.Get(permissionLabelKey(p), settings.Locale), cbPermToggle(purpose, subject, newMask))})
	}
	rows = append(rows, []InlineButton{button(h.Texts.Get("moderators.toggle_done", settings.Locale), cbPermConfirm(purpose, subject, mask))})
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, cbPermStart(purpose, subject))})
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) permConfirmView(ctx context.Context, target replyTarget, settings chat.Settings, purpose permWizardPurpose, subject string, mask int) error {
	perms := permsFromMask(mask)
	list := h.permissionListText(perms, settings.Locale)
	if len(perms) == 0 {
		list += h.Texts.Get("moderators.confirm_empty_warning", settings.Locale)
	}
	var text string
	switch purpose {
	case permPurposeEditModerator:
		text = h.Texts.Get("moderators.confirm_title_edit", settings.Locale, escapeHTML(h.permSubjectLabel(ctx, purpose, subject)), list)
	case permPurposeAssignNew:
		text = h.Texts.Get("moderators.confirm_title_assign", settings.Locale, escapeHTML(h.permSubjectLabel(ctx, purpose, subject)), list)
	default:
		text = h.Texts.Get("moderators.confirm_title_invite", settings.Locale, list)
	}
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("moderators.confirm_save", settings.Locale), cbPermApply(purpose, subject, mask))},
		{h.backButton(settings.Locale, cbPermStart(purpose, subject))},
	}}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), kb)
}

func parseBase36UserID(s string) (common.UserID, error) {
	id, err := strconv.ParseInt(s, 36, 64)
	if err != nil {
		return common.UserID{}, err
	}
	return common.UserID{Value: id}, nil
}

// applyEditPermissions saves a new permission set for an already-appointed
// moderator.
func (h *UpdateHandler) applyEditPermissions(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, subject string, perms []chat.Permission) (bool, error) {
	userID, err := parseBase36UserID(subject)
	if err != nil {
		return false, newValidationError("invalid moderator id")
	}
	if err := h.Chats.SetModeratorPermissions(ctx, settings.ChatID, userID, perms); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "moderator_permissions_changed", h.userLabel(ctx, userID))
	if err := h.toast(ctx, cb.ID, h.Texts.Get("moderators.permissions_saved", settings.Locale)); err != nil {
		return false, err
	}
	return true, h.moderatorCardView(ctx, target, settings, userID)
}

// applyAssignNew appoints a brand-new moderator picked from the participant
// list, with the permission set chosen in the wizard.
func (h *UpdateHandler) applyAssignNew(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, subject string, perms []chat.Permission) (bool, error) {
	userID, err := parseBase36UserID(subject)
	if err != nil {
		return false, newValidationError("invalid user id")
	}
	displayName := userID.String()
	username := ""
	if profile, _ := h.Chats.UserProfile(ctx, userID); profile != nil {
		username = profile.Username
		if strings.TrimSpace(profile.DisplayName) != "" {
			displayName = profile.DisplayName
		}
	}
	actor := common.UserID{Value: cb.From.ID}
	m := chat.Moderator{ChatID: settings.ChatID, UserID: userID, AppointedBy: actor, Username: username, DisplayName: displayName, Permissions: perms}
	if err := h.Chats.AddModerator(ctx, m); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "moderator_added", displayName)
	if err := h.toast(ctx, cb.ID, h.Texts.Get("moderators.assigned", settings.Locale, displayName)); err != nil {
		return false, err
	}
	return true, h.moderatorsView(ctx, target, settings)
}

// generateInvitationToken returns a 16-hex-character (64-bit) random token
// — short enough to fit comfortably in callback_data alongside its scope
// and command prefixes, while being infeasible to guess.
func generateInvitationToken() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

func (h *UpdateHandler) invitationDeepLink(token string) string {
	if h.BotUsername == "" {
		return ""
	}
	return "https://t.me/" + h.BotUsername + "?start=" + invitationDeepLinkPrefix + token
}

// createInvitation is the wizard's "i" purpose apply step: it has no target
// user yet, so instead of writing a moderator row it mints a one-time link
// that will do so once accepted.
func (h *UpdateHandler) createInvitation(ctx context.Context, cb *CallbackQuery, target replyTarget, settings chat.Settings, perms []chat.Permission) (bool, error) {
	if h.Invitations == nil {
		return false, newValidationError("invitations are not configured")
	}
	token, err := generateInvitationToken()
	if err != nil {
		return false, err
	}
	now := h.Clock.Now()
	inv := chat.ModeratorInvitation{
		Token: token, ChatID: settings.ChatID, Permissions: perms,
		CreatedBy: common.UserID{Value: cb.From.ID}, CreatedAt: now, ExpiresAt: now.Add(chat.ModeratorInvitationTTL),
	}
	if err := h.Invitations.CreateInvitation(ctx, inv); err != nil {
		return false, err
	}
	h.logAdminAction(ctx, settings.ChatID, &cb.From, "invitation_created", "")
	return false, h.invitationCreatedView(ctx, target, settings, inv)
}

func (h *UpdateHandler) invitationCreatedView(ctx context.Context, target replyTarget, settings chat.Settings, inv chat.ModeratorInvitation) error {
	loc := chatZone(settings)
	expires := inv.ExpiresAt.In(loc).Format("02.01.2006 15:04")
	link := h.invitationDeepLink(inv.Token)
	text := h.Texts.Get("moderators.invite_created_title", settings.Locale, expires, link)
	text += "\n\n" + h.Texts.Get("moderators.invite_permissions_label", settings.Locale) + "\n" + h.permissionListText(inv.Permissions, settings.Locale)
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("moderators.invite_revoke", settings.Locale), cbInvitationRevokeAsk(inv.Token))},
		{h.backButton(settings.Locale, "settings:moderators")},
	}}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), kb)
}

func (h *UpdateHandler) invitationRevokeAskView(ctx context.Context, target replyTarget, settings chat.Settings, token string) error {
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("moderators.invite_revoke", settings.Locale), cbInvitationRevokeDo(token))},
		{h.backButton(settings.Locale, "settings:moderators")},
	}}
	return h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("moderators.invite_revoke_question", settings.Locale)), kb)
}

// --- invitation accept flow (private chat, no managed-chat authorization) ---

// invitationDeepLinkPrefix is the "/start invite_<token>" payload generated
// by invitationDeepLink.
const invitationDeepLinkPrefix = "invite_"

func parseInvitationDeepLink(text string) (string, bool) {
	payload := strings.TrimSpace(strings.TrimPrefix(text, "/start "))
	if !strings.HasPrefix(payload, invitationDeepLinkPrefix) {
		return "", false
	}
	token := strings.TrimPrefix(payload, invitationDeepLinkPrefix)
	if token == "" {
		return "", false
	}
	return token, true
}

// invitationStatusMessage maps a non-pending invitation status to its
// explanatory text; ok is false for InvitationPending, which has nothing to
// report on its own.
func (h *UpdateHandler) invitationStatusMessage(status chat.InvitationStatus, locale common.LocaleCode) (string, bool) {
	switch status {
	case chat.InvitationUsed:
		return h.Texts.Get("invite.used", locale), true
	case chat.InvitationRevoked:
		return h.Texts.Get("invite.revoked", locale), true
	case chat.InvitationExpired:
		return h.Texts.Get("invite.expired", locale), true
	default:
		return "", false
	}
}

// openInvitationAccept renders the "/start invite_<token>" landing screen:
// the group's name, the permissions on offer, and Accept/Decline buttons.
func (h *UpdateHandler) openInvitationAccept(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, token string) error {
	if h.Invitations == nil {
		return h.privateStatsMenu(ctx, target, userID, locale)
	}
	inv, err := h.Invitations.Invitation(ctx, token)
	if err != nil {
		return err
	}
	if inv == nil {
		return h.respond(ctx, target, h.Texts.Get("invite.not_found", locale), nil)
	}
	if msg, done := h.invitationStatusMessage(inv.Status(h.Clock.Now()), locale); done {
		return h.respond(ctx, target, msg, nil)
	}
	chatTitle := h.chatTitleOrID(ctx, inv.ChatID)
	text := h.Texts.Get("invite.accept_title", locale, escapeHTML(chatTitle), h.permissionListText(inv.Permissions, locale))
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("invite.accept_button", locale), cbInvitationAccept(token))},
		{button(h.Texts.Get("invite.decline_button", locale), cbInvitationDecline(token))},
	}}
	return h.respond(ctx, target, text, &kb)
}

func (h *UpdateHandler) chatTitleOrID(ctx context.Context, chatID common.ChatID) string {
	settings, err := h.Chats.Find(ctx, chatID)
	if err != nil || settings == nil || strings.TrimSpace(settings.Title) == "" {
		return chatID.String()
	}
	return settings.Title
}

// acceptInvitation is the "✅ Accept" callback: re-validates the invitation
// (it may have been used/revoked/expired since the accept screen was
// rendered), checks the accepting user is actually a member of the target
// chat, then atomically claims the invitation and appoints them.
func (h *UpdateHandler) acceptInvitation(ctx context.Context, cb *CallbackQuery, target replyTarget, userID common.UserID, locale common.LocaleCode, token string) error {
	if h.Invitations == nil {
		return newValidationError("invitations are not configured")
	}
	inv, err := h.Invitations.Invitation(ctx, token)
	if err != nil {
		return err
	}
	if inv == nil {
		return h.respond(ctx, target, h.Texts.Get("invite.not_found", locale), nil)
	}
	if msg, done := h.invitationStatusMessage(inv.Status(h.Clock.Now()), locale); done {
		return h.respond(ctx, target, msg, nil)
	}
	chatTitle := h.chatTitleOrID(ctx, inv.ChatID)
	isMember, err := h.Authorization.IsMember(ctx, inv.ChatID, userID)
	if err != nil {
		return err
	}
	if !isMember {
		return h.respond(ctx, target, h.Texts.Get("invite.not_member", locale, escapeHTML(chatTitle)), nil)
	}
	ok, err := h.Invitations.UseInvitation(ctx, token, userID, h.Clock.Now())
	if err != nil {
		return err
	}
	if !ok {
		return h.respond(ctx, target, h.Texts.Get("invite.used", locale), nil)
	}
	displayName := cb.From.DisplayName()
	username := ""
	if cb.From.Username != nil {
		username = *cb.From.Username
	}
	m := chat.Moderator{ChatID: inv.ChatID, UserID: userID, AppointedBy: inv.CreatedBy, Username: username, DisplayName: displayName, Permissions: inv.Permissions}
	if err := h.Chats.AddModerator(ctx, m); err != nil {
		return err
	}
	h.logAdminAction(ctx, inv.ChatID, &cb.From, "invitation_accepted", displayName)
	h.logAdminAction(ctx, inv.ChatID, &cb.From, "moderator_added", displayName)
	return h.respond(ctx, target, h.Texts.Get("invite.accepted", locale, escapeHTML(chatTitle)), nil)
}
