package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeTargets is a minimal in-memory subscription.TargetRepository for the
// team-follow flow's tests: subscribe, unsubscribe, and list, nothing more.
type fakeTargets struct {
	subs []subscription.TargetSubscription
}

func (f *fakeTargets) SubscribeTarget(_ context.Context, s subscription.TargetSubscription) (subscription.TargetSubscription, error) {
	for i, existing := range f.subs {
		if existing.ChatID == s.ChatID && existing.Kind == s.Kind && existing.TargetID == s.TargetID {
			f.subs[i] = s
			return s, nil
		}
	}
	f.subs = append(f.subs, s)
	return s, nil
}

func (f *fakeTargets) UnsubscribeTarget(_ context.Context, chatID common.ChatID, kind subscription.TargetKind, targetID string) error {
	for i, s := range f.subs {
		if s.ChatID == chatID && s.Kind == kind && s.TargetID == targetID {
			f.subs[i].Active = false
		}
	}
	return nil
}

func (f *fakeTargets) TargetSubscriptions(_ context.Context, chatID common.ChatID) ([]subscription.TargetSubscription, error) {
	var out []subscription.TargetSubscription
	for _, s := range f.subs {
		if s.ChatID == chatID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeTargets) ChatsForTarget(_ context.Context, kind subscription.TargetKind, targetID string) ([]common.ChatID, error) {
	var out []common.ChatID
	for _, s := range f.subs {
		if s.Kind == kind && s.TargetID == targetID && s.Active {
			out = append(out, s.ChatID)
		}
	}
	return out, nil
}

func targetTestChat(t *testing.T, handler *UpdateHandler, chats *fakeChats, chatID common.ChatID) chat.Settings {
	t.Helper()
	settings, err := chats.Save(context.Background(), chat.Settings{
		ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true,
		EnabledGames: []competition.GameCode{competition.GameCS2},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	return settings
}

// allButtonLabels extracts every button's visible text across every
// recorded call, keyed by its callback_data (scope-stripped, same as
// findKeyboardButtons) — findKeyboardButtons only ever returns
// callback_data and url, never text, so label assertions need this
// instead.
func allButtonLabels(calls []map[string]any) map[string]string {
	labels := map[string]string{}
	for _, c := range calls {
		markup, ok := c["reply_markup"].(map[string]any)
		if !ok {
			continue
		}
		rows, _ := markup["inline_keyboard"].([]any)
		for _, row := range rows {
			for _, btn := range row.([]any) {
				b := btn.(map[string]any)
				cd, hasCD := b["callback_data"].(string)
				text, hasText := b["text"].(string)
				if hasCD && hasText {
					_, payload := splitCallbackScope(cd)
					labels[payload] = text
				}
			}
		}
	}
	return labels
}

func targetCallback(chatID int64, data string) *CallbackQuery {
	return &CallbackQuery{ID: "cb", From: User{ID: 7, FirstName: "Admin"},
		Message: &Message{MessageID: 1, Chat: Chat{ID: chatID, Type: "group"}}, Data: &data}
}

// The menu entry: reachable from the events menu, exactly where the
// tournament subscribe flow already lives.
func TestTargets_MenuIsReachableFromEventsMenu(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "menu:events")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "menu:targets") {
		t.Fatalf("no follow-a-team entry on the events menu: %v", cds)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "menu:targets")); err != nil {
		t.Fatal(err)
	}
	cds, _ = findKeyboardButtons(*calls)
	if !slices.Contains(cds, "targets:search") || !slices.Contains(cds, "targets:mine") {
		t.Fatalf("targets menu missing follow/mine buttons: %v", cds)
	}
}

// Search-then-pick, end to end: a team name reply lists candidates, and
// tapping one actually creates the subscription.
func TestTargets_SearchPickAndSubscribe(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	teamID := common.NewTeamID()
	catalog := &dataCatalog{teams: []competition.Team{{ID: teamID, Name: "Astralis"}}}
	handler.Catalog = catalog
	targets := &fakeTargets{}
	handler.Targets = targets

	promptText := stripHTML(handler.Texts.Get("targets.search_prompt", settings.Locale))
	replyText := "Astralis"
	msg := &Message{
		MessageID: 2, Chat: Chat{ID: settings.ChatID.Value, Type: "group"}, Text: &replyText,
		From:           &User{ID: 7},
		ReplyToMessage: &Message{Text: &promptText},
	}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	var subscribeCD string
	for _, cd := range cds {
		if strings.HasPrefix(cd, "targets:sub:") {
			subscribeCD = cd
		}
	}
	if subscribeCD == "" {
		t.Fatalf("no subscribe candidate rendered: %v", cds)
	}
	labels := allButtonLabels(*calls)
	if !strings.Contains(labels[subscribeCD], "Astralis") {
		t.Fatalf("candidate button does not name the team: %q", labels[subscribeCD])
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, subscribeCD)); err != nil {
		t.Fatal(err)
	}
	subs, err := targets.TargetSubscriptions(context.Background(), settings.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].TargetName != "Astralis" || !subs[0].Active {
		t.Fatalf("subscription not created as expected: %+v", subs)
	}
}

// Unsubscribing from the "mine" screen actually clears it.
func TestTargets_ListAndUnsubscribe(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	teamID := common.NewTeamID()
	handler.Catalog = &dataCatalog{teams: []competition.Team{{ID: teamID, Name: "Astralis"}}}
	targets := &fakeTargets{}
	handler.Targets = targets
	if _, err := targets.SubscribeTarget(context.Background(), subscription.TargetSubscription{
		ChatID: settings.ChatID, Kind: subscription.TargetTeam, TargetID: teamID.Value.String(),
		TargetName: "Astralis", Active: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "targets:mine")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	var unsubCD string
	for _, cd := range cds {
		if strings.HasPrefix(cd, "targets:unsub:") {
			unsubCD = cd
		}
	}
	if unsubCD == "" {
		t.Fatalf("no unfollow button for the existing subscription: %v", cds)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, unsubCD)); err != nil {
		t.Fatal(err)
	}
	subs, err := targets.TargetSubscriptions(context.Background(), settings.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Active {
		t.Fatalf("unsubscribe did not take effect: %+v", subs)
	}
}
