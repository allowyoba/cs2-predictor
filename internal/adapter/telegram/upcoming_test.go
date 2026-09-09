package telegram

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

func TestUpcoming_GroupsEventsAndLocalDates(t *testing.T) {
	for _, locale := range []common.LocaleCode{common.LocaleRU, common.LocaleEN} {
		t.Run(locale.Tag(), func(t *testing.T) {
			server, calls := newRecordingServer(t)
			defer server.Close()
			h, _ := newTestHandler(t, server)
			now := time.Date(2026, 9, 8, 20, 0, 0, 0, time.UTC)
			h.Clock = common.FixedClock(now)
			settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: locale, Timezone: "Europe/Moscow"}
			a, b := common.NewEventID(), common.NewEventID()
			format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
			makeMatch := func(event common.EventID, name string, offset time.Duration, stage string) competition.Match {
				when := now.Add(offset)
				return competition.Match{ID: common.NewMatchID(), EventID: event, ScheduledAt: &when, Format: format,
					FirstTeam: &competition.Team{Name: name}, Stage: &stage}
			}
			// Both the subscriptions and each event's matches arrive out of order.
			h.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{EventID: b}, {EventID: a}}}
			h.Catalog = &dataCatalog{
				events: map[common.EventID]competition.Event{
					a: {ID: a, Name: "Cup <A>"}, b: {ID: b, Name: "Cup B"},
				},
				unstartedMatches: map[common.EventID][]competition.Match{
					a: {makeMatch(a, "A2", 90*time.Minute, "  "), makeMatch(a, "A&B", 30*time.Minute, " Group <B> ")},
					b: {makeMatch(b, "B1", 45*time.Minute, "Group A")},
				},
			}
			if err := h.upcoming(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
				t.Fatal(err)
			}
			want := h.Texts.Get("upcoming.title", locale) + "\n\n" +
				"<b>Cup &lt;A&gt;</b>\n\n<i>08.09 · " + h.Texts.Get("upcoming.today", locale) + "</i>\n" +
				"<code>23:30</code> <b>A&amp;B</b> — <b>TBD</b>\n  BO3 · Group &lt;B&gt;\n\n" +
				"<i>09.09 · " + h.Texts.Get("upcoming.tomorrow", locale) + "</i>\n" +
				"<code>00:30</code> <b>A2</b> — <b>TBD</b>\n  BO3\n\n" +
				"<b>Cup B</b>\n\n<i>08.09 · " + h.Texts.Get("upcoming.today", locale) + "</i>\n" +
				"<code>23:45</code> <b>B1</b> — <b>TBD</b>\n  BO3 · Group A"
			if got := lastText(*calls); got != want {
				t.Fatalf("schedule:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestUpcoming_LimitsNearestEligibleMatchesBeforeGrouping(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	h.Clock = common.FixedClock(now)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: "UTC"}
	a, b := common.NewEventID(), common.NewEventID()
	h.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{EventID: a}, {EventID: b}}}
	catalog := &dataCatalog{unstartedMatches: make(map[common.EventID][]competition.Match)}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	for i := 11; i >= 0; i-- {
		event := a
		if i%2 != 0 {
			event = b
		}
		when := now.Add(time.Duration(i) * time.Hour)
		catalog.unstartedMatches[event] = append(catalog.unstartedMatches[event], competition.Match{
			ID: common.NewMatchID(), EventID: event, ScheduledAt: &when, Format: format,
			SecondTeam: &competition.Team{Name: fmt.Sprintf("Team %02d", i)},
		})
	}
	catalog.unstartedMatches[a] = append(catalog.unstartedMatches[a],
		competition.Match{EventID: a, FirstTeam: &competition.Team{Name: "Unscheduled"}},
		competition.Match{EventID: a, ScheduledAt: &now})
	h.Catalog = catalog
	if err := h.upcoming(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
		t.Fatal(err)
	}
	body := lastText(*calls)
	if strings.Count(body, "<code>") != 10 || strings.Contains(body, "Unscheduled") {
		t.Fatalf("expected ten scheduled matches with at least one participant: %s", body)
	}
	for i := range 12 {
		if got := strings.Contains(body, fmt.Sprintf("Team %02d", i)); got != (i < 10) {
			t.Errorf("Team %02d included = %v, want %v", i, got, i < 10)
		}
	}
	// Missing event metadata yields the same fallback name, but distinct IDs
	// must still produce separate tournament groups.
	if strings.Count(body, "<b>Турнир</b>") != 2 {
		t.Fatalf("different event IDs with the same name were merged: %s", body)
	}
}

func TestUpcoming_EmptyWhenNoEligibleMatches(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleEN, Timezone: "UTC"}
	if err := h.upcoming(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
		t.Fatal(err)
	}
	if got, want := lastText(*calls), h.Texts.Get("upcoming.title", settings.Locale)+"\n\n"+h.Texts.Get("upcoming.empty", settings.Locale); got != want {
		t.Fatalf("empty schedule = %q, want %q", got, want)
	}
}
