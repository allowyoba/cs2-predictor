package telegram

import (
	"context"
	"fmt"
	"strings"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The achievements shelf in somebody's own cabinet: the milestones they have
// passed, and how far the next one is.
//
// Counted across every chat, unlike the congratulation itself. A milestone
// announced in a room is that room's moment; the shelf is the person's, and
// splitting their own record by which group they happened to be predicting
// in would make it smaller than what they actually did.

// achievementsView renders the shelf.
func (h *UpdateHandler) achievementsView(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	back, err := h.personalRootBack(ctx, userID)
	if err != nil {
		return err
	}
	kb := &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, back)}}}
	if h.Milestones == nil {
		return h.respond(ctx, target, h.Texts.Get("achievements.empty", locale), kb)
	}

	total, err := h.Milestones.UserExactTotal(ctx, userID)
	if err != nil {
		return err
	}
	reached, err := h.Milestones.UserMilestones(ctx, userID)
	if err != nil {
		return err
	}

	lines := []string{bold(h.Texts.Get("achievements.title", locale))}
	lines = append(lines, "", h.Texts.Get("achievements.exact_total", locale, total))
	if next, ok := scoring.NextMilestone(total); ok {
		lines = append(lines, h.Texts.Get("achievements.next", locale, next, next-total))
	} else {
		lines = append(lines, h.Texts.Get("achievements.complete", locale))
	}
	if total == 0 {
		// The ladder alone does not say why it starts at ten, and a shelf
		// somebody has just opened for the first time is exactly where that
		// belongs.
		lines = append(lines, "", h.Texts.Get("achievements.empty", locale))
	}
	lines = append(lines, "", milestoneLadder(h.Texts, locale, total, reached))
	return h.respond(ctx, target, strings.Join(lines, "\n"), kb)
}

// milestoneLadder draws the whole ladder rather than only what has been
// earned: the rungs still ahead are what give the earned ones their scale,
// and an empty shelf that shows nothing at all says neither what this is nor
// how to get one.
func milestoneLadder(texts *Texts, locale common.LocaleCode, total int, reached []scoring.MilestoneRecord) string {
	earnedAt := map[int]string{}
	for _, record := range reached {
		// The first time a milestone was reached anywhere is the one worth
		// dating: passing the same number again in a second chat is the same
		// achievement, not a new one.
		key := record.Milestone
		if existing, seen := earnedAt[key]; !seen || record.ReachedAt.Format("2006-01-02") < existing {
			earnedAt[key] = record.ReachedAt.Format("02.01.2006")
		}
	}
	var rungs []string
	for _, milestone := range scoring.ExactMilestones {
		switch {
		case earnedAt[milestone] != "":
			rungs = append(rungs, "🏅 "+texts.Get("achievements.reached", locale, milestone, earnedAt[milestone]))
		case total >= milestone:
			// Reached before the shelf existed: earned, just never dated.
			rungs = append(rungs, "🏅 "+fmt.Sprint(milestone))
		default:
			rungs = append(rungs, "▫️ "+fmt.Sprint(milestone))
		}
	}
	return strings.Join(rungs, "\n")
}
