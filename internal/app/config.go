package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"cs2predictor/internal/domain/feedback"
)

// Config is the full application configuration, loaded from environment
// variables documented in the project README / .env.example.
type Config struct {
	Port string

	DatabaseURL      string
	DatabaseUser     string
	DatabasePassword string
	DatabasePoolSize int

	Telegram   TelegramEnvConfig
	PandaScore PandaScoreEnvConfig
	Providers  ProviderRoutingConfig
	HTTP       HTTPClientConfig

	SyncEventsDelay  time.Duration
	SyncMatchesDelay time.Duration
	// SyncMatchesColdInterval is how often the match sync asks about every
	// subscribed tournament instead of only the ones with a match near or
	// in play — see CompetitionSynchronization.selectEventsToSync. Zero
	// fetches everything on every run, which is what this used to do.
	SyncMatchesColdInterval time.Duration
	SyncPollCloseDelay      time.Duration
	DigestCheckDelay        time.Duration

	// PollReminderLead is how far ahead of a poll closing the opt-in
	// private reminder goes out; zero disables the job entirely.
	// PollReminderParticipantWindow bounds how recently someone must have
	// voted in a chat to count as playing there.
	PollReminderLead              time.Duration
	PollReminderCheckDelay        time.Duration
	PollReminderParticipantWindow time.Duration
	OutboxDelay                   time.Duration
	OutboxBatchSize               int
	SchedulingEnabled             bool
	// JobTimeout bounds each individual run of a scheduled job (sync,
	// poll-close, outbox dispatch): without it, a single stuck run (a hung
	// DB query, a provider that stops responding mid-read) could hold its
	// goroutine — and whatever pool connection it's using — indefinitely,
	// since the fixed-delay scheduler otherwise passes the job the
	// long-lived process context with no deadline of its own.
	JobTimeout time.Duration

	WebhookRateLimit WebhookRateLimitConfig

	Retention RetentionConfig

	LogLevel string

	Enrichment EnrichmentConfig

	// EventEveLead is how far ahead of a tournament's first match the
	// day-before nudge goes out (EVENT_EVE_LEAD). Zero uses
	// app.DefaultEventEveLead; negative turns the nudge off entirely,
	// which is the documented way to opt out of it.
	EventEveLead time.Duration
	// MiniAppURL is the HTTPS address the Mini App is served from, as
	// Telegram will open it. Empty hides the button that opens it: a
	// deployment that does not host the page must not offer one.
	MiniAppURL string
	// HostLimits are the machine-resource levels that raise an alert. See
	// DefaultHostLimits for why they fire with room to spare.
	HostLimits HostLimits
	// WatchdogInterval paces the two health watchers (the Telegram webhook
	// and the outbox's dead letters). Both are cheap — one API call and
	// one grouped count — and both cover failures that are otherwise
	// invisible, so the default is frequent enough to notice within
	// minutes rather than hours.
	WatchdogInterval time.Duration
	// Feedback bounds the Ideas channel — the one surface anybody on the
	// internet can write into. See feedback.Policy.
	Feedback feedback.Policy
	// DeliveryBacklogAlert is how many updates Telegram may be holding
	// before that alone counts as broken delivery. A working webhook
	// drains its queue in seconds.
	DeliveryBacklogAlert int

	// TeamMatchOperatorChatIDs receive a ping when a new team-identity
	// review request needs attention, and are the only chat ids allowed to
	// use /team_matches — reuses DEPLOY_NOTIFY_CHAT_IDS (already set for
	// deploy-failure notifications, see ansible/roles/notify_admins)
	// rather than introducing a second admin-contact list.
	TeamMatchOperatorChatIDs []int64
}

// EnrichmentConfig controls the optional team/match data enrichment sources
// layered on top of PandaScore (which remains the sole source of truth for
// events/matches/schedule/results). GRID and Liquipedia are both gated
// behind an API key that must be requested from each provider directly (see
// grid.gg/open-access and liquipedia.net/api) — the adapters here are
// unverified against the live APIs until one is actually configured.
type EnrichmentConfig struct {
	ValveVRSEnabled      bool
	ValveVRSSyncInterval time.Duration

	GRIDEnabled      bool
	GRIDAPIKey       string
	GRIDSyncInterval time.Duration

	LiquipediaEnabled      bool
	LiquipediaAPIKey       string
	LiquipediaSyncInterval time.Duration

	// HLTVEnabled fetches, via the same public Apify actor (see
	// internal/adapter/apifyhltv), BOTH HLTV.org's own weekly world ranking
	// AND Valve's official ranking as mirrored on hltv.org — a paired
	// weekly fetch, gated behind an Apify API token like GRID/Liquipedia
	// are behind their own keys, so it defaults to disabled rather than
	// silently running unconfigured. The Valve/VRS half feeds the exact
	// same VALVE_VRS ranking ValveVRSEnabled's free GitHub-based feed
	// does — that feed keeps running independently as the fallback for
	// whichever week this one didn't fire (see ApifyRankingGate).
	HLTVEnabled  bool
	HLTVAPIToken string
	// ApifyRankingCheckInterval is how often the *gate* is checked, not
	// how often a fetch actually happens — actual cadence is capped to
	// once a week (Monday, end of day) by ApifyRankingGate regardless of
	// how often this ticks; this only bounds how promptly that weekly
	// window is noticed.
	ApifyRankingCheckInterval time.Duration
	// ApifyMaxTeams bounds both relevance and Apify's pay-per-result cost
	// for both the HLTV and Valve/VRS fetches — see
	// apifyhltv.defaultMaxTeams's doc comment.
	ApifyMaxTeams int
}

// RetentionConfig bounds how long the traffic-driven tables keep rows (see
// RetentionSweep). A zero duration disables the TTL sweep for that table,
// which is the escape hatch if an operator wants to keep everything; a zero
// MaxRows likewise disables the row-count cap for the two tables that have
// one.
type RetentionConfig struct {
	Interval            time.Duration
	ProcessedUpdatesTTL time.Duration
	PublishedOutboxTTL  time.Duration
	ResolvedRequestsTTL time.Duration
	AdminActionsTTL     time.Duration

	// ProcessedUpdatesMaxRows/PublishedOutboxMaxRows are a hard row-count
	// backstop under the TTL above — see the doc comment where these are
	// parsed in LoadConfig for why a time window alone isn't enough at scale.
	ProcessedUpdatesMaxRows int
	PublishedOutboxMaxRows  int
}

// WebhookRateLimitConfig bounds the rate of accepted /telegram/webhook
// requests (env vars WEBHOOK_RATE_LIMIT_*) — defense in depth beyond the
// webhook secret token check, in case that token ever leaks.
type WebhookRateLimitConfig struct {
	RequestsPerSecond float64
	Burst             int
}

type TelegramEnvConfig struct {
	Token         string
	WebhookSecret string
	BaseURL       string
}

type PandaScoreEnvConfig struct {
	Token          string
	BaseURL        string
	PageSize       int
	EventBatchSize int
	MaxConcurrency int
	// MatchWindowPast/MatchWindowFuture bound which matches are asked for
	// at all — see pandascore.Config for why.
	MatchWindowPast   time.Duration
	MatchWindowFuture time.Duration
}

// HTTPClientConfig mirrors app.http.* — connect/read/response timeouts
// shared by every outbound HTTP client (PandaScore, Telegram).
type HTTPClientConfig struct {
	ConnectTimeout  time.Duration
	ReadTimeout     time.Duration
	ResponseTimeout time.Duration
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q: %w", key, v, err)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", key, v, err)
	}
	return d, nil
}

// envDurationMillis reads a plain-integer-milliseconds env var, the
// SYNC_*_DELAY_MS / OUTBOX_DELAY_MS convention.
func envDurationMillis(key string, defMillis int64) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return time.Duration(defMillis) * time.Millisecond, nil
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer milliseconds %q: %w", key, v, err)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// envInt64List parses a comma-separated list of integers (blank entries and
// surrounding whitespace ignored), or nil if the variable is unset/empty —
// the same convention ansible/roles/notify_admins already applies to this
// exact variable on the deploy side.
func envInt64List(key string) ([]int64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid integer %q: %w", key, part, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid float %q: %w", key, v, err)
	}
	return f, nil
}

// LoadConfig reads Config from the environment. Required secrets
// (TELEGRAM_BOT_TOKEN, TELEGRAM_WEBHOOK_SECRET >= 16 chars, PANDASCORE_TOKEN)
// fail fast at startup rather than at first use.
//
//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func LoadConfig() (Config, error) {
	var cfg Config
	cfg.Port = envString("PORT", "8080")

	cfg.DatabaseURL = envString("DATABASE_URL", "postgresql://localhost:5432/cs2predictor")
	cfg.DatabaseUser = envString("DATABASE_USER", "cs2predictor")
	cfg.DatabasePassword = envString("DATABASE_PASSWORD", "cs2predictor")
	// Above the number of scheduled jobs on purpose: each one holds a
	// cluster lock — and with it a pooled connection — for as long as it
	// runs, including while it waits on a provider's API. Sized below the
	// job count, one slow provider starves every other job into its own
	// timeout, which is exactly what a production deploy demonstrated.
	poolSize, err := envInt("DATABASE_POOL_SIZE", 25)
	if err != nil {
		return Config{}, err
	}
	cfg.DatabasePoolSize = poolSize

	cfg.Telegram = TelegramEnvConfig{
		Token:         os.Getenv("TELEGRAM_BOT_TOKEN"),
		WebhookSecret: os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		BaseURL:       envString("TELEGRAM_BASE_URL", "https://api.telegram.org"),
	}
	if strings.TrimSpace(cfg.Telegram.Token) == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if len(cfg.Telegram.WebhookSecret) < 16 {
		return Config{}, fmt.Errorf("TELEGRAM_WEBHOOK_SECRET is required and must be at least 16 characters")
	}

	pageSize, err := envInt("PANDASCORE_PAGE_SIZE", 100)
	if err != nil {
		return Config{}, err
	}
	eventBatchSize, err := envInt("PANDASCORE_EVENT_BATCH_SIZE", 50)
	if err != nil {
		return Config{}, err
	}
	maxConcurrency, err := envInt("PANDASCORE_MAX_CONCURRENCY", 4)
	if err != nil {
		return Config{}, err
	}
	matchWindowPast, err := envDuration("PANDASCORE_MATCH_WINDOW_PAST", 48*time.Hour)
	if err != nil {
		return Config{}, err
	}
	matchWindowFuture, err := envDuration("PANDASCORE_MATCH_WINDOW_FUTURE", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cfg.PandaScore = PandaScoreEnvConfig{
		Token:           os.Getenv("PANDASCORE_TOKEN"),
		BaseURL:         envString("PANDASCORE_BASE_URL", "https://api.pandascore.co"),
		PageSize:        pageSize,
		EventBatchSize:  eventBatchSize,
		MaxConcurrency:  maxConcurrency,
		MatchWindowPast: matchWindowPast, MatchWindowFuture: matchWindowFuture,
	}
	if strings.TrimSpace(cfg.PandaScore.Token) == "" {
		return Config{}, fmt.Errorf("PANDASCORE_TOKEN is required")
	}

	order := strings.Split(envString("COMPETITION_PROVIDERS_ORDER", "PANDASCORE"), ",")
	for i := range order {
		order[i] = strings.TrimSpace(order[i])
	}

	operatorIDs, err := envInt64List("DEPLOY_NOTIFY_CHAT_IDS")
	if err != nil {
		return Config{}, err
	}
	cfg.TeamMatchOperatorChatIDs = operatorIDs
	startupGrace, err := envDuration("COMPETITION_PROVIDERS_HEALTH_STARTUP_GRACE", 2*time.Minute)
	if err != nil {
		return Config{}, err
	}
	maxStaleness, err := envDuration("COMPETITION_PROVIDERS_HEALTH_MAX_STALENESS", 2*time.Hour)
	if err != nil {
		return Config{}, err
	}
	circuitThreshold, err := envInt("COMPETITION_PROVIDERS_CIRCUIT_BREAKER_THRESHOLD", 3)
	if err != nil {
		return Config{}, err
	}
	circuitBaseDelay, err := envDuration("COMPETITION_PROVIDERS_CIRCUIT_BREAKER_BASE_DELAY", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	circuitMaxDelay, err := envDuration("COMPETITION_PROVIDERS_CIRCUIT_BREAKER_MAX_DELAY", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	// A non-positive delay here wouldn't crash anything, but it would
	// silently defeat the breaker: recordFailure treats a <=0 computed
	// delay as "use CircuitBreakerMaxDelay instead", so a <=0 MaxDelay
	// makes every opened circuit expire immediately (now+0), i.e. never
	// actually short-circuit a call — failing fast here surfaces that
	// misconfiguration at startup instead of as a puzzling non-symptom.
	if circuitThreshold > 0 && (circuitBaseDelay <= 0 || circuitMaxDelay <= 0) {
		return Config{}, fmt.Errorf("COMPETITION_PROVIDERS_CIRCUIT_BREAKER_BASE_DELAY and _MAX_DELAY must be positive when _THRESHOLD > 0")
	}
	cfg.Providers = ProviderRoutingConfig{
		Order: order, HealthStartupGrace: startupGrace, HealthMaxStaleness: maxStaleness,
		CircuitBreakerThreshold: circuitThreshold, CircuitBreakerBaseDelay: circuitBaseDelay, CircuitBreakerMaxDelay: circuitMaxDelay,
	}

	connectTimeout, err := envDuration("HTTP_CONNECT_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	readTimeout, err := envDuration("HTTP_READ_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	responseTimeout, err := envDuration("HTTP_RESPONSE_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.HTTP = HTTPClientConfig{ConnectTimeout: connectTimeout, ReadTimeout: readTimeout, ResponseTimeout: responseTimeout}

	if cfg.SyncEventsDelay, err = envDurationMillis("SYNC_EVENTS_DELAY_MS", 3600000); err != nil {
		return Config{}, err
	}
	if cfg.SyncMatchesDelay, err = envDurationMillis("SYNC_MATCHES_DELAY_MS", 180000); err != nil {
		return Config{}, err
	}
	if cfg.SyncMatchesColdInterval, err = envDurationMillis("SYNC_MATCHES_COLD_INTERVAL_MS", 1800000); err != nil {
		return Config{}, err
	}
	if cfg.SyncPollCloseDelay, err = envDurationMillis("SYNC_POLL_CLOSE_DELAY_MS", 5000); err != nil {
		return Config{}, err
	}
	if cfg.DigestCheckDelay, err = envDurationMillis("DIGEST_CHECK_DELAY_MS", 60000); err != nil {
		return Config{}, err
	}
	if cfg.OutboxDelay, err = envDurationMillis("OUTBOX_DELAY_MS", 2000); err != nil {
		return Config{}, err
	}
	if cfg.OutboxBatchSize, err = envInt("OUTBOX_BATCH_SIZE", 50); err != nil {
		return Config{}, err
	}
	cfg.SchedulingEnabled = envBool("APP_SCHEDULING_ENABLED", true)
	if cfg.JobTimeout, err = envDuration("APP_JOB_TIMEOUT", 2*time.Minute); err != nil {
		return Config{}, err
	}

	webhookRPS, err := envFloat("WEBHOOK_RATE_LIMIT_RPS", 20)
	if err != nil {
		return Config{}, err
	}
	webhookBurst, err := envInt("WEBHOOK_RATE_LIMIT_BURST", 40)
	if err != nil {
		return Config{}, err
	}
	cfg.WebhookRateLimit = WebhookRateLimitConfig{RequestsPerSecond: webhookRPS, Burst: webhookBurst}

	cfg.LogLevel = envString("APP_LOG_LEVEL", "INFO")

	// Defaults: dedup rows only need to outlive Telegram's retry window
	// (hours), published outbox rows are worth a fortnight for debugging, and
	// a resolved confirmation about a month for "what happened to my
	// request".
	// Opt-in pre-close voting reminders. A zero lead disables the job.
	if cfg.PollReminderLead, err = envDuration("POLL_REMINDER_LEAD", 30*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.PollReminderCheckDelay, err = envDuration("POLL_REMINDER_CHECK_DELAY", time.Minute); err != nil {
		return Config{}, err
	}
	// The day-before tournament nudge. Unset means DefaultEventEveLead; a
	// negative value turns it off.
	cfg.MiniAppURL = strings.TrimSpace(envString("MINIAPP_URL", ""))
	if cfg.MiniAppURL != "" && !strings.HasPrefix(cfg.MiniAppURL, "https://") {
		// Telegram refuses anything else outright, and the failure shows
		// up as a button that does nothing rather than as an error.
		return Config{}, fmt.Errorf("MINIAPP_URL must be an https:// address, got %q", cfg.MiniAppURL)
	}
	if cfg.WatchdogInterval, err = envDuration("WATCHDOG_INTERVAL", 5*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.HostLimits, err = loadHostLimits(); err != nil {
		return Config{}, err
	}
	if cfg.Feedback, err = loadFeedbackPolicy(); err != nil {
		return Config{}, err
	}
	if cfg.DeliveryBacklogAlert, err = envInt("WEBHOOK_BACKLOG_ALERT", 100); err != nil {
		return Config{}, err
	}
	if cfg.EventEveLead, err = envDuration("EVENT_EVE_LEAD", DefaultEventEveLead); err != nil {
		return Config{}, err
	}
	if cfg.PollReminderParticipantWindow, err = envDuration("POLL_REMINDER_PARTICIPANT_WINDOW", 60*24*time.Hour); err != nil {
		return Config{}, err
	}
	// Sweep interval and the two traffic-driven TTLs (dedup ledger, published
	// outbox) were tightened from their original 6h/48h/14d defaults as part
	// of a deliberate size-stabilization pass: at higher chat counts, a
	// time-window retention policy's steady-state size scales with traffic
	// volume, not just with time, so a shorter window directly caps how big
	// that steady state gets. Neither value has any correctness cost — a
	// dedup row only has to outlive Telegram's own webhook retry window (well
	// under 24h in practice), and a published outbox row past its first few
	// days is pure debugging convenience, not something anything reads back.
	if cfg.Retention.Interval, err = envDuration("RETENTION_SWEEP_INTERVAL", 2*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.Retention.ProcessedUpdatesTTL, err = envDuration("RETENTION_PROCESSED_UPDATES_TTL", 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.Retention.PublishedOutboxTTL, err = envDuration("RETENTION_PUBLISHED_OUTBOX_TTL", 5*24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.Retention.ResolvedRequestsTTL, err = envDuration("RETENTION_RESOLVED_REQUESTS_TTL", 30*24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.Retention.AdminActionsTTL, err = envDuration("RETENTION_ADMIN_ACTIONS_TTL", 90*24*time.Hour); err != nil {
		return Config{}, err
	}
	// MaxRows are a hard backstop under the TTL above, not the primary
	// control: whichever of "older than the TTL" or "beyond the row cap"
	// triggers first wins, so a genuine traffic surge (well beyond the ×100
	// chat-count growth these were sized for) still can't grow either table
	// without bound between sweeps. Generous on purpose — sized to comfortably
	// exceed realistic steady-state volume, not to actively trim it.
	if cfg.Retention.ProcessedUpdatesMaxRows, err = envInt("RETENTION_PROCESSED_UPDATES_MAX_ROWS", 500_000); err != nil {
		return Config{}, err
	}
	if cfg.Retention.PublishedOutboxMaxRows, err = envInt("RETENTION_PUBLISHED_OUTBOX_MAX_ROWS", 200_000); err != nil {
		return Config{}, err
	}

	cfg.Enrichment.ValveVRSEnabled = envBool("VALVE_VRS_ENABLED", true)
	if cfg.Enrichment.ValveVRSSyncInterval, err = envDuration("VALVE_VRS_SYNC_INTERVAL", 6*time.Hour); err != nil {
		return Config{}, err
	}

	// GRID and Liquipedia both require a real API key (requested from each
	// provider directly), so unlike Valve VRS neither defaults to enabled —
	// an operator who flips *_ENABLED without setting the matching *_API_KEY
	// almost certainly meant to configure it, so this fails fast at startup
	// rather than silently running with a provider that can never succeed.
	cfg.Enrichment.GRIDEnabled = envBool("GRID_ENABLED", false)
	cfg.Enrichment.GRIDAPIKey = os.Getenv("GRID_API_KEY")
	if cfg.Enrichment.GRIDEnabled && strings.TrimSpace(cfg.Enrichment.GRIDAPIKey) == "" {
		return Config{}, fmt.Errorf("GRID_API_KEY is required when GRID_ENABLED=true")
	}
	if cfg.Enrichment.GRIDSyncInterval, err = envDuration("GRID_SYNC_INTERVAL", 3*time.Hour); err != nil {
		return Config{}, err
	}

	cfg.Enrichment.LiquipediaEnabled = envBool("LIQUIPEDIA_ENABLED", false)
	cfg.Enrichment.LiquipediaAPIKey = os.Getenv("LIQUIPEDIA_API_KEY")
	if cfg.Enrichment.LiquipediaEnabled && strings.TrimSpace(cfg.Enrichment.LiquipediaAPIKey) == "" {
		return Config{}, fmt.Errorf("LIQUIPEDIA_API_KEY is required when LIQUIPEDIA_ENABLED=true")
	}
	if cfg.Enrichment.LiquipediaSyncInterval, err = envDuration("LIQUIPEDIA_SYNC_INTERVAL", 24*time.Hour); err != nil {
		return Config{}, err
	}

	cfg.Enrichment.HLTVEnabled = envBool("HLTV_ENABLED", false)
	cfg.Enrichment.HLTVAPIToken = os.Getenv("APIFY_TOKEN")
	if cfg.Enrichment.HLTVEnabled && strings.TrimSpace(cfg.Enrichment.HLTVAPIToken) == "" {
		return Config{}, fmt.Errorf("APIFY_TOKEN is required when HLTV_ENABLED=true")
	}
	// Hourly by default — this only bounds how promptly ApifyRankingGate
	// notices a qualifying moment, not how often a fetch actually happens
	// (capped to once a week by the gate itself, regardless of this
	// interval): unlike Valve VRS's free GitHub snapshots, every Apify
	// fetch here costs real money (bills per result — see apifyhltv's doc
	// comment), so the actual fetch stays weekly no matter how often this
	// check runs.
	if cfg.Enrichment.ApifyRankingCheckInterval, err = envDuration("APIFY_RANKING_CHECK_INTERVAL", time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.Enrichment.ApifyMaxTeams, err = envInt("APIFY_MAX_TEAMS", 100); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// loadFeedbackPolicy reads the Ideas channel's limits, each defaulting to
// feedback.DefaultPolicy's value. Every one of them is validated as
// positive: a zero here would not "disable a limit", it would leave the
// one surface open to the whole internet with no ceiling at all, which is
// never what somebody setting an environment variable meant.
func loadFeedbackPolicy() (feedback.Policy, error) {
	policy := feedback.DefaultPolicy()
	var err error
	if policy.MaxRunes, err = envInt("FEEDBACK_MAX_CHARS", policy.MaxRunes); err != nil {
		return feedback.Policy{}, err
	}
	if policy.Cooldown, err = envDuration("FEEDBACK_COOLDOWN", policy.Cooldown); err != nil {
		return feedback.Policy{}, err
	}
	if policy.Quota, err = envInt("FEEDBACK_QUOTA", policy.Quota); err != nil {
		return feedback.Policy{}, err
	}
	if policy.QuotaWindow, err = envDuration("FEEDBACK_QUOTA_WINDOW", policy.QuotaWindow); err != nil {
		return feedback.Policy{}, err
	}
	if policy.FloodAttempts, err = envInt("FEEDBACK_FLOOD_ATTEMPTS", policy.FloodAttempts); err != nil {
		return feedback.Policy{}, err
	}
	if policy.FloodWindow, err = envDuration("FEEDBACK_FLOOD_WINDOW", policy.FloodWindow); err != nil {
		return feedback.Policy{}, err
	}
	if policy.MaxRunes <= 0 || policy.Quota <= 0 || policy.QuotaWindow <= 0 ||
		policy.FloodAttempts <= 0 || policy.FloodWindow <= 0 || policy.Cooldown < 0 {
		return feedback.Policy{}, fmt.Errorf("every FEEDBACK_* limit must be positive: %+v", policy)
	}
	return policy, nil
}

// loadHostLimits reads the machine-resource alert levels. Each is a
// fraction (0..1) except load, which is per CPU; zero switches that one
// alert off, which is a deliberate choice rather than an accident — unlike
// the feedback limits, a missing host alert costs visibility, not safety.
func loadHostLimits() (HostLimits, error) {
	limits := DefaultHostLimits()
	var err error
	if limits.Memory, err = envFloat("HOST_ALERT_MEMORY", limits.Memory); err != nil {
		return HostLimits{}, err
	}
	if limits.Disk, err = envFloat("HOST_ALERT_DISK", limits.Disk); err != nil {
		return HostLimits{}, err
	}
	if limits.Load, err = envFloat("HOST_ALERT_LOAD_PER_CPU", limits.Load); err != nil {
		return HostLimits{}, err
	}
	if limits.Memory > 1 || limits.Disk > 1 {
		return HostLimits{}, fmt.Errorf("HOST_ALERT_MEMORY and HOST_ALERT_DISK are fractions of 1, got %.2f and %.2f",
			limits.Memory, limits.Disk)
	}
	return limits, nil
}
