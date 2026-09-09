// Package grid implements enrichment.TeamStatsProvider and
// enrichment.MatchStatsProvider against GRID's Open Access API
// (https://grid.gg/open-access/) — recent team form and head-to-head,
// derived from the Central Data and Series State GraphQL APIs.
//
// GRID Open Access is gated behind a free API key that must be requested
// directly from GRID (grid.gg/open-access-application-form); the exact
// GraphQL schema (in particular, filter arguments for "series involving
// team X") could not be confirmed against the live API while building this
// adapter, since no key was available. The endpoint URLs, auth header, and
// request/response envelope below ARE confirmed (drawn from GRID's own
// public blog examples and a working third-party client); the specific
// allSeries/seriesState query shape used here follows that same confirmed
// pattern but does client-side name matching rather than a server-side
// team filter, since the filter argument names are unverified. Once a real
// key is available, this should be revisited: introspect the schema and
// switch to a proper server-side filter if one exists, which would both
// reduce request volume and remove the client-side name-matching step's
// false-match risk.
package grid

// Config points at GRID's Open Access endpoints and carries the API key.
type Config struct {
	CentralDataURL string
	SeriesStateURL string
	APIKey         string
}

// DefaultConfig returns the real GRID Open Access endpoints with the given
// API key.
func DefaultConfig(apiKey string) Config {
	return Config{
		CentralDataURL: "https://api-op.grid.gg/central-data/graphql",
		SeriesStateURL: "https://api-op.grid.gg/live-data-feed/series-state/graphql",
		APIKey:         apiKey,
	}
}
