package competition

import (
	"context"
	"errors"

	"cs2predictor/internal/platform/common"
)

// ErrEventNotFound/ErrMatchNotFound mark a lookup that was expected to
// succeed (e.g. an event referenced by a match that must already exist)
// failing — callers can distinguish this from other persistence errors via
// errors.Is. Catalog.FindEvent/FindMatch themselves keep returning (nil,
// nil) for a plain "not found" on an optional lookup; these sentinels are
// for call sites where the referenced entity is a required invariant.
var (
	ErrEventNotFound = errors.New("event not found")
	ErrMatchNotFound = errors.New("match not found")
)

type TeamBalance struct {
	Wins   int
	Losses int
	Draws  int
}

type MatchFacts struct {
	FirstEventBalance  TeamBalance
	SecondEventBalance TeamBalance
}

// MatchFactsCatalog is an optional extension implemented by catalogs that can
// derive compact pre-match facts from already cached data. Telegram checks it
// with a type assertion so external/fake Catalog implementations need not
// implement it. MatchFacts takes the already-loaded Match (not just its id)
// so a caller that already fetched the match (as PollGateway.Send does) need
// not pay for a second identical lookup.
type MatchFactsCatalog interface {
	MatchFacts(ctx context.Context, match *Match) (MatchFacts, error)
}

// Catalog is the persistence port for events and matches.
type Catalog interface {
	// games restricts results to those GameCodes — a chat that hasn't
	// enabled any game (empty games) sees nothing, not everything.
	SearchEvents(ctx context.Context, query string, limit int, topTierOnly bool, games []GameCode) ([]Event, error)
	FindEvent(ctx context.Context, id common.EventID) (*Event, error)
	// FindEvents batch-fetches events by id in a single round trip — used to
	// avoid an N+1 query pattern when rendering a list backed by several
	// event ids (e.g. a chat's subscriptions). Missing ids are silently
	// omitted from the result rather than erroring.
	FindEvents(ctx context.Context, ids []common.EventID) ([]Event, error)
	FindMatch(ctx context.Context, id common.MatchID) (*Match, error)
	FindUnstartedMatches(ctx context.Context, eventID common.EventID) ([]Match, error)
	// FindUnstartedMatchesForEvents batch-fetches not-started matches across
	// several events in a single round trip, for the same N+1-avoidance
	// reason as FindEvents.
	FindUnstartedMatchesForEvents(ctx context.Context, eventIDs []common.EventID) ([]Match, error)
	FindMatches(ctx context.Context, eventID common.EventID) ([]Match, error)
	SaveEvent(ctx context.Context, event Event) (Event, error)
	SaveMatch(ctx context.Context, match Match) (Match, error)
}

// DataProvider is an external competition-data feed (PandaScore today).
// Implementations must resolve the supplied canonical Events back to their
// own provider-specific ids before fetching matches — Matches is not a pure
// pass-through by EventID.
type DataProvider interface {
	ProviderName() string
	UpcomingEvents(ctx context.Context) ([]Event, error)
	Matches(ctx context.Context, events []Event) ([]Match, error)
}
