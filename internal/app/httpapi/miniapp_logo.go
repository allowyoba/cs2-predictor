package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
	"github.com/google/uuid"
)

// Crests are served from here, from bytes this bot fetched once, so no
// viewer's browser ever talks to HLTV or PandaScore — see
// enrichment.TeamLogoCache.

// MiniAppLogos is the read behind the image endpoint.
type MiniAppLogos interface {
	FindLogo(ctx context.Context, teamID common.TeamID, source enrichment.Source) (*enrichment.TeamLogo, error)
	LogoDigests(ctx context.Context) (map[common.TeamID]map[enrichment.Source]string, error)
	LogoChips(ctx context.Context) (map[common.TeamID]map[enrichment.Source]bool, error)
}

// logoHandler serves GET /api/miniapp/v1/teams/{id}/logo?src=<hltv|provider>.
//
// Public, like the teams list it belongs to: a crest is a picture of a
// logo, not somebody's data, and putting it behind a signed launch would
// mean the page could not use a plain <img> tag.
func logoHandler(logos MiniAppLogos) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if logos == nil {
			http.Error(w, "crest cache is not configured", http.StatusServiceUnavailable)
			return
		}
		raw := r.PathValue("id")
		parsed, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, "invalid team id", http.StatusBadRequest)
			return
		}
		source := enrichment.Source("PANDASCORE")
		if strings.EqualFold(r.URL.Query().Get("src"), "hltv") {
			source = enrichment.SourceHLTV
		}

		logo, err := logos.FindLogo(r.Context(), common.TeamID{Value: parsed}, source)
		if err != nil {
			http.Error(w, "crest lookup failed", http.StatusInternalServerError)
			return
		}
		if logo == nil {
			// The app draws initials for a team with no crest, which is
			// already its behaviour for teams nobody published one for.
			http.NotFound(w, r)
			return
		}

		// The URL carries the content hash, so these bytes can never be
		// the wrong answer for this URL: a new crest is a new URL. That is
		// what makes a year of immutable caching safe, and it is what
		// keeps a chat opening the app at once from re-fetching anything.
		w.Header().Set("Content-Type", logo.ContentType)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("ETag", `"`+logo.Digest+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Somebody else's artwork rendered from our origin: no referrer
		// leaves with the request, and it is never framed.
		w.Header().Set("Referrer-Policy", "no-referrer")
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, logo.Digest) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(logo.Bytes)
	})
}

// logoURL is the address the app renders for one team's crest, or "" when
// nothing has been mirrored yet. The digest is a query parameter rather
// than part of the path so the route stays one pattern.
func logoURL(teamID common.TeamID, source enrichment.Source, digest string) string {
	if digest == "" {
		return ""
	}
	src := "provider"
	if source == enrichment.SourceHLTV {
		src = "hltv"
	}
	return "/api/miniapp/v1/teams/" + teamID.Value.String() + "/logo?src=" + src + "&v=" + digest
}
