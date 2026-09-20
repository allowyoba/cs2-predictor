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
		if logos != nil {
			if found, err := logos.LogoDigests(r.Context()); err == nil {
				digests = found
			}
		}
		for _, t := range teams {
			provider := logoURL(t.ID, "PANDASCORE", digests[t.ID]["PANDASCORE"])
			hltv := logoURL(t.ID, enrichment.SourceHLTV, digests[t.ID][enrichment.SourceHLTV])
			chosen := provider
			if preferHLTV && hltv != "" {
				chosen = hltv
			} else if chosen == "" {
				chosen = hltv
			}
			body.Teams = append(body.Teams, miniappTeam{
				ID: t.ID.Value.String(), Name: t.Name, Location: t.LocationFor(preferHLTV),
				Logo: chosen, LogoProvider: provider, LogoHLTV: hltv,
			})
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
