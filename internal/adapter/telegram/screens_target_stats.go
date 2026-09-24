package telegram

import (
	"context"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// Requirement (c)'s minimal stats cut: a chat's team-scoped leaderboard,
// following personal_insights.go's "one screen per breakdown" shape rather
// than a miniapp redesign.
//
// This is a cross-tournament aggregate (scoring.AllTime().ForTeam(id)), not
// a per-tournament one: fanOutNewPolls only ever creates polls for
// tournament-subscribed chats (see CompetitionSynchronization), so a chat
// with only a team subscription has no predictions at all in a tournament
// it isn't also subscribed to — nothing exists there yet to scope into a
// screen. What a team subscription does give a chat real data for is every
// tournament it *is* subscribed to where that team also played, summed
// across all of them — exactly what this screen shows.

// targetStatsMenu lists the chat's active team subscriptions as buttons —
// player subscriptions are stored (see subscription.TargetPlayer) but have
// no leaderboard of their own yet: there is no Player/roster domain
// concept to resolve which matches a player appeared in.
func (h *UpdateHandler) targetStatsMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	if h.Targets == nil {
		return h.respond(ctx, target, h.Texts.Get("stats.empty", settings.Locale), h.statsExit(settings.Locale, "menu:stats"))
	}
	subs, err := h.Targets.TargetSubscriptions(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	var rows [][]InlineButton
	for _, s := range subs {
		if s.Kind != subscription.TargetTeam || s.TargetName == "" {
			continue
		}
		rows = append(rows, []InlineButton{button(truncate(s.TargetName, 48), "stats:target:team:"+s.TargetID)})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:stats")})
	body := h.Texts.Get("stats.empty", settings.Locale)
	if len(rows) > 1 {
		body = bold(escapeHTML(h.Texts.Get("stats.team", settings.Locale)))
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, body), &InlineKeyboard{InlineKeyboard: rows})
}

// targetTeamStats renders the chat's all-time leaderboard restricted to
// matches that one followed team played, across every tournament the chat
// is (or was) itself subscribed to.
func (h *UpdateHandler) targetTeamStats(ctx context.Context, target replyTarget, settings chat.Settings, teamIDStr string, viewer common.UserID) error {
	parsed, err := uuid.Parse(teamIDStr)
	if err != nil {
		return newValidationError("invalid team id")
	}
	id := common.TeamID{Value: parsed}
	period := scoring.AllTime().ForTeam(&id)
	return h.renderLeaderboard(ctx, target, settings, period, "stats:teams", viewer)
}
