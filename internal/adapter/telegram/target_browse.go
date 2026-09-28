package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// Browsing teams and players by button, as an alternative to typing a name.
//
// Search-by-reply was the only way in, and in a group it is a poor one: it
// costs a ForceReply nobody expects, it fails silently if the reply is sent
// to the wrong message, and it assumes the person already knows what the
// thing is called. A list with a page of buttons asks nothing and shows what
// is actually there — which is also the only way to discover a player whose
// handle you would never guess how to spell.
//
// Typing still works, and now finds far more (see competition.FoldForSearch);
// this is the path for when you would rather just look.

const targetBrowsePageSize = 8

// targetBrowseKind distinguishes the two lists without duplicating the whole
// screen for each.
type targetBrowseKind struct {
	kind subscription.TargetKind
	// prefix is this list's own callback namespace.
	prefix string
	// subscribePrefix is what tapping an entry leads to.
	subscribePrefix string
	titleKey        string
	emptyKey        string
	searchData      string
	searchLabelKey  string
}

var (
	browseTeams = targetBrowseKind{
		kind: subscription.TargetTeam, prefix: "targets:browse:", subscribePrefix: "targets:sub:",
		titleKey: "targets.browse_teams", emptyKey: "targets.browse_empty",
		searchData: "targets:search", searchLabelKey: "targets.search_by_name",
	}
	browsePlayers = targetBrowseKind{
		kind: subscription.TargetPlayer, prefix: "targets:pbrowse:", subscribePrefix: "targets:psub:",
		titleKey: "targets.browse_players", emptyKey: "targets.browse_players_empty",
		searchData: "targets:psearch", searchLabelKey: "targets.search_by_nickname",
	}
)

// browseEntry is one row of either list, flattened so the rendering below
// does not care which of the two it is showing.
type browseEntry struct {
	id       string
	label    string
	followed bool
}

// browseTargets renders one page of whichever list, with the already-followed
// entries marked rather than hidden: seeing what you follow in the same list
// you pick from is how you notice you already have it.
func (h *UpdateHandler) browseTargets(ctx context.Context, target replyTarget, settings chat.Settings,
	kind targetBrowseKind, page int) error {
	entries, err := h.browseEntries(ctx, settings, kind)
	if err != nil {
		return err
	}
	back := h.backButton(settings.Locale, "menu:targets")
	if len(entries) == 0 {
		kb := InlineKeyboard{InlineKeyboard: [][]InlineButton{
			{button(h.Texts.Get(kind.searchLabelKey, settings.Locale), kind.searchData)},
			{back},
		}}
		text := bold(escapeHTML(h.Texts.Get(kind.titleKey, settings.Locale))) + "\n\n" +
			h.Texts.Get(kind.emptyKey, settings.Locale)
		return h.respond(ctx, target, managedScreenContext(target, settings, text), &kb)
	}

	totalPages := (len(entries) + targetBrowsePageSize - 1) / targetBrowsePageSize
	page = clampPage(page, totalPages)
	start := page * targetBrowsePageSize
	end := min(start+targetBrowsePageSize, len(entries))

	rows := make([][]InlineButton, 0, targetBrowsePageSize+3)
	for _, entry := range entries[start:end] {
		label := truncate(entry.label, 44)
		if entry.followed {
			label = "✅ " + label
		}
		rows = append(rows, []InlineButton{button(label, kind.subscribePrefix+entry.id)})
	}
	if nav := paginationRow(page, totalPages, fmt.Sprintf("%d / %d", page+1, totalPages), func(p int) string {
		return kind.prefix + strconv.Itoa(p)
	}); nav != nil {
		rows = append(rows, nav)
	}
	// Typing stays one tap away rather than being the only way in: a long
	// list is faster to search than to page through.
	rows = append(rows,
		[]InlineButton{button(h.Texts.Get(kind.searchLabelKey, settings.Locale), kind.searchData)},
		[]InlineButton{back},
	)
	text := bold(escapeHTML(h.Texts.Get(kind.titleKey, settings.Locale))) + "\n\n" +
		h.Texts.Get("targets.browse_hint", settings.Locale)
	return h.respond(ctx, target, managedScreenContext(target, settings, text), &InlineKeyboard{InlineKeyboard: rows})
}

// browseEntries is the list itself: the teams or players this chat's games
// have actually seen play, most recent first, which is the order that puts
// the answer on the first page.
func (h *UpdateHandler) browseEntries(ctx context.Context, settings chat.Settings, kind targetBrowseKind) ([]browseEntry, error) {
	followed, err := h.followedTargetIDs(ctx, settings.ChatID, kind.kind)
	if err != nil {
		return nil, err
	}
	if kind.kind == subscription.TargetPlayer {
		if h.Rosters == nil {
			return nil, nil
		}
		players, err := h.Rosters.SearchPlayers(ctx, "", targetBrowseLimit, settings.EnabledGames)
		if err != nil {
			return nil, err
		}
		entries := make([]browseEntry, 0, len(players))
		for _, p := range players {
			entries = append(entries, browseEntry{
				id: compactUUIDString(p.ID.Value.String()), label: playerLabel(p),
				followed: followed[p.ID.Value.String()],
			})
		}
		return entries, nil
	}
	if h.Catalog == nil {
		return nil, nil
	}
	teams, err := h.teamsToBrowse(ctx, settings)
	if err != nil {
		return nil, err
	}
	entries := make([]browseEntry, 0, len(teams))
	for _, t := range teams {
		entries = append(entries, browseEntry{
			id: compactUUIDString(t.ID.Value.String()), label: t.Name,
			followed: followed[t.ID.Value.String()],
		})
	}
	return entries, nil
}

// targetBrowseLimit bounds the list a chat pages through. Well above what
// anybody will scroll, far below the whole catalogue.
const targetBrowseLimit = 120

// teamsToBrowse prefers the teams actually in play (TeamsForGame orders by
// most recently seen), falling back to the plain catalogue search for a
// catalog implementation that cannot answer that.
func (h *UpdateHandler) teamsToBrowse(ctx context.Context, settings chat.Settings) ([]competition.Team, error) {
	lister, ok := h.Catalog.(interface {
		TeamsForGame(ctx context.Context, game competition.GameCode, limit int) ([]competition.Team, error)
	})
	if !ok {
		return h.Catalog.SearchTeams(ctx, "", targetBrowseLimit, settings.EnabledGames)
	}
	var out []competition.Team
	seen := map[common.TeamID]bool{}
	for _, game := range settings.EnabledGames {
		teams, err := lister.TeamsForGame(ctx, game, targetBrowseLimit)
		if err != nil {
			return nil, err
		}
		for _, t := range teams {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// compactUUIDString trims the dashes so the id fits a callback alongside its
// prefix, matching what cbTargetSubscribe/cbPlayerSubscribe already produce.
func compactUUIDString(id string) string {
	return strings.ReplaceAll(id, "-", "")
}

func clampPage(page, totalPages int) int {
	if page < 0 {
		return 0
	}
	if page >= totalPages {
		return totalPages - 1
	}
	return page
}

func parseBrowsePage(data, prefix string) int {
	page, err := strconv.Atoi(strings.TrimPrefix(data, prefix))
	if err != nil || page < 0 {
		return 0
	}
	return page
}
