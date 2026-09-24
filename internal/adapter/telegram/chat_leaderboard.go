package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The cross-chat leaderboard answers a different question than everything
// else in the personal cabinet: not "how am I doing" or "how is my chat
// doing", but "which chat is collectively best at this" — chats ranked
// against each other. It only ever shows chat titles and chat-level totals
// (see scoring.ChatStanding): no member list, no per-user breakdown of a
// chat somebody viewing this screen may not even be in. That is what makes
// it safe to show to anyone, unlike a single chat's own leaderboard.

// chatLeaderboardPeriod is one of the period cuts this screen offers. Kept
// to three fixed choices (rather than the full month/year picker the
// per-chat leaderboard has) because there is no one chat whose calendar
// months would anchor a "which month" picker for every chat at once.
type chatLeaderboardPeriod string

const (
	chatLeaderboardAllTime chatLeaderboardPeriod = "a"
	chatLeaderboardYear    chatLeaderboardPeriod = "y"
	chatLeaderboardMonth   chatLeaderboardPeriod = "m"
)

func (p chatLeaderboardPeriod) statsPeriod(now time.Time) scoring.StatsPeriod {
	switch p {
	case chatLeaderboardYear:
		return scoring.ForYear(now.Year())
	case chatLeaderboardMonth:
		return scoring.ForMonth(now.Year(), now.Month())
	default:
		return scoring.AllTime()
	}
}

// chatLeaderboardCallback builds the callback data for one combination of
// period and game filter, following the same "pstats:<screen>:<cuts>"
// convention as insightsFilterCallback.
func chatLeaderboardCallback(period chatLeaderboardPeriod, game competition.GameCode) string {
	return fmt.Sprintf("pstats:chatlb:%s:%s", period, game)
}

// parseChatLeaderboardCallback splits the remainder of a
// "pstats:chatlb:" callback into its period and game segments. An unknown
// period token falls back to all-time rather than an error, since a stale
// button should degrade rather than fail.
func parseChatLeaderboardCallback(remainder string) (chatLeaderboardPeriod, competition.GameCode) {
	parts := strings.SplitN(remainder, ":", 2)
	period := chatLeaderboardPeriod(parts[0])
	switch period {
	case chatLeaderboardYear, chatLeaderboardMonth, chatLeaderboardAllTime:
	default:
		period = chatLeaderboardAllTime
	}
	var game competition.GameCode
	if len(parts) > 1 {
		game = competition.GameCode(parts[1])
	}
	return period, game
}

// chatLeaderboardRepo resolves the cross-chat leaderboard port. Like
// personalInsights, this is a type assertion rather than a declared field:
// the same postgres repository implements every scoring port, and a
// deployment wired with a scoring store that doesn't gets a clear error
// instead of a panic.
func (h *UpdateHandler) chatLeaderboardRepo() (scoring.ChatLeaderboardRepository, error) {
	repo, ok := h.Scoring.(scoring.ChatLeaderboardRepository)
	if !ok {
		return nil, fmt.Errorf("scoring repository does not support the cross-chat leaderboard")
	}
	return repo, nil
}

// chatLeaderboardDisplayLimit caps how many chats the DM screen actually
// lists — the query itself can return up to scoring.ChatLeaderboardMaxChats,
// but a Telegram message showing every one of those would be unreadable.
const chatLeaderboardDisplayLimit = 20

// renderChatLeaderboard draws the cross-chat leaderboard for one period and
// game cut: which chat is collectively predicting best right now.
func (h *UpdateHandler) renderChatLeaderboard(ctx context.Context, target replyTarget, locale common.LocaleCode,
	period chatLeaderboardPeriod, game competition.GameCode) error {
	repo, err := h.chatLeaderboardRepo()
	if err != nil {
		return err
	}
	statsPeriod := period.statsPeriod(h.Clock.Now()).ForGame(game)
	standings, err := repo.ChatLeaderboard(ctx, statsPeriod)
	if err != nil {
		return err
	}

	keyboard := h.chatLeaderboardKeyboard(locale, period, game)
	if len(standings) == 0 {
		return h.respond(ctx, target, h.Texts.Get("chatlb.empty", locale), keyboard)
	}

	var b strings.Builder
	b.WriteString(bold(h.Texts.Get("chatlb.title", locale)))
	for i, s := range standings {
		if i >= chatLeaderboardDisplayLimit {
			break
		}
		fmt.Fprintf(&b, "\n%s <b>%s</b> — %d %s, %d%% (%d)",
			medalFor(s.Rank), escapeHTML(truncate(s.ChatTitle, 32)), s.Points,
			h.Texts.Get("chatlb.points_unit", locale), s.AccuracyPercent(), s.Predictions)
	}
	return h.respond(ctx, target, b.String(), keyboard)
}

// chatLeaderboardKeyboard renders the period row, the game row (only when
// there is more than one game to choose between — same reasoning as
// statsGameFilterRow), and the back button.
func (h *UpdateHandler) chatLeaderboardKeyboard(locale common.LocaleCode, period chatLeaderboardPeriod, game competition.GameCode) *InlineKeyboard {
	texts := h.Texts
	periodRow := []InlineButton{
		button(statsFilterLabel(period == chatLeaderboardAllTime, texts.Get("stats.all_time", locale)), chatLeaderboardCallback(chatLeaderboardAllTime, game)),
		button(statsFilterLabel(period == chatLeaderboardYear, texts.Get("stats.year", locale)), chatLeaderboardCallback(chatLeaderboardYear, game)),
		button(statsFilterLabel(period == chatLeaderboardMonth, texts.Get("stats.month", locale)), chatLeaderboardCallback(chatLeaderboardMonth, game)),
	}
	rows := [][]InlineButton{periodRow}

	if len(competition.Games) > 1 {
		gameRow := []InlineButton{button(statsFilterLabel(game == "", texts.Get("stats.all_games", locale)), chatLeaderboardCallback(period, ""))}
		for _, g := range competition.Games {
			gameRow = append(gameRow, button(statsFilterLabel(game == g, texts.Get(gameShortLabelKey(g), locale)), chatLeaderboardCallback(period, g)))
		}
		rows = append(rows, gameRow)
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "pstats:menu")})
	return &InlineKeyboard{InlineKeyboard: rows}
}
