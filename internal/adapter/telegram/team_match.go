package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// errTeamMatchAlreadyResolved is returned by confirmTeamMatch/rejectTeamMatch
// when the request they were asked to act on is no longer pending — an old
// /team_matches card's buttons stay tappable even after another operator (or
// this same operator, via a double tap) already resolved it. The callback
// dispatcher (callbacks.go) turns this into a toast rather than a hard
// error.
var errTeamMatchAlreadyResolved = errors.New("team match request already resolved")

// The team-identity review surface: /team_matches (a global, chat-
// independent DM screen — every other admin surface in this file is scoped
// to one group's settings, this one isn't, since Valve VRS identity is a
// bot-wide concern) and /team_match_admin (root-operator-only appointment
// of the delegated operator tier). See internal/app/team_match.go for the
// pipeline these screens are the human-facing end of.

const teamMatchQueuePageSize = 8

// isRootTeamMatchOperator reports whether userID is one of the fixed root
// operators (Config.TeamMatchOperatorChatIDs, from DEPLOY_NOTIFY_CHAT_IDS)
// — the only people who may appoint or revoke the delegated tier below.
func (h *UpdateHandler) isRootTeamMatchOperator(userID common.UserID) bool {
	for _, id := range h.TeamMatchOperatorChatIDs {
		if id == userID.Value {
			return true
		}
	}
	return false
}

// isTeamMatchOperator reports whether userID may use /team_matches: a root
// operator, or someone a root operator appointed via /team_match_admin.
func (h *UpdateHandler) isTeamMatchOperator(ctx context.Context, userID common.UserID) bool {
	if h.isRootTeamMatchOperator(userID) {
		return true
	}
	if h.TeamMatchOperators == nil {
		return false
	}
	ok, err := h.TeamMatchOperators.IsOperator(ctx, userID)
	return err == nil && ok
}

// handleTeamMatchAdminCommand implements "/team_match_admin [add|remove|list] [user_id]",
// root-operator-only. Deliberately plain numeric-id text rather than a
// reply-to-forward flow (like /moderator's group-chat appointment): this is
// a DM-only, chat-independent surface, and a numeric id is both simpler to
// implement and unaffected by a target's forwarding-privacy settings.
func (h *UpdateHandler) handleTeamMatchAdminCommand(ctx context.Context, chatID common.ChatID, actor common.UserID, locale common.LocaleCode, args string) error {
	if !h.isRootTeamMatchOperator(actor) {
		return chat.ErrAccessDenied
	}
	if h.TeamMatchOperators == nil {
		return newValidationError("team match operators are not configured")
	}
	replyTo := sendTarget(chatID, nil)
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return h.listTeamMatchOperators(ctx, replyTo, actor, locale)
	}
	switch fields[0] {
	case "add", "remove":
		if len(fields) != 2 {
			return newValidationError("usage: /team_match_admin %s <user_id>", fields[0])
		}
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return newValidationError("invalid user id %q", fields[1])
		}
		targetID := common.UserID{Value: id}
		if fields[0] == "add" {
			if err := h.TeamMatchOperators.AddOperator(ctx, targetID, actor); err != nil {
				return err
			}
		} else if err := h.TeamMatchOperators.RemoveOperator(ctx, targetID); err != nil {
			return err
		}
		return h.listTeamMatchOperators(ctx, replyTo, actor, locale)
	case "list":
		return h.listTeamMatchOperators(ctx, replyTo, actor, locale)
	default:
		return newValidationError("usage: /team_match_admin add|remove|list [user_id]")
	}
}

// listTeamMatchOperators renders the delegated-operator roster with a
// one-tap remove button per entry, plus an "add" entry point into the
// chat/participant picker below — root-operator-only, same as every other
// call site that mutates this roster (checked here too, not just at the
// menu button that links to it, since callback data isn't otherwise
// authenticated).
func (h *UpdateHandler) listTeamMatchOperators(ctx context.Context, target replyTarget, actor common.UserID, locale common.LocaleCode) error {
	if !h.isRootTeamMatchOperator(actor) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), nil)
	}
	operators, err := h.TeamMatchOperators.ListOperators(ctx)
	if err != nil {
		return err
	}
	profiles, err := h.Chats.UserProfiles(ctx, operators)
	if err != nil {
		return err
	}
	text := bold(h.Texts.Get("teammatch.admin_title", locale))
	if len(operators) == 0 {
		text += "\n\n" + h.Texts.Get("teammatch.admin_empty", locale)
	}
	var rows [][]InlineButton
	for _, id := range operators {
		rows = append(rows, []InlineButton{button(operatorLabel(id, profiles), cbTeamMatchAdminRemove(id))})
	}
	rows = append(rows, []InlineButton{button(h.Texts.Get("teammatch.admin_add", locale), "tmatch_admin:add")})
	rows = append(rows, []InlineButton{h.backButton(locale, "hub:system")})
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

// operatorLabel renders a user's display name when the bot has one cached
// (a prior /start, vote, or moderator appointment), falling back to the
// bare numeric id — the same fallback UserProfile's own callers already
// accept, since not every appointed id is guaranteed a cached profile.
func operatorLabel(id common.UserID, profiles map[common.UserID]chat.UserProfile) string {
	if p, ok := profiles[id]; ok && p.DisplayName != "" {
		return "✖ " + p.DisplayName
	}
	return fmt.Sprintf("✖ %d", id.Value)
}

// teamMatchAdminAddMenu starts the "appoint from a real participant" flow:
// pick one of the actor's own managed chats first (the same picker
// managedChatsMenu uses), then that chat's recent voters. Reusing
// ManagedChats/ChatParticipants here — rather than exposing every chat in
// the database — keeps this to the same visibility boundary every other
// admin surface already respects: an operator only browses chats they
// themselves have passed a manager check in.
func (h *UpdateHandler) teamMatchAdminAddMenu(ctx context.Context, target replyTarget, actor common.UserID, locale common.LocaleCode) error {
	if !h.isRootTeamMatchOperator(actor) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), nil)
	}
	chats, err := h.Chats.ManagedChats(ctx, actor)
	if err != nil {
		return err
	}
	back := []InlineButton{h.backButton(locale, "hub:team_match_operators")}
	if len(chats) == 0 {
		return h.respond(ctx, target, h.Texts.Get("teammatch.admin_pick_chat_empty", locale), &InlineKeyboard{InlineKeyboard: [][]InlineButton{back}})
	}
	rows := make([][]InlineButton, 0, len(chats)+1)
	for _, c := range chats {
		rows = append(rows, []InlineButton{button(truncate(c.Title, 40), cbTeamMatchAdminPickChat(c.ChatID))})
	}
	rows = append(rows, back)
	return h.respond(ctx, target, h.Texts.Get("teammatch.admin_pick_chat_title", locale), &InlineKeyboard{InlineKeyboard: rows})
}

// teamMatchParticipantWindow mirrors TeamMatchService's own window (see
// internal/app/team_match.go) for what counts as "recent" — the pool a
// crowd-verification ask draws from is exactly the pool an operator
// appointment should draw from too: real, currently-active voters.
const teamMatchParticipantWindow = 90 * 24 * time.Hour

// teamMatchAdminPickUserMenu lists chatID's recent voters as appointment
// candidates. Batched name lookup (UserProfiles) rather than one query per
// candidate — the same N+1 this package avoids elsewhere.
func (h *UpdateHandler) teamMatchAdminPickUserMenu(ctx context.Context, target replyTarget, actor common.UserID, locale common.LocaleCode, chatID common.ChatID) error {
	if !h.isRootTeamMatchOperator(actor) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), nil)
	}
	participants, err := h.Predictions.ChatParticipants(ctx, chatID, h.Clock.Now().Add(-teamMatchParticipantWindow))
	if err != nil {
		return err
	}
	back := []InlineButton{h.backButton(locale, "tmatch_admin:add")}
	if len(participants) == 0 {
		return h.respond(ctx, target, h.Texts.Get("teammatch.admin_pick_user_empty", locale), &InlineKeyboard{InlineKeyboard: [][]InlineButton{back}})
	}
	profiles, err := h.Chats.UserProfiles(ctx, participants)
	if err != nil {
		return err
	}
	rows := make([][]InlineButton, 0, len(participants)+1)
	for _, id := range participants {
		label := fmt.Sprintf("%d", id.Value)
		if p, ok := profiles[id]; ok && p.DisplayName != "" {
			label = p.DisplayName
		}
		rows = append(rows, []InlineButton{button(truncate(label, 40), cbTeamMatchAdminAppoint(chatID, id))})
	}
	rows = append(rows, back)
	return h.respond(ctx, target, h.Texts.Get("teammatch.admin_pick_user_title", locale), &InlineKeyboard{InlineKeyboard: rows})
}

// appointTeamMatchOperator is the picker flow's final step — mirrors
// handleTeamMatchAdminCommand's "add" case exactly (same AddOperator call,
// same root-only guard, same "just re-render the roster" confirmation),
// just reached via chat/participant taps instead of a typed numeric id.
func (h *UpdateHandler) appointTeamMatchOperator(ctx context.Context, target replyTarget, actor common.UserID, locale common.LocaleCode, appointee common.UserID) error {
	if !h.isRootTeamMatchOperator(actor) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), nil)
	}
	if h.TeamMatchOperators == nil {
		return newValidationError("team match operators are not configured")
	}
	if err := h.TeamMatchOperators.AddOperator(ctx, appointee, actor); err != nil {
		return err
	}
	return h.listTeamMatchOperators(ctx, target, actor, locale)
}

// removeTeamMatchOperator is listTeamMatchOperators' one-tap "✖ Name"
// button — no separate confirm step, matching how the rest of this
// root-only roster already treats add/remove as low-risk, reversible
// toggles (an operator can always be re-appointed the same way).
func (h *UpdateHandler) removeTeamMatchOperator(ctx context.Context, target replyTarget, actor common.UserID, locale common.LocaleCode, operatorID common.UserID) error {
	if !h.isRootTeamMatchOperator(actor) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), nil)
	}
	if h.TeamMatchOperators == nil {
		return newValidationError("team match operators are not configured")
	}
	if err := h.TeamMatchOperators.RemoveOperator(ctx, operatorID); err != nil {
		return err
	}
	return h.listTeamMatchOperators(ctx, target, actor, locale)
}

// teamMatchQueueMenu lists pending review requests, best-score first (the
// ones most likely to be a quick, confident tap come first) — bounded to a
// generous batch and paginated in Go, the same convention
// privateChatsMenu/privateBetsMenu already use for a bounded-but-sizable
// list.
func (h *UpdateHandler) teamMatchQueueMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, page int) error {
	if !h.isTeamMatchOperator(ctx, userID) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), nil)
	}
	if h.TeamMatches == nil {
		return newValidationError("team match review is not configured")
	}
	pending, err := h.TeamMatches.ListPending(ctx, 200)
	if err != nil {
		return err
	}
	// This list is opened from the system panel, so back returns there —
	// not to the personal dashboard, which is a different section
	// entirely and was never on the way here.
	back := []InlineButton{h.backButton(locale, "hub:system")}
	if len(pending) == 0 {
		return h.respond(ctx, target, bold(h.Texts.Get("teammatch.title", locale))+"\n\n"+h.Texts.Get("teammatch.empty", locale),
			&InlineKeyboard{InlineKeyboard: [][]InlineButton{back}})
	}

	maxPage := (len(pending) - 1) / teamMatchQueuePageSize
	if page > maxPage {
		page = maxPage
	}
	if page < 0 {
		page = 0
	}
	start := page * teamMatchQueuePageSize
	end := min(start+teamMatchQueuePageSize, len(pending))

	var rows [][]InlineButton
	for _, req := range pending[start:end] {
		label := fmt.Sprintf("%s — %d%%", truncate(req.ExternalName, 26), req.BestScore)
		rows = append(rows, []InlineButton{button(label, "team_matches:open:"+req.ID.String())})
	}
	totalPages := maxPage + 1
	if nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return fmt.Sprintf("team_matches:list:%d", p)
	}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, back)
	return h.respond(ctx, target, bold(h.Texts.Get("teammatch.title", locale)), &InlineKeyboard{InlineKeyboard: rows})
}

// teamMatchCardMenu shows one request's candidates, each rendered with its
// current score and crowd tally. Picking one confirms it immediately —
// there is no separate "are you sure" step, matching how confidently-
// scored a request has to be to reach this screen at all (the fully
// automatic tiers already handle anything less ambiguous).
func (h *UpdateHandler) teamMatchCardMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, reqID common.RequestID) error {
	if !h.isTeamMatchOperator(ctx, userID) {
		return h.respond(ctx, target, h.Texts.Get("error.forbidden", locale), nil)
	}
	req, candidates, err := h.TeamMatches.FindRequest(ctx, reqID)
	if err != nil {
		return err
	}
	if req == nil {
		return h.teamMatchQueueMenu(ctx, target, userID, locale, 0)
	}

	text := bold(h.Texts.Get("teammatch.title", locale)) + "\n\n" +
		h.Texts.Get("teammatch.card_external", locale, escapeHTML(req.ExternalName))

	var rows [][]InlineButton
	for i, c := range candidates {
		if i >= enrichment.MaxCandidatesPerRequest {
			break
		}
		label := fmt.Sprintf("✅ %s — %d%% (%d👍/%d👎)", truncate(c.TeamName, 22), c.Score, c.Yes, c.No)
		rows = append(rows, []InlineButton{button(label, fmt.Sprintf("team_matches:pick:%s:%d", reqID.String(), i))})
	}
	rows = append(rows, []InlineButton{button(h.Texts.Get("teammatch.reject", locale), "team_matches:reject:"+reqID.String())})
	rows = append(rows, []InlineButton{h.backButton(locale, "team_matches:list:0")})
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

// confirmTeamMatch is an operator's "yes, this candidate is right" decision
// — the only path (besides the fully-automatic high-fuzzy-score tier) that
// ever writes a Valve VRS identity mapping. It also saves the ranking
// itself immediately from the cached snapshot when available, so the
// mapping shows up in the very next poll rather than waiting for the next
// scheduled Valve sync.
func (h *UpdateHandler) confirmTeamMatch(ctx context.Context, userID common.UserID, reqID common.RequestID, index int) error {
	if !h.isTeamMatchOperator(ctx, userID) {
		return chat.ErrAccessDenied
	}
	req, candidates, err := h.TeamMatches.FindRequest(ctx, reqID)
	if err != nil {
		return err
	}
	if req == nil || index < 0 || index >= len(candidates) {
		return newValidationError("invalid team match candidate")
	}
	if req.Status != enrichment.TeamMatchPending {
		return errTeamMatchAlreadyResolved
	}
	chosen := candidates[index]
	externalID := enrichment.NormalizeTeamName(req.ExternalName)
	if err := h.TeamIdentity.SaveIdentity(ctx, chosen.TeamID, req.Source, externalID, req.ExternalName, enrichment.ConfidenceManual); err != nil {
		return err
	}
	h.applyCachedRanking(ctx, chosen.TeamID, req.Source, externalID)
	return h.TeamMatches.Resolve(ctx, reqID, enrichment.TeamMatchConfirmed, &chosen.TeamID, h.Clock.Now())
}

// applyCachedRanking looks the just-confirmed team up in the cached
// snapshot for source and saves its ranking immediately if found — best
// effort, since the scheduled sync will pick it up on its own next run
// regardless.
func (h *UpdateHandler) applyCachedRanking(ctx context.Context, teamID common.TeamID, source enrichment.Source, externalID string) {
	if h.TeamSnapshots == nil || h.TeamRankings == nil {
		return
	}
	snapshot, err := h.TeamSnapshots.AllSnapshot(ctx, source)
	if err != nil {
		loggerFrom(ctx, h.Log).Warn("team match snapshot lookup failed after confirm", "error", err)
		return
	}
	for _, rt := range snapshot {
		if enrichment.NormalizeTeamName(rt.Identity.Name) != externalID {
			continue
		}
		ranking := enrichment.TeamRanking{
			TeamID: teamID, GlobalRank: rt.GlobalRank, RegionalRank: rt.RegionalRank,
			Region: rt.Region, Points: rt.Points, Roster: rt.Identity.Roster,
			PublishedAt: rt.PublishedAt, Source: source,
		}
		if err := h.TeamRankings.SaveRanking(ctx, ranking); err != nil {
			loggerFrom(ctx, h.Log).Warn("team match ranking save failed after confirm", "error", err)
		}
		return
	}
}

// rejectTeamMatch is an operator's "none of these candidates are right"
// decision — the request is marked rejected and Valve keeps reporting this
// external name unmatched (it will not be re-opened automatically; a
// genuinely new local team for it would need a fresh /team_match_admin-
// independent path, out of scope here).
func (h *UpdateHandler) rejectTeamMatch(ctx context.Context, userID common.UserID, reqID common.RequestID) error {
	if !h.isTeamMatchOperator(ctx, userID) {
		return chat.ErrAccessDenied
	}
	req, _, err := h.TeamMatches.FindRequest(ctx, reqID)
	if err != nil {
		return err
	}
	if req == nil {
		return newValidationError("team match request not found")
	}
	if req.Status != enrichment.TeamMatchPending {
		return errTeamMatchAlreadyResolved
	}
	return h.TeamMatches.Resolve(ctx, reqID, enrichment.TeamMatchRejected, nil, h.Clock.Now())
}

// answerTeamMatchAsk handles a crowd helper's yes/no tap (tmatch:ans:...).
// The candidate being answered about is always the request's current top
// one — not re-encoded in the callback data, which keeps it comfortably
// under Telegram's 64-byte budget; the tiny window between a question being
// sent and answered essentially never sees the top candidate change.
func (h *UpdateHandler) answerTeamMatchAsk(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, raw string) error {
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return newValidationError("invalid team match answer")
	}
	reqID, err := common.ParseRequestID(parts[0])
	if err != nil {
		return newValidationError("invalid team match request id")
	}
	answer := enrichment.TeamMatchAnswerNo
	if parts[1] == "yes" {
		answer = enrichment.TeamMatchAnswerYes
	}

	req, candidates, err := h.TeamMatches.FindRequest(ctx, reqID)
	if err != nil {
		return err
	}
	if req == nil || len(candidates) == 0 || req.Status != enrichment.TeamMatchPending {
		return h.respond(ctx, target, h.Texts.Get("teammatch.ask_expired", locale), nil)
	}
	if err := h.TeamMatches.RecordResponse(ctx, reqID, userID, candidates[0].TeamID, answer); err != nil {
		return err
	}

	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{
		button(h.Texts.Get("teammatch.continue_yes", locale), "tmatch:continue:yes"),
		button(h.Texts.Get("teammatch.continue_no", locale), "tmatch:continue:no"),
	}}}
	return h.respond(ctx, target, h.Texts.Get("teammatch.ask_thanks", locale), &kb)
}

// setTeamMatchHelperOptOut handles the "keep asking me these?" follow-up —
// see internal/domain/enrichment/team_match.go's MaxLifetimeAsksPerUser doc
// comment for why this exists alongside the lifetime cap rather than
// instead of it.
func (h *UpdateHandler) setTeamMatchHelperOptOut(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, optOut bool) error {
	if h.TeamMatchHelpers != nil {
		if err := h.TeamMatchHelpers.SetOptedOut(ctx, userID, optOut); err != nil {
			return err
		}
	}
	key := "teammatch.optin_confirmed"
	if optOut {
		key = "teammatch.optout_confirmed"
	}
	return h.respond(ctx, target, h.Texts.Get(key, locale), nil)
}
