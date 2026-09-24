package telegram

import (
	"context"
	"fmt"
	"strings"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// eventBetsMaxRows bounds one tournament's worth of matches read for a
// single participant — generous enough for any real tournament length
// without pulling a whole career's history like UserBetsMaxRows does.
const eventBetsMaxRows = 200

// renderEventBets shows one participant's match-by-match results within one
// tournament — the same matchup/predicted/actual/points line personal_bets.go
// already renders, just scoped to an event and (unlike that DM-only screen)
// rendered directly in the group it's asked from. subjectName is shown in
// the title only when subjectID != viewer, so asking for your own results
// reads as "your results", not "<your own name>'s results".
func (h *UpdateHandler) renderEventBets(ctx context.Context, target replyTarget, settings chat.Settings, eventID common.EventID, subjectID common.UserID, subjectName string, viewer common.UserID, backData string) error {
	repo, err := h.personalBets()
	if err != nil {
		return err
	}
	bets, err := repo.UserBetsForEvent(ctx, subjectID, settings.ChatID, eventID, eventBetsMaxRows)
	if err != nil {
		return err
	}

	title := h.Texts.Get("evbets.title_mine", settings.Locale)
	if subjectID != viewer {
		title = h.Texts.Get("evbets.title_other", settings.Locale, escapeHTML(truncate(subjectName, 32)))
	}

	back := h.backButton(settings.Locale, backData)
	if len(bets) == 0 {
		kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{back}}}
		return h.respond(ctx, target, managedScreenContext(target, settings, bold(title)+"\n\n"+h.Texts.Get("evbets.empty", settings.Locale)), &kb)
	}

	var b strings.Builder
	b.WriteString(bold(title))
	for _, bet := range bets {
		b.WriteString("\n\n" + h.betLine(bet, settings.Locale, true, chatZone(settings)))
	}
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{back}}}
	return h.respond(ctx, target, managedScreenContext(target, settings, b.String()), &kb)
}

// eventParticipantPickerPageSize keeps the picker on one screen for all but
// the largest chats — a name-only button list, so more fit per page than
// the full leaderboard rows do.
const eventParticipantPickerPageSize = 20

// renderEventParticipantPicker lists everyone with a settled prediction in
// eventID as a button, so any of them (not just the viewer) can be picked to
// see their match-by-match results — the "table for a chosen participant"
// half of the feature, as opposed to renderEventBets's "my own" half.
func (h *UpdateHandler) renderEventParticipantPicker(ctx context.Context, target replyTarget, settings chat.Settings, eventID common.EventID, page int, backData string) error {
	standings, err := h.leaderboard(ctx, settings.ChatID, scoring.ForEvent(eventID))
	if err != nil {
		return err
	}
	if len(standings) == 0 {
		kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, backData)}}}
		return h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("stats.empty", settings.Locale)), &kb)
	}

	maxPage := (len(standings) - 1) / eventParticipantPickerPageSize
	if page > maxPage {
		page = maxPage
	}
	if page < 0 {
		page = 0
	}
	start := page * eventParticipantPickerPageSize
	end := min(start+eventParticipantPickerPageSize, len(standings))

	var rows [][]InlineButton
	for _, s := range standings[start:end] {
		label := truncate(s.DisplayName, 28)
		rows = append(rows, []InlineButton{button(label, eventParticipantCallback(eventID, s.UserID))})
	}
	totalPages := maxPage + 1
	if nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return eventParticipantPickerCallback(eventID, p)
	}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, backData)})

	text := bold(h.Texts.Get("evbets.pick_title", settings.Locale))
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

func eventBetsMineCallback(eventID common.EventID) string {
	return "stats:evbets:me:" + eventID.Value.String()
}

func eventParticipantPickerCallback(eventID common.EventID, page int) string {
	return fmt.Sprintf("stats:evpick:%s:%d", eventID.Value.String(), page)
}

func eventParticipantCallback(eventID common.EventID, userID common.UserID) string {
	return fmt.Sprintf("stats:evbets:u:%s:%d", eventID.Value.String(), userID.Value)
}
