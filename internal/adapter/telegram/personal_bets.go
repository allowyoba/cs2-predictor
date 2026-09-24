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

// betLine renders one settled bet: a 🎯/✅/❌ result marker, the matchup, the
// predicted scoreline against the actual one, and the points it earned (if
// any — a correct pick can still earn 0 if the awards for that poll haven't
// settled, so this stays silent rather than claiming "+0"). The chat name is
// only shown on the all-chats screen — on a chat-scoped one it would repeat
// the same chat on every single row.
// betMarker distinguishes the two ways a bet can be right. Calling the
// winner is worth a point; calling the scoreline exactly is worth several,
// and is the thing people actually boast about — one glyph for both made
// the harder result invisible on the very screen it is listed.
func betMarker(bet scoring.UserBet) string {
	switch bet.ResultKind() {
	case scoring.BetResultExact:
		return "🎯"
	case scoring.BetResultWinner:
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

// renderPersonalBets draws the root "мои ставки" screen — every settled bet
// across every chat, filterable by game, chat and result kind, all three
// combinable. It always reads the full unscoped history and filters it
// locally (unlike privateBetsMenu's chat-scoped entry point, which asks the
// repository for one chat directly) because the filter picker itself needs
// to know what games/chats/kinds exist across the whole history to decide
// which rows are even worth offering.
func (h *UpdateHandler) renderPersonalBets(ctx context.Context, target replyTarget, userID common.UserID,
	locale common.LocaleCode, game competition.GameCode, chatID *common.ChatID, kind scoring.BetResultKind, page int) error {
	repo, err := h.personalBets()
	if err != nil {
		return err
	}
	bets, err := repo.UserBets(ctx, userID, nil, scoring.UserBetsMaxRows)
	if err != nil {
		return err
	}
	zone := h.userZone(ctx, userID)

	// A stale filter button (the game/chat/kind it names no longer appears
	// in the history) falls back to "all" rather than rendering an empty
	// screen the picker itself disagrees with.
	if game != "" && !hasBetGame(bets, game) {
		game = ""
	}
	if chatID != nil && !hasBetChat(bets, *chatID) {
		chatID = nil
	}
	if kind != "" && !hasBetResultKind(bets, kind) {
		kind = ""
	}

	selected := bets
	if game != "" {
		selected = filterBetsByGame(selected, game)
	}
	if chatID != nil {
		selected = filterBetsByChat(selected, *chatID)
	}
	if kind != "" {
		selected = filterBetsByResultKind(selected, kind)
	}

	title := bold(h.Texts.Get("bets.title", locale))
	if len(selected) == 0 {
		kb := h.betsFilterKeyboard(ctx, locale, bets, game, chatID, kind, 0, 1)
		empty := h.Texts.Get("bets.empty", locale)
		if len(bets) > 0 {
			// Some data exists, just not under this filter combination — a
			// plain "no bets yet" would read as if the account had none at
			// all.
			empty = h.Texts.Get("bets.empty_filtered", locale)
		}
		return h.respond(ctx, target, title+"\n\n"+empty, kb)
	}

	maxPage := (len(selected) - 1) / privateBetsPageSize
	if page > maxPage {
		page = maxPage
	}
	if page < 0 {
		page = 0
	}
	start := page * privateBetsPageSize
	end := min(start+privateBetsPageSize, len(selected))

	var b strings.Builder
	b.WriteString(title)
	for _, bet := range selected[start:end] {
		b.WriteString("\n\n" + h.betLine(bet, locale, chatID != nil, zone))
	}

	totalPages := maxPage + 1
	kb := h.betsFilterKeyboard(ctx, locale, bets, game, chatID, kind, page, totalPages)
	return h.respond(ctx, target, b.String(), kb)
}

// betsFilterKeyboard renders the game/chat/result-kind filter rows above the
// pagination and back button — following personal_insights.go's
// insightsKeyboard convention: a filter row only appears when there is more
// than one option to pick between, since a picker with a single meaningless
// choice is furniture, not a filter.
func (h *UpdateHandler) betsFilterKeyboard(ctx context.Context, locale common.LocaleCode, bets []scoring.UserBet,
	game competition.GameCode, chatID *common.ChatID, kind scoring.BetResultKind, page, totalPages int) *InlineKeyboard {
	var rows [][]InlineButton
	if games := distinctBetGames(bets); len(games) > 1 {
		row := []InlineButton{button(
			statsFilterLabel(game == "", h.Texts.Get("bets.all_games", locale)), betsFilterCallback("", chatID, kind, 0))}
		for _, g := range games {
			row = append(row, button(
				statsFilterLabel(game == g, h.Texts.Get(gameShortLabelKey(g), locale)), betsFilterCallback(g, chatID, kind, 0)))
		}
		rows = append(rows, row)
	}
	if chats := distinctBetChats(bets); len(chats) > 1 {
		row := []InlineButton{button(
			statsFilterLabel(chatID == nil, h.Texts.Get("bets.all_chats", locale)), betsFilterCallback(game, nil, kind, 0))}
		for _, c := range chats {
			row = append(row, button(
				statsFilterLabel(chatID != nil && *chatID == c, h.chatShortLabel(ctx, c)), betsFilterCallback(game, &c, kind, 0)))
		}
		rows = append(rows, row)
	}
	if kinds := distinctBetResultKinds(bets); len(kinds) > 1 {
		row := []InlineButton{button(
			statsFilterLabel(kind == "", h.Texts.Get("bets.all_results", locale)), betsFilterCallback(game, chatID, "", 0))}
		for _, k := range kinds {
			row = append(row, button(
				statsFilterLabel(kind == k, h.Texts.Get(betResultKindLabelKey(k), locale)), betsFilterCallback(game, chatID, k, 0)))
		}
		rows = append(rows, row)
	}
	if nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return betsFilterCallback(game, chatID, kind, p)
	}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []InlineButton{h.backButton(locale, "pstats:menu")})
	return &InlineKeyboard{InlineKeyboard: rows}
}

// betResultKindLabelKey maps a BetResultKind to its button text key.
func betResultKindLabelKey(kind scoring.BetResultKind) string {
	switch kind {
	case scoring.BetResultExact:
		return "bets.kind_exact"
	case scoring.BetResultWinner:
		return "bets.kind_winner"
	default:
		return "bets.kind_miss"
	}
}

// distinctBetGames/distinctBetChats/distinctBetResultKinds list what the
// filter picker should offer, in first-seen (games/chats) or fixed
// (result kinds, so the row's layout doesn't shuffle between screens) order.

func distinctBetGames(bets []scoring.UserBet) []competition.GameCode {
	seen := make(map[competition.GameCode]bool)
	var out []competition.GameCode
	for _, b := range bets {
		if !seen[b.Game] {
			seen[b.Game] = true
			out = append(out, b.Game)
		}
	}
	return out
}

func distinctBetChats(bets []scoring.UserBet) []common.ChatID {
	seen := make(map[common.ChatID]bool)
	var out []common.ChatID
	for _, b := range bets {
		if !seen[b.ChatID] {
			seen[b.ChatID] = true
			out = append(out, b.ChatID)
		}
	}
	return out
}

func distinctBetResultKinds(bets []scoring.UserBet) []scoring.BetResultKind {
	seen := make(map[scoring.BetResultKind]bool)
	for _, b := range bets {
		seen[b.ResultKind()] = true
	}
	var out []scoring.BetResultKind
	for _, k := range []scoring.BetResultKind{scoring.BetResultExact, scoring.BetResultWinner, scoring.BetResultMiss} {
		if seen[k] {
			out = append(out, k)
		}
	}
	return out
}

func hasBetGame(bets []scoring.UserBet, game competition.GameCode) bool {
	for _, b := range bets {
		if b.Game == game {
			return true
		}
	}
	return false
}

func hasBetChat(bets []scoring.UserBet, chatID common.ChatID) bool {
	for _, b := range bets {
		if b.ChatID == chatID {
			return true
		}
	}
	return false
}

func hasBetResultKind(bets []scoring.UserBet, kind scoring.BetResultKind) bool {
	for _, b := range bets {
		if b.ResultKind() == kind {
			return true
		}
	}
	return false
}

func filterBetsByGame(bets []scoring.UserBet, game competition.GameCode) []scoring.UserBet {
	out := make([]scoring.UserBet, 0, len(bets))
	for _, b := range bets {
		if b.Game == game {
			out = append(out, b)
		}
	}
	return out
}

func filterBetsByChat(bets []scoring.UserBet, chatID common.ChatID) []scoring.UserBet {
	out := make([]scoring.UserBet, 0, len(bets))
	for _, b := range bets {
		if b.ChatID == chatID {
			out = append(out, b)
		}
	}
	return out
}

func filterBetsByResultKind(bets []scoring.UserBet, kind scoring.BetResultKind) []scoring.UserBet {
	out := make([]scoring.UserBet, 0, len(bets))
	for _, b := range bets {
		if b.ResultKind() == kind {
			out = append(out, b)
		}
	}
	return out
}

// betsFilterCallback builds the callback data for one combination of the
// bets screen's three filters plus a page, mirroring
// insightsFilterCallback/parseInsightsCallback's convention of encoding
// optional filter segments into one callback string. Unlike the insights
// callback (where a trailing segment can simply be omitted), this one always
// has four segments — page always needs to be there for pagination — so an
// unset game/chat/kind is encoded as an empty segment instead of an omitted
// one.
func betsFilterCallback(game competition.GameCode, chatID *common.ChatID, kind scoring.BetResultKind, page int) string {
	chatPart := ""
	if chatID != nil {
		chatPart = strconv.FormatInt(chatID.Value, 10)
	}
	return fmt.Sprintf("pstats:bets:f:%s:%s:%s:%d", game, chatPart, kind, page)
}

// parseBetsFilterCallback splits the remainder of a "pstats:bets:f:" callback
// back into its game, chat, result-kind and page segments.
func parseBetsFilterCallback(remainder string) (competition.GameCode, *common.ChatID, scoring.BetResultKind, int, error) {
	parts := strings.Split(remainder, ":")
	if len(parts) != 4 {
		return "", nil, "", 0, fmt.Errorf("invalid bets filter callback")
	}
	game := competition.GameCode(parts[0])
	var chatID *common.ChatID
	if parts[1] != "" {
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return "", nil, "", 0, err
		}
		c := common.ChatID{Value: id}
		chatID = &c
	}
	kind := scoring.BetResultKind(parts[2])
	page, err := strconv.Atoi(parts[3])
	if err != nil || page < 0 {
		return "", nil, "", 0, fmt.Errorf("invalid bets filter page")
	}
	return game, chatID, kind, page, nil
}
