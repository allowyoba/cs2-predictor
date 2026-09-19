package app

import "testing"

// configEnvVars lists every env var LoadConfig reads. Tests must not rely on
// the ambient process environment being clean of these — a developer's own
// .env, or a Makefile that exports it, can otherwise leak real secrets into
// "is this required var enforced as missing" assertions.
var configEnvVars = []string{
	"PORT", "DATABASE_URL", "DATABASE_USER", "DATABASE_PASSWORD", "DATABASE_POOL_SIZE",
	"TELEGRAM_BOT_TOKEN", "TELEGRAM_WEBHOOK_SECRET", "TELEGRAM_BASE_URL",
	"PANDASCORE_TOKEN", "PANDASCORE_BASE_URL", "PANDASCORE_PAGE_SIZE", "PANDASCORE_EVENT_BATCH_SIZE", "PANDASCORE_MAX_CONCURRENCY",
	"COMPETITION_PROVIDERS_ORDER", "COMPETITION_PROVIDERS_HEALTH_STARTUP_GRACE", "COMPETITION_PROVIDERS_HEALTH_MAX_STALENESS",
	"COMPETITION_PROVIDERS_CIRCUIT_BREAKER_THRESHOLD", "COMPETITION_PROVIDERS_CIRCUIT_BREAKER_BASE_DELAY", "COMPETITION_PROVIDERS_CIRCUIT_BREAKER_MAX_DELAY",
	"HTTP_CONNECT_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_RESPONSE_TIMEOUT",
	"SYNC_EVENTS_DELAY_MS", "SYNC_MATCHES_DELAY_MS", "SYNC_POLL_CLOSE_DELAY_MS", "DIGEST_CHECK_DELAY_MS",
	"OUTBOX_DELAY_MS", "OUTBOX_BATCH_SIZE", "APP_SCHEDULING_ENABLED", "APP_JOB_TIMEOUT",
	"WEBHOOK_RATE_LIMIT_RPS", "WEBHOOK_RATE_LIMIT_BURST", "APP_LOG_LEVEL",
	"VALVE_VRS_ENABLED", "VALVE_VRS_SYNC_INTERVAL",
	"RETENTION_SWEEP_INTERVAL", "RETENTION_PROCESSED_UPDATES_TTL",
	"RETENTION_PUBLISHED_OUTBOX_TTL", "RETENTION_RESOLVED_REQUESTS_TTL",
	"GRID_ENABLED", "GRID_API_KEY", "GRID_SYNC_INTERVAL",
	"LIQUIPEDIA_ENABLED", "LIQUIPEDIA_API_KEY", "LIQUIPEDIA_SYNC_INTERVAL",
}

// withEnv clears every var LoadConfig reads (so the test is hermetic
// regardless of the ambient environment) before applying kv.
func withEnv(t *testing.T, kv map[string]string, fn func()) {
	t.Helper()
	for _, k := range configEnvVars {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
	fn()
}

func validEnv() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN":      "test-token",
		"TELEGRAM_WEBHOOK_SECRET": "a-secret-at-least-16-chars",
		"PANDASCORE_TOKEN":        "panda-token",
	}
}

func TestLoadConfig_RequiresTelegramToken(t *testing.T) {
	env := validEnv()
	delete(env, "TELEGRAM_BOT_TOKEN")
	withEnv(t, env, func() {
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected an error when TELEGRAM_BOT_TOKEN is missing")
		}
	})
}

func TestLoadConfig_RequiresWebhookSecretAtLeast16Chars(t *testing.T) {
	env := validEnv()
	env["TELEGRAM_WEBHOOK_SECRET"] = "short"
	withEnv(t, env, func() {
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected an error when TELEGRAM_WEBHOOK_SECRET is under 16 characters")
		}
	})
}

func TestLoadConfig_RequiresPandaScoreToken(t *testing.T) {
	env := validEnv()
	delete(env, "PANDASCORE_TOKEN")
	withEnv(t, env, func() {
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected an error when PANDASCORE_TOKEN is missing")
		}
	})
}

func TestLoadConfig_Defaults(t *testing.T) {
	withEnv(t, validEnv(), func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Port != "8080" {
			t.Errorf("Port = %q, want 8080", cfg.Port)
		}
		if cfg.DatabaseURL != "postgresql://localhost:5432/cs2predictor" {
			t.Errorf("DatabaseURL = %q, want the local default", cfg.DatabaseURL)
		}
		if len(cfg.Providers.Order) != 1 || cfg.Providers.Order[0] != "PANDASCORE" {
			t.Errorf("Providers.Order = %v, want [PANDASCORE]", cfg.Providers.Order)
		}
		if !cfg.SchedulingEnabled {
			t.Error("SchedulingEnabled should default to true")
		}
		if cfg.DigestCheckDelay.String() != "1m0s" {
			t.Errorf("DigestCheckDelay = %s, want 1m0s", cfg.DigestCheckDelay)
		}
	})
}

func TestLoadConfig_DatabaseURLIsTakenVerbatim(t *testing.T) {
	env := validEnv()
	env["DATABASE_URL"] = "postgresql://db:5432/mydb"
	withEnv(t, env, func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DatabaseURL != "postgresql://db:5432/mydb" {
			t.Errorf("DatabaseURL = %q, want it used exactly as set", cfg.DatabaseURL)
		}
	})
}

func TestLoadConfig_CustomProviderOrderIsCommaSeparatedAndTrimmed(t *testing.T) {
	env := validEnv()
	env["COMPETITION_PROVIDERS_ORDER"] = "PANDASCORE, OTHER"
	withEnv(t, env, func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"PANDASCORE", "OTHER"}
		if len(cfg.Providers.Order) != 2 || cfg.Providers.Order[0] != want[0] || cfg.Providers.Order[1] != want[1] {
			t.Errorf("Providers.Order = %v, want %v", cfg.Providers.Order, want)
		}
	})
}

func TestLoadConfig_RejectsZeroCircuitBreakerDelayWhenThresholdEnabled(t *testing.T) {
	env := validEnv()
	env["COMPETITION_PROVIDERS_CIRCUIT_BREAKER_THRESHOLD"] = "3"
	env["COMPETITION_PROVIDERS_CIRCUIT_BREAKER_MAX_DELAY"] = "0s"
	withEnv(t, env, func() {
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected an error: a zero max delay would silently defeat the breaker (every open circuit expires immediately)")
		}
	})
}

func TestLoadConfig_AllowsZeroCircuitBreakerDelayWhenThresholdDisabled(t *testing.T) {
	env := validEnv()
	env["COMPETITION_PROVIDERS_CIRCUIT_BREAKER_THRESHOLD"] = "0"
	env["COMPETITION_PROVIDERS_CIRCUIT_BREAKER_MAX_DELAY"] = "0s"
	withEnv(t, env, func() {
		if _, err := LoadConfig(); err != nil {
			t.Fatalf("threshold=0 disables the breaker outright, delays should be irrelevant: %v", err)
		}
	})
}

func TestLoadConfig_InvalidIntegerEnvVarFailsFast(t *testing.T) {
	env := validEnv()
	env["OUTBOX_BATCH_SIZE"] = "not-a-number"
	withEnv(t, env, func() {
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected an error for a non-numeric OUTBOX_BATCH_SIZE")
		}
	})
}

func TestLoadConfig_ValveVRSDefaultsToEnabled(t *testing.T) {
	withEnv(t, validEnv(), func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Enrichment.ValveVRSEnabled {
			t.Fatal("expected VALVE_VRS_ENABLED to default to true (no API key required)")
		}
	})
}

func TestLoadConfig_GRIDDefaultsToDisabledAndRequiresAPIKeyWhenEnabled(t *testing.T) {
	withEnv(t, validEnv(), func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Enrichment.GRIDEnabled {
			t.Fatal("expected GRID_ENABLED to default to false (no confirmed API access)")
		}
	})

	env := validEnv()
	env["GRID_ENABLED"] = "true"
	withEnv(t, env, func() {
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected an error when GRID_ENABLED=true but GRID_API_KEY is unset")
		}
	})

	env2 := validEnv()
	env2["GRID_ENABLED"] = "true"
	env2["GRID_API_KEY"] = "grid-key"
	withEnv(t, env2, func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Enrichment.GRIDEnabled || cfg.Enrichment.GRIDAPIKey != "grid-key" {
			t.Fatalf("cfg.Enrichment = %+v, want GRIDEnabled=true GRIDAPIKey=grid-key", cfg.Enrichment)
		}
	})
}

func TestLoadConfig_LiquipediaDefaultsToDisabledAndRequiresAPIKeyWhenEnabled(t *testing.T) {
	withEnv(t, validEnv(), func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Enrichment.LiquipediaEnabled {
			t.Fatal("expected LIQUIPEDIA_ENABLED to default to false (no confirmed API access)")
		}
	})

	env := validEnv()
	env["LIQUIPEDIA_ENABLED"] = "true"
	withEnv(t, env, func() {
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected an error when LIQUIPEDIA_ENABLED=true but LIQUIPEDIA_API_KEY is unset")
		}
	})

	env2 := validEnv()
	env2["LIQUIPEDIA_ENABLED"] = "true"
	env2["LIQUIPEDIA_API_KEY"] = "lp-key"
	withEnv(t, env2, func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Enrichment.LiquipediaEnabled || cfg.Enrichment.LiquipediaAPIKey != "lp-key" {
			t.Fatalf("cfg.Enrichment = %+v, want LiquipediaEnabled=true LiquipediaAPIKey=lp-key", cfg.Enrichment)
		}
	})
}

// The pool has to be wider than the number of jobs that can hold a
// connection at once: every scheduled job takes a cluster lock for its
// whole run, including the part where it is waiting on somebody's API. A
// pool narrower than that turns one slow provider into a chain of timeouts
// across unrelated jobs — which is how a production deploy behaved before
// this default was raised.
func TestLoadConfig_DatabasePoolIsWiderThanTheJobCount(t *testing.T) {
	withEnv(t, validEnv(), func() {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		// cmd/bot schedules about a dozen background jobs; the exact
		// number moves, the relationship must not.
		const scheduledJobs = 12
		if cfg.DatabasePoolSize <= scheduledJobs {
			t.Fatalf("DatabasePoolSize = %d, want more than the %d jobs that each pin a connection",
				cfg.DatabasePoolSize, scheduledJobs)
		}
	})
}
