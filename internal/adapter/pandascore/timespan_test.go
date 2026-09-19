package pandascore

import (
	"testing"
	"time"
)

// A tournament's window is folded together from its series, each of which
// may be missing a start or an end. Picking the wrong side of a nil would
// silently shorten a tournament — and with it, which matches are fetched.
func TestEarlierAndLaterTime_TreatUnknownAsNoOpinion(t *testing.T) {
	early := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	late := early.Add(48 * time.Hour)

	cases := []struct {
		name    string
		a, b    *time.Time
		earlier *time.Time
		later   *time.Time
	}{
		{"both known", &early, &late, &early, &late},
		{"both known, reversed", &late, &early, &early, &late},
		{"first unknown", nil, &late, &late, &late},
		{"second unknown", &early, nil, &early, &early},
		{"neither known", nil, nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := earlierTime(c.a, c.b); !sameTime(got, c.earlier) {
				t.Fatalf("earlierTime = %v, want %v", got, c.earlier)
			}
			if got := laterTime(c.a, c.b); !sameTime(got, c.later) {
				t.Fatalf("laterTime = %v, want %v", got, c.later)
			}
		})
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
