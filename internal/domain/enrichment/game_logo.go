package enrichment

import (
	"context"
	"time"

	"cs2predictor/internal/domain/competition"
)

// Each game's own logo, mirrored here.
//
// Same rule as the team crests: the bot fetches the picture once and
// serves it from its own origin, so opening the app never sends anybody's
// browser to a third party.

// GameLogoSources is where each game's official artwork comes from.
//
// The publisher's own CDN in both cases — Valve serves these for its own
// games — which is the only source worth calling official for a logo. The
// match provider does not publish one at all: its videogames endpoint
// carries an id, a name and a slug and no image.
//
// A game missing from this map simply has no logo, and the app falls back
// to its name, which is what it did before any of this existed.
var GameLogoSources = map[competition.GameCode]string{
	competition.GameCS2:   "https://cdn.cloudflare.steamstatic.com/steam/apps/730/logo.png",
	competition.GameDota2: "https://cdn.cloudflare.steamstatic.com/steam/apps/570/logo.png",
}

// GameLogo is one cached game logo.
type GameLogo struct {
	Game         competition.GameCode
	SourceURL    string
	ContentType  string
	Bytes        []byte
	Digest       string
	ETag         string
	LastModified string
	IsLight      *bool
	FetchedAt    time.Time
}

// GameLogoCache stores them.
type GameLogoCache interface {
	// PendingGameLogos lists the games whose logo is missing, or stored
	// from a URL that is no longer the one configured.
	PendingGameLogos(ctx context.Context) ([]GameLogoNeed, error)
	SaveGameLogo(ctx context.Context, logo GameLogo) error
	// FindGameLogo reads one back for serving; a miss is (nil, nil).
	FindGameLogo(ctx context.Context, game competition.GameCode) (*GameLogo, error)
	// GameLogoDigests returnsevery stored logo's digest, so the API can build
	// immutable URLs without reading bytes it will not serve.
	GameLogoDigests(ctx context.Context) (map[competition.GameCode]string, error)
}

// GameLogoNeed is one logo to fetch.
type GameLogoNeed struct {
	Game      competition.GameCode
	SourceURL string
}
