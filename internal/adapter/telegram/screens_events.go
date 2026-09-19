package telegram

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// The tournament screens: the main menu, browsing the catalog, what this
// chat is subscribed to, and what is coming up.

// --- menu renderers ---
//
// Every renderer below takes a replyTarget rather than inferring where to
// reply from settings: a /command has nothing on screen yet (sendTarget),
// while a button tap edits the message that carried it in place
// (editTargetFromCallback) — see routeCallback's doc comment for why only
// some screens work this way.

// menu renders the top-level menu. In its normal group context (dmContext
// false) the Events/Settings admin surfaces are replaced by a single
// deep-link button handing them off to a DM (see dmDeepLink) — the group
// stays read-only/public (Stats/Upcoming/Rules); when reached from within a
// DM admin session (dmContext true, i.e. the caller is already managing
// this chat from its private chat), the real Events/Settings buttons are
// shown instead, since there's no "hand off to DM" left to do.
func (h *UpdateHandler) menu(ctx context.Context, target replyTarget, settings chat.Settings, dmContext bool) error {
	locale := settings.Locale
	var rows [][]InlineButton
	switch {
	case dmContext:
		rows = [][]InlineButton{
			{button(h.Texts.Get("menu.stats", locale), "menu:stats"), button(h.Texts.Get("menu.upcoming", locale), "menu:upcoming")},
			{button(h.Texts.Get("menu.events", locale), "menu:events"), button(h.Texts.Get("menu.settings", locale), "menu:settings")},
			{button(h.Texts.Get("menu.rules", locale), "menu:rules"), button(h.Texts.Get("menu.help", locale), "menu:help")},
			{button(h.Texts.Get("dm.change_group", locale), "manage:chats")},
		}
	default:
		rows = [][]InlineButton{
			{button(h.Texts.Get("menu.stats", locale), "menu:stats"), button(h.Texts.Get("menu.upcoming", locale), "menu:upcoming")},
			{button(h.Texts.Get("menu.rules", locale), "menu:rules"), button(h.Texts.Get("menu.help", locale), "menu:help")},
		}
		if link, ok := h.dmDeepLink(settings.ChatID); ok {
			rows = append(rows, []InlineButton{urlButton(h.Texts.Get("menu.manage_in_dm", locale), link)})
		} else {
			// BotUsername hasn't been resolved (shouldn't happen outside
			// tests/a getMe outage) — fall back to the direct in-group
			// entry points rather than hide admin actions entirely.
			rows = append(rows, []InlineButton{button(h.Texts.Get("menu.events", locale), "menu:events"), button(h.Texts.Get("menu.settings", locale), "menu:settings")})
		}
	}
	text := h.Texts.Get("menu.title", locale)
	if dmContext {
		text = "🎮 " + bold(escapeHTML(settings.Title)) + "\n" + h.Texts.Get("menu.manage_context", locale)
	}
	// A chat that has picked no game gets nothing at all — no tournaments
	// to find, no polls, no leaderboard — and nothing on this screen would
	// otherwise say why. The one step that unblocks everything else goes
	// first, and only until it is done.
	if len(settings.EnabledGames) == 0 {
		text += "\n\n" + h.Texts.Get("menu.setup_games", locale)
		if dmContext {
			rows = append([][]InlineButton{{button(h.Texts.Get("settings.games", locale), "settings:games")}}, rows...)
		}
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) readOnlyGroupMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	rows := [][]InlineButton{
		{button(h.Texts.Get("menu.stats", settings.Locale), "menu:stats"), button(h.Texts.Get("menu.upcoming", settings.Locale), "menu:upcoming")},
		{button(h.Texts.Get("menu.rules", settings.Locale), "menu:rules"), button(h.Texts.Get("menu.help", settings.Locale), "menu:help")},
		{h.backButton(settings.Locale, "pstats:menu")},
	}
	text := "🎮 " + bold(escapeHTML(settings.Title)) + "\n" + h.Texts.Get("menu.view_context", settings.Locale)
	return h.respond(ctx, target, text, &InlineKeyboard{InlineKeyboard: rows})
}

// emptySubscriptionsText explains why a chat is seeing nothing, in terms of
// the thing it can actually change: a chat with no games enabled is not
// "out of tournaments", it has not chosen a game yet, and no amount of
// searching will help until it does.
func (h *UpdateHandler) emptySubscriptionsText(settings chat.Settings, dmContext bool) string {
	if len(settings.EnabledGames) == 0 {
		if dmContext {
			return h.Texts.Get("events.empty_no_games", settings.Locale)
		}
		return h.Texts.Get("events.empty_no_games_group", settings.Locale)
	}
	if dmContext {
		return h.Texts.Get("events.empty", settings.Locale)
	}
	return h.Texts.Get("events.empty_group", settings.Locale)
}

func (h *UpdateHandler) eventMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	rows := [][]InlineButton{
		{button(h.Texts.Get("events.add", settings.Locale), "events:add"), button(h.Texts.Get("events.mine", settings.Locale), "events:mine")},
		{h.backButton(settings.Locale, "menu:main")},
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, bold(escapeHTML(h.Texts.Get("menu.events", settings.Locale)))), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) eventAddMenu(ctx context.Context, target replyTarget, settings chat.Settings) error {
	rows := [][]InlineButton{
		{button(h.Texts.Get("events.browse_top", settings.Locale), "events:browse:top:0")},
		{button(h.Texts.Get("events.browse_all", settings.Locale), "events:browse:all:0")},
		{button(h.Texts.Get("events.search", settings.Locale), "events:search")},
		{h.backButton(settings.Locale, "menu:events")},
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, h.Texts.Get("events.add_choose", settings.Locale)), &InlineKeyboard{InlineKeyboard: rows})
}

const eventBrowsePageSize = 8

func parseEventBrowseCallback(data string) (mode string, page int, err error) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 || parts[0] != "events" || parts[1] != "browse" || (parts[2] != "top" && parts[2] != "all") {
		return "", 0, newValidationError("invalid event browse callback")
	}
	page, err = strconv.Atoi(parts[3])
	if err != nil || page < 0 {
		return "", 0, newValidationError("invalid event browse page")
	}
	return parts[2], page, nil
}

func (h *UpdateHandler) renderEventBrowse(ctx context.Context, target replyTarget, settings chat.Settings, topTierOnly bool, page int) error {
	// The catalog is the cache: browsing never calls PandaScore directly.
	// Fetch one extra page so we know whether to render a Next button.
	want := (page+1)*eventBrowsePageSize + 1
	// Pull a broad cached page, then remove tournaments already subscribed in
	// this chat before paginating. This keeps browse pages dense and prevents
	// duplicate add buttons. Request want itself (not a fixed cap) so paging
	// deep enough no longer silently loses the "›" Next button just because
	// a fixed request size undershot how many rows this page needs.
	found, err := h.Catalog.SearchEvents(ctx, "", want, topTierOnly, settings.EnabledGames)
	if err != nil {
		return err
	}
	found, err = h.filterUnsubscribedEvents(ctx, settings.ChatID, found)
	if err != nil {
		return err
	}
	if len(found) > want {
		found = found[:want]
	}
	if len(found) == 0 {
		page = 0
	} else {
		maxPage := (len(found) - 1) / eventBrowsePageSize
		if page > maxPage {
			page = maxPage
		}
	}
	start := page * eventBrowsePageSize
	end := start + eventBrowsePageSize
	if end > len(found) {
		end = len(found)
	}
	rows := h.gameSectionRows(found[start:end], settings.Locale, func(e competition.Event) []InlineButton {
		return []InlineButton{button(eventLabel(e), cbSubscribe(e.ID))}
	})
	mode := "all"
	if topTierOnly {
		mode = "top"
	}
	maxPage := 0
	if len(found) > 0 {
		maxPage = (len(found) - 1) / eventBrowsePageSize
	}
	totalPages := maxPage + 1
	if nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return fmt.Sprintf("events:browse:%s:%d", mode, p)
	}); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "events:add")})

	title := h.Texts.Get("events.browse_all", settings.Locale)
	if topTierOnly {
		title = h.Texts.Get("events.browse_top", settings.Locale)
	}
	body := bold(escapeHTML(title))
	if start >= len(found) {
		body += "\n\n" + h.Texts.Get("events.none_active", settings.Locale)
	} else {
		body += "\n\n" + h.Texts.Get("events.choose_active", settings.Locale)
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, body), &InlineKeyboard{InlineKeyboard: rows})
}

// eventsByID batch-fetches events for the given subscriptions in a single
// query and returns them keyed by EventID — avoids one FindEvent call per
// subscription (N+1) in every menu that lists a chat's subscribed events.
func (h *UpdateHandler) eventsByID(ctx context.Context, subs []subscription.EventSubscription) (map[common.EventID]competition.Event, error) {
	ids := make([]common.EventID, len(subs))
	for i, s := range subs {
		ids[i] = s.EventID
	}
	events, err := h.Catalog.FindEvents(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[common.EventID]competition.Event, len(events))
	for _, e := range events {
		byID[e.ID] = e
	}
	return byID, nil
}

// distinctGames reports which games a set of tournaments spans.
func distinctGames(events []competition.Event) map[competition.GameCode]struct{} {
	out := map[competition.GameCode]struct{}{}
	for _, e := range events {
		out[e.Game] = struct{}{}
	}
	return out
}

// gameSectionRows lays a list of tournaments out under one header row per
// game, in competition.Games' own order, and returns the rows ready for a
// keyboard.
//
// A chat that follows only one game gets exactly what it always got: the
// headers appear only when there is something to separate. Mixing two
// games into one flat list is fine until a chat enables both and every
// screen becomes a scavenger hunt — an inline keyboard has no grouping of
// its own, so the header is a disabled ("noop") button, the same device
// the pagination counter already uses.
func (h *UpdateHandler) gameSectionRows(events []competition.Event, locale common.LocaleCode,
	row func(competition.Event) []InlineButton) [][]InlineButton {
	byGame := map[competition.GameCode][]competition.Event{}
	for _, e := range events {
		byGame[e.Game] = append(byGame[e.Game], e)
	}
	if len(byGame) <= 1 {
		out := make([][]InlineButton, 0, len(events))
		for _, e := range events {
			out = append(out, row(e))
		}
		return out
	}

	var out [][]InlineButton
	appendSection := func(code competition.GameCode, section []competition.Event) {
		if len(section) == 0 {
			return
		}
		out = append(out, []InlineButton{button("— "+h.Texts.Get(gameLabelKey(code), locale)+" —", "noop")})
		for _, e := range section {
			out = append(out, row(e))
		}
	}
	for _, code := range competition.Games {
		appendSection(code, byGame[code])
		delete(byGame, code)
	}
	// Anything the catalog reports under a game this build does not know
	// still has to be reachable, so it goes last rather than vanishing.
	for code, section := range byGame {
		appendSection(code, section)
	}
	return out
}

// gameSectionLines is gameSectionRows for a plain-text list: same rule
// (headings only when there is more than one game to separate), same
// ordering, rendered as italic headings instead of disabled buttons.
func (h *UpdateHandler) gameSectionLines(events []competition.Event, locale common.LocaleCode,
	line func(competition.Event) string) []string {
	byGame := map[competition.GameCode][]competition.Event{}
	for _, e := range events {
		byGame[e.Game] = append(byGame[e.Game], e)
	}
	if len(byGame) <= 1 {
		out := make([]string, 0, len(events))
		for _, e := range events {
			out = append(out, line(e))
		}
		return out
	}

	var out []string
	appendSection := func(code competition.GameCode, section []competition.Event) {
		if len(section) == 0 {
			return
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, italic(escapeHTML(h.Texts.Get(gameLabelKey(code), locale))))
		for _, e := range section {
			out = append(out, line(e))
		}
	}
	for _, code := range competition.Games {
		appendSection(code, byGame[code])
		delete(byGame, code)
	}
	for code, section := range byGame {
		appendSection(code, section)
	}
	return out
}

func eventLabel(e competition.Event) string {
	return e.Tier.Badge() + truncate(e.Name, 48)
}

func (h *UpdateHandler) filterUnsubscribedEvents(ctx context.Context, chatID common.ChatID, events []competition.Event) ([]competition.Event, error) {
	subs, err := h.Subscriptions.Subscriptions(ctx, chatID)
	if err != nil {
		return nil, err
	}
	added := make(map[common.EventID]struct{}, len(subs))
	for _, sub := range subs {
		added[sub.EventID] = struct{}{}
	}
	out := make([]competition.Event, 0, len(events))
	for _, event := range events {
		if _, exists := added[event.ID]; exists {
			continue
		}
		out = append(out, event)
	}
	return out, nil
}

// requestEventSearch prompts the clicking manager for a search term via a
// ForceReply, sent into cb.Message.Chat (the group or, today, exclusively
// the DM the button was tapped in) — not necessarily settings.ChatID, the
// chat whose subscriptions are being searched for. "selective" alone does
// nothing without one of the two things Telegram actually keys it on: the
// message being a reply to that user, or the message text containing a
// mention of them (see the Bot API docs on ForceReply.selective) — without
// either, EVERY member of a group chat gets their reply box forced open,
// not just the manager who tapped the button (harmless but redundant in a
// DM, where there's only the one other participant anyway). mentionHTML
// supplies that mention (as a tg://user?id= link, which works even for a
// user with no @username) so selective genuinely scopes to them.
func (h *UpdateHandler) requestEventSearch(ctx context.Context, cb *CallbackQuery, settings chat.Settings) error {
	text := h.Texts.Get("events.search_prompt", settings.Locale) + "\n" + mentionHTML(cb.From)
	payload := map[string]any{
		"chat_id":    cb.Message.Chat.ID,
		"text":       text,
		"parse_mode": "HTML",
		"reply_markup": map[string]any{
			"force_reply":             true,
			"selective":               true,
			"input_field_placeholder": "FISSURE",
		},
	}
	if cb.Message != nil && cb.Message.MessageThreadID != nil {
		payload["message_thread_id"] = *cb.Message.MessageThreadID
	}
	_, err := h.Client.Call(ctx, "sendMessage", payload)
	return err
}

// subscribedEvents is deliberately selection-only. Tournament-specific
// actions live on eventDetails, so a long subscription list stays one row
// per tournament instead of doubling in height.
func (h *UpdateHandler) subscribedEvents(ctx context.Context, target replyTarget, settings chat.Settings, dmContext bool) error {
	subs, err := h.Subscriptions.Subscriptions(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	byID, err := h.eventsByID(ctx, subs)
	if err != nil {
		return err
	}
	// Finished tournaments accumulate silently — they never produce another
	// poll, but they do push live ones off the bottom of this list. Sort
	// them last, and offer to clear them out in one go (see cleanupMenu).
	live, finished := splitByLiveness(subs, byID, h.Clock.Now())
	subs = append(live, finished...)
	finishedIDs := make(map[common.EventID]struct{}, len(finished))
	for _, sub := range finished {
		finishedIDs[sub.EventID] = struct{}{}
	}
	hidden := 0
	if len(subs) > subscribedEventsPageSize {
		hidden = len(subs) - subscribedEventsPageSize
		subs = subs[:subscribedEventsPageSize]
	}
	ordered := make([]competition.Event, 0, len(subs))
	for _, s := range subs {
		if event, ok := byID[s.EventID]; ok {
			ordered = append(ordered, event)
		}
	}
	rows := h.gameSectionRows(ordered, settings.Locale, func(event competition.Event) []InlineButton {
		label := eventLabel(event)
		if _, isFinished := finishedIDs[event.ID]; isFinished {
			label += " · " + h.Texts.Get("events.status_finished", settings.Locale)
		}
		return []InlineButton{button(label, cbEventView(event.ID))}
	})
	// Bulk removal is administration, so DM only — same rule as the
	// per-event unsubscribe button above.
	if dmContext && len(finished) > 0 {
		rows = append(rows, []InlineButton{button(h.Texts.Get("events.cleanup", settings.Locale, len(finished)), "events:cleanup")})
	}
	// An empty list is a dead end unless it says what to do next: in the
	// DM panel that is the tournament picker one row down, and in a group
	// the same handoff every other administrative action uses.
	hasEvents := len(rows) > 0
	if !hasEvents && dmContext {
		rows = append(rows, []InlineButton{button(h.Texts.Get("events.add", settings.Locale), "events:add")})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:events")})
	body := h.emptySubscriptionsText(settings, dmContext)
	if hasEvents {
		body = bold(escapeHTML(h.Texts.Get("events.mine", settings.Locale))) + "\n" +
			h.Texts.Get("events.mine_summary", settings.Locale, len(live), len(finished))
	}
	// The list is capped, and silently dropping the tail let a chat believe
	// it had fewer subscriptions than it does. Say so — and, in DM, the
	// cleanup button above is usually the thing that fixes it.
	if hidden > 0 {
		body += "\n\n" + italic(h.Texts.Get("events.mine_truncated", settings.Locale, hidden))
	}
	return h.respond(ctx, target, managedScreenContext(target, settings, body), &InlineKeyboard{InlineKeyboard: rows})
}

// eventDetails keeps the subscription list focused on choosing a tournament.
// Actions appear only after a tournament is selected, which avoids a second
// row of buttons for every item in long lists.
func (h *UpdateHandler) eventDetails(ctx context.Context, target replyTarget, settings chat.Settings, eventID common.EventID, dmContext bool) error {
	event, err := h.Catalog.FindEvent(ctx, eventID)
	if err != nil {
		return err
	}
	if event == nil {
		return h.subscribedEvents(ctx, target, settings, dmContext)
	}

	status := h.eventStatusText(event.Status, settings.Locale)
	lines := []string{bold(escapeHTML(event.Name)), h.Texts.Get("events.status", settings.Locale, status)}
	if tier := strings.ToUpper(strings.TrimSpace(string(event.Tier))); tier != "" && event.Tier != competition.TierUnranked {
		lines = append(lines, h.Texts.Get("events.tier", settings.Locale, event.Tier.Badge()+tier))
	}
	if line := h.liquipediaLine(ctx, eventID, settings.Locale); line != "" {
		lines = append(lines, line)
	}

	rows := [][]InlineButton{{button(h.Texts.Get("menu.stats", settings.Locale), cbStatsEvent(eventID))}}
	if dmContext {
		rows = append(rows, []InlineButton{button(h.Texts.Get("events.unsubscribe", settings.Locale), cbUnsubscribe(eventID))})
	} else if target.topicID != nil {
		rows = append(rows, []InlineButton{button(h.Texts.Get("events.topic", settings.Locale), cbEventTopic(eventID))})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "events:mine")})
	return h.respond(ctx, target, managedScreenContext(target, settings, strings.Join(lines, "\n")), &InlineKeyboard{InlineKeyboard: rows})
}

func (h *UpdateHandler) eventStatusText(status competition.EventStatus, locale common.LocaleCode) string {
	key := "events.status_unknown"
	switch status {
	case competition.EventRunning:
		key = "events.status_running"
	case competition.EventUpcoming:
		key = "events.status_upcoming"
	case competition.EventFinished:
		key = "events.status_finished"
	case competition.EventCancelled:
		key = "events.status_cancelled"
	}
	return h.Texts.Get(key, locale)
}

// liquipediaLine surfaces the region/series metadata Liquipedia already
// caches for this tournament (see internal/app.TournamentMetadataSync) —
// fetched and stored for a while but never actually shown anywhere until
// now. Best-effort like every other optional enrichment source: a nil
// repository, a lookup error, or simply no cached row yet all just omit
// the line rather than fail the whole card.
func (h *UpdateHandler) liquipediaLine(ctx context.Context, eventID common.EventID, locale common.LocaleCode) string {
	if h.TournamentMetadata == nil {
		return ""
	}
	meta, err := h.TournamentMetadata.FindTournamentMetadata(ctx, eventID, enrichment.SourceLiquipedia)
	if err != nil || meta == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if meta.Series != "" {
		parts = append(parts, escapeHTML(meta.Series))
	}
	if meta.Region != "" {
		parts = append(parts, escapeHTML(meta.Region))
	}
	if len(parts) == 0 {
		return ""
	}
	return h.Texts.Get("events.liquipedia", locale, strings.Join(parts, " · "))
}

// upcoming lists the soonest matches across everything this chat is
// subscribed to, grouped by tournament and then by local calendar day.
func (h *UpdateHandler) upcoming(ctx context.Context, target replyTarget, settings chat.Settings) error {
	subs, err := h.Subscriptions.Subscriptions(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	loc := chatZone(settings)

	eventIDs := make([]common.EventID, len(subs))
	for i, sub := range subs {
		eventIDs[i] = sub.EventID
	}
	matches, err := h.Catalog.FindUnstartedMatchesForEvents(ctx, eventIDs)
	if err != nil {
		return err
	}
	events, err := h.Catalog.FindEvents(ctx, eventIDs)
	if err != nil {
		return err
	}
	byEvent := make(map[common.EventID]competition.Event, len(events))
	for _, event := range events {
		byEvent[event.ID] = event
	}

	// With more than one game followed, a tournament name alone does not
	// say which game its matches belong to — and team names do not either.
	// The label is added only then: a single-game chat has nothing to
	// disambiguate and does not need the extra word.
	multiGame := len(distinctGames(events)) > 1

	upcoming := make([]upcomingMatch, 0, len(matches))
	for _, m := range matches {
		if m.ScheduledAt == nil {
			continue
		}
		// Two unresolved qualifier slots add no useful information to a nearest-
		// matches screen; they will appear automatically after the next cached
		// match refresh resolves at least one participant.
		if m.FirstTeam == nil && m.SecondTeam == nil {
			continue
		}
		eventName := h.Texts.Get("upcoming.default_tournament_name", settings.Locale)
		if event, ok := byEvent[m.EventID]; ok {
			eventName = event.Tier.Badge() + event.Name
			if multiGame {
				eventName += " · " + h.Texts.Get(gameShortLabelKey(event.Game), settings.Locale)
			}
		}
		upcoming = append(upcoming, upcomingMatch{
			eventName: eventName,
			when:      m.ScheduledAt.In(loc), match: m,
		})
	}
	sort.Slice(upcoming, func(i, j int) bool {
		if upcoming[i].when.Equal(upcoming[j].when) {
			return upcoming[i].match.ID.Value.String() < upcoming[j].match.ID.Value.String()
		}
		return upcoming[i].when.Before(upcoming[j].when)
	})
	if len(upcoming) > upcomingMatchLimit {
		upcoming = upcoming[:upcomingMatchLimit]
	}

	body, rows := h.upcomingBody(target, settings, upcoming, len(events), loc)
	text := h.Texts.Get("upcoming.title", settings.Locale) + "\n\n" + body
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:main")})
	kb := InlineKeyboard{InlineKeyboard: rows}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &kb)
}

// upcomingBody renders the schedule, or — when there is none — says which
// of its two very different causes applies. A chat that follows nothing yet
// can act on that; one whose tournaments have no published schedule is
// simply waiting, and offering it a button would be misleading.
func (h *UpdateHandler) upcomingBody(target replyTarget, settings chat.Settings, upcoming []upcomingMatch,
	followed int, loc *time.Location) (string, [][]InlineButton) {
	if len(upcoming) > 0 {
		return h.renderUpcomingGroups(upcoming, settings, h.Clock.Now().In(loc)), nil
	}
	if followed > 0 {
		return h.Texts.Get("upcoming.empty", settings.Locale), nil
	}
	// Same signal managedScreenContext reads: a screen rendered somewhere
	// other than the chat it is about is the DM panel, which is the only
	// place the administrative buttons belong.
	dmPanel := target.chatID != settings.ChatID
	body := h.emptySubscriptionsText(settings, dmPanel)
	if !dmPanel {
		return body, nil
	}
	return body, [][]InlineButton{{button(h.Texts.Get("events.add", settings.Locale), "events:add")}}
}

// upcomingMatchLimit bounds the screen. Grouping makes each match cost
// fewer characters, but Telegram's message limit is still the ceiling.
const upcomingMatchLimit = 10

type upcomingMatch struct {
	eventName string
	// when is already in the chat's timezone: every heading and time on
	// this screen is local, and converting once keeps day boundaries and
	// clock times from disagreeing.
	when  time.Time
	match competition.Match
}

// renderUpcomingGroups groups the next ten chronological matches by event
// identity, preserving the order of each event's earliest match.
func (h *UpdateHandler) renderUpcomingGroups(upcoming []upcomingMatch, settings chat.Settings, now time.Time) string {
	locale := settings.Locale
	var eventOrder []common.EventID
	byEvent := make(map[common.EventID][]upcomingMatch)
	for _, item := range upcoming {
		id := item.match.EventID
		if _, seen := byEvent[id]; !seen {
			eventOrder = append(eventOrder, id)
		}
		byEvent[id] = append(byEvent[id], item)
	}

	var blocks []string
	for _, id := range eventOrder {
		matches := byEvent[id]
		lines := []string{bold(escapeHTML(matches[0].eventName))}
		lastDay := ""
		for _, item := range matches {
			if day := dayKey(item.when); day != lastDay {
				lastDay = day
				lines = append(lines, "", italic(escapeHTML(h.dayHeading(item.when, locale, now))))
			}
			lines = append(lines, h.upcomingMatchLines(item, settings))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n")
}

// upcomingMatchLines renders one match under its day heading. Time and teams
// are the primary line; format and stage are secondary metadata; the
// broadcast link, labelled by platform, comes last — with its language
// spelled out only when it isn't the one the chat asked for.
func (h *UpdateHandler) upcomingMatchLines(item upcomingMatch, settings chat.Settings) string {
	locale := settings.Locale
	// The same flags the poll carries: this list and the poll are the two
	// places a team is named, and they should look like the same product.
	first := teamNameWithFlag(item.match.FirstTeam, formatTeamCompact(item.match.FirstTeam))
	second := teamNameWithFlag(item.match.SecondTeam, formatTeamCompact(item.match.SecondTeam))
	match := matchStatusIcon(item.match.Status) + " " + code(item.when.Format("15:04")) + " " + bold(escapeHTML(first)) + " — " + bold(escapeHTML(second))
	meta := item.match.Format.Label()
	if item.match.Stage != nil {
		if stage := strings.TrimSpace(*item.match.Stage); stage != "" {
			meta += " · " + stage
		}
	}
	lines := match + "\n  " + escapeHTML(meta)
	preferred := settings.StreamLocale()
	if stream, ok := item.match.StreamFor(preferred); ok {
		label := streamPlatformLabel(stream.URL, h.Texts.Get("upcoming.stream", locale))
		line := "📺 " + link(stream.URL, escapeHTML(label))
		if !strings.EqualFold(stream.Language, preferred.Language()) {
			line += " · " + strings.ToUpper(escapeHTML(stream.Language))
		}
		lines += "\n  " + line
	}
	return lines
}

func matchStatusIcon(status competition.MatchStatus) string {
	switch status {
	case competition.MatchRunning:
		return "🔴"
	case competition.MatchFinished, competition.MatchForfeit:
		return "✅"
	case competition.MatchCancelled:
		return "❌"
	case competition.MatchPostponed:
		return "⏸"
	default:
		return "🕒"
	}
}

// dayKey identifies a calendar day in whatever location t carries, for
// deciding when a new day heading is due.
func dayKey(t time.Time) string { return t.Format("2006-01-02") }

// dayHeading names a day the way someone reading a schedule would: the
// next two days by name, everything else by date and weekday, since "11
// сен, чт" is what tells you whether you will be around for it.
func (h *UpdateHandler) dayHeading(when time.Time, locale common.LocaleCode, now time.Time) string {
	switch dayKey(when) {
	case dayKey(now):
		return when.Format("02.01") + " · " + h.Texts.Get("upcoming.today", locale)
	case dayKey(now.AddDate(0, 0, 1)):
		return when.Format("02.01") + " · " + h.Texts.Get("upcoming.tomorrow", locale)
	}
	return fmt.Sprintf("%d %s, %s", when.Day(), shortMonthName(when.Month(), locale), shortWeekdayName(when.Weekday(), locale))
}

func formatTeamCompact(team *competition.Team) string {
	if team == nil {
		return "TBD"
	}
	return team.Name
}
