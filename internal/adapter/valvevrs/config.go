// Package valvevrs implements enrichment.RankingProvider against Valve's
// own public GitHub repository of CS2 Regional Standings snapshots
// (github.com/ValveSoftware/counter-strike_regional_standings). There is no
// JSON API: standings are published as dated Markdown pipe-tables under
// live/<year>/, so this package lists that directory via the GitHub
// Contents API to find the latest snapshot, then parses the table.
package valvevrs

const (
	defaultAPIBaseURL = "https://api.github.com"
	defaultRawBaseURL = "https://raw.githubusercontent.com"
	repoOwner         = "ValveSoftware"
	repoName          = "counter-strike_regional_standings"
)

// Config holds the (normally default, only overridden in tests) base URLs
// for the GitHub Contents API and raw file content.
type Config struct {
	APIBaseURL string
	RawBaseURL string
}

func DefaultConfig() Config {
	return Config{APIBaseURL: defaultAPIBaseURL, RawBaseURL: defaultRawBaseURL}
}
