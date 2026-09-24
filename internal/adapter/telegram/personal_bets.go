package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/competition"
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

// betsFilter is the combinable filter state for one "my bets" screen: which
// chat, which game and which outcome to show. Every field's zero value means
// "all", so the screen's default is the full history rather than an
// arbitrarily narrowed slice.
//
// Result reuses the same correct/wrong taxonomy as the Mini App's history
// screen (httpapi.historyFilter) rather than inventing a new one — the
// domain has no separate "bet type" concept (outright/handicap/total or
// similar): every bet is a same-shaped scoreline prediction, and the only
// thing that meaningfully splits them is whether it settled right.
type betsFilter struct {
	ChatID *common.ChatID
	Game   competition.GameCode
	Result string // "", "correct" or "wrong"
}

func (f betsFilter) matches(bet scoring.UserBet) bool {
	if f.ChatID != nil && bet.ChatID != *f.ChatID {
		return false
	}
	if f.Game != "" && bet.Game != f.Game {
		return false
	}
	switch f.Result {
	case "correct":
		return bet.Correct
	case "wrong":
		return !bet.Correct
	default:
		return true
	}
}

// betChat is one entry in the chat filter row: enough to render a button and
// to match bets against.
type betChat struct {
	ChatID common.ChatID
	Title  string
}

// distinctBetChats lists the chats that appear anywhere in bets, each once,
// in first-seen (i.e. newest-first, since bets is already sorted that way)
// order — so the filter row offers exactly the chats this person actually
// has settled bets in, never an empty room.
func distinctBetChats(bets []scoring.UserBet) []betChat {
	var chats []betChat
	seen := map[common.ChatID]bool{}
	for _, bet := range bets {
		if seen[bet.ChatID] {
			continue
		}
		seen[bet.ChatID] = true
		chats = append(chats, betChat{ChatID: bet.ChatID, Title: bet.ChatTitle})
	}
	return chats
}

// distinctBetGames is distinctBetChats' counterpart for the game axis.
func distinctBetGames(bets []scoring.UserBet) []competition.GameCode {
	var games []competition.GameCode
	seen := map[competition.GameCode]bool{}
	for _, bet := range bets {
		if seen[bet.Game] {
			continue
		}
		seen[bet.Game] = true
		games = append(games, bet.Game)
	}
	return games
}

func hasBetChat(chats []betChat, chatID common.ChatID) bool {
	for _, c := range chats {
		if c.ChatID == chatID {
			return true
		}
	}
	return false
}

func hasBetGame(games []competition.GameCode, game competition.GameCode) bool {
	for _, g := range games {
		if g == game {
			return true
		}
	}
	return false
}

// hasBetResult reports whether any bet in the (unfiltered) list settled with
// the given outcome — the result filter row is only offered once both
// outcomes actually exist to choose between.
func hasBetResult(bets []scoring.UserBet, correct bool) bool {
	for _, bet := range bets {
		if bet.Correct == correct {
			return true
		}
	}
	return false
}

// privateBetsMenu renders one page of a person's own settled bets, narrowed
// by filter's chat/game/result combination — every field defaulting to "all"
// — newest first, each row showing what was predicted against what actually
// happened. listPage is only meaningful when filter.ChatID != nil: it's the
// chats-picker page the caller came from, carried through so the back button
// returns to the exact same chat-detail screen rather than always its first
// page.
//
// The read itself is always unscoped (every chat): filtering by chat, game
// and result all happen here in memory, the same reasoning the Mini App's
// history screen already applies (see httpapi.buildHistory) — the read is
// already capped and sorted, so narrowing it in memory costs nothing, while
// this is also what lets the filter rows themselves be built from the
// person's whole history rather than just whatever slice is currently shown.
func (h *UpdateHandler) privateBetsMenu(ctx context.Context, target replyTarget, userID common.UserID, locale common.LocaleCode, filter betsFilter, page, listPage int) error {
	repo, err := h.personalBets()
	if err != nil {
		return err
	}
	all, err := repo.UserBets(ctx, userID, nil, scoring.UserBetsMaxRows)
	if err != nil {
		return err
	}
	zone := h.userZone(ctx, userID)

	chats := distinctBetChats(all)
	games := distinctBetGames(all)
	// A stale filter (left the chat, never played that game) degrades to
	// "all" rather than a screen stuck showing nothing forever.
	if filter.ChatID != nil && !hasBetChat(chats, *filter.ChatID) {
		filter.ChatID = nil
	}
	if filter.Game != "" && !hasBetGame(games, filter.Game) {
		filter.Game = ""
	}

	bets := make([]scoring.UserBet, 0, len(all))
	for _, bet := range all {
		if filter.matches(bet) {
			bets = append(bets, bet)
		}
	}

	back := h.backButton(locale, betsBackTarget(filter.ChatID, listPage))
	title := bold(h.Texts.Get("bets.title", locale))

	if len(bets) == 0 {
		kb := h.betsKeyboard(chats, games, all, filter, locale, listPage, nil, back)
		return h.respond(ctx, target, title+"\n\n"+h.Texts.Get("bets.empty", locale), kb)
	}

	maxPage := (len(bets) - 1) / privateBetsPageSize
	page = max(0, min(page, maxPage))
	start := page * privateBetsPageSize
	end := min(start+privateBetsPageSize, len(bets))

	var b strings.Builder
	b.WriteString(title)
	for _, bet := range bets[start:end] {
		b.WriteString("\n\n" + h.betLine(bet, locale, filter.ChatID != nil, zone))
	}

	totalPages := maxPage + 1
	nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return betsCallback(filter, listPage, p)
	})
	kb := h.betsKeyboard(chats, games, all, filter, locale, listPage, nav, back)
	return h.respond(ctx, target, b.String(), kb)
}

// betsKeyboard renders the filter rows (game, chat, result) above the
// pagination and back rows, each following the same "✓ " active-marker
// convention as the personal-insights picker, and each only appearing when
// there is more than one value to choose between — a filter with a single
// option is furniture, not a choice.
func (h *UpdateHandler) betsKeyboard(chats []betChat, games []competition.GameCode, all []scoring.UserBet, filter betsFilter,
	locale common.LocaleCode, listPage int, nav []InlineButton, back InlineButton) *InlineKeyboard {
	var rows [][]InlineButton
	if len(games) > 1 {
		row := []InlineButton{button(
			statsFilterLabel(filter.Game == "", h.Texts.Get("insights.all_games", locale)),
			betsCallback(betsFilter{ChatID: filter.ChatID, Result: filter.Result}, listPage, 0))}
		for _, game := range games {
			row = append(row, button(
				statsFilterLabel(filter.Game == game, h.Texts.Get(gameShortLabelKey(game), locale)),
				betsCallback(betsFilter{ChatID: filter.ChatID, Game: game, Result: filter.Result}, listPage, 0)))
		}
		rows = append(rows, row)
	}
	if len(chats) > 1 {
		row := []InlineButton{button(
			statsFilterLabel(filter.ChatID == nil, h.Texts.Get("insights.all_chats", locale)),
			betsCallback(betsFilter{Game: filter.Game, Result: filter.Result}, 0, 0))}
		for _, c := range chats {
			chatID := c.ChatID
			row = append(row, button(
				statsFilterLabel(filter.ChatID != nil && *filter.ChatID == chatID, truncate(c.Title, 16)),
				betsCallback(betsFilter{ChatID: &chatID, Game: filter.Game, Result: filter.Result}, 0, 0)))
		}
		rows = append(rows, row)
	}
	if hasBetResult(all, true) && hasBetResult(all, false) {
		rows = append(rows, []InlineButton{
			button(statsFilterLabel(filter.Result == "", h.Texts.Get("bets.filter_all", locale)),
				betsCallback(betsFilter{ChatID: filter.ChatID, Game: filter.Game}, listPage, 0)),
			button(statsFilterLabel(filter.Result == "correct", h.Texts.Get("bets.filter_correct", locale)),
				betsCallback(betsFilter{ChatID: filter.ChatID, Game: filter.Game, Result: "correct"}, listPage, 0)),
			button(statsFilterLabel(filter.Result == "wrong", h.Texts.Get("bets.filter_wrong", locale)),
				betsCallback(betsFilter{ChatID: filter.ChatID, Game: filter.Game, Result: "wrong"}, listPage, 0)),
		})
	}
	if nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []InlineButton{back})
	return &InlineKeyboard{InlineKeyboard: rows}
}

// betMarker distinguishes the two ways a bet can be right. Calling the
// winner is worth a point; calling the scoreline exactly is worth several,
// and is the thing people actually boast about — one glyph for both made
// the harder result invisible on the very screen it is listed.
func betMarker(bet scoring.UserBet) string {
	switch {
	case bet.Correct && bet.PredictedScore == bet.ActualScore:
		return "🎯"
	case bet.Correct:
		return "✅"
	default:
		return "❌"
	}
}

func (h *UpdateHandler) betLine(bet scoring.UserBet, locale common.LocaleCode, hideChatName bool, zone *time.Location) string {
	marker := betMarker(bet)
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
// carrying the full filter combination plus the chats-picker page it was
// opened from (only meaningful when filter.ChatID != nil, so the back
// button can return there).
func betsCallback(filter betsFilter, listPage, page int) string {
	chat := "-"
	if filter.ChatID != nil {
		chat = strconv.FormatInt(filter.ChatID.Value, 10)
	}
	result := filter.Result
	if result == "" {
		result = "-"
	}
	return fmt.Sprintf("pstats:bets:%s:%s:%s:%d:%d", filter.Game, chat, result, listPage, page)
}

// parseBetsCallback reads betsCallback's format back. An unparsable filter
// is refused rather than silently defaulted — a stale or tampered-with
// button that quietly showed the wrong slice would be worse than an error
// screen.
func parseBetsCallback(remainder string) (filter betsFilter, listPage, page int, err error) {
	parts := strings.Split(remainder, ":")
	if len(parts) != 5 {
		return betsFilter{}, 0, 0, fmt.Errorf("invalid bets callback")
	}
	filter.Game = competition.GameCode(parts[0])
	if parts[1] != "-" {
		id, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil {
			return betsFilter{}, 0, 0, parseErr
		}
		chatID := common.ChatID{Value: id}
		filter.ChatID = &chatID
	}
	if parts[2] != "-" {
		filter.Result = parts[2]
	}
	listPage, err = strconv.Atoi(parts[3])
	if err != nil || listPage < 0 {
		return betsFilter{}, 0, 0, fmt.Errorf("invalid bets list page")
	}
	page, err = strconv.Atoi(parts[4])
	if err != nil || page < 0 {
		return betsFilter{}, 0, 0, fmt.Errorf("invalid bets page")
	}
	return filter, listPage, page, nil
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
