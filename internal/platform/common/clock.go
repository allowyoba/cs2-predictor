package common

import "time"

// Clock abstracts "now" so services (PredictionService, ScoringService,
// ResultSettlementService, EventCompletionService, ...) can be tested with
// a fixed instant. The app wires a UTC system clock; tests wire a fixed one.
type Clock interface {
	Now() time.Time
}

type systemUTCClock struct{}

func (systemUTCClock) Now() time.Time { return time.Now().UTC() }

// SystemUTCClock is the app-wide clock — always UTC.
func SystemUTCClock() Clock { return systemUTCClock{} }

// FixedClock is a Clock that always returns the same instant, for tests.
type FixedClock time.Time

func (f FixedClock) Now() time.Time { return time.Time(f) }
