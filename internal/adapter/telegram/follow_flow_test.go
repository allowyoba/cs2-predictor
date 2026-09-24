package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeTargets is an in-memory subscription.TargetRepository.
type fakeTargets struct {
	byChat  map[common.ChatID][]subscription.Target
	rosters map[string][]common.TeamID
	search  []subscription.Target
}

func newFakeTargets() *fakeTargets {
	return &fakeTargets{byChat: map[common.ChatID][]subscription.Target{}, rosters: map[string][]common.TeamID{}}
}

func (f *fakeTargets) SubscribeTarget(_ context.Context, chatID common.ChatID, t subscription.Target, _ time.Time) error {
	f.byChat[chatID] = append(f.byChat[chatID], t)
	return nil
}
func (f *fakeTargets) UnsubscribeTarget(_ context.Context, chatID common.ChatID, t subscription.Target) error {
	var kept []subscription.Target
	for _, existing := range f.byChat[chatID] {
		if existing.Key() != t.Key() {
			kept = append(kept, existing)
		}
	}
	f.byChat[chatID] = kept
	return nil
}
func (f *fakeTargets) Targets(_ context.Context, chatID common.ChatID) ([]subscription.Target, error) {
	return f.byChat[chatID], nil
}
func (f *fakeTargets) ResolveTeams(_ context.Context, targets []subscription.Target) (map[string][]common.TeamID, error) {
	out := map[string][]common.TeamID{}
	for _, t := range targets {
		if t.Kind == subscription.TargetTeam {
			out[t.Key()] = []common.TeamID{t.TeamID}
		} else if t.Kind == subscription.TargetPlayer {
			out[t.Key()] = f.rosters[t.Player]
		}
	}
	return out, nil
}
func (f *fakeTargets) ChatsFollowingTeams(context.Context, []common.TeamID) ([]common.ChatID, error) {
	return nil, nil
}
func (f *fakeTargets) FollowedEventIDs(context.Context) ([]common.EventID, error) { return nil, nil }
func (f *fakeTargets) SearchTargets(context.Context, string, int) ([]subscription.Target, error) {
	return f.search, nil
}
func (f *fakeTargets) ClaimCrossSellOffer(context.Context, common.ChatID, common.EventID, time.Time, time.Duration) (bool, error) {
	return true, nil
}

// recordingScoring remembers every leaderboard period asked for.
type recordingScoring struct {
	fakeScoring
	periods []scoring.StatsPeriod
}

func (r *recordingScoring) Leaderboard(_ context.Context, _ common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	r.periods = append(r.periods, period)
	return nil, nil
}

func TestTargetToken_RoundTrips(t *testing.T) {
	team := subscription.TeamTarget(common.NewTeamID(), "NAVI")
	player := subscription.PlayerTarget("s1mple")
	for _, target := range []subscription.Target{team, player} {
		token, ok := targetToken(target)
		if !ok {
			t.Fatalf("no token for %+v", target)
		}
		back, err := parseTargetToken(token)
		if err != nil || back.Key() != target.Key() {
			t.Fatalf("round trip of %q = %+v, %v", token, back, err)
		}
		if len("c:zzzzzzzzzzzz|stats:follow:"+token) > 64 {
			t.Fatalf("callback for %q exceeds Telegram's 64 bytes", token)
		}
	}
	if _, ok := targetToken(subscription.PlayerTarget(strings.Repeat("x", 40))); ok {
		t.Fatal("an overlong player name must not produce a callback")
	}
}

// Every tournament board the bot renders goes through the scoping helper:
// a team follower not subscribed to the tournament gets only its slice.
func TestTournamentBoard_IsSlicedForAFollowerChat(t *testing.T) {
	srv, _ := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleEN, Timezone: chat.DefaultTimezone, Active: true})
	targets := newFakeTargets()
	team := common.NewTeamID()
	_ = targets.SubscribeTarget(context.Background(), chatID, subscription.TeamTarget(team, "NAVI"), time.Time{})
	_ = targets.SubscribeTarget(context.Background(), chatID, subscription.PlayerTarget("s1mple"), time.Time{})
	targets.rosters["s1mple"] = []common.TeamID{team}
	rec := &recordingScoring{}
	handler.Scoring = rec
	handler.Scopes = app.SubscriptionScopes{Targets: targets}

	if _, err := handler.leaderboard(context.Background(), chatID, scoring.ForEvent(common.NewEventID())); err != nil {
		t.Fatal(err)
	}
	got := rec.periods[0]
	if got.Teams == nil || len(got.Teams.IDs) != 1 || got.Teams.IDs[0] != team {
		t.Fatalf("board period teams = %+v, want exactly the one followed team", got.Teams)
	}
}

func TestFollowCallback_SubscribesAndStatsCutUsesTheTeam(t *testing.T) {
	srv, calls := newRecordingServer(t)
	defer srv.Close()
	handler, chats := newTestHandler(t, srv)
	chatID := common.ChatID{Value: -1}
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: chatID, Locale: common.LocaleEN, Timezone: chat.DefaultTimezone, Active: true})
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleAdministrator})
	targets := newFakeTargets()
	rec := &recordingScoring{}
	handler.Scoring = rec
	handler.Scopes = app.SubscriptionScopes{Targets: targets}
	handler.Follows = &app.FollowService{Targets: targets, Catalog: handler.Catalog, Chats: chats, Clock: handler.Clock, Log: handler.Log}

	team := subscription.TeamTarget(common.NewTeamID(), "")
	token, _ := targetToken(team)
	tap := func(data string) {
		cb := &CallbackQuery{ID: "cb", From: User{ID: 1, FirstName: "Admin"}, Message: &Message{Chat: Chat{ID: -1, Type: "group"}}, Data: &data}
		if err := handler.handleCallback(context.Background(), cb); err != nil {
			t.Fatal(err)
		}
	}
	tap("follow:" + token)
	if len(targets.byChat[chatID]) != 1 {
		t.Fatalf("targets = %+v, want the team", targets.byChat[chatID])
	}
	tap("stats:follow:" + token)
	if len(rec.periods) != 1 || rec.periods[0].Teams == nil || rec.periods[0].Teams.IDs[0] != team.TeamID {
		t.Fatalf("stats cut periods = %+v", rec.periods)
	}
	if len(*calls) == 0 {
		t.Fatal("expected screens to be rendered")
	}

	// A plain member may not follow.
	handler.Authorization = chat.NewAuthorizationService(handler.Chats, fakeMembership{role: chat.RoleMember})
	other, _ := targetToken(subscription.PlayerTarget("zywoo"))
	tap("follow:" + other)
	if len(targets.byChat[chatID]) != 1 {
		t.Fatal("a plain member's follow must be denied")
	}
}

func TestBigEventPublisher_FollowCrossSellNamesTheTeamAndOffersSubscribe(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(Config{BaseURL: server.URL, Token: "test-token"}, server.Client())
	chats := newFakeChats()
	_, _ = chats.Save(context.Background(), chat.Settings{ChatID: common.ChatID{Value: -1}, Locale: common.LocaleEN, Timezone: chat.DefaultTimezone, Active: true})
	pub := NewBigEventPublisher(client, chats, texts)
	if !pub.Supports("telegram.follow-cross-sell") {
		t.Fatal("the cross-sell must reuse the big-event publisher")
	}
	eventID := "11111111-1111-1111-1111-111111111111"
	payload, _ := json.Marshal(common.BigEventDiscoveredNotification{ChatID: -1, EventID: eventID, EventName: "Major", Tier: "s", Reason: "NAVI"})
	if err := pub.Publish(context.Background(), common.OutboxMessage{Type: "telegram.follow-cross-sell", Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
	text, _ := (*calls)[0]["text"].(string)
	if !strings.Contains(text, "NAVI") || !strings.Contains(text, "Major") {
		t.Fatalf("text = %q", text)
	}
	raw, _ := json.Marshal((*calls)[0]["reply_markup"])
	if !strings.Contains(string(raw), "subscribe:"+eventID) {
		t.Fatalf("expected a one-tap subscribe button, got %s", raw)
	}
}
