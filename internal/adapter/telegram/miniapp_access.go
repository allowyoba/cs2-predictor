package telegram

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// Who may open the Mini App, and how they come to.
//
// The app shows somebody their whole prediction history, so it is closed
// by default and opened per person by a root operator. Three states are
// visible here and each one has exactly one thing to offer:
//
//   - never asked → a button that asks;
//   - asked → nothing to do but wait, said plainly;
//   - granted → the button that opens it.
//
// Root operators skip all of it: they are who approves the others, and a
// bootstrap that has to grant itself cannot start.

// miniAppAccessRepo resolves the repository behind the grant, nil-safe for
// deployments (and tests) that wire no such store.
func (h *UpdateHandler) miniAppAccessRepo() (chat.MiniAppAccessRepository, bool) {
	repo, ok := h.Chats.(chat.MiniAppAccessRepository)
	return repo, ok
}

// miniAppStatusFor is the person's standing, plus whether they are exempt.
func (h *UpdateHandler) miniAppStatusFor(ctx context.Context, userID common.UserID) (chat.MiniAppStatus, bool) {
	if h.isRootTeamMatchOperator(userID) {
		return chat.MiniAppGranted, true
	}
	repo, ok := h.miniAppAccessRepo()
	if !ok {
		return "", false
	}
	access, err := repo.MiniAppAccess(ctx, userID)
	if err != nil {
		loggerFrom(ctx, h.Log).Warn("mini app access lookup failed", "userId", userID.Value, "error", err)
		return "", false
	}
	if access == nil {
		return "", false
	}
	return access.Status, false
}

// miniAppRow renders the dashboard's Mini App row for this person: the
// button that opens it, the button that asks for it, or the line that says
// the request is waiting.
func (h *UpdateHandler) miniAppRow(ctx context.Context, userID common.UserID, locale common.LocaleCode) []InlineButton {
	if h.MiniAppURL == "" {
		return nil
	}
	status, _ := h.miniAppStatusFor(ctx, userID)
	switch status {
	case chat.MiniAppGranted:
		return []InlineButton{webAppButton(h.Texts.Get("private.miniapp", locale), h.MiniAppURL)}
	case chat.MiniAppPending:
		return []InlineButton{button(h.Texts.Get("miniapp.pending", locale), "miniapp:pending")}
	case chat.MiniAppDenied:
		// A refusal is a decision, not a queue position: no button that
		// re-asks, because re-asking is what a refused person would do
		// forever otherwise.
		return nil
	default:
		return []InlineButton{button(h.Texts.Get("miniapp.request", locale), "miniapp:request")}
	}
}

// requestMiniAppAccess records the ask and tells the operators.
func (h *UpdateHandler) requestMiniAppAccess(ctx context.Context, cb *CallbackQuery, target replyTarget,
	userID common.UserID, locale common.LocaleCode) error {
	repo, ok := h.miniAppAccessRepo()
	if !ok {
		return h.toast(ctx, cb.ID, h.Texts.Get("miniapp.unavailable", locale))
	}
	if status, operator := h.miniAppStatusFor(ctx, userID); operator || status != "" {
		// Already decided or already asked: re-asking must not reset
		// anything, and the toast says where things stand.
		return h.toast(ctx, cb.ID, h.Texts.Get("miniapp.pending", locale))
	}
	if err := repo.RequestMiniAppAccess(ctx, userID, h.Clock.Now()); err != nil {
		return err
	}
	h.notifyOperatorsOfMiniAppRequest(ctx, userID, cb.From)
	if err := h.toast(ctx, cb.ID, "✅ "+h.Texts.Get("miniapp.requested", locale)); err != nil {
		return err
	}
	return h.privateStatsMenu(ctx, target, userID, locale)
}

// notifyOperatorsOfMiniAppRequest DMs each root operator with the two
// decisions they can make. Best effort: the request is already recorded,
// and an operator who missed the message can still find it in the list.
func (h *UpdateHandler) notifyOperatorsOfMiniAppRequest(ctx context.Context, userID common.UserID, from User) {
	if h.Outbox == nil {
		return
	}
	for _, operator := range h.TeamMatchOperatorChatIDs {
		payload := common.MiniAppAccessNotification{
			ChatID: operator, UserID: userID.Value,
			DisplayName: from.DisplayName(), Username: usernameOf(from),
		}
		if err := enqueueJSON(ctx, h.Outbox, "MINIAPP_ACCESS", strconv.FormatInt(userID.Value, 10),
			"telegram.miniapp-access-request", payload); err != nil {
			loggerFrom(ctx, h.Log).Error("mini app access request notification failed",
				"userId", userID.Value, "operator", operator, "error", err)
		}
	}
}

// decideMiniAppAccess applies one operator's decision, then tells the
// person who asked — a grant nobody hears about is a grant nobody uses.
func (h *UpdateHandler) decideMiniAppAccess(ctx context.Context, cb *CallbackQuery, operator common.UserID,
	locale common.LocaleCode, raw string, status chat.MiniAppStatus) error {
	if !h.isRootTeamMatchOperator(operator) {
		return chat.ErrAccessDenied
	}
	repo, ok := h.miniAppAccessRepo()
	if !ok {
		return h.toast(ctx, cb.ID, h.Texts.Get("miniapp.unavailable", locale))
	}
	target, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return newValidationError("invalid mini app access decision %q", raw)
	}
	subject := common.UserID{Value: target}
	if err := repo.DecideMiniAppAccess(ctx, subject, status, operator, h.Clock.Now()); err != nil {
		return err
	}
	h.tellSubjectAboutMiniAppDecision(ctx, subject, status)

	toastKey := "miniapp.granted_toast"
	if status == chat.MiniAppDenied {
		toastKey = "miniapp.denied_toast"
	}
	return h.toast(ctx, cb.ID, "✅ "+h.Texts.Get(toastKey, locale))
}

// tellSubjectAboutMiniAppDecision delivers the outcome through the outbox,
// like every other message this bot sends to somebody's DM.
func (h *UpdateHandler) tellSubjectAboutMiniAppDecision(ctx context.Context, userID common.UserID, status chat.MiniAppStatus) {
	if h.Outbox == nil {
		return
	}
	payload := common.MiniAppAccessNotification{
		ChatID: userID.Value, UserID: userID.Value, Granted: status == chat.MiniAppGranted, Decision: true,
	}
	if err := enqueueJSON(ctx, h.Outbox, "MINIAPP_ACCESS", strconv.FormatInt(userID.Value, 10),
		"telegram.miniapp-access-decision", payload); err != nil {
		loggerFrom(ctx, h.Log).Error("mini app access decision notification failed", "userId", userID.Value, "error", err)
	}
}

func usernameOf(from User) string {
	if from.Username == nil {
		return ""
	}
	return *from.Username
}

// enqueueJSON marshals a notification and hands it to the outbox — the
// same two lines every publisher-bound message in this package would
// otherwise repeat.
func enqueueJSON(ctx context.Context, outbox common.Outbox, aggregateType, aggregateID, eventType string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = outbox.Enqueue(ctx, aggregateType, aggregateID, eventType, string(body))
	return err
}
