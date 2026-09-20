package enrichment

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// Nobody else's server should be serving our users.
//
// A team crest arrives from a ranking feed or a match provider as a URL
// pointing at their CDN. Handing that URL to every viewer means every
// screen anybody opens fetches images from a third party who never agreed
// to carry our traffic — their bandwidth, their logs, and every viewer's
// IP address disclosed to them. At this size it is not a load anyone would
// notice, which is exactly why it is worth fixing before it is.
//
// So the bot fetches each crest once and serves it from its own origin.

// TeamLogo is one cached crest.
type TeamLogo struct {
	TeamID      common.TeamID
	Source      Source
	SourceURL   string
	ContentType string
	Bytes       []byte
	// Digest is the content hash. It goes in the URL the app renders, so a
	// crest can be served immutable: a changed picture is a changed URL,
	// and nothing has to guess how long the old one stays good.
	Digest string
	// ETag and LastModified are the CDN's own validators, kept so the next
	// check can be conditional. Asking "has this changed?" and being told
	// no costs a header exchange; re-downloading to find out costs the
	// image.
	ETag         string
	LastModified string
	// IsLight says whether the mark itself is light or dark, so the app
	// can put the opposite chip behind it. Nil when the format could not
	// be measured — an SVG, say — and the app then falls back rather than
	// guessing, since a wrong chip is what this is here to avoid.
	IsLight   *bool
	FetchedAt time.Time
}

// TeamLogoNeed is one crest the mirror has not got, or has got from a
// different URL than the catalogue now carries.
type TeamLogoNeed struct {
	TeamID    common.TeamID
	Source    Source
	SourceURL string
	// ETag and LastModified are empty for a crest we have never fetched,
	// and carry the stored validators when this is a revalidation.
	ETag         string
	LastModified string
}

// TeamLogoCache stores the copies.
type TeamLogoCache interface {
	// PendingLogos lists what is missing or stale, newest teams first, at
	// most limit of them. Bounded because this feeds a job that makes one
	// outbound request per row and must stay predictable.
	PendingLogos(ctx context.Context, limit int) ([]TeamLogoNeed, error)
	// SaveLogo stores or replaces one crest.
	SaveLogo(ctx context.Context, logo TeamLogo) error
	// FindLogo reads one back for serving. A miss is (nil, nil): the app
	// falls back to initials, which is what it already does for a team
	// whose crest nobody published.
	FindLogo(ctx context.Context, teamID common.TeamID, source Source) (*TeamLogo, error)
	// LogoDigests returns the digest of every cached crest, so the API can
	// build immutable URLs without reading the bytes it is not serving.
	LogoDigests(ctx context.Context) (map[common.TeamID]map[Source]string, error)
	// LogoChips reports, per team and source, whether the mark is light.
	// Read alongside the digests so the teams endpoint can tell the app
	// which chip to draw without loading a single image.
	LogoChips(ctx context.Context) (map[common.TeamID]map[Source]bool, error)
	// StaleLogos lists crests worth re-checking: teams with a match about
	// to be played whose picture has not been checked since notBefore.
	//
	// Tied to a team taking the field rather than to a timer over the
	// whole catalogue, because that is when a crest is about to be looked
	// at by a lot of people at once, and because it spreads the checks out
	// by itself — a tournament's teams come up as their matches do,
	// instead of the whole table being swept on one schedule.
	StaleLogos(ctx context.Context, notBefore time.Time, within time.Duration, limit int) ([]TeamLogoNeed, error)
	// TouchLogo records that a crest was checked and had not changed, so
	// an unchanged answer is not mistaken for one never asked about.
	TouchLogo(ctx context.Context, teamID common.TeamID, source Source, at time.Time) error
	// UnmeasuredLogos returns stored crests nothing has measured yet, with
	// their bytes, so the measurement can be made from what is already
	// held rather than by fetching anything again.
	//
	// It exists because measuring happens when a crest is fetched, and a
	// crest is only fetched when it is missing or has moved. Every image
	// mirrored before the measurement existed would therefore never be
	// measured at all — the same shape of gap as the one migration 0049's
	// appearance backfill closed, and worth closing the same way: from
	// local bytes, with no request to anybody.
	UnmeasuredLogos(ctx context.Context, limit int) ([]TeamLogo, error)
	// SetLogoLightness records a measurement without touching the bytes.
	SetLogoLightness(ctx context.Context, teamID common.TeamID, source Source, light bool) error
}
