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
	// SearchTeams finds teams by a case-insensitive substring of their name,
	// restricted to games (same "no games enabled, no results" rule as
	// SearchEvents), for the team-follow subscribe flow's search-then-pick
	// step.
	SearchTeams(ctx context.Context, query string, limit int, games []GameCode) ([]Team, error)
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
	// FindPlayableMatchesForEvents is the same batch widened by one status:
	// matches already under way as well as ones still to come.
	//
	// A match does not stop existing when it kicks off. It is at its most
	// interesting between the poll closing and the result landing, and a
	// screen called "what's on" that drops it at exactly that moment is
	// answering a different question than the one being asked. Separate
	// from FindUnstartedMatchesForEvents because the poll pipeline must
	// keep its narrower meaning: a poll on a match already in play is a
	// poll nobody can answer.
	FindPlayableMatchesForEvents(ctx context.Context, eventIDs []common.EventID) ([]Match, error)
	FindMatches(ctx context.Context, eventID common.EventID) ([]Match, error)
	SaveEvent(ctx context.Context, event Event) (Event, error)
	SaveMatch(ctx context.Context, match Match) (Match, error)
}

// LiveMatchCatalog is an optional extension, checked with a type assertion
// like MatchFactsCatalog: which of these events have a match in play right
// now. It exists so the match sync can tell a tournament that is actually
// being played from one that merely has not finished yet — the difference
// between "poll this every few minutes" and "poll this twice an hour".
type LiveMatchCatalog interface {
	EventsWithLiveMatches(ctx context.Context, eventIDs []common.EventID) ([]common.EventID, error)
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
