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
		{button(latestMonthLabel, fmt.Sprintf("stats:month:%04d-%02d", latest.Year, latest.Month)), button(strconv.Itoa(latest.Year), fmt.Sprintf("stats:year:%d", latest.Year))},
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
			button(strconv.Itoa(y), fmt.Sprintf("stats:year:%d", y)),
			button(h.Texts.Get("stats.months_button", settings.Locale), fmt.Sprintf("stats:months:%d", y)),
		})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:stats")})
	text := bold(escapeHTML(h.Texts.Get("stats.year", settings.Locale)))
	if len(seen) == 0 {
		text += "\n\n" + h.Texts.Get("stats.empty", settings.Locale)
	}
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
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
		row = append(row, button(label, fmt.Sprintf("stats:month:%04d-%02d", year, period.Month)))
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
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
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

	var rows [][]InlineButton
	for _, id := range ids {
		event, ok := byID[id]
		if !ok {
			continue
		}
		rows = append(rows, []InlineButton{button(event.Tier.Badge()+truncate(event.Name, 48), "stats:event:"+id.Value.String())})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:stats")})
	body := h.Texts.Get("stats.empty", settings.Locale)
	if len(rows) > 1 {
		body = bold(escapeHTML(h.Texts.Get("stats.event", settings.Locale)))
	}
	return h.respond(ctx, target, body, &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) personalStats(ctx context.Context, target replyTarget, settings chat.Settings, from User, eventID common.EventID) error {
	standings, err := h.Scoring.Leaderboard(ctx, settings.ChatID, scoring.ForEvent(eventID))
	if err != nil {
		return err
	}
	for _, s := range standings {
		if s.UserID.Value == from.ID {
			text := fmt.Sprintf("%s\n%s · %s · %s/%s",
				bold(escapeHTML(s.DisplayName)), code(fmt.Sprintf("#%d", s.Rank)), code(strconv.Itoa(s.Points)),
				code(strconv.Itoa(s.ExactPredictions)), code(strconv.Itoa(s.Predictions)))
			return h.respond(ctx, target, text, h.statsExit(settings.Locale))
		}
	}
	return h.respond(ctx, target, h.Texts.Get("stats.empty", settings.Locale), h.statsExit(settings.Locale))
}

// leaderboardRows renders each standing as "<medal> <rank>. <name> — <points>",
// with the rank/name column wrapped in <code> (Telegram's monospace font)
// and space-padded to a fixed width — the only way to get real column
// alignment in a chat message despite variable-width display names.
func leaderboardRows(standings []scoring.UserStanding) string {
	var b strings.Builder
	for i, s := range standings {
		name := truncate(s.DisplayName, 24)
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(medalFor(s.Rank) + " ")
		fmt.Fprintf(&b, "%s — %s", code(escapeHTML(fmt.Sprintf("%2d. %-24s", s.Rank, name))), bold(strconv.Itoa(s.Points)))
	}
	return b.String()
}

// statsExit gives the leaderboard screens a way onward. They are sent as
// new messages rather than edits (see routeCallback's doc comment), so
// without it the only route to another period was to re-open the menu
// from scratch — and a leaderboard reached from a match-result
// notification's button had no route anywhere at all.
func (h *UpdateHandler) statsExit(locale common.LocaleCode) *InlineKeyboard {
	return &InlineKeyboard{InlineKeyboard: [][]InlineButton{
		{button(h.Texts.Get("menu.stats", locale), "menu:stats")},
	}}
}

func (h *UpdateHandler) renderLeaderboard(ctx context.Context, target replyTarget, settings chat.Settings, period scoring.StatsPeriod) error {
	standings, err := h.Scoring.Leaderboard(ctx, settings.ChatID, period)
	if err != nil {
		return err
	}
	if len(standings) > 10 {
		standings = standings[:10]
	}

	body := h.Texts.Get("stats.empty", settings.Locale)
	if len(standings) > 0 {
		body = leaderboardRows(standings)
	}

	periodName, err := h.periodName(ctx, settings, period)
	if err != nil {
		return err
	}
	text := h.Texts.Get("stats.title", settings.Locale, escapeHTML(periodName)) + "\n\n" + body
	return h.respond(ctx, target, text, h.statsExit(settings.Locale))
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
