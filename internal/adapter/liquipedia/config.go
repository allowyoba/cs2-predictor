// Package liquipedia implements enrichment.TournamentMetadataProvider
// against the Liquipedia Database (LPDB) v3 REST API
// (https://liquipedia.net/api) — richer tournament naming/context than
// PandaScore's own event name.
//
// LPDB v3 access is gated behind a free API key that is not self-service —
// it must be requested via https://liquipedia.net/api (free tier for
// educational/non-commercial/community use). The base URL, auth header,
// query parameters, and response envelope below ARE confirmed (drawn from
// the source of a real third-party LPDB v3 client, github.com/Dyl-M/
// liquipydia, since Liquipedia's own docs pages return Cloudflare 403s to
// automated fetches); the exact set of fields worth trusting for
// Region/Stage was not — this adapter only maps the fields it's confident
// about (FullName, Series) and leaves Region/Stage unset rather than guess
// at an unconfirmed shape.
package liquipedia

// Config points at the Liquipedia LPDB v3 API and carries the wiki to
// query and the API key.
type Config struct {
	BaseURL string
	Wiki    string
	APIKey  string
}

// DefaultConfig returns the real LPDB v3 endpoint scoped to the
// counterstrike wiki, with the given API key.
func DefaultConfig(apiKey string) Config {
	return Config{
		BaseURL: "https://api.liquipedia.net/api/v3/",
		Wiki:    "counterstrike",
		APIKey:  apiKey,
	}
}
