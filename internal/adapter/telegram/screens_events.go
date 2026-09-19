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
	var rows [][]InlineButton
	for _, e := range found[start:end] {
		rows = append(rows, []InlineButton{button(eventLabel(e), cbSubscribe(e.ID))})
	}
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
	var rows [][]InlineButton
	for _, s := range subs {
		event, ok := byID[s.EventID]
		if !ok {
			continue
		}
		label := eventLabel(event)
		if _, isFinished := finishedIDs[s.EventID]; isFinished {
			label += " · " + h.Texts.Get("events.status_finished", settings.Locale)
		}
		rows = append(rows, []InlineButton{button(label, cbEventView(s.EventID))})
	}
	// Bulk removal is administration, so DM only — same rule as the
	// per-event unsubscribe button above.
	if dmContext && len(finished) > 0 {
		rows = append(rows, []InlineButton{button(h.Texts.Get("events.cleanup", settings.Locale, len(finished)), "events:cleanup")})
	}
	rows = append(rows, []InlineButton{h.backButton(settings.Locale, "menu:events")})
	body := h.Texts.Get("events.empty", settings.Locale)
	if len(rows) > 1 {
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

	body := h.Texts.Get("upcoming.empty", settings.Locale)
	if len(upcoming) > 0 {
		body = h.renderUpcomingGroups(upcoming, settings, h.Clock.Now().In(loc))
	}
	text := h.Texts.Get("upcoming.title", settings.Locale) + "\n\n" + body
	kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{{h.backButton(settings.Locale, "menu:main")}}}
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &kb)
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
	first, second := formatTeamCompact(item.match.FirstTeam), formatTeamCompact(item.match.SecondTeam)
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
