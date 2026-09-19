// Package app is the composition root: config loading, the multi-provider
// gateway, scheduled jobs, the outbox dispatcher, and the HTTP server. It
// may import any domain or adapter package — nothing may import app.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// ProviderRoutingConfig controls provider order, health thresholds, and
// circuit-breaker backoff (env vars COMPETITION_PROVIDERS_*).
type ProviderRoutingConfig struct {
	Order              []string
	HealthStartupGrace time.Duration
	HealthMaxStaleness time.Duration

	// CircuitBreakerThreshold is the number of consecutive failures a
	// provider must accumulate before its circuit opens: further calls are
	// short-circuited (skipped without an HTTP round trip) until the
	// backoff delay elapses. Zero (the zero value) disables the breaker
	// entirely — every call is always attempted, which is also what every
	// existing bare ProviderRoutingConfig{...} literal in tests gets.
	CircuitBreakerThreshold int
	// CircuitBreakerBaseDelay is how long the circuit stays open after the
	// threshold is first crossed; CircuitBreakerMaxDelay caps the delay as
	// it doubles with each further consecutive failure.
	CircuitBreakerBaseDelay time.Duration
	CircuitBreakerMaxDelay  time.Duration
}

func DefaultProviderRoutingConfig() ProviderRoutingConfig {
	return ProviderRoutingConfig{
		Order:                   []string{"PANDASCORE"},
		HealthStartupGrace:      2 * time.Minute,
		HealthMaxStaleness:      2 * time.Hour,
		CircuitBreakerThreshold: 3,
		CircuitBreakerBaseDelay: 30 * time.Second,
		CircuitBreakerMaxDelay:  15 * time.Minute,
	}
}

// ProviderMetrics is an optional hook for recording per-call metrics
// (wired to Prometheus counters/timers in the app composition). A nil
// ProviderMetrics is a no-op.
type ProviderMetrics interface {
	RecordCall(provider, operation, result string, duration time.Duration)
}

// CompetitionProviderGateway is the only entry point from scheduled
// synchronization to external feeds — calls are global, never scoped to a
// Telegram chat. Providers are tried in configured order with automatic
// fallback to the next one on failure.
type CompetitionProviderGateway struct {
	config          ProviderRoutingConfig
	providersByName map[string]competition.DataProvider
	metrics         ProviderMetrics
	clock           common.Clock
	startedAt       time.Time

	// health, when set, is told about the two transitions worth alerting
	// on: a provider's circuit opening, and its first success afterwards.
	// Optional — nil simply means nobody is listening.
	health common.ProviderHealthObserver

	mu               sync.Mutex
	lastSuccess      map[string]time.Time
	lastFailure      string
	failureStreak    map[string]int
	circuitOpenUntil map[string]time.Time
}

// ObserveHealth attaches the observer after construction, since the
// alerter it belongs to needs the outbox, which is wired later than the
// gateway. Not concurrency-guarded on purpose: it is a composition-root
// call, made before any job can reach the gateway.
func (g *CompetitionProviderGateway) ObserveHealth(observer common.ProviderHealthObserver) {
	g.health = observer
}

// ErrProvidersUnavailable is returned when every configured provider failed
// for a single logical call.
type ErrProvidersUnavailable struct {
	Operation string
	Errors    []error
}

func (e *ErrProvidersUnavailable) Error() string {
	return fmt.Sprintf("all competition providers failed for %s: %v", e.Operation, e.Errors)
}

// NewCompetitionProviderGateway validates that every provider named in
// config.Order is present in providers and that there are no duplicate
// provider names.
func NewCompetitionProviderGateway(providers []competition.DataProvider, config ProviderRoutingConfig, metrics ProviderMetrics, clock common.Clock) (*CompetitionProviderGateway, error) {
	byName := map[string]competition.DataProvider{}
	for _, p := range providers {
		name := strings.ToUpper(p.ProviderName())
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("duplicate competition providers: %s", name)
		}
		byName[name] = p
	}
	var missing []string
	for _, name := range config.Order {
		if _, ok := byName[strings.ToUpper(name)]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("configured competition providers are unavailable: %v", missing)
	}
	return &CompetitionProviderGateway{
		config: config, providersByName: byName, metrics: metrics, clock: clock,
		startedAt: clock.Now(), lastSuccess: map[string]time.Time{},
		failureStreak: map[string]int{}, circuitOpenUntil: map[string]time.Time{},
	}, nil
}

func (g *CompetitionProviderGateway) UpcomingEvents(ctx context.Context) ([]competition.Event, error) {
	return execute(g, "events", "", func(p competition.DataProvider) ([]competition.Event, error) {
		return p.UpcomingEvents(ctx)
	})
}

// Matches groups events by their own provider and, for each group, tries
// that provider first before falling through the configured order. A
// group's failure (including its provider's circuit being open) does NOT
// abort the whole call: the remaining groups are still tried, so a single
// exhausted/failing provider can't also drop matches that belong to a
// different, healthy provider's events. The return value can carry both a
// non-empty match slice AND a non-nil error — that error still signals
// something needs attention, but callers should persist the matches that
// did come back rather than discard them.
func (g *CompetitionProviderGateway) Matches(ctx context.Context, events []competition.Event) ([]competition.Match, error) {
	if len(events) == 0 {
		return nil, nil
	}
	byProvider := map[string][]competition.Event{}
	var order []string
	for _, e := range events {
		key := strings.ToUpper(e.Provider)
		if _, seen := byProvider[key]; !seen {
			order = append(order, key)
		}
		byProvider[key] = append(byProvider[key], e)
	}

	var all []competition.Match
	var errs []error
	for _, preferred := range order {
		group := byProvider[preferred]
		matches, err := execute(g, "matches", preferred, func(p competition.DataProvider) ([]competition.Match, error) {
			return p.Matches(ctx, group)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("provider group %s: %w", preferred, err))
			continue
		}
		all = append(all, matches...)
	}
	if len(errs) > 0 {
		return all, fmt.Errorf("matches failed for %d/%d provider group(s): %w", len(errs), len(order), errors.Join(errs...))
	}
	return all, nil
}

func (g *CompetitionProviderGateway) orderedProviders(preferred string) []competition.DataProvider {
	var out []competition.DataProvider
	if preferred != "" {
		if p, ok := g.providersByName[preferred]; ok {
			out = append(out, p)
		}
	}
	for _, name := range g.config.Order {
		name = strings.ToUpper(name)
		if name == preferred {
			continue
		}
		out = append(out, g.providersByName[name])
	}
	return out
}

func execute[T any](g *CompetitionProviderGateway, operation, preferred string, call func(competition.DataProvider) (T, error)) (T, error) {
	var zero T
	var errs []error
	for _, provider := range g.orderedProviders(preferred) {
		name := provider.ProviderName()
		if until, open := g.circuitOpen(name); open {
			g.record(name, operation, "circuit_open", 0)
			errs = append(errs, fmt.Errorf("%s: circuit open until %s after repeated failures", name, until.Format(time.RFC3339)))
			continue
		}

		start := g.clock.Now()
		result, err := call(provider)
		duration := g.clock.Now().Sub(start)
		if err == nil {
			g.record(name, operation, "success", duration)
			g.recordSuccess(name)
			return result, nil
		}
		g.record(name, operation, "failure", duration)
		g.recordFailure(name)
		errs = append(errs, err)
		g.mu.Lock()
		g.lastFailure = fmt.Sprintf("%s: %v", name, err)
		g.mu.Unlock()
	}
	return zero, &ErrProvidersUnavailable{Operation: operation, Errors: errs}
}

func (g *CompetitionProviderGateway) record(provider, operation, result string, d time.Duration) {
	if g.metrics != nil {
		g.metrics.RecordCall(provider, operation, result, d)
	}
}

// circuitOpen reports whether provider is currently short-circuited, i.e.
// still within its backoff window from a prior run of consecutive
// failures. CircuitBreakerThreshold == 0 disables the breaker outright.
func (g *CompetitionProviderGateway) circuitOpen(provider string) (time.Time, bool) {
	if g.config.CircuitBreakerThreshold <= 0 {
		return time.Time{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.circuitOpenUntil[provider]
	if !ok || !g.clock.Now().Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// recordSuccess resets the failure streak and closes the circuit.
func (g *CompetitionProviderGateway) recordSuccess(provider string) {
	g.mu.Lock()
	_, wasOpen := g.circuitOpenUntil[provider]
	g.lastSuccess[provider] = g.clock.Now()
	g.lastFailure = ""
	g.failureStreak[provider] = 0
	delete(g.circuitOpenUntil, provider)
	g.mu.Unlock()

	// Reported outside the lock: an observer enqueues into the outbox, and
	// nothing that touches the database belongs under this mutex.
	if wasOpen && g.health != nil {
		g.health.ProviderRecovered(context.Background(), provider)
	}
}

// recordFailure bumps the provider's consecutive-failure streak and, once
// it reaches CircuitBreakerThreshold, opens (or re-opens, doubling the
// delay each additional failure, capped at CircuitBreakerMaxDelay) its
// circuit.
func (g *CompetitionProviderGateway) recordFailure(provider string) {
	if g.config.CircuitBreakerThreshold <= 0 {
		return
	}
	g.mu.Lock()
	g.failureStreak[provider]++
	streak := g.failureStreak[provider]
	if streak < g.config.CircuitBreakerThreshold {
		g.mu.Unlock()
		return
	}

	exp := streak - g.config.CircuitBreakerThreshold
	if exp > 30 { // guards against a shift large enough to overflow/wrap a time.Duration
		exp = 30
	}
	delay := g.config.CircuitBreakerBaseDelay << exp
	if delay <= 0 || delay > g.config.CircuitBreakerMaxDelay {
		delay = g.config.CircuitBreakerMaxDelay
	}
	_, alreadyOpen := g.circuitOpenUntil[provider]
	g.circuitOpenUntil[provider] = g.clock.Now().Add(delay)
	lastFailure := g.lastFailure
	g.mu.Unlock()

	// Only the transition, and only outside the lock: an already-open
	// circuit re-opening on the next retry is the same outage, not a new
	// one, and an observer writes to the database.
	if !alreadyOpen && g.health != nil {
		g.health.ProviderDown(context.Background(), provider, streak, lastFailure)
	}
}

// HealthStatus is the three-state health of the gateway: UNKNOWN (no call
// has succeeded yet, within the startup grace period), DOWN, or UP.
type HealthStatus string

const (
	HealthUnknown HealthStatus = "UNKNOWN"
	HealthDown    HealthStatus = "DOWN"
	HealthUp      HealthStatus = "UP"
)

// Health reports UNKNOWN during the configured startup grace period if no
// call has ever succeeded, DOWN once that grace period has elapsed with
// still no success (or once the last success is older than
// HealthMaxStaleness), and UP otherwise.
func (g *CompetitionProviderGateway) Health() (HealthStatus, map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.clock.Now()
	var newestSuccess time.Time
	for _, t := range g.lastSuccess {
		if t.After(newestSuccess) {
			newestSuccess = t
		}
	}
	lastFailure := g.lastFailure
	if lastFailure == "" {
		lastFailure = "none"
	}
	details := map[string]any{"configured": g.config.Order, "lastFailure": lastFailure}

	if newestSuccess.IsZero() {
		details["lastSuccess"] = "never"
		if now.Before(g.startedAt.Add(g.config.HealthStartupGrace)) {
			return HealthUnknown, details
		}
		return HealthDown, details
	}
	details["lastSuccess"] = newestSuccess.Format(time.RFC3339)
	if newestSuccess.Add(g.config.HealthMaxStaleness).After(now) {
		return HealthUp, details
	}
	return HealthDown, details
}
