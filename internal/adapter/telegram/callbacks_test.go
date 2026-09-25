package telegram

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// inMemoryPredictions is a minimal in-memory prediction.Repository, enough
// to exercise prediction.Service.Create end-to-end (including the real
// PollGateway, so a subscribe test observes an actual sendPoll call).
type inMemoryPredictions struct {
	polls        map[common.PollID]prediction.Poll
	participants []common.UserID
}

func newInMemoryPredictions() *inMemoryPredictions {
	return &inMemoryPredictions{polls: map[common.PollID]prediction.Poll{}}
}
func (r *inMemoryPredictions) FindPoll(_ context.Context, id common.PollID) (*prediction.Poll, error) {
	if p, ok := r.polls[id]; ok {
		return &p, nil
	}
	return nil, nil
}
func (r *inMemoryPredictions) FindByTelegramPollID(context.Context, string) (*prediction.Poll, error) {
	return nil, nil
}
func (r *inMemoryPredictions) FindByMatchAndChat(_ context.Context, matchID common.MatchID, chatID common.ChatID) (*prediction.Poll, error) {
	for _, p := range r.polls {
		if p.MatchID == matchID && p.ChatID == chatID {
			cp := p
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *inMemoryPredictions) OpenPollsForMatch(context.Context, common.MatchID) ([]prediction.Poll, error) {
	return nil, nil
}
func (r *inMemoryPredictions) PollsForMatch(context.Context, common.MatchID) ([]prediction.Poll, error) {
	return nil, nil
}
func (r *inMemoryPredictions) SavePoll(_ context.Context, p prediction.Poll) (prediction.Poll, error) {
	r.polls[p.ID] = p
	return p, nil
}
func (r *inMemoryPredictions) OpenPollsDue(context.Context, time.Time) ([]prediction.Poll, error) {
	return nil, nil
}
func (r *inMemoryPredictions) Votes(context.Context, common.PollID) ([]prediction.Vote, error) {
	return nil, nil
}
func (r *inMemoryPredictions) SaveVote(context.Context, prediction.Vote) error { return nil }
func (r *inMemoryPredictions) PollsAwaitingReminder(context.Context, time.Time, int) ([]prediction.Poll, error) {
	return nil, nil
}
func (r *inMemoryPredictions) MarkReminded(context.Context, common.PollID, time.Time) error {
	return nil
}
func (r *inMemoryPredictions) ChatParticipants(context.Context, common.ChatID, time.Time) ([]common.UserID, error) {
	return r.participants, nil
}
func (r *inMemoryPredictions) RemoveVote(context.Context, common.PollID, common.UserID) error {
	return nil
}

// dataCatalog is a stateful competition.Catalog fake for tests that need
// FindEvent/FindUnstartedMatches to return real data (fakeCatalog in
// handler_test.go always returns nil).
type dataCatalog struct {
	events           map[common.EventID]competition.Event
	unstartedMatches map[common.EventID][]competition.Match
	teams            []competition.Team
}

func (c *dataCatalog) SearchEvents(context.Context, string, int, bool, []competition.GameCode) ([]competition.Event, error) {
	return nil, nil
}

// SearchTeams is a plain case-insensitive substring match over the fake's
// fixed team list — enough to exercise the search-then-pick flow without a
// real database.
func (c *dataCatalog) SearchTeams(_ context.Context, query string, limit int, games []competition.GameCode) ([]competition.Team, error) {
	if len(games) == 0 {
		return nil, nil
	}
	var out []competition.Team
	for _, t := range c.teams {
		if strings.Contains(strings.ToLower(t.Name), strings.ToLower(query)) {
			out = append(out, t)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (c *dataCatalog) FindEvent(_ context.Context, id common.EventID) (*competition.Event, error) {
	if e, ok := c.events[id]; ok {
		return &e, nil
	}
	return nil, nil
}
func (c *dataCatalog) FindEvents(_ context.Context, ids []common.EventID) ([]competition.Event, error) {
	var out []competition.Event
	for _, id := range ids {
		if e, ok := c.events[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}
func (c *dataCatalog) FindMatch(_ context.Context, id common.MatchID) (*competition.Match, error) {
	for _, matches := range c.unstartedMatches {
		for _, m := range matches {
			if m.ID == id {
				return &m, nil
			}
		}
	}
	return nil, nil
}
func (c *dataCatalog) FindUnstartedMatches(_ context.Context, id common.EventID) ([]competition.Match, error) {
	return c.unstartedMatches[id], nil
}
func (c *dataCatalog) FindUnstartedMatchesForEvents(_ context.Context, ids []common.EventID) ([]competition.Match, error) {
	var out []competition.Match
	for _, id := range ids {
		out = append(out, c.unstartedMatches[id]...)
	}
	return out, nil
}
func (c *dataCatalog) FindMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (c *dataCatalog) SaveEvent(_ context.Context, e competition.Event) (competition.Event, error) {
	return e, nil
}
func (c *dataCatalog) SaveMatch(_ context.Context, m competition.Match) (competition.Match, error) {
	return m, nil
}

// dataSubs is a stateful subscription.Repository fake recording Subscribe
// calls and returning canned Subscriptions.
type dataSubs struct {
	subs         []subscription.EventSubscription
	subscribed   []subscription.EventSubscription
	unsubscribed []common.EventID
}

func (s *dataSubs) Subscribe(_ context.Context, sub subscription.EventSubscription) (subscription.EventSubscription, error) {
	s.subscribed = append(s.subscribed, sub)
	return sub, nil
}
func (s *dataSubs) Unsubscribe(_ context.Context, _ common.ChatID, eventID common.EventID) error {
	s.unsubscribed = append(s.unsubscribed, eventID)
	s.subs = slices.DeleteFunc(s.subs, func(sub subscription.EventSubscription) bool {
		return sub.EventID == eventID
	})
	return nil
}
func (s *dataSubs) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (s *dataSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) { return nil, nil }
func (s *dataSubs) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return s.subs, nil
}

// dataScoring is a stateful scoring.Repository fake returning a canned
// leaderboard.
type dataScoring struct {
	leaderboard []scoring.UserStanding
	months      []scoring.StatsMonth
}

func (s *dataScoring) AvailableMonths(context.Context, common.ChatID) ([]scoring.StatsMonth, error) {
	return s.months, nil
}
func (s *dataScoring) AvailableEventIDs(context.Context, common.ChatID) ([]common.EventID, error) {
	return nil, nil
}
func (s *dataScoring) ReplaceAwards(context.Context, common.PollID, []scoring.Award) error {
	return nil
}
func (s *dataScoring) Leaderboard(context.Context, common.ChatID, scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	return s.leaderboard, nil
}
func (s *dataScoring) MedalCounts(context.Context, common.ChatID) (map[common.UserID]scoring.MedalCount, error) {
	return nil, nil
}
func (s *dataScoring) AwardMedals(context.Context, common.ChatID, common.EventID, []scoring.UserStanding, time.Time) error {
	return nil
}
func (s *dataScoring) EventCompletionHash(context.Context, common.ChatID, common.EventID) (string, bool, error) {
	return "", false, nil
}
func (s *dataScoring) MarkEventCompleted(context.Context, common.ChatID, common.EventID, string, time.Time) error {
	return nil
}
func (s *dataScoring) LockEventCompletion(context.Context, common.ChatID, common.EventID) error {
	return nil
}

// personalDataScoring adds the user-scoped statistics port on top of the
// regular group leaderboard fake so private-chat navigation can be exercised
// without a database.
type personalDataScoring struct {
	*dataScoring
	months []scoring.StatsMonth
	stats  *scoring.UserStanding
	chats  []scoring.UserChatStanding
	period scoring.StatsPeriod
	userID common.UserID
	// predictions backs the vote-level insights port; predictionLimit
	// records what the screen asked for.
	predictions     []scoring.UserPrediction
	predictionLimit int
	// bets backs the per-bet history port; betsChatID records the chat
	// filter (nil for "every chat") the screen most recently asked for.
	bets       []scoring.UserBet
	betsChatID *common.ChatID
}

func (s *personalDataScoring) UserBets(_ context.Context, userID common.UserID, chatID *common.ChatID, _ int) ([]scoring.UserBet, error) {
	s.userID = userID
	s.betsChatID = chatID
	if chatID == nil {
		return s.bets, nil
	}
	var filtered []scoring.UserBet
	for _, b := range s.bets {
		if b.ChatID == *chatID {
			filtered = append(filtered, b)
		}
	}
	return filtered, nil
}

func (s *personalDataScoring) UserBetsForEvent(_ context.Context, userID common.UserID, chatID common.ChatID, _ common.EventID, _ int) ([]scoring.UserBet, error) {
	s.userID = userID
	s.betsChatID = &chatID
	var filtered []scoring.UserBet
	for _, b := range s.bets {
		if b.ChatID == chatID {
			filtered = append(filtered, b)
		}
	}
	return filtered, nil
}

func (s *personalDataScoring) UserPredictions(_ context.Context, userID common.UserID, limit int) ([]scoring.UserPrediction, error) {
	s.userID = userID
	s.predictionLimit = limit
	return s.predictions, nil
}

func (s *personalDataScoring) AvailableUserMonths(_ context.Context, userID common.UserID) ([]scoring.StatsMonth, error) {
	s.userID = userID
	return s.months, nil
}
func (s *personalDataScoring) UserStats(_ context.Context, userID common.UserID, period scoring.StatsPeriod) (*scoring.UserStanding, error) {
	s.userID = userID
	s.period = period
	return s.stats, nil
}
func (s *personalDataScoring) UserChatStats(_ context.Context, userID common.UserID) ([]scoring.UserChatStanding, error) {
	s.userID = userID
	return s.chats, nil
}

func TestPrivateStatsNavigation_AggregatesUserAndKeepsYearView(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	personal := &personalDataScoring{
		dataScoring: &dataScoring{},
		months: []scoring.StatsMonth{
			{Year: 2026, Month: time.September},
			{Year: 2026, Month: time.August},
			{Year: 2025, Month: time.December},
		},
		stats: &scoring.UserStanding{UserID: common.UserID{Value: 42}, DisplayName: "Alex", Points: 17, CorrectPredictions: 5, Predictions: 8, Tournaments: 3},
		chats: []scoring.UserChatStanding{
			{ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2", Points: 10, CorrectPredictions: 3, Predictions: 5, Tournaments: 2},
			{ChatID: common.ChatID{Value: -1002}, ChatTitle: "Friends", Points: 7, CorrectPredictions: 2, Predictions: 3, Tournaments: 2},
		},
	}
	handler.Scoring = personal

	text := "/stats"
	msg := &Message{MessageID: 1, Chat: Chat{ID: 42, Type: "private"}, From: &User{ID: 42, FirstName: "Alex"}, Text: &text}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if _, ok := chats.settings[42]; ok {
		t.Fatal("private statistics must not create a telegram_chat row")
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one private menu send, got %d", len(*calls))
	}
	labels := buttonLabels(t, (*calls)[0])
	if !containsAll(labels, "сен 2026", "2026", ru(t, "stats.all_time"), ru(t, "private.chats"), ru(t, "stats.other_period")) {
		t.Fatalf("unexpected private stats buttons: %v", labels)
	}

	*calls = nil
	yearsData := "pstats:years"
	cb := &CallbackQuery{ID: "years", From: User{ID: 42, FirstName: "Alex"}, Message: &Message{MessageID: 10, Chat: Chat{ID: 42, Type: "private"}}, Data: &yearsData}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	var edit map[string]any
	for _, call := range *calls {
		if call["__method"] == "editMessageText" {
			edit = call
			break
		}
	}
	if edit == nil {
		t.Fatalf("expected year picker edit, calls=%+v", *calls)
	}
	labels = buttonLabels(t, edit)
	if !containsAll(labels, "2026", "2025", ru(t, "stats.months_button")) {
		t.Fatalf("year picker must expose yearly stats and month drill-down, got %v", labels)
	}

	*calls = nil
	yearData := "pstats:year:2026"
	cb.Data = &yearData
	cb.ID = "year"
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if personal.period.Kind != scoring.PeriodYear || personal.period.Year != 2026 || personal.userID.Value != 42 {
		t.Fatalf("unexpected personal stats request: user=%v period=%+v", personal.userID, personal.period)
	}
	edit = nil
	for _, call := range *calls {
		if call["__method"] == "editMessageText" {
			edit = call
			break
		}
	}
	if edit == nil {
		t.Fatalf("expected yearly stats edit, calls=%+v", *calls)
	}
	body, _ := edit["text"].(string)
	for _, want := range []string{
		ru(t, "private.stats_title", ru(t, "stats.period_year", 2026)),
		ru(t, "private.points", 17),
		ru(t, "private.activity", 8, 3),
		ru(t, "private.accuracy", 63),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("yearly personal stats missing %q: %q", want, body)
		}
	}
}

func TestPrivateStatsChats_ShowsEveryParticipatingChat(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.Scoring = &personalDataScoring{
		dataScoring: &dataScoring{},
		months:      []scoring.StatsMonth{{Year: 2026, Month: time.September}},
		chats: []scoring.UserChatStanding{
			{ChatID: common.ChatID{Value: -1001}, ChatTitle: "Office CS2", Points: 10, CorrectPredictions: 3, Predictions: 5, Tournaments: 2},
			{ChatID: common.ChatID{Value: -1002}, ChatTitle: "Friends", Points: 2, CorrectPredictions: 1, Predictions: 3, Tournaments: 1},
		},
	}

	data := "pstats:chats:0"
	cb := &CallbackQuery{ID: "chats", From: User{ID: 42, FirstName: "Alex"}, Message: &Message{MessageID: 10, Chat: Chat{ID: 42, Type: "private"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	var edit map[string]any
	for _, call := range *calls {
		if call["__method"] == "editMessageText" {
			edit = call
			break
		}
	}
	if edit == nil {
		t.Fatalf("expected chats menu edit, calls=%+v", *calls)
	}
	labels := buttonLabels(t, edit)
	if !containsAll(labels, "Office CS2", "Friends") {
		t.Fatalf("participating chats are missing from private stats: %v", labels)
	}
}

func TestStatsCommand_OpensPeriodPickerInsteadOfOnlyAllTime(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, _ := newTestHandler(t, srv)
	handler.Scoring = &dataScoring{months: []scoring.StatsMonth{{Year: 2026, Month: time.September}}}

	text := "/stats"
	title := "CS2 group"
	msg := &Message{MessageID: 1, Chat: Chat{ID: -1001, Type: "group", Title: &title}, From: &User{ID: 42, FirstName: "Alex"}, Text: &text}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one stats menu reply, got %d", len(*calls))
	}
	labels := buttonLabels(t, (*calls)[0])
	if !containsAll(labels, "сен 2026", "2026", ru(t, "stats.all_time"), ru(t, "stats.event"), ru(t, "stats.other_period")) {
		t.Fatalf("/stats must expose all period choices, got %v", labels)
	}
}

func TestStatsYearMenu_OffersWholeYearAndOnlyAvailableMonths(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)
	handler.Scoring = &dataScoring{months: []scoring.StatsMonth{
		{Year: 2026, Month: time.September},
		{Year: 2026, Month: time.July},
		{Year: 2025, Month: time.December},
	}}

	if err := handler.yearMenu(context.Background(), sendTarget(settings.ChatID, nil), settings); err != nil {
		t.Fatal(err)
	}
	labels := buttonLabels(t, (*calls)[0])
	if !containsAll(labels, "2026", "2025", ru(t, "stats.months_button")) {
		t.Fatalf("year menu must keep direct whole-year buttons: %v", labels)
	}

	*calls = nil
	if err := handler.monthMenu(context.Background(), sendTarget(settings.ChatID, nil), settings, 2026); err != nil {
		t.Fatal(err)
	}
	labels = buttonLabels(t, (*calls)[0])
	if !containsAll(labels, "сен", "июл") {
		t.Fatalf("expected only months with data, got %v", labels)
	}
	for _, unwanted := range []string{"янв", "фев", "мар", "апр", "май", "июн", "авг", "окт", "ноя", "дек"} {
		for _, got := range labels {
			if got == unwanted {
				t.Fatalf("month without statistics %q must not be shown: %v", unwanted, labels)
			}
		}
	}
}

func TestRenderLeaderboard_KeepsRowsFocusedOnRankNameAndPoints(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)
	handler.Scoring = &dataScoring{leaderboard: []scoring.UserStanding{{
		UserID: common.UserID{Value: 1}, DisplayName: "Alex", Points: 9, CorrectPredictions: 5, Predictions: 8, Tournaments: 3, Rank: 1,
	}}}

	periods := []scoring.StatsPeriod{scoring.AllTime(), scoring.ForYear(2026), scoring.ForMonth(2026, time.September)}
	for _, period := range periods {
		*calls = nil
		if err := handler.renderLeaderboard(context.Background(), sendTarget(settings.ChatID, nil), settings, period, "menu:stats", common.UserID{}); err != nil {
			t.Fatal(err)
		}
		body, _ := (*calls)[0]["text"].(string)
		if !strings.Contains(body, "Alex") || !strings.Contains(body, "<code>9</code>") {
			t.Fatalf("period %+v must show rank/name/points, got %q", period, body)
		}
		for _, noisy := range []string{"🗳", "🎯", "голосования", "эффективность"} {
			if strings.Contains(body, noisy) {
				t.Fatalf("period %+v contains secondary leaderboard noise %q in %q", period, noisy, body)
			}
		}
	}
}

func TestRenderLeaderboard_FormatsMedalsAndAppliesPeriodName(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)

	sc := &dataScoring{leaderboard: []scoring.UserStanding{
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", Points: 10, CorrectPredictions: 4, Predictions: 7, Tournaments: 3, Rank: 1},
		{UserID: common.UserID{Value: 2}, DisplayName: "Bob", Points: 5, CorrectPredictions: 1, Predictions: 4, Tournaments: 2, Rank: 2},
	}}
	handler.Scoring = sc

	if err := handler.renderLeaderboard(context.Background(), sendTarget(settings.ChatID, nil), settings, scoring.AllTime(), "menu:stats", common.UserID{}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one sendMessage, got %d", len(*calls))
	}
	text, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(text, "🥇") || !strings.Contains(text, "Alex") || !strings.Contains(text, "🥈") || !strings.Contains(text, "Bob") {
		t.Fatalf("expected medal-decorated standings, got %q", text)
	}
	for _, noisy := range []string{"🗳", "🎯", "голосования", "эффективность"} {
		if strings.Contains(text, noisy) {
			t.Fatalf("leaderboard should leave secondary metrics for detail screens, got %q", text)
		}
	}
	if (*calls)[0]["parse_mode"] != "HTML" {
		t.Fatalf("expected parse_mode HTML, got %v", (*calls)[0]["parse_mode"])
	}
}

func TestSettingsLocaleCallback_TogglesAndRequiresManager(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	data := "settings:locale"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	updated, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Locale != common.LocaleEN {
		t.Fatalf("locale = %s, want EN after toggle", updated.Locale)
	}
	if len(*calls) == 0 {
		t.Fatal("expected the menu to be re-rendered after toggling locale")
	}
}

func TestSettingsLocaleCallback_DeniedForPlainMember(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	original := chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), original)
	// newTestHandler already wires a MEMBER-role membership by default.

	data := "settings:locale"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Random"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	updated, _ := chats.Find(context.Background(), chatID)
	if updated.Locale != common.LocaleRU {
		t.Fatalf("locale = %s, want unchanged RU (access should have been denied)", updated.Locale)
	}
	body := findSendMessageText(t, *calls)
	if !strings.Contains(body, ru(t, "error.forbidden")) {
		t.Fatalf("expected the forbidden reply, got %q", body)
	}
}

// menu:settings and settings:moderators must deny a plain, non-privileged
// group member exactly like every one of their own child actions already
// does (settings:locale above, settings:timezone, moderators:add, ...) —
// regression coverage for a gap where both routes had no guard at all and
// so were reachable (including via a forged/replayed callback_data
// naming a chat the caller has nothing to do with — Telegram doesn't bind
// callback_data to the button actually tapped) with no check whatsoever.
func TestMenuSettingsAndModeratorsCallback_DeniedForPlainMember(t *testing.T) {
	for _, data := range []string{"menu:settings", "settings:moderators"} {
		t.Run(data, func(t *testing.T) {
			srv, calls := newRecordingServer(t)
			defer srv.Close()
			handler, chats := newTestHandler(t, srv)
			chatID := common.ChatID{Value: -1}
			_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
			// newTestHandler already wires a MEMBER-role membership by default.

			cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Random"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
			if err := handler.handleCallback(context.Background(), cb); err != nil {
				t.Fatal(err)
			}
			body := findSendMessageText(t, *calls)
			if !strings.Contains(body, ru(t, "error.forbidden")) {
				t.Fatalf("%s: expected the forbidden reply, got %q", data, body)
			}
		})
	}
}

// findSendMessageText returns the text of the first sendMessage call, or
// fails the test if there isn't one.
func findSendMessageText(t *testing.T, calls []map[string]any) string {
	t.Helper()
	for _, c := range calls {
		if c["__method"] == "sendMessage" {
			text, _ := c["text"].(string)
			return text
		}
	}
	t.Fatalf("no sendMessage call found among %+v", calls)
	return ""
}

func TestSubscribeCallback_CreatesPollsForEachUnstartedMatch(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	eventID := common.NewEventID()
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	scheduledAt := time.Now().Add(time.Hour)
	firstTeam := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "1"}
	secondTeam := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "2"}
	catalog := &dataCatalog{
		events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major", Game: competition.GameCS2}},
		unstartedMatches: map[common.EventID][]competition.Match{eventID: {
			{ID: common.NewMatchID(), EventID: eventID, Format: format, Status: competition.MatchNotStarted,
				FirstTeam: &firstTeam, SecondTeam: &secondTeam, ScheduledAt: &scheduledAt},
		}},
	}
	handler.Catalog = catalog
	subs := &dataSubs{}
	handler.Subscriptions = subs
	pollGateway := NewPollGateway(handler.Client, catalog, chats, handler.Texts, handler.Log, PollEnrichmentSources{})
	handler.Predictions = prediction.NewService(newInMemoryPredictions(), pollGateway, handler.Clock)

	data := "subscribe:" + eventID.Value.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	if len(subs.subscribed) != 1 || subs.subscribed[0].EventID != eventID {
		t.Fatalf("expected a subscription to be recorded for %v, got %+v", eventID, subs.subscribed)
	}
	var methods []string
	for _, c := range *calls {
		methods = append(methods, c["__method"].(string))
	}
	// The confirmation edits the search-results message in place
	// (editMessageText) rather than sending a new one — see routeCallback's
	// doc comment on why subscribe/unsubscribe are safe to edit in place.
	if !containsAll(methods, "sendPoll", "editMessageText") {
		t.Fatalf("expected sendPoll (poll creation) + editMessageText (confirmation), got %v", methods)
	}
}

func TestSubscribeCallback_SilentlyNoOpsForUnknownEvent(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	handler.Catalog = &dataCatalog{events: map[common.EventID]competition.Event{}}
	handler.Subscriptions = &dataSubs{}

	data := "subscribe:" + common.NewEventID().Value.String()
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	// Only answerCallbackQuery should have been called — no sendMessage/sendPoll.
	for _, c := range *calls {
		if m := c["__method"]; m == "sendMessage" || m == "sendPoll" {
			t.Fatalf("expected no message for an unknown event, got call: %+v", c)
		}
	}
}

// TestEventsSearchCallback_MentionsClickerSoSelectiveActuallyScopes verifies
// requestEventSearch's ForceReply targets only the manager who tapped
// "Add event": Telegram's reply_markup.selective does nothing without either
// a reply-to-message or a mention of that user in the text, so the message
// must carry a tg://user?id=<clicker> mention (the HTML text_mention
// equivalent) rather than relying on selective alone.
func TestEventsSearchCallback_MentionsClickerSoSelectiveActuallyScopes(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	data := "events:search"
	clicker := User{ID: 424242, FirstName: "Sasha"}
	cb := &CallbackQuery{ID: "cb1", From: clicker, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	call := (*calls)[0]
	if call["__method"] != "sendMessage" {
		t.Fatalf("expected sendMessage, got %+v", call)
	}
	text, _ := call["text"].(string)
	wantMention := `<a href="tg://user?id=424242">Sasha</a>`
	if !strings.Contains(text, wantMention) {
		t.Fatalf("text = %q, want it to contain the mention %q", text, wantMention)
	}
	markup, _ := call["reply_markup"].(map[string]any)
	if markup["selective"] != true || markup["force_reply"] != true {
		t.Fatalf("reply_markup = %+v, want force_reply/selective both true", markup)
	}
}

func TestRenderLeaderboard_PaginatesAndKeepsViewerVisible(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	settings := chat.Settings{ChatID: common.ChatID{Value: -1}, Title: "Test Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}
	_, _ = chats.Save(context.Background(), settings)

	standings := make([]scoring.UserStanding, 12)
	for i := range standings {
		standings[i] = scoring.UserStanding{
			UserID: common.UserID{Value: int64(i + 1)}, DisplayName: fmt.Sprintf("User%d", i+1), Rank: i + 1, Points: 100 - i,
		}
	}
	handler.Scoring = &dataScoring{leaderboard: standings}
	viewer := common.UserID{Value: 12}

	if err := handler.renderLeaderboard(context.Background(), sendTarget(settings.ChatID, nil), settings, scoring.AllTime(), "menu:stats", viewer); err != nil {
		t.Fatal(err)
	}
	text := lastText(*calls)
	if !strings.Contains(text, "12 место") {
		t.Fatalf("viewer outside page must still be shown, got %q", text)
	}
	labels := buttonLabels(t, (*calls)[len(*calls)-1])
	if !slices.Contains(labels, "1 / 2") || !slices.Contains(labels, "›") {
		t.Fatalf("expected compact leaderboard pagination, got %v", labels)
	}

	*calls = nil
	if err := handler.renderLeaderboard(context.Background(), sendTarget(settings.ChatID, nil), settings, scoring.AllTime(), "menu:stats", viewer, 1); err != nil {
		t.Fatal(err)
	}
	text = lastText(*calls)
	if !strings.Contains(text, "User12") || !strings.Contains(text, "👤") {
		t.Fatalf("viewer row on its page must be marked, got %q", text)
	}
	labels = buttonLabels(t, (*calls)[len(*calls)-1])
	if !slices.Contains(labels, "2 / 2") || !slices.Contains(labels, "‹") {
		t.Fatalf("expected second-page pagination, got %v", labels)
	}
}

// TestStatsPageCallbacks_RouteThroughRealDispatch covers routeStatsPage and
// its four per-kind helpers (routeStatsPageAllTime/Year/Month/Event) via
// handleCallback with real callback data — every earlier leaderboard test
// called renderLeaderboard directly, bypassing the actual "stats:p:..."
// parsing these functions do.
func TestStatsPageCallbacks_RouteThroughRealDispatch(t *testing.T) {
	standings := []scoring.UserStanding{{UserID: common.UserID{Value: 1}, DisplayName: "Alex", Rank: 1, Points: 10}}
	eventID := common.NewEventID()

	cases := []struct {
		name string
		data string
	}{
		{"all-time", "stats:p:a:0"},
		{"year", "stats:p:y:2026:0:r"},
		{"year-back-to-years-list", "stats:p:y:2026:0:y"},
		{"month", "stats:p:m:202601:0:r"},
		{"month-back-to-months-list", "stats:p:m:202601:0:m"},
		{"event", "stats:p:e:" + strings.ReplaceAll(eventID.Value.String(), "-", "") + ":0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := newRecordingServer(t)
			defer srv.Close()
			handler, chats := newTestHandler(t, srv)
			chatID := common.ChatID{Value: -1}
			_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
			handler.Scoring = &dataScoring{leaderboard: standings}

			data := tc.data
			cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Alex"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
			if err := handler.handleCallback(context.Background(), cb); err != nil {
				t.Fatal(err)
			}
			if len(*calls) == 0 {
				t.Fatal("expected a rendered leaderboard")
			}
			text := lastText(*calls)
			if !strings.Contains(text, "Alex") {
				t.Fatalf("expected the leaderboard to render, got %q", text)
			}
		})
	}
}

// TestStatsPageCallback_RejectsMalformedPayloads checks the validation
// paths routeStatsPage's helpers each carry (wrong field count, bad
// numbers) actually reject rather than panic on a malformed callback.
func TestStatsPageCallback_RejectsMalformedPayloads(t *testing.T) {
	for _, data := range []string{
		"stats:p:a",      // missing page
		"stats:p:y:2026", // missing page/back
		"stats:p:m:notyyyymm:0:r",
		"stats:p:e:not-a-uuid:0",
		"stats:p:x:0",
	} {
		t.Run(data, func(t *testing.T) {
			srv, calls := newRecordingServer(t)
			defer srv.Close()
			handler, chats := newTestHandler(t, srv)
			chatID := common.ChatID{Value: -1}
			_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})

			d := data
			cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Alex"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &d}
			if err := handler.handleCallback(context.Background(), cb); err != nil {
				t.Fatal(err)
			}
			// A validation error is caught by handleCommandError and turned
			// into a user-facing reply rather than propagating — just check
			// nothing panicked and no leaderboard was rendered.
			for _, c := range *calls {
				if c["__method"] == "editMessageText" || c["__method"] == "sendMessage" {
					if text, _ := c["text"].(string); strings.Contains(text, "Alex") {
						t.Fatalf("expected malformed payload %q to be rejected, got a rendered leaderboard", data)
					}
				}
			}
		})
	}
}

// TestStatsYearAndMonthCallbacks_RouteThroughRealDispatch covers
// routeStatsYear/routeStatsMonth (the "jump to this whole year/month"
// buttons, distinct from their paginated stats:p: siblings) via real
// dispatch, including the "back to the years/months list" variants.
func TestStatsYearAndMonthCallbacks_RouteThroughRealDispatch(t *testing.T) {
	standings := []scoring.UserStanding{{UserID: common.UserID{Value: 1}, DisplayName: "Alex", Rank: 1, Points: 10}}
	for _, data := range []string{"stats:year:2026", "stats:year:2026:menu", "stats:month:2026-01", "stats:month:2026-01:months:2026"} {
		t.Run(data, func(t *testing.T) {
			srv, calls := newRecordingServer(t)
			defer srv.Close()
			handler, chats := newTestHandler(t, srv)
			chatID := common.ChatID{Value: -1}
			_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
			handler.Scoring = &dataScoring{leaderboard: standings}

			d := data
			cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Alex"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &d}
			if err := handler.handleCallback(context.Background(), cb); err != nil {
				t.Fatal(err)
			}
			text := lastText(*calls)
			if !strings.Contains(text, "Alex") {
				t.Fatalf("expected the leaderboard to render for %q, got %q", data, text)
			}
		})
	}
}

// TestEventsBrowseCallback_RoutesTopAndAllWithPagination covers
// routeEventsBrowse via real dispatch — every existing browse-related test
// exercised /events top/all as a text command, never the "browse the
// catalog" pagination buttons this route serves.
func TestEventsBrowseCallback_RoutesTopAndAllWithPagination(t *testing.T) {
	events := make([]competition.Event, 10)
	for i := range events {
		events[i] = competition.Event{ID: common.NewEventID(), Name: fmt.Sprintf("Tournament %d", i+1), Tier: competition.TierA}
	}
	catalog := &searchCatalog{results: events}

	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Catalog = catalog
	handler.Subscriptions = &dataSubs{}

	data := "events:browse:top:0"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if !catalog.gotTopTierOnly {
		t.Fatal("expected events:browse:top: to search with topTierOnly=true")
	}
	var labels []string
	for _, b := range lastEditedButtons(t, *calls) {
		if s, ok := b["text"].(string); ok {
			labels = append(labels, s)
		}
	}
	if !slices.ContainsFunc(labels, func(l string) bool { return strings.Contains(l, "Tournament 1") }) {
		t.Fatalf("expected the first page of results as buttons, got %v", labels)
	}
	if !slices.Contains(labels, "›") {
		t.Fatalf("expected a next-page button for 10 results, got %v", labels)
	}

	*calls = nil
	data = "events:browse:all:0"
	cb2 := &CallbackQuery{ID: "cb2", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb2); err != nil {
		t.Fatal(err)
	}
	if catalog.gotTopTierOnly {
		t.Fatal("expected events:browse:all: to search with topTierOnly=false")
	}
}

func TestModeratorRemove_RequiresConfirmationBeforeRemoving(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	modID := common.UserID{Value: 99}
	_ = chats.AddModerator(context.Background(), chat.Moderator{ChatID: chatID, UserID: modID})

	askData := cbModeratorRemove(modID)
	cb := &CallbackQuery{ID: "ask", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: -1, Type: "group"}}, Data: &askData}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	if stillMod, _ := chats.IsModerator(context.Background(), chatID, modID); !stillMod {
		t.Fatal("tapping remove should only ask for confirmation, not remove immediately")
	}
	var screenCall map[string]any
	for _, c := range *calls {
		if c["__method"] == "editMessageText" || c["__method"] == "sendMessage" {
			screenCall = c
		}
	}
	if screenCall == nil {
		t.Fatalf("expected a rendered confirmation screen, calls: %+v", *calls)
	}
	labels := buttonLabels(t, screenCall)
	if !containsAll(labels, ru(t, "moderators.remove_confirm")) {
		t.Fatalf("expected a confirm button, got %v", labels)
	}

	doData := cbModeratorRemoveDo(modID)
	cb2 := &CallbackQuery{ID: "do", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{MessageID: 1, Chat: Chat{ID: -1, Type: "group"}}, Data: &doData}
	if err := handler.handleCallback(context.Background(), cb2); err != nil {
		t.Fatal(err)
	}
	if stillMod, _ := chats.IsModerator(context.Background(), chatID, modID); stillMod {
		t.Fatal("expected the moderator to be removed after confirming")
	}
}

// A chat that follows two games must not get them interleaved: the browse
// list gets one header row per game, and each game's tournaments sit under
// their own header. A chat following one game keeps the flat list it had.
func TestEventsBrowse_SeparatesGamesWithHeaderRows(t *testing.T) {
	events := []competition.Event{
		{ID: common.NewEventID(), Name: "CS Major", Tier: competition.TierS, Game: competition.GameCS2},
		{ID: common.NewEventID(), Name: "The International", Tier: competition.TierS, Game: competition.GameDota2},
		{ID: common.NewEventID(), Name: "CS Cup", Tier: competition.TierA, Game: competition.GameCS2},
	}
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Catalog = &searchCatalog{results: events}
	handler.Subscriptions = &dataSubs{}

	data := "events:browse:all:0"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	var labels []string
	for _, b := range lastEditedButtons(t, *calls) {
		if s, ok := b["text"].(string); ok {
			labels = append(labels, s)
		}
	}
	cs2Header := "— " + ru(t, "game.cs2") + " —"
	dotaHeader := "— " + ru(t, "game.dota2") + " —"
	csIdx := slices.Index(labels, cs2Header)
	dotaIdx := slices.Index(labels, dotaHeader)
	if csIdx < 0 || dotaIdx < 0 {
		t.Fatalf("expected a header per game, got %v", labels)
	}
	// Both CS2 tournaments must sit between the CS2 header and the Dota one.
	for _, name := range []string{"CS Major", "CS Cup"} {
		idx := slices.IndexFunc(labels, func(l string) bool { return strings.Contains(l, name) })
		if idx < csIdx || idx > dotaIdx {
			t.Fatalf("%q at %d is outside its own game's section (cs2 %d, dota %d): %v", name, idx, csIdx, dotaIdx, labels)
		}
	}
	dotaEvent := slices.IndexFunc(labels, func(l string) bool { return strings.Contains(l, "The International") })
	if dotaEvent < dotaIdx {
		t.Fatalf("the Dota tournament must follow its own header, got %v", labels)
	}
}

// One game followed means nothing to separate, so no headers appear — the
// grouping is there to help, not to add a row to every list.
func TestEventsBrowse_SingleGameKeepsAFlatList(t *testing.T) {
	events := []competition.Event{
		{ID: common.NewEventID(), Name: "CS Major", Tier: competition.TierS, Game: competition.GameCS2},
		{ID: common.NewEventID(), Name: "CS Cup", Tier: competition.TierA, Game: competition.GameCS2},
	}
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Catalog = &searchCatalog{results: events}
	handler.Subscriptions = &dataSubs{}

	data := "events:browse:all:0"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	for _, b := range lastEditedButtons(t, *calls) {
		if s, ok := b["text"].(string); ok && strings.HasPrefix(s, "— ") {
			t.Fatalf("expected no game headers for a single-game list, got %q", s)
		}
	}
}

// FindPlayableMatchesForEvents widens the same fixture by one status; the
// tests here only ever seed unstarted matches, so it answers the same way.
func (c *dataCatalog) FindPlayableMatchesForEvents(ctx context.Context, ids []common.EventID) ([]competition.Match, error) {
	return c.FindUnstartedMatchesForEvents(ctx, ids)
}
