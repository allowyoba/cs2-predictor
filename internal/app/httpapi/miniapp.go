package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// The Mini App's first endpoint: the teams of one game, with their crests.
//
// The Mini App renders boards of teams, and a board of names is far slower
// to read than a board of crests. Everything it needs is already in this
// bot's own catalogue — collected as a by-product of the match sync, plus
// whatever HLTV's ranking published — so this is a read of data that is
// already there rather than a new integration.
//
// Deliberately not user-scoped: a team's name and crest are the same for
// everybody, which is what lets this response be cached at the edge and
// skip Telegram's initData handshake entirely. The endpoints that are
// about a person — dashboard, history, coach — will need that handshake,
// and it does not belong on this one.

// TeamCatalog is the read this endpoint needs, and nothing more.
type TeamCatalog interface {
	// TeamsForGame lists that game's teams, most recently seen in a match
	// first, capped by limit.
	TeamsForGame(ctx context.Context, game competition.GameCode, limit int) ([]competition.Team, error)
}

// miniappTeam is one row as the Mini App consumes it: both crests, and the
// one the caller should use by default.
type miniappTeam struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location,omitempty"`
	// Logo is the crest to render; LogoProvider and LogoHLTV are the raw
	// sources behind it, so a client can offer the same switch the chat
	// settings do without a second request.
	Logo         string `json:"logo,omitempty"`
	LogoProvider string `json:"logo_provider,omitempty"`
	LogoHLTV     string `json:"logo_hltv,omitempty"`
	// Chip is the background to draw behind this crest: "dark" for a light
	// mark, "light" for a dark one, empty when it could not be measured.
	// No single colour shows a white wordmark and a black one equally
	// well, so the answer is per team rather than per app.
	Chip string `json:"chip,omitempty"`
}

// teamsResponse is the endpoint's envelope. Counts are included because a
// client showing "6 of 30" needs them and cannot derive them from a capped
// list.
type teamsResponse struct {
	Game    string        `json:"game"`
	Count   int           `json:"count"`
	Teams   []miniappTeam `json:"teams"`
	LogoSrc string        `json:"logo_source"`
}

// miniappTeamsLimit bounds one response. The Mini App paints a grid, not a
// directory: past a hundred crests nobody is reading, and an unbounded
// list would let one request pull the whole catalogue.
const miniappTeamsLimit = 100

// teamsHandler serves GET /api/miniapp/v1/teams?game=<code>&logos=hltv.
// teamRow renders one team: which crest to draw, both sources behind it,
// and which chip belongs behind the chosen one.
func teamRow(t competition.Team, preferHLTV bool, digests map[enrichment.Source]string, chips map[enrichment.Source]bool) miniappTeam {
	provider := logoURL(t.ID, "PANDASCORE", digests["PANDASCORE"])
	hltv := logoURL(t.ID, enrichment.SourceHLTV, digests[enrichment.SourceHLTV])

	chosen, source := provider, enrichment.Source("PANDASCORE")
	if preferHLTV && hltv != "" {
		chosen, source = hltv, enrichment.SourceHLTV
	} else if chosen == "" && hltv != "" {
		chosen, source = hltv, enrichment.SourceHLTV
	}

	// "dark" means a dark chip behind a light mark. A crest nothing could
	// measure gets no answer at all rather than a guessed one.
	chip := ""
	if light, measured := chips[source]; measured && chosen != "" {
		chip = ternaryString(light, "dark", "light")
	}
	return miniappTeam{
		ID: t.ID.Value.String(), Name: t.Name, Location: t.LocationFor(preferHLTV),
		Logo: chosen, LogoProvider: provider, LogoHLTV: hltv, Chip: chip,
	}
}

// ternaryString keeps a two-way choice on one line where writing it out
// would be four lines saying less.
func ternaryString(cond bool, ifTrue, ifFalse string) string {
	if cond {
		return ifTrue
	}
	return ifFalse
}

func teamsHandler(catalog TeamCatalog, logos MiniAppLogos) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if catalog == nil {
			http.Error(w, "team catalog is not configured", http.StatusServiceUnavailable)
			return
		}
		game, ok := parseGame(r.URL.Query().Get("game"))
		if !ok {
			http.Error(w, "unknown game", http.StatusBadRequest)
			return
		}
		preferHLTV := r.URL.Query().Get("logos") == "hltv"
		limit := miniappTeamsLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = min(parsed, miniappTeamsLimit)
		}

		teams, err := catalog.TeamsForGame(r.Context(), game, limit)
		if err != nil {
			http.Error(w, "team lookup failed", http.StatusInternalServerError)
			return
		}
		body := teamsResponse{Game: string(game), Count: len(teams), LogoSrc: "provider", Teams: make([]miniappTeam, 0, len(teams))}
		if preferHLTV {
			body.LogoSrc = "hltv"
		}
		// Every crest the app renders points back here, never at the CDN
		// it came from: the bot fetched these once so that nobody's
		// browser has to — see enrichment.TeamLogoCache.
		digests := map[common.TeamID]map[enrichment.Source]string{}
		// chips say whether each mark is light, so the app can put the
		// opposite background behind it — there is no one colour that
		// shows a white wordmark and a black one equally well.
		chips := map[common.TeamID]map[enrichment.Source]bool{}
		if logos != nil {
			if found, err := logos.LogoDigests(r.Context()); err == nil {
				digests = found
			}
			if found, err := logos.LogoChips(r.Context()); err == nil {
				chips = found
			}
		}
		for _, t := range teams {
			body.Teams = append(body.Teams, teamRow(t, preferHLTV, digests[t.ID], chips[t.ID]))
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		// Crests change about as often as a team rebrands, so a few
		// minutes of caching costs nothing and keeps a Mini App opened by
		// a whole chat at once off the database.
		w.Header().Set("Cache-Control", "public, max-age=300")
		_ = json.NewEncoder(w).Encode(body)
	})
}

// parseGame accepts the game codes this bot actually follows, case
// -insensitively, and refuses anything else rather than returning an empty
// list that would read as "this game has no teams".
func parseGame(raw string) (competition.GameCode, bool) {
	for _, game := range competition.Games {
		if strings.EqualFold(string(game), raw) {
			return game, true
		}
	}
	return "", false
}
