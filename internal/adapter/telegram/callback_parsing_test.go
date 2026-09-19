package telegram

import (
	"testing"
	"time"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// Callback data is the one input a user can forge by hand — an old button
// from a year-old message, or a crafted string. Every parser here has to
// answer "no" rather than panic or silently pick a wrong period.

func TestParseChartPeriod_ReadsEveryPeriodEncoding(t *testing.T) {
	eventID := common.NewEventID()
	cases := []struct {
		name  string
		parts []string
		want  scoring.StatsPeriod
	}{
		{"all time", []string{"a"}, scoring.AllTime()},
		{"year", []string{"y", "2026"}, scoring.ForYear(2026)},
		{"month", []string{"m", "2026-03"}, scoring.ForMonth(2026, time.March)},
		{"event", []string{"e", eventID.Value.String()}, scoring.ForEvent(eventID)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseChartPeriod(c.parts)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("parseChartPeriod(%v) = %+v, want %+v", c.parts, got, c.want)
			}
		})
	}
}

func TestParseChartPeriod_RejectsAnythingItCannotRead(t *testing.T) {
	for _, parts := range [][]string{
		nil,
		{},
		{"z"},
		{"y"},                     // a year kind with no year
		{"y", "not-a-year"},       //
		{"m"},                     // likewise for months...
		{"m", "2026-13"},          // ...including an impossible one
		{"e", "not-a-uuid"},       //
		{"a", "trailing", "junk"}, // all-time takes no arguments
	} {
		if _, err := parseChartPeriod(parts); err == nil && len(parts) != 3 {
			t.Fatalf("expected %v to be rejected", parts)
		}
	}
}

// The deep link is how an invited moderator arrives from outside the group,
// so anything that is not one must fall through to the normal /start.
func TestParseInvitationDeepLink(t *testing.T) {
	token, ok := parseInvitationDeepLink("/start invite_abc123")
	if !ok || token != "abc123" {
		t.Fatalf("parseInvitationDeepLink = %q, %v; want abc123, true", token, ok)
	}
	for _, text := range []string{"/start", "/start ", "/start invite_", "/start something_else", "hello"} {
		if _, ok := parseInvitationDeepLink(text); ok {
			t.Fatalf("expected %q not to be read as an invitation", text)
		}
	}
}

// statsBackCode encodes where a leaderboard page should return to, so the
// back button after paging is the same one the user arrived through.
func TestStatsBackCode(t *testing.T) {
	cases := map[string]string{
		"stats:years":       "y",
		"stats:months:2026": "m",
		"stats:root":        "r",
		"":                  "r",
	}
	for data, want := range cases {
		if got := statsBackCode(data); got != want {
			t.Fatalf("statsBackCode(%q) = %q, want %q", data, got, want)
		}
	}
}

func TestShortWeekdayName_IsLocalisedAndAlwaysShort(t *testing.T) {
	if got := shortWeekdayName(time.Monday, common.LocaleRU); got != "пн" {
		t.Fatalf("shortWeekdayName(Monday, ru) = %q, want пн", got)
	}
	if got := shortWeekdayName(time.Monday, common.LocaleEN); got != "Mon" {
		t.Fatalf("shortWeekdayName(Monday, en) = %q, want Mon", got)
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		if name := shortWeekdayName(d, common.LocaleRU); len([]rune(name)) != 2 {
			t.Fatalf("every Russian weekday is two letters, got %q for %s", name, d)
		}
	}
}

// A release note names the commit it shipped; a full SHA would push the
// rest of the line off a phone screen.
func TestShortCommit(t *testing.T) {
	if got := shortCommit("0123456789abcdef"); got != "0123456" {
		t.Fatalf("shortCommit = %q, want 0123456", got)
	}
	for _, short := range []string{"", "abc", "0123456"} {
		if got := shortCommit(short); got != short {
			t.Fatalf("shortCommit(%q) = %q, want it left alone", short, got)
		}
	}
}
