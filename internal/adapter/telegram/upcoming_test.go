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
				"🕒 <code>23:30</code> <b>A&amp;B</b> — <b>TBD</b>\n  BO3 · Group &lt;B&gt;\n\n" +
				"<i>09.09 · " + h.Texts.Get("upcoming.tomorrow", locale) + "</i>\n" +
				"🕒 <code>00:30</code> <b>A2</b> — <b>TBD</b>\n  BO3\n\n" +
				"<b>Cup B</b>\n\n<i>08.09 · " + h.Texts.Get("upcoming.today", locale) + "</i>\n" +
				"🕒 <code>23:45</code> <b>B1</b> — <b>TBD</b>\n  BO3 · Group A"
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

// TestUpcoming_RendersBroadcastLinkPerChatLanguage covers the whole stream
// line: the platform-named link, the "· EN" marker that appears only when
// the chat's own language wasn't available, and the silence for a match
// whose only broadcast is an unofficial community channel.
func TestUpcoming_RendersBroadcastLinkPerChatLanguage(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	h.Clock = common.FixedClock(now)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: "UTC"}
	russian, english, community := common.NewEventID(), common.NewEventID(), common.NewEventID()
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	first, second, third := now.Add(time.Hour), now.Add(2*time.Hour), now.Add(3*time.Hour)
	h.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{EventID: russian}, {EventID: english}, {EventID: community}}}
	match := func(event common.EventID, at time.Time, a, b string, streams []competition.Stream) competition.Match {
		return competition.Match{ID: common.NewMatchID(), EventID: event, ScheduledAt: &at, Format: format,
			FirstTeam: &competition.Team{Name: a}, SecondTeam: &competition.Team{Name: b}, Streams: streams}
	}
	enMain := competition.Stream{Language: "en", URL: "https://kick.com/cct_cs2", Main: true, Official: true}
	ruCommunity := competition.Stream{Language: "ru", URL: "https://www.twitch.tv/betboom_cs_ru3"}
	h.Catalog = &dataCatalog{
		events: map[common.EventID]competition.Event{
			russian: {ID: russian, Name: "RU Cup"}, english: {ID: english, Name: "EN Cup"}, community: {ID: community, Name: "Dark Cup"},
		},
		unstartedMatches: map[common.EventID][]competition.Match{
			russian: {match(russian, first, "A", "B", []competition.Stream{
				{Language: "ru", URL: "https://vkvideo.ru/official_ru", Official: true}, enMain})},
			english:   {match(english, second, "C", "D", []competition.Stream{ruCommunity, enMain})},
			community: {match(community, third, "E", "F", []competition.Stream{ruCommunity})},
		},
	}
	if err := h.upcoming(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
		t.Fatal(err)
	}
	body := lastText(*calls)

	// The chat's own language: named by platform, no language marker.
	if want := `📺 <a href="https://vkvideo.ru/official_ru">VK Video</a>`; !strings.Contains(body, want+"\n") {
		t.Fatalf("expected the Russian broadcast link %q with no language marker in:\n%s", want, body)
	}
	// No Russian broadcast: the English one, marked as such.
	if want := `📺 <a href="https://kick.com/cct_cs2">Kick</a> · EN`; !strings.Contains(body, want) {
		t.Fatalf("expected the English fallback link %q in:\n%s", want, body)
	}
	if strings.Contains(body, "betboom_cs_ru3") {
		t.Fatalf("an unofficial community stream must never be linked:\n%s", body)
	}
	if got := strings.Count(body, "📺"); got != 2 {
		t.Fatalf("expected exactly 2 broadcast lines (the community-only match has none), got %d in:\n%s", got, body)
	}
}

// Every screen suppresses Telegram's link preview — an auto-expanded stream
// thumbnail would dwarf the match list it belongs to.
func TestUpcoming_DisablesLinkPreview(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	h.Clock = common.FixedClock(now)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: "UTC"}
	event := common.NewEventID()
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	at := now.Add(time.Hour)
	h.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{EventID: event}}}
	h.Catalog = &dataCatalog{
		events: map[common.EventID]competition.Event{event: {ID: event, Name: "Cup"}},
		unstartedMatches: map[common.EventID][]competition.Match{event: {{
			ID: common.NewMatchID(), EventID: event, ScheduledAt: &at, Format: format,
			FirstTeam: &competition.Team{Name: "A"}, SecondTeam: &competition.Team{Name: "B"},
			Streams: []competition.Stream{{Language: "en", URL: "https://kick.com/cct_cs2", Main: true, Official: true}},
		}}},
	}
	if err := h.upcoming(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
		t.Fatal(err)
	}
	last := (*calls)[len(*calls)-1]
	options, ok := last["link_preview_options"].(map[string]any)
	if !ok || options["is_disabled"] != true {
		t.Fatalf("expected link_preview_options.is_disabled = true, got %v", last["link_preview_options"])
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

// The broadcast language is a chat setting, toggled from the settings menu
// like any other: it starts out following the chat's UI language and, once
// set, overrides it. Same manager-only guard as the other settings.
func TestSettingsStreamLanguageCallback_TogglesAndRequiresManager(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	data := "settings:stream_language"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	updated, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	// An RU chat was following RU implicitly, so one press moves it to EN.
	if updated.StreamLanguage != common.LocaleEN || updated.StreamLocale() != common.LocaleEN {
		t.Fatalf("StreamLanguage = %q, want EN after one toggle", updated.StreamLanguage)
	}
	if len(*calls) == 0 {
		t.Fatal("expected the settings view to be re-rendered after toggling")
	}

	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleMember})
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	unchanged, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.StreamLanguage != common.LocaleEN {
		t.Fatal("a plain member's toggle attempt must be denied")
	}
}

// Platform naming is what makes the link self-describing; an unknown host
// still beats a generic word, and a URL with no host falls back to it.
func TestStreamPlatformLabel(t *testing.T) {
	cases := map[string]string{
		"https://www.twitch.tv/betboom_cs_ru3": "Twitch",
		"https://m.twitch.tv/esl_csgo":         "Twitch",
		"https://kick.com/cct_cs2":             "Kick",
		"https://youtu.be/abc":                 "YouTube",
		"https://watch.cct.live/stream":        "watch.cct.live",
		"not a url at all":                     "Трансляция",
	}
	for raw, want := range cases {
		if got := streamPlatformLabel(raw, "Трансляция"); got != want {
			t.Errorf("streamPlatformLabel(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Team names do not say which game they play, so a chat following two of
// them gets the game named on each tournament heading. A chat following one
// does not: there is nothing to disambiguate.
func TestUpcoming_NamesTheGameOnlyWhenMoreThanOneIsFollowed(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: "UTC"}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	cs2, dota := common.NewEventID(), common.NewEventID()
	at := now.Add(time.Hour)

	render := func(t *testing.T, events map[common.EventID]competition.Event) string {
		t.Helper()
		server, calls := newRecordingServer(t)
		defer server.Close()
		h, _ := newTestHandler(t, server)
		h.Clock = common.FixedClock(now)
		var subs []subscription.EventSubscription
		unstarted := map[common.EventID][]competition.Match{}
		for id := range events {
			subs = append(subs, subscription.EventSubscription{EventID: id})
			unstarted[id] = []competition.Match{{
				ID: common.NewMatchID(), EventID: id, ScheduledAt: &at, Format: format,
				FirstTeam: &competition.Team{Name: "A"}, SecondTeam: &competition.Team{Name: "B"},
			}}
		}
		h.Subscriptions = &dataSubs{subs: subs}
		h.Catalog = &dataCatalog{events: events, unstartedMatches: unstarted}
		if err := h.upcoming(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
			t.Fatal(err)
		}
		return lastText(*calls)
	}

	both := render(t, map[common.EventID]competition.Event{
		cs2:  {ID: cs2, Name: "CS Major", Game: competition.GameCS2},
		dota: {ID: dota, Name: "The International", Game: competition.GameDota2},
	})
	if !strings.Contains(both, ru(t, "game.cs2_short")) || !strings.Contains(both, ru(t, "game.dota2_short")) {
		t.Fatalf("expected both games named when both are followed:\n%s", both)
	}

	single := render(t, map[common.EventID]competition.Event{
		cs2: {ID: cs2, Name: "CS Major", Game: competition.GameCS2},
	})
	if strings.Contains(single, ru(t, "game.cs2_short")) {
		t.Fatalf("a single-game chat needs no game label:\n%s", single)
	}
}

// The upcoming list names teams the same way the poll does, flags included
// — the two are the only places a team is named, and a flag in one but not
// the other reads as a bug.
func TestUpcoming_CarriesTheTeamsCountryFlags(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, _ := newTestHandler(t, server)
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	h.Clock = common.FixedClock(now)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: "UTC"}
	event := common.NewEventID()
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	at := now.Add(time.Hour)
	h.Subscriptions = &dataSubs{subs: []subscription.EventSubscription{{EventID: event}}}
	h.Catalog = &dataCatalog{
		events: map[common.EventID]competition.Event{event: {ID: event, Name: "Major"}},
		unstartedMatches: map[common.EventID][]competition.Match{event: {{
			ID: common.NewMatchID(), EventID: event, ScheduledAt: &at, Format: format,
			FirstTeam:  &competition.Team{Name: "Spirit", Location: "RU"},
			SecondTeam: &competition.Team{Name: "Falcons"}, // country unknown
		}}},
	}

	if err := h.upcoming(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
		t.Fatal(err)
	}
	body := lastText(*calls)
	if !strings.Contains(body, "🇷🇺") {
		t.Fatalf("expected the known team's flag:\n%s", body)
	}
	if !strings.Contains(body, "Falcons") {
		t.Fatalf("a team without a country must still be named:\n%s", body)
	}
}
