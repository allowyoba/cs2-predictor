package telegram

import (
	"context"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

func TestParseEventSearch_RecognizesTopAndAllTierPrefixesInBothLanguages(t *testing.T) {
	trueVal, falseVal := true, false
	cases := []struct {
		raw          string
		wantOverride *bool
		wantRest     string
		desc         string
	}{
		{"top cologne", &trueVal, "cologne", "lowercase top"},
		{"Top Cologne", &trueVal, "Cologne", "mixed-case top prefix"},
		{"топ колонь", &trueVal, "колонь", "Russian lowercase топ"},
		{"ТОП колонь", &trueVal, "колонь", "Russian uppercase ТОП"},
		{"all cologne", &falseVal, "cologne", "lowercase all"},
		{"all", &falseVal, "", "all by itself opens the all-events catalog"},
		{"top", &trueVal, "", "top by itself opens the top-tier catalog"},
		{"все колонь", &falseVal, "колонь", "Russian все"},
		{"cologne", nil, "cologne", "no filter word: no override"},
		{"topical event", nil, "topical event", "prefix must be a whole word (\"top \"), not just a substring"},
	}
	for _, tc := range cases {
		override, rest := parseEventSearch(tc.raw)
		gotNil := override == nil
		wantNil := tc.wantOverride == nil
		if gotNil != wantNil || (!gotNil && *override != *tc.wantOverride) || rest != tc.wantRest {
			t.Errorf("%s: parseEventSearch(%q) = (%v, %q), want override=%v rest=%q", tc.desc, tc.raw, override, rest, tc.wantOverride, tc.wantRest)
		}
	}
}

func TestEventLabel_UsesADistinctBadgePerTier(t *testing.T) {
	cases := []struct {
		tier  competition.EventTier
		label string
	}{
		{competition.TierS, "🌟 IEM Katowice"},
		{competition.TierA, "⭐ IEM Katowice"},
		{competition.TierB, "🔹 IEM Katowice"},
		{competition.TierC, "IEM Katowice"},
		{competition.TierD, "IEM Katowice"},
		{competition.TierUnranked, "IEM Katowice"},
		{competition.TierUnknown, "IEM Katowice"},
	}
	seenBadges := map[string]bool{}
	for _, tc := range cases {
		got := eventLabel(competition.Event{Name: "IEM Katowice", Tier: tc.tier})
		if got != tc.label {
			t.Errorf("tier %q: label = %q, want %q", tc.tier, got, tc.label)
		}
		if badge := strings.TrimSuffix(got, "IEM Katowice"); badge != "" {
			seenBadges[badge] = true
		}
	}
	if len(seenBadges) != 3 {
		t.Fatalf("expected 3 distinct non-empty badges (S/A/B), got %v", seenBadges)
	}
}

// TestSettingsTopTierCallback_TogglesAndRequiresManager mirrors
// TestSettingsLocaleCallback_TogglesAndRequiresManager for the new
// "settings:top_tier" toggle: a manager can flip chat.Settings.DefaultTopTierOnly,
// a plain member cannot.
func TestSettingsTopTierCallback_TogglesAndRequiresManager(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	data := "settings:top_tier"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	updated, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.DefaultTopTierOnly {
		t.Fatal("expected DefaultTopTierOnly = true after one toggle")
	}
	if len(*calls) == 0 {
		t.Fatal("expected the settings view to be re-rendered after toggling")
	}

	// A plain member must not be able to toggle it back.
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleMember})
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	stillOn, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !stillOn.DefaultTopTierOnly {
		t.Fatal("a plain member's toggle attempt must be denied, DefaultTopTierOnly should remain true")
	}
}

// Same shape as the top_tier toggle above, for "settings:auto_subscribe".
func TestSettingsAutoSubscribeCallback_TogglesAndRequiresManager(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	data := "settings:auto_subscribe"
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	updated, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.AutoSubscribeTopTier {
		t.Fatal("expected AutoSubscribeTopTier = true after one toggle")
	}
	if len(*calls) == 0 {
		t.Fatal("expected the settings view to be re-rendered after toggling")
	}

	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleMember})
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	stillOn, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !stillOn.AutoSubscribeTopTier {
		t.Fatal("a plain member's toggle attempt must be denied, AutoSubscribeTopTier should remain true")
	}
}

// TestSearchEvents_ChatDefaultAppliesUnlessExplicitlyOverridden verifies the
// three-way precedence: a plain query uses the chat's DefaultTopTierOnly,
// an explicit "top"/"топ" prefix forces it on, and "all"/"все" forces it
// off, regardless of the chat's default.
func TestSearchEvents_ChatDefaultAppliesUnlessExplicitlyOverridden(t *testing.T) {
	cases := []struct {
		name        string
		chatDefault bool
		query       string
		wantTopOnly bool
	}{
		{"default off, plain query", false, "cologne", false},
		{"default on, plain query", true, "cologne", true},
		{"default on, explicit all overrides to off", true, "all cologne", false},
		{"default off, explicit top overrides to on", false, "top cologne", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newRecordingServer(t)
			defer server.Close()
			handler, chats := newTestHandler(t, server)
			handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
			chatID := common.ChatID{Value: -100}
			_, _ = chats.Save(context.Background(), chat.Settings{
				ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true, DefaultTopTierOnly: tc.chatDefault,
			})

			catalog := &searchCatalog{}
			handler.Catalog = catalog

			text := "/events " + tc.query
			msg := &Message{MessageID: 1, Chat: Chat{ID: -100, Type: "group"}, Text: &text, From: &User{ID: 42}}
			if err := handler.handleMessage(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if catalog.gotTopTierOnly != tc.wantTopOnly {
				t.Errorf("topTierOnly = %v, want %v", catalog.gotTopTierOnly, tc.wantTopOnly)
			}
		})
	}
}

// searchCatalog overrides fakeCatalog.SearchEvents to return canned results
// and record the topTierOnly flag it was called with — everything else
// (FindEvent, SaveMatch, ...) is unused by /events and stays the fakeCatalog
// no-op.
type searchCatalog struct {
	fakeCatalog
	results        []competition.Event
	gotTopTierOnly bool
}

func (c *searchCatalog) SearchEvents(_ context.Context, _ string, _ int, topTierOnly bool, _ []competition.GameCode) ([]competition.Event, error) {
	c.gotTopTierOnly = topTierOnly
	return c.results, nil
}

// TestSearchEvents_TopFilterAndBadging is an end-to-end test of the
// "/events top <query>" flow: the top-tier flag must reach the catalog, and
// an S/A tier result must render with the ⭐ badge while a lower-tier result
// must not.
func TestSearchEvents_TopFilterAndBadging(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	catalog := &searchCatalog{results: []competition.Event{
		{ID: common.NewEventID(), Name: "IEM Katowice", Tier: competition.TierS},
		{ID: common.NewEventID(), Name: "Regional Qualifier", Tier: competition.TierC},
	}}
	handler.Catalog = catalog

	text := "/events top katowice"
	userID := int64(42)
	msg := &Message{MessageID: 1, Chat: Chat{ID: -100, Type: "group"}, Text: &text, From: &User{ID: userID}}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	if !catalog.gotTopTierOnly {
		t.Fatal("expected SearchEvents to be called with topTierOnly=true for \"/events top ...\"")
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one reply, got %d: %+v", len(*calls), *calls)
	}
	body := (*calls)[0]

	labels := buttonLabels(t, body)
	if len(labels) != 3 { // 2 results + the back button
		t.Fatalf("expected 3 buttons (2 results + back), got %+v", labels)
	}
	if labels[0] != "🌟 IEM Katowice" {
		t.Errorf("S-tier button label = %q, want the semantic 🌟 tier badge", labels[0])
	}
	if labels[1] != "Regional Qualifier" {
		t.Errorf("C-tier button label = %q, want no decorative status or tier badge", labels[1])
	}
}

// buttonLabels extracts the button text of every row in a recorded
// sendMessage call's reply_markup.inline_keyboard.
func buttonLabels(t *testing.T, call map[string]any) []string {
	t.Helper()
	markup, ok := call["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("call has no reply_markup: %+v", call)
	}
	rows, ok := markup["inline_keyboard"].([]any)
	if !ok {
		t.Fatalf("reply_markup has no inline_keyboard: %+v", markup)
	}
	var labels []string
	for _, row := range rows {
		for _, btn := range row.([]any) {
			labels = append(labels, btn.(map[string]any)["text"].(string))
		}
	}
	return labels
}

// TestSubscribedEvents_OffersTopicButtonOnlyInsideAnActualTopic checks that
// the "bind polls to this topic" button only appears when the tap happens
// inside an actual forum topic (cb.Message.MessageThreadID != nil), since
// setEventTopic only works there; tapping it from General would otherwise
// produce a confusing generic error. The button must be omitted whenever
// there's no topic to bind to.
func TestSubscribedEvents_OffersTopicButtonOnlyInsideAnActualTopic(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, fakeChatsRepo := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = fakeChatsRepo.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	eventID := common.NewEventID()
	handler.Catalog = &dataCatalog{events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major"}}}
	subDataSubs := &dataSubsForTopicButtonTest{subs: []subscription.EventSubscription{{ChatID: chatID, EventID: eventID, Active: true}}}
	handler.Subscriptions = subDataSubs

	data := "events:mine"

	// Inside a topic, select the tournament before offering the bind action.
	threadID := int64(99)
	cbInTopic := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"},
		Message: &Message{MessageID: 1, Chat: Chat{ID: -1, Type: "group"}, MessageThreadID: &threadID}, Data: &data}
	if err := handler.handleCallback(context.Background(), cbInTopic); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !containsAll(cds, cbEventView(eventID)) || containsPrefix(cds, "event-topic:") {
		t.Fatalf("expected a tournament picker before the topic action, got %v", cds)
	}
	data = cbEventView(eventID)
	if err := handler.handleCallback(context.Background(), cbInTopic); err != nil {
		t.Fatal(err)
	}
	labelsInTopic := buttonLabels(t, (*calls)[len(*calls)-2]) // editMessageText, then answerCallbackQuery
	if !containsAll(labelsInTopic, ru(t, "events.topic")) {
		t.Fatalf("expected the topic button when viewed from inside a topic, got %v", labelsInTopic)
	}

	// Case 2: viewed from General (no MessageThreadID) -> button omitted.
	cbGeneral := &CallbackQuery{ID: "cb2", From: User{ID: 1, FirstName: "Admin"},
		Message: &Message{MessageID: 1, Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cbGeneral); err != nil {
		t.Fatal(err)
	}
	labelsGeneral := buttonLabels(t, (*calls)[len(*calls)-2])
	for _, l := range labelsGeneral {
		if l == ru(t, "events.topic") {
			t.Fatalf("expected no topic button when viewed from General, got %v", labelsGeneral)
		}
	}
}

// dataSubsForTopicButtonTest is a minimal subscription.Repository fake with
// a fixed Subscriptions() result — kept separate from the shared dataSubs
// fake (in callbacks_test.go) so this test doesn't need to reach into its
// Subscribe/Unsubscribe bookkeeping.
type dataSubsForTopicButtonTest struct {
	subs []subscription.EventSubscription
}

func (s *dataSubsForTopicButtonTest) Subscribe(_ context.Context, sub subscription.EventSubscription) (subscription.EventSubscription, error) {
	return sub, nil
}
func (s *dataSubsForTopicButtonTest) Unsubscribe(context.Context, common.ChatID, common.EventID) error {
	return nil
}
func (s *dataSubsForTopicButtonTest) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (s *dataSubsForTopicButtonTest) ActiveEventIDs(context.Context) ([]common.EventID, error) {
	return nil, nil
}
func (s *dataSubsForTopicButtonTest) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return s.subs, nil
}

// TestSettingsGamesToggleCallback_TogglesAndRequiresManager mirrors
// TestSettingsTopTierCallback_TogglesAndRequiresManager for
// "settings:games:toggle:<code>": a manager can turn a game on and back
// off, a plain member cannot change it at all.
func TestSettingsGamesToggleCallback_TogglesAndRequiresManager(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})

	data := "settings:games:toggle:" + string(competition.GameDota2)
	cb := &CallbackQuery{ID: "cb1", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}

	updated, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.GameEnabled(competition.GameDota2) {
		t.Fatal("expected Dota2 enabled after one toggle")
	}
	if len(*calls) == 0 {
		t.Fatal("expected the games view to be re-rendered after toggling")
	}

	// A plain member must not be able to toggle it back off.
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleMember})
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	stillOn, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !stillOn.GameEnabled(competition.GameDota2) {
		t.Fatal("a plain member's toggle attempt must be denied, Dota2 should remain enabled")
	}

	// Switching a game OFF is not the mirror image of switching it on: it
	// silences every tournament of that game at once, so it waits for a
	// second manager (see games_flow.go) instead of applying on the tap.
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	if err := handler.handleCallback(context.Background(), cb); err != nil {
		t.Fatal(err)
	}
	stillEnabled, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !stillEnabled.GameEnabled(competition.GameDota2) {
		t.Fatal("expected Dota2 to stay enabled until the disable request is approved")
	}
}
