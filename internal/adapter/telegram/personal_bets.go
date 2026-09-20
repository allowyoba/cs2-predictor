package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// privateBetsPageSize is smaller than privateChatsPageSize: each bet renders
// as a multi-line block (matchup, predicted vs actual score, chat, points),
// so fewer of them fit on one screen.
const privateBetsPageSize = 6

// personalBets resolves the per-bet history port — the same
// type-assertion pattern as personalInsights: the same postgres repository
// implements every scoring port, so a deployment wired with one that
// doesn't gets a clear error screen rather than a panic.
func (h *UpdateHandler) personalBets() (scoring.PersonalBetsRepository, error) {
	repo, ok := h.Scoring.(scoring.PersonalBetsRepository)
	if !ok {
		return nil, fmt.Errorf("scoring repository does not support personal bets")
	}
	return repo, nil
}

// privateBetsMenu renders one page of a person's own settled bets, either
// across every chat (chatID == nil, reached from the root personal-stats
// menu) or narrowed to one (chatID != nil, reached from
// renderPrivateChatStats's "my bets here" button) — newest first, each row
// showing what was predicted against what actually happened. listPage is
// only meaningful when chatID != nil: it's the chats-picker page the caller
// came from, carried through so the back button returns to the exact same
// chat-detail screen rather than always its first page.
func (h *UpdateHandler) privateBetsMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, chatID *common.ChatID, page, listPage int) error {
	repo, err := h.personalBets()
	if err != nil {
		return err
	}
	bets, err := repo.UserBets(ctx, userID, chatID, scoring.UserBetsMaxRows)
	if err != nil {
		return err
	}
	zone := h.userZone(ctx, userID)

	back := h.backButton(locale, betsBackTarget(chatID, listPage))
	if len(bets) == 0 {
		kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{back}}}
		return h.respond(ctx, target, bold(h.Texts.Get("bets.title", locale))+"\n\n"+h.Texts.Get("bets.empty", locale), &kb)
	}

	maxPage := (len(bets) - 1) / privateBetsPageSize
	if page > maxPage {
		page = maxPage
	}
	if page < 0 {
		page = 0
	}
	start := page * privateBetsPageSize
	end := min(start+privateBetsPageSize, len(bets))

	var b strings.Builder
	b.WriteString(bold(h.Texts.Get("bets.title", locale)))
	for _, bet := range bets[start:end] {
		b.WriteString("\n\n" + h.betLine(bet, locale, chatID != nil, zone))
	}

	var rows [][]InlineButton
	totalPages := maxPage + 1
	if nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return betsCallback(chatID, listPage, p)
	}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []InlineButton{back})
	return h.respond(ctx, target, b.String(), &InlineKeyboard{InlineKeyboard: rows})
}

// betLine renders one settled bet: a ✅/❌ result marker, the matchup, the
// predicted scoreline against the actual one, and the points it earned (if
// any — a correct pick can still earn 0 if the awards for that poll haven't
// settled, so this stays silent rather than claiming "+0"). The chat name is
// only shown on the all-chats screen — on a chat-scoped one it would repeat
// the same chat on every single row.
func (h *UpdateHandler) betLine(bet scoring.UserBet, locale common.LocaleCode, hideChatName bool, zone *time.Location) string {
	marker := "❌"
	if bet.Correct {
		marker = "✅"
	}
	matchup := fmt.Sprintf("%s — %s", escapeHTML(truncate(bet.FirstTeamName, 24)), escapeHTML(truncate(bet.SecondTeamName, 24)))
	line := marker + " " + bold(matchup)
	line += "\n" + h.Texts.Get("bets.line_scores", locale, bet.PredictedScore.String(), bet.ActualScore.String())
	// In the reader's own zone: a match that started at 01:00 Moscow time
	// was played the evening before in Berlin, and a list of dates that
	// disagrees with the reader's calendar is quietly wrong on every row.
	meta := bet.PlayedAt.In(zone).Format("02.01.2006")
	if !hideChatName {
		meta = escapeHTML(truncate(bet.ChatTitle, 30)) + " · " + meta
	}
	if bet.Points > 0 {
		meta += " · " + h.Texts.Get("bets.line_points", locale, bet.Points)
	}
	line += "\n" + italic(meta)
	return line
}

// betsCallback builds the callback data for one page of the bets screen,
// either scoped to one chat (carrying the chats-picker page it was opened
// from, so the back button can return there) or across all chats.
func betsCallback(chatID *common.ChatID, listPage, page int) string {
	if chatID == nil {
		return fmt.Sprintf("pstats:bets:%d", page)
	}
	return fmt.Sprintf("pstats:bets:c:%d:%d:%d", chatID.Value, listPage, page)
}

// betsBackTarget is where the bets screen's back button returns to: the
// chat-detail screen it was opened from (at the chats-picker page that led
// to it) when scoped to one chat, or the root personal-stats menu
// otherwise.
func betsBackTarget(chatID *common.ChatID, listPage int) string {
	if chatID == nil {
		return "pstats:menu"
	}
	return fmt.Sprintf("pstats:chat:%d:%d", chatID.Value, listPage)
}
