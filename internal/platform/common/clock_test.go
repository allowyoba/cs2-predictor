package common

import "testing"

func TestSystemUTCClock_ReturnsUTC(t *testing.T) {
	now := SystemUTCClock().Now()
	if now.Location() != now.UTC().Location() {
		t.Fatalf("SystemUTCClock().Now() is not in UTC: %v", now)
	}
}

func TestFixedClock_AlwaysReturnsTheSameInstant(t *testing.T) {
	fixed := SystemUTCClock().Now()
	clock := FixedClock(fixed)
	if got := clock.Now(); !got.Equal(fixed) {
		t.Fatalf("FixedClock.Now() = %v, want %v", got, fixed)
	}
	if got := clock.Now(); !got.Equal(fixed) {
		t.Fatalf("FixedClock.Now() changed between calls: %v", got)
	}
}
