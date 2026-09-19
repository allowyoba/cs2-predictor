// Package pandascore implements competition.DataProvider against the
// PandaScore API.
package pandascore

import "time"

// Config holds the PandaScore client settings, loaded from env vars by
// internal/app's config loader (PANDASCORE_TOKEN, PANDASCORE_BASE_URL, PANDASCORE_PAGE_SIZE,
// PANDASCORE_EVENT_BATCH_SIZE, PANDASCORE_MAX_CONCURRENCY).
type Config struct {
	BaseURL        string
	Token          string
	PageSize       int
	EventBatchSize int
	// MaxConcurrency bounds how many event batches Matches fetches in
	// parallel — each batch is an independent HTTP call, so bounded
	// concurrency cuts wall-clock sync time for accounts tracking many
	// events without unbounded fan-out against PandaScore's rate limits.
	MaxConcurrency int
	// MatchWindowPast/MatchWindowFuture bound the matches asked for to the
	// ones that can still matter: a long tournament accumulates hundreds
	// of finished matches, and re-fetching all of them every few minutes
	// costs pages — which is to say requests — for rows that will never
	// change again. Past has to be generous enough to cover a bot that was
	// down for a while and still owes those matches their settlement.
	MatchWindowPast   time.Duration
	MatchWindowFuture time.Duration
}

func DefaultConfig() Config {
	return Config{
		BaseURL:        "https://api.pandascore.co",
		PageSize:       100,
		EventBatchSize: 50,
		MaxConcurrency: 4,
		// Two days back covers any realistic outage plus the reboot after
		// it; a month ahead covers every schedule a provider publishes
		// this far in advance.
		MatchWindowPast:   48 * time.Hour,
		MatchWindowFuture: 30 * 24 * time.Hour,
	}
}
