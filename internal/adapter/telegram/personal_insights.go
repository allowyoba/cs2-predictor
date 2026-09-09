package telegram

import (
	"context"
	"fmt"
	"strings"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The private stats screens answer "how much have I scored". These answer
// "how well am I doing, lately, and at what" — the questions people
// actually compare notes on. Everything here is user-scoped across every
// chat they play in, which is the one view a group leaderboard cannot
// give them.

// personalInsights resolves the vote-level repository. Like
// personalScoring it is an assertion rather than a declared field because
// the same postgres repository implements every scoring port; a
// deployment wired with a scoring store that doesn't gets an error screen
// rather than a panic.
func (h *UpdateHandler) personalInsights() (scoring.PersonalInsightsRepository, error) {
	repo, ok := h.Scoring.(scoring.PersonalInsightsRepository)
	if !ok {
		return nil, fmt.Errorf("scoring repository does not support personal insights")
	}
	return repo, nil
}

func (h *UpdateHandler) renderPersonalInsights(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode) error {
	repo, err := h.personalInsights()
	if err != nil {
		return err
	}
	predictions, err := repo.UserPredictions(ctx, userID, scoring.InsightsMaxPredictions)
	if err != nil {
		return err
	}
	insights := scoring.BuildPersonalInsights(predictions, h.Clock.Now())

	back := InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, "pstats:menu")}}}
	if !insights.HasData() {
		return h.respond(ctx, target, h.Texts.Get("insights.empty", locale), &back)
	}
	return h.respond(ctx, target, h.insightsText(insights, locale), &back)
}

func (h *UpdateHandler) insightsText(insights scoring.PersonalInsights, locale common.LocaleCode) string {
	var b strings.Builder
	b.WriteString(bold(h.Texts.Get("insights.title", locale)))

	correct := 0
	for _, prediction := range insights.RecentForm {
		if prediction {
			correct++
		}
	}
	b.WriteString("\n\n" + h.Texts.Get("insights.form_count", locale, len(insights.RecentForm), correct))
	b.WriteString("\n\n" + h.Texts.Get("insights.streak_current", locale, insights.CurrentStreak))
	b.WriteString("\n" + h.Texts.Get("insights.streak_longest", locale, insights.LongestStreak))

	// The trend is the one line that can mislead: shown only when there is
	// an earlier window of the same length to compare against.
	if delta, ok := insights.Trend(); ok {
		b.WriteString("\n\n" + h.Texts.Get("insights.trend", locale,
			insights.Recent.AccuracyPercent(), insights.Previous.AccuracyPercent(), fmt.Sprintf("%+d", delta)))
	} else if insights.Recent.Predictions > 0 {
		b.WriteString("\n\n" + h.Texts.Get("insights.trend_recent_only", locale,
			insights.Recent.AccuracyPercent(), insights.Recent.Predictions))
	}

	if len(insights.Teams) > 0 {
		b.WriteString("\n\n" + h.Texts.Get("insights.teams", locale))
		limit := min(len(insights.Teams), 3)
		for i, team := range insights.Teams[:limit] {
			fmt.Fprintf(&b, "\n%d. %s — %d%%",
				i+1, bold(escapeHTML(truncate(team.TeamName, 30))), team.AccuracyPercent())
		}
	}
	return b.String()
}
