package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The leaderboard screens: period pickers, the standings themselves, and
// one person's own numbers within a chat.

// statsBreakdown renders "(exact-outcome-wrong)": how many predictions
// nailed the exact score, how many got only the winner right, and how many
// missed entirely — exact+outcome+wrong always sums to total. Shown next to
// a participant's points everywhere a leaderboard renders one, so the total
// is never the only visible number.
func statsBreakdown(exact, correct, total int) string {
	return fmt.Sprintf("(%d-%d-%d)", exact, correct-exact, total-correct)
}

func parseYearMonth(s string) (int, time.Month, error) {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return 0, 0, err
	}
	return t.Year(), t.Month(), nil
}

func (h *UpdateHandler) statsMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	available, err := h.Scoring.AvailableMonths(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	if len(available) == 0 {
		rows := [][]InlineButton{{h.backButton(settings.Locale, "menu:main")}}
		text := h.Texts.Get("stats.choose", settings.Locale) + "\n\n" + h.Texts.Get("stats.empty", settings.Locale)
		if target.chatID != settings.ChatID {
			text = h.Texts.Get("stats.managed_choose", settings.Locale, escapeHTML(settings.Title)) + "\n\n" + h.Texts.Get("stats.empty", settings.Locale)
		}
		return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
	}
	latest := available[0]
	latestMonthLabel := fmt.Sprintf("%s %d", shortMonthName(latest.Month, settings.Locale), latest.Year)
	rows := [][]InlineButton{
		{button(latestMonthLabel, fmt.Sprintf("stats:month:%04d-%02d:menu", latest.Year, latest.Month)), button(strconv.Itoa(latest.Year), fmt.Sprintf("stats:year:%d:menu", latest.Year))},
		{button(h.Texts.Get("stats.all_time", settings.Locale), "stats:all"), button(h.Texts.Get("stats.event", settings.Locale), "stats:events")},
		{button(h.Texts.Get("stats.other_period", settings.Locale), "stats:years")},
	}
	if target.chatID == settings.ChatID {
		if link, ok := h.statsDeepLink(settings.ChatID); ok {
			rows = append(rows, []InlineButton{urlButton(h.Texts.Get("stats.open_dm", settings.Locale), link)})
		}
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:main")})
	text := h.Texts.Get("stats.choose", settings.Locale)
	if target.chatID != settings.ChatID {
		text = h.Texts.Get("stats.managed_choose", settings.Locale, escapeHTML(settings.Title))
	}
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) yearMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	available, err := h.Scoring.AvailableMonths(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	seen := map[int]bool{}
	var rows [][]InlineButton
	for _, period := range available {
		if seen[period.Year] {
			continue
		}
		seen[period.Year] = true
		y := period.Year
		rows = append(rows, []InlineButton{
			button(strconv.Itoa(y), fmt.Sprintf("stats:year:%d:years", y)),
			button(h.Texts.Get("stats.months_button", settings.Locale), fmt.Sprintf("stats:months:%d", y)),
		})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:stats")})
	text := bold(escapeHTML(h.Texts.Get("stats.year", settings.Locale)))
	if len(seen) == 0 {
		text += "\n\n" + h.Texts.Get("stats.empty", settings.Locale)
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

func shortMonthName(m time.Month, locale common.LocaleCode) string {
	if locale == common.LocaleRU {
		return monthNamesRU[m-1]
	}
	return m.String()[:3]
}

func (h *UpdateHandler) monthMenu(ctx context.Context, target replyTarget, settings chat.Settings, year int) error {
	available, err := h.Scoring.AvailableMonths(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	var rows [][]InlineButton
	var row []InlineButton
	for _, period := range available {
		if period.Year != year {
			continue
		}
		label := shortMonthName(period.Month, settings.Locale)
		row = append(row, button(label, fmt.Sprintf("stats:month:%04d-%02d:months:%d", year, period.Month, year)))
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "stats:years")})
	text := bold(escapeHTML(fmt.Sprintf("%s · %d", h.Texts.Get("stats.month", settings.Locale), year)))
	if len(rows) == 1 {
		text += "\n\n" + h.Texts.Get("stats.empty", settings.Locale)
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) eventStatsMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	ids, err := h.Scoring.AvailableEventIDs(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	if len(ids) > 20 {
		ids = ids[:20]
	}
	events, err := h.Catalog.FindEvents(ctx, ids)
	if err != nil {
		return err
	}
	byID := make(map[common.EventID]competition.Event, len(events))
	for _, event := range events {
		byID[event.ID] = event
	}

	ordered := make([]competition.Event, 0, len(ids))
	for _, id := range ids {
		if event, ok := byID[id]; ok {
			ordered = append(ordered, event)
		}
	}
	rows := h.gameSectionRows(ordered, settings.Locale, func(event competition.Event) []InlineButton {
		return []InlineButton{button(event.Tier.Badge()+truncate(event.Name, 48), "stats:event:"+event.ID.Value.String())}
	})
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:stats")})
	body := h.Texts.Get("stats.empty", settings.Locale)
	if len(rows) > 1 {
		body = bold(escapeHTML(h.Texts.Get("stats.event", settings.Locale)))
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, body), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) personalStats(ctx context.Context, target replyTarget, settings chat.Settings, from User, eventID common.EventID) error {
	standings, err := h.Scoring.Leaderboard(ctx, settings.ChatID, scoring.ForEvent(eventID))
	if err != nil {
		return err
	}
	for _, s := range standings {
		if s.UserID.Value == from.ID {
			text := fmt.Sprintf("%s %s\n%s · %s",
				bold(escapeHTML(s.DisplayName)), code(statsBreakdown(s.ExactPredictions, s.CorrectPredictions, s.Predictions)),
				code(fmt.Sprintf("#%d", s.Rank)), code(strconv.Itoa(s.Points)))
			return h.respond(ctx, target, managedScreenContext(target, settings, text), h.statsExit(settings.Locale, "stats:events"))
		}
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("stats.empty", settings.Locale)), h.statsExit(settings.Locale, "stats:events"))
}

// leaderboardRows favors a compact, wrap-safe list over a faux table.
// Telegram clients render proportional text, emoji and Unicode names at
// different widths, so padding with spaces is not stable on mobile. viewer
// marks that row with 👤 when it matches one of the standings; the zero
// common.UserID never matches a real Telegram user, so callers with no
// viewer to highlight can simply pass it.
func leaderboardRows(standings []scoring.UserStanding, viewer common.UserID) string {
	var b strings.Builder
	for i, s := range standings {
		name := truncate(s.DisplayName, 24)
		if i > 0 {
			b.WriteString("\n")
		}
		prefix := medalFor(s.Rank)
		if s.Rank > 3 {
			prefix = fmt.Sprintf("%d.", s.Rank)
		}
		movement := standingMovement(s)
		if movement != "" {
			movement = " " + italic(movement)
		}
		viewerMark := ""
		if s.UserID == viewer {
			viewerMark = " · 👤"
		}
		wins := ""
		if s.TournamentWins > 0 {
			wins = fmt.Sprintf(" · 🏆%d", s.TournamentWins)
		}
		fmt.Fprintf(&b, "%s %s %s%s · %s%s%s", prefix, bold(escapeHTML(name)),
			code(statsBreakdown(s.ExactPredictions, s.CorrectPredictions, s.Predictions)), movement, code(strconv.Itoa(s.Points)), wins, viewerMark)
	}
	return b.String()
}

func standingMovement(s scoring.UserStanding) string {
	if s.PreviousRank == nil {
		return ""
	}
	switch {
	case s.Rank < *s.PreviousRank:
		return fmt.Sprintf("↑%d", *s.PreviousRank-s.Rank)
	case s.Rank > *s.PreviousRank:
		return fmt.Sprintf("↓%d", s.Rank-*s.PreviousRank)
	default:
		return "•"
	}
}

func (h *UpdateHandler) statsExit(locale common.LocaleCode, backData string) *InlineKeyboard {
	return &InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(locale, backData)}}}
}

func statsFilterLabel(active bool, label string) string {
	if active {
		return "✓ " + label
	}
	return label
}

const leaderboardPageSize = 10

func statsBackCode(backData string) string {
	switch {
	case backData == "stats:years":
		return "y"
	case strings.HasPrefix(backData, "stats:months:"):
		return "m"
	default:
		return "r"
	}
}

func leaderboardPageData(period scoring.StatsPeriod, page int, backData string) string {
	return appendGame(leaderboardPageBase(period, page, backData), period.Game)
}

func leaderboardPageBase(period scoring.StatsPeriod, page int, backData string) string {
	switch period.Kind {
	case scoring.PeriodYear:
		return fmt.Sprintf("stats:p:y:%d:%d:%s", period.Year, page, statsBackCode(backData))
	case scoring.PeriodMonth:
		return fmt.Sprintf("stats:p:m:%04d%02d:%d:%s", period.Year, period.Month, page, statsBackCode(backData))
	case scoring.PeriodEvent:
		return fmt.Sprintf("stats:p:e:%s:%d", strings.ReplaceAll(period.EventID.Value.String(), "-", ""), page)
	default:
		return fmt.Sprintf("stats:p:a:%d", page)
	}
}

func (h *UpdateHandler) leaderboardKeyboard(settings chat.Settings, period scoring.StatsPeriod, backData string, page, totalPages int) *InlineKeyboard {
	// Switching period keeps whichever game is selected: the two filters
	// are independent, and losing one because the other was touched is the
	// classic way a filter bar becomes untrustworthy.
	monthData, yearData := "stats:years", "stats:years"
	switch period.Kind {
	case scoring.PeriodMonth:
		monthData = appendGame(fmt.Sprintf("stats:month:%04d-%02d:menu", period.Year, period.Month), period.Game)
		yearData = appendGame(fmt.Sprintf("stats:year:%d:menu", period.Year), period.Game)
	case scoring.PeriodYear:
		yearData = appendGame(fmt.Sprintf("stats:year:%d:menu", period.Year), period.Game)
	}
	allTimeData := appendGame("stats:all", period.Game)
	rows := [][]InlineButton{
		{
			button(statsFilterLabel(period.Kind == scoring.PeriodMonth, h.Texts.Get("stats.month_short", settings.Locale)), monthData),
			button(statsFilterLabel(period.Kind == scoring.PeriodYear, h.Texts.Get("stats.year_short", settings.Locale)), yearData),
			button(statsFilterLabel(period.Kind == scoring.PeriodAllTime, h.Texts.Get("stats.all_time_short", settings.Locale)), allTimeData),
		},
		{button(statsFilterLabel(period.Kind == scoring.PeriodEvent, h.Texts.Get("stats.event", settings.Locale)), "stats:events")},
	}
	// Match-by-match results only make sense scoped to one tournament — a
	// month or a year can span many, so these two buttons only show on an
	// event-scoped leaderboard.
	if period.Kind == scoring.PeriodEvent {
		rows = append(rows, []InlineButton{
			button(h.Texts.Get("evbets.mine_button", settings.Locale), eventBetsMineCallback(period.EventID)),
			button(h.Texts.Get("evbets.pick_button", settings.Locale), eventParticipantPickerCallback(period.EventID, 0)),
		})
	}
	// The game filter sits directly under the period filter: both answer
	// "which slice of this chat am I looking at", and splitting them
	// across the screen would read as two unrelated controls.
	if row := h.statsGameFilterRow(settings, period, func(p scoring.StatsPeriod) string {
		return leaderboardPageData(p, 0, backData)
	}); row != nil {
		rows = append(rows, row)
	}
	rows = append(rows, []InlineButton{
		button(h.Texts.Get("chart.button", settings.Locale), chartCallbackData(period)),
		button(h.Texts.Get("chart.rank_button", settings.Locale), rankChartCallbackData(period)),
	})
	if nav := paginationRow(page, totalPages, h.Texts.Get("stats.page", settings.Locale, page+1, totalPages), func(p int) string {
		return leaderboardPageData(period, p, backData)
	}); nav != nil {
		rows = append(rows, nav)
	}
	// The scoring rules belong on the board that shows the scores. This is
	// where "why do I have that many points" is actually asked, and the
	// answer used to live one menu up, next to the command reference,
	// where nobody looking at a table would think to go.
	rows = append(rows, []InlineButton{
		button(h.Texts.Get("menu.rules", settings.Locale), rulesTarget(backData)),
		h.backButton(settings.Locale, backData),
	})
	return &InlineKeyboard{InlineKeyboard: rows}
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (h *UpdateHandler) renderLeaderboard(ctx context.Context, target replyTarget, settings chat.Settings, period scoring.StatsPeriod, backData string, viewer common.UserID, pageOpt ...int) error {
	standings, err := h.Scoring.Leaderboard(ctx, settings.ChatID, period)
	if err != nil {
		return err
	}

	page := 0
	if len(pageOpt) > 0 && pageOpt[0] > 0 {
		page = pageOpt[0]
	}
	totalPages := 1
	if len(standings) > 0 {
		totalPages = (len(standings) + leaderboardPageSize - 1) / leaderboardPageSize
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	viewerIndex := -1
	for i := range standings {
		if standings[i].UserID == viewer {
			viewerIndex = i
			break
		}
	}

	start := page * leaderboardPageSize
	end := start + leaderboardPageSize
	if end > len(standings) {
		end = len(standings)
	}
	visible := standings[start:end]
	body := h.Texts.Get("stats.empty", settings.Locale)
	if len(visible) > 0 {
		body = leaderboardRows(visible, viewer)
	}
	viewerVisible := viewerIndex >= start && viewerIndex < end
	if viewerIndex >= 0 && !viewerVisible {
		viewerStanding := standings[viewerIndex]
		movement := standingMovement(viewerStanding)
		if movement != "" {
			movement = " " + italic(movement)
		}
		breakdown := statsBreakdown(viewerStanding.ExactPredictions, viewerStanding.CorrectPredictions, viewerStanding.Predictions)
		body += "\n\n────────\n" + h.Texts.Get("stats.you", settings.Locale, viewerStanding.Rank, viewerStanding.Points, movement, breakdown)
	}

	periodName, err := h.periodName(ctx, settings, period)
	if err != nil {
		return err
	}
	// A board narrowed to one game must say so in its own title: the rows
	// look identical either way, and "first place" means something
	// different in each.
	periodName += h.statsGameSuffix(settings, period)
	var text string
	if target.chatID != settings.ChatID {
		text = h.Texts.Get("stats.managed_title", settings.Locale, escapeHTML(settings.Title), escapeHTML(periodName)) + "\n\n" + body
	} else {
		text = h.Texts.Get("stats.title", settings.Locale, escapeHTML(periodName)) + "\n\n" + body
	}
	return h.respond(ctx, target, text, h.leaderboardKeyboard(settings, period, backData, page, totalPages))
}

func (h *UpdateHandler) periodName(ctx context.Context, settings chat.Settings, period scoring.StatsPeriod) (string, error) {
	switch period.Kind {
	case scoring.PeriodYear:
		return h.Texts.Get("stats.period_year", settings.Locale, period.Year), nil
	case scoring.PeriodMonth:
		monthYear := fmt.Sprintf("%s %d", shortMonthName(period.Month, settings.Locale), period.Year)
		return h.Texts.Get("stats.period_month", settings.Locale, monthYear), nil
	case scoring.PeriodEvent:
		event, err := h.Catalog.FindEvent(ctx, period.EventID)
		if err != nil {
			return "", err
		}
		if event != nil {
			return event.Name, nil
		}
		return h.Texts.Get("stats.period_event", settings.Locale), nil
	default:
		return h.Texts.Get("stats.period_all", settings.Locale), nil
	}
}
