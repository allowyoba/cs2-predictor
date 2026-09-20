package chat

import (
	"testing"
	"time"
)

// Quiet hours exist so a chat can say "not at night" without muting the
// bot. Every case here is about the same two questions: is it quiet now,
// and when does the silence end — because a held message has to be
// delivered later, never dropped.

// hour renders a whole-hour boundary, which is every window a chat can
// actually pick from the screen.
func hour(h int) *int {
	v := h * 60
	return &v
}

// at builds an instant from the wall clock in the default zone — the one
// almost every chat here uses. It is still an absolute instant, so the
// cross-zone case below reads the very same moment on a Berlin clock.
func at(t *testing.T, h, minute int) time.Time {
	t.Helper()
	return time.Date(2026, time.March, 10, h, minute, 0, 0, ZoneOrDefault(DefaultTimezone))
}

func TestQuietUntil_NoWindowMeansNeverQuiet(t *testing.T) {
	settings := Settings{Timezone: DefaultTimezone}
	if _, quiet := settings.QuietUntil(at(t, 3, 0)); quiet {
		t.Fatal("a chat that set no window is never quiet")
	}
}

// The common shape: silence overnight, wrapping past midnight.
func TestQuietUntil_WindowThatWrapsPastMidnight(t *testing.T) {
	settings := Settings{Timezone: DefaultTimezone, QuietFromMinute: hour(23), QuietToMinute: hour(8)}

	until, quiet := settings.QuietUntil(at(t, 23, 30))
	if !quiet {
		t.Fatal("23:30 is inside a 23:00–08:00 window")
	}
	if until.Hour() != 8 || until.Day() != 11 {
		t.Fatalf("until = %s, want 08:00 the next morning", until)
	}

	until, quiet = settings.QuietUntil(at(t, 2, 0))
	if !quiet {
		t.Fatal("02:00 is still inside last night's window")
	}
	if until.Hour() != 8 || until.Day() != 10 {
		t.Fatalf("until = %s, want 08:00 the same morning", until)
	}

	if _, quiet := settings.QuietUntil(at(t, 12, 0)); quiet {
		t.Fatal("midday is not inside a 23:00–08:00 window")
	}
	// The boundaries themselves: the window opens at from and is over at to.
	if _, quiet := settings.QuietUntil(at(t, 8, 0)); quiet {
		t.Fatal("the window ends at 08:00, it does not include it")
	}
	if _, quiet := settings.QuietUntil(at(t, 23, 0)); !quiet {
		t.Fatal("the window starts at 23:00 and includes it")
	}
}

// A window inside one day works the same way, without the wrap.
func TestQuietUntil_WindowInsideOneDay(t *testing.T) {
	settings := Settings{Timezone: DefaultTimezone, QuietFromMinute: hour(1), QuietToMinute: hour(8)}

	until, quiet := settings.QuietUntil(at(t, 3, 0))
	if !quiet || until.Hour() != 8 {
		t.Fatalf("expected silence until 08:00, got %s quiet=%v", until, quiet)
	}
	if _, quiet := settings.QuietUntil(at(t, 0, 30)); quiet {
		t.Fatal("00:30 is before a 01:00–08:00 window")
	}
}

// The window is read in the chat's own zone: the same instant is night in
// Moscow and evening in Berlin.
func TestQuietUntil_IsReadInTheChatsOwnZone(t *testing.T) {
	moscow := Settings{Timezone: "Europe/Moscow", QuietFromMinute: hour(23), QuietToMinute: hour(8)}
	berlin := Settings{Timezone: "Europe/Berlin", QuietFromMinute: hour(23), QuietToMinute: hour(8)}
	instant := at(t, 23, 30)

	if _, quiet := moscow.QuietUntil(instant); !quiet {
		t.Fatal("23:30 Moscow time is inside the Moscow chat's window")
	}
	if _, quiet := berlin.QuietUntil(instant); quiet {
		t.Fatal("the same instant is 21:30 in Berlin, which is not inside its window")
	}
}

// Tapping the same hour twice must not mute a chat forever.
func TestQuietUntil_AnEmptyWindowIsNoWindow(t *testing.T) {
	settings := Settings{Timezone: DefaultTimezone, QuietFromMinute: hour(9), QuietToMinute: hour(9)}
	for hour := 0; hour < 24; hour++ {
		if _, quiet := settings.QuietUntil(at(t, hour, 0)); quiet {
			t.Fatalf("an empty window must never be quiet, was at %02d:00", hour)
		}
	}
}
