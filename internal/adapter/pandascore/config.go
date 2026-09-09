// Package pandascore implements competition.DataProvider against the
// PandaScore API.
package pandascore

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
}

func DefaultConfig() Config {
	return Config{
		BaseURL:        "https://api.pandascore.co",
		PageSize:       100,
		EventBatchSize: 50,
		MaxConcurrency: 4,
	}
}
