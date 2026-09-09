package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

type fixedProvider struct {
	name       string
	events     []competition.Event
	calls      int
	matchCalls int
	fail       bool
}

func (p *fixedProvider) ProviderName() string { return p.name }
func (p *fixedProvider) UpcomingEvents(context.Context) ([]competition.Event, error) {
	p.calls++
	if p.fail {
		return nil, errors.New("boom")
	}
	return p.events, nil
}
func (p *fixedProvider) Matches(context.Context, []competition.Event) ([]competition.Match, error) {
	p.matchCalls++
	if p.fail {
		return nil, errors.New("boom")
	}
	return nil, nil
}

// mutableClock is a common.Clock whose Now() can be advanced mid-test — used
// to verify the circuit breaker's backoff window actually elapses.
type mutableClock struct{ t time.Time }

func (c *mutableClock) Now() time.Time { return c.t }

func TestGateway_CircuitBreakerSkipsCallsUntilBackoffElapses(t *testing.T) {
	clock := &mutableClock{t: time.Now()}
	primary := &fixedProvider{name: "PRIMARY", fail: true}
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{primary},
		ProviderRoutingConfig{
			Order: []string{"PRIMARY"}, CircuitBreakerThreshold: 2,
			CircuitBreakerBaseDelay: time.Minute, CircuitBreakerMaxDelay: time.Hour,
		}, nil, clock)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if _, err := gw.UpcomingEvents(context.Background()); err == nil {
			t.Fatal("expected failure")
		}
	}
	if primary.calls != 2 {
		t.Fatalf("calls = %d, want 2 (threshold just reached)", primary.calls)
	}

	// Circuit is now open: a further call must be short-circuited, not
	// reach the provider at all.
	if _, err := gw.UpcomingEvents(context.Background()); err == nil {
		t.Fatal("expected error while circuit is open")
	}
	if primary.calls != 2 {
		t.Fatalf("calls = %d after circuit opened, want still 2 (short-circuited)", primary.calls)
	}

	// Advance past the backoff window and let the provider recover: the
	// call should reach it again and succeed.
	clock.t = clock.t.Add(2 * time.Minute)
	primary.fail = false
	if _, err := gw.UpcomingEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if primary.calls != 3 {
		t.Fatalf("calls = %d, want 3 after backoff elapsed", primary.calls)
	}
}

func TestGateway_CircuitBreakerDisabledByDefaultZeroValue(t *testing.T) {
	primary := &fixedProvider{name: "PRIMARY", fail: true}
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{primary},
		ProviderRoutingConfig{Order: []string{"PRIMARY"}}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, _ = gw.UpcomingEvents(context.Background())
	}
	if primary.calls != 5 {
		t.Fatalf("calls = %d, want 5 (breaker disabled: every call must reach the provider)", primary.calls)
	}
}

func testEvent(externalID string, provider string) competition.Event {
	return competition.Event{
		ID: common.NewEventID(), Game: competition.GameCS2, Name: "Event " + externalID,
		ExternalID: externalID, Status: competition.EventUpcoming, Provider: provider,
	}
}

func TestGateway_FallsBackToNextProviderWhenPreferredFails(t *testing.T) {
	primary := &fixedProvider{name: "PRIMARY", fail: true}
	secondary := &fixedProvider{name: "SECONDARY", events: []competition.Event{testEvent("e1", "SECONDARY")}}
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{primary, secondary},
		ProviderRoutingConfig{Order: []string{"PRIMARY", "SECONDARY"}}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}

	result, err := gw.UpcomingEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].ExternalID != "e1" {
		t.Fatalf("result = %+v", result)
	}
	if primary.calls != 1 || secondary.calls != 1 {
		t.Fatalf("calls: primary=%d secondary=%d, want 1 1", primary.calls, secondary.calls)
	}
}

func TestGateway_ErrorsWhenEveryProviderFails(t *testing.T) {
	primary := &fixedProvider{name: "PRIMARY", fail: true}
	secondary := &fixedProvider{name: "SECONDARY", fail: true}
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{primary, secondary},
		ProviderRoutingConfig{Order: []string{"PRIMARY", "SECONDARY"}}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.UpcomingEvents(context.Background()); err == nil {
		t.Fatal("expected error when every provider fails")
	}
}

func TestGateway_RejectsUnknownConfiguredProvider(t *testing.T) {
	primary := &fixedProvider{name: "PRIMARY"}
	_, err := NewCompetitionProviderGateway([]competition.DataProvider{primary},
		ProviderRoutingConfig{Order: []string{"PRIMARY", "SECONDARY"}}, nil, common.SystemUTCClock())
	if err == nil {
		t.Fatal("expected error for unknown provider in config")
	}
}

func TestGateway_HealthUnknownDuringGraceThenDownAfterFailure(t *testing.T) {
	primary := &fixedProvider{name: "PRIMARY", fail: true}
	secondary := &fixedProvider{name: "SECONDARY", fail: true}
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{primary, secondary},
		ProviderRoutingConfig{Order: []string{"PRIMARY", "SECONDARY"}, HealthStartupGrace: 0}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.UpcomingEvents(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if status, _ := gw.Health(); status != HealthDown {
		t.Fatalf("health = %s, want DOWN", status)
	}
}

func TestGateway_HealthUpAfterSuccessWithinStaleness(t *testing.T) {
	primary := &fixedProvider{name: "PRIMARY", events: []competition.Event{testEvent("e1", "PRIMARY")}}
	secondary := &fixedProvider{name: "SECONDARY"}
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{primary, secondary},
		ProviderRoutingConfig{Order: []string{"PRIMARY", "SECONDARY"}, HealthMaxStaleness: time.Hour}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.UpcomingEvents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, _ := gw.Health(); status != HealthUp {
		t.Fatalf("health = %s, want UP", status)
	}
}

// TestGateway_MatchesReturnsPartialResultsWhenOneProviderGroupFails
// verifies that one provider group's failure doesn't drop matches
// successfully fetched for a different, healthy provider's events: both
// the partial match slice and a non-nil error must come back together.
func TestGateway_MatchesReturnsPartialResultsWhenOneProviderGroupFails(t *testing.T) {
	healthy := &fixedProvider{name: "HEALTHY"}
	broken := &fixedProvider{name: "BROKEN", fail: true}
	// Order deliberately excludes HEALTHY as a fallback for BROKEN's group:
	// including it would let BROKEN's group silently succeed via HEALTHY
	// (the existing, correct fallback behavior), which would defeat the
	// point of this test — it needs BROKEN's group to fail outright while
	// HEALTHY's own group (still tried as its own preferred provider,
	// independent of Order) succeeds.
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{healthy, broken},
		ProviderRoutingConfig{Order: []string{"BROKEN"}}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}

	events := []competition.Event{testEvent("e1", "HEALTHY"), testEvent("e2", "BROKEN")}
	_, err = gw.Matches(context.Background(), events)
	if err == nil {
		t.Fatal("expected a non-nil error since the BROKEN provider group failed")
	}
	if healthy.matchCalls != 1 {
		t.Fatalf("healthy.matchCalls = %d, want 1 (its group must still be attempted despite the other group failing)", healthy.matchCalls)
	}
	if broken.matchCalls != 1 {
		t.Fatalf("broken.matchCalls = %d, want 1", broken.matchCalls)
	}
}

func TestGateway_MatchesPrefersEventsOwnProvider(t *testing.T) {
	primary := &fixedProvider{name: "PRIMARY"}
	secondary := &fixedProvider{name: "SECONDARY"}
	gw, err := NewCompetitionProviderGateway([]competition.DataProvider{primary, secondary},
		ProviderRoutingConfig{Order: []string{"PRIMARY", "SECONDARY"}}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := gw.Matches(context.Background(), []competition.Event{testEvent("e1", "SECONDARY")}); err != nil {
		t.Fatal(err)
	}
	if primary.matchCalls != 0 || secondary.matchCalls != 1 {
		t.Fatalf("matchCalls: primary=%d secondary=%d, want 0 1", primary.matchCalls, secondary.matchCalls)
	}
}
