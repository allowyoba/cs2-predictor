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

// insightsText renders "My Form" as a small visual profile card rather than
// a flat list of labeled numbers: a headline accuracy bar, a win/loss form
// guide in colored squares, a streak framed by how hot it actually is, the
// 30-day trend (only when there's an earlier window to compare against —
// see PersonalInsights.Trend), and a medal-ranked list of best-read teams.
func (h *UpdateHandler) insightsText(insights scoring.PersonalInsights, locale common.LocaleCode) string {
	var b strings.Builder
	b.WriteString(bold(h.Texts.Get("insights.title", locale)))

	correct := 0
	for _, won := range insights.RecentForm {
		if won {
			correct++
		}
	}
	percent := scoring.PeriodSummary{Correct: correct, Predictions: len(insights.RecentForm)}.AccuracyPercent()
	b.WriteString("\n\n" + h.Texts.Get("insights.accuracy_heading", locale, len(insights.RecentForm), percent))
	b.WriteString("\n" + accuracyBar(percent) + "\n" + formGuide(insights.RecentForm))

	b.WriteString("\n\n" + streakLine(h.Texts, locale, insights.CurrentStreak))
	if insights.LongestStreak > 0 {
		b.WriteString("\n" + h.Texts.Get("insights.streak_best", locale, insights.LongestStreak))
	}

	// The trend is the one line that can mislead: shown only when there is
	// an earlier window of the same length to compare against.
	if delta, ok := insights.Trend(); ok {
		key, magnitude := "insights.trend_flat", 0
		switch {
		case delta > 0:
			key, magnitude = "insights.trend_up", delta
		case delta < 0:
			key, magnitude = "insights.trend_down", -delta
		}
		if delta == 0 {
			b.WriteString("\n\n" + h.Texts.Get(key, locale, insights.Recent.AccuracyPercent()))
		} else {
			b.WriteString("\n\n" + h.Texts.Get(key, locale, insights.Recent.AccuracyPercent(), insights.Previous.AccuracyPercent(), magnitude))
		}
	} else if insights.Recent.Predictions > 0 {
		b.WriteString("\n\n" + h.Texts.Get("insights.trend_recent_only", locale,
			insights.Recent.AccuracyPercent(), insights.Recent.Predictions))
	}

	if len(insights.Teams) > 0 {
		b.WriteString("\n\n" + bold(h.Texts.Get("insights.teams_heading", locale)))
		for i, team := range insights.Teams {
			fmt.Fprintf(&b, "\n%s %s — <b>%d%%</b> (%d)",
				medalFor(i+1), escapeHTML(truncate(team.TeamName, 30)), team.AccuracyPercent(), team.Predictions)
		}
	}
	return b.String()
}

// accuracyBarSlots is how many cells the visual accuracy bar renders —
// enough resolution to distinguish e.g. 70% from 80% without being wider
// than a mobile Telegram client comfortably shows on one line.
const accuracyBarSlots = 10

// accuracyBar renders percent as a filled/empty block bar ("▰▰▰▰▰▰▰▱▱▱") —
// the same number conveyed at a glance as well as in text, which is the
// whole point of a "should be interesting to look at" personal stats
// screen: a bare percentage is data, a bar is a picture of it.
func accuracyBar(percent int) string {
	filled := (percent*accuracyBarSlots + 50) / 100
	filled = max(0, min(filled, accuracyBarSlots))
	return strings.Repeat("▰", filled) + strings.Repeat("▱", accuracyBarSlots-filled)
}

// formGuide renders a win/loss history as colored squares, oldest to
// newest (matching RecentForm's own order) — the same "form guide" idea
// sports scores pages use, and immediately recognizable without a legend.
func formGuide(form []bool) string {
	var b strings.Builder
	for _, won := range form {
		if won {
			b.WriteString("🟩")
		} else {
			b.WriteString("🟥")
		}
	}
	return b.String()
}

// streakLine frames the current streak by how notable it actually is,
// rather than reporting the same neutral "current streak: N" regardless of
// whether N is 1 or 15 — the framing is part of what makes a personal
// stats screen feel alive rather than like a spreadsheet cell.
func streakLine(texts *Texts, locale common.LocaleCode, streak int) string {
	switch {
	case streak >= 5:
		return texts.Get("insights.streak_hot", locale, streak)
	case streak >= 3:
		return texts.Get("insights.streak_warm", locale, streak)
	case streak >= 1:
		return texts.Get("insights.streak_active", locale, streak)
	default:
		return texts.Get("insights.streak_broken", locale)
	}
}
