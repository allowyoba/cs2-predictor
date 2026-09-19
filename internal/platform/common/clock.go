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

// StartOfWeekUTC returns 00:00 UTC of t's calendar week's Monday. Shared
// because the weekly cadence it defines has to mean exactly the same thing
// to the job that decides when to fetch and to the provider that decides
// whether an already-finished remote run still counts as this week's.
func StartOfWeekUTC(t time.Time) time.Time {
	day := t.UTC().Truncate(24 * time.Hour)
	daysSinceMonday := (int(day.Weekday()) - int(time.Monday) + 7) % 7
	return day.AddDate(0, 0, -daysSinceMonday)
}
