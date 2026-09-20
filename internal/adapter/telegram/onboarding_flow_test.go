package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// A bot added to a group used to say nothing, and a group that says
// nothing back stays silent forever: no games, no tournaments, no polls,
// and nothing on screen explaining why. These cover the two moments where
// the bot now says what happens next.

func addedToChat(chatID int64, title string) *ChatMemberUpdated {
	return &ChatMemberUpdated{
		Chat:          Chat{ID: chatID, Type: "supergroup", Title: &title},
		NewChatMember: ChatMember{Status: "administrator"},
	}
}

func TestOnboarding_GreetsAGroupTheMomentItIsAdded(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, chats := newTestHandler(t, server)

	if err := h.handleMyChatMember(context.Background(), addedToChat(-100, "Прогнозы")); err != nil {
		t.Fatal(err)
	}

	// The chat is recorded, so everything else in the bot can find it.
	settings, err := chats.Find(context.Background(), common.ChatID{Value: -100})
	if err != nil {
		t.Fatal(err)
	}
	if settings == nil || !settings.Active || settings.Title != "Прогнозы" {
		t.Fatalf("expected the chat to be recorded as active, got %+v", settings)
	}

	if len(*calls) == 0 {
		t.Fatal("expected the bot to introduce itself")
	}
	text, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(text, strings.SplitN(ru(t, "onboarding.welcome"), "<", 2)[0]) && !strings.Contains(text, "Привет") {
		t.Fatalf("expected the welcome text, got %q", text)
	}
	// And the one step that unblocks everything else is a tap away.
	markup, _ := json.Marshal((*calls)[0]["reply_markup"])
	if !strings.Contains(string(markup), "settings:games") {
		t.Fatalf("expected a route to the games screen, got %s", markup)
	}
}

// Being removed and re-added must not produce a second introduction: the
// row already exists by then, and a group does not need to be told what
// the bot is twice.
func TestOnboarding_DoesNotRepeatItselfForAChatItAlreadyKnows(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, chats := newTestHandler(t, server)
	chatID := common.ChatID{Value: -100}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: false}); err != nil {
		t.Fatal(err)
	}

	if err := h.handleMyChatMember(context.Background(), addedToChat(-100, "Прогнозы")); err != nil {
		t.Fatal(err)
	}

	if len(*calls) != 0 {
		t.Fatalf("expected no second introduction, got %v", *calls)
	}
	settings, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Active {
		t.Fatal("re-adding the bot must still reactivate the chat")
	}
}

// The first game on its own produces nothing: without a tournament there
// are no matches to poll about. That is the moment to say so.
func TestOnboarding_PointsAtATournamentAfterTheFirstGame(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, chats := newTestHandler(t, server)
	h.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	settings, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.setGameEnabled(context.Background(), *settings, competition.GameCS2, true, &User{ID: 1, FirstName: "Admin"}); err != nil {
		t.Fatal(err)
	}

	var nudged bool
	for _, c := range *calls {
		if text, _ := c["text"].(string); strings.Contains(text, ru(t, "onboarding.pick_tournament")) {
			nudged = true
		}
	}
	if !nudged {
		t.Fatal("expected the chat to be told a tournament is still missing")
	}
}

// A chat already following something is past onboarding: switching another
// game on must not send it back to step two.
func TestOnboarding_StaysQuietForAChatThatAlreadyFollowsATournament(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	h, chats := newTestHandler(t, server)
	h.Authorization = chat.NewAuthorizationService(chats, fakeMembership{role: chat.RoleAdministrator})
	h.Subscriptions = fakeSubs{}
	chatID := common.ChatID{Value: -1}
	if _, err := chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(context.Background(), chatID, []competition.GameCode{competition.GameCS2}); err != nil {
		t.Fatal(err)
	}
	settings, err := chats.Find(context.Background(), chatID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.setGameEnabled(context.Background(), *settings, competition.GameDota2, true, &User{ID: 1, FirstName: "Admin"}); err != nil {
		t.Fatal(err)
	}

	for _, c := range *calls {
		if text, _ := c["text"].(string); strings.Contains(text, ru(t, "onboarding.pick_tournament")) {
			t.Fatal("a chat past its first game must not be nudged again")
		}
	}
}
