package chat

import (
	"testing"
	"time"
)

func TestPendingUnsubscribe_Expired(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	future := PendingUnsubscribe{ExpiresAt: now.Add(time.Hour)}
	if future.Expired(now) {
		t.Fatal("a request expiring an hour from now must not be expired yet")
	}

	past := PendingUnsubscribe{ExpiresAt: now.Add(-time.Hour)}
	if !past.Expired(now) {
		t.Fatal("a request that expired an hour ago must be expired")
	}

	exact := PendingUnsubscribe{ExpiresAt: now}
	if !exact.Expired(now) {
		t.Fatal("a request expiring at exactly now must be treated as expired, not still valid")
	}
}
