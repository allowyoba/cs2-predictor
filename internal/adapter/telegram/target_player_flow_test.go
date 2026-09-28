package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeRosters is a player catalogue over a fixed list: nickname substring
// search and lookup by id, which is all the follow flow reads.
type fakeRosters struct {
	players []competition.Player
}

func (f *fakeRosters) SavePlayer(context.Context, competition.GameCode, string, string, competition.Player) (competition.Player, error) {
	return competition.Player{}, nil
}
func (f *fakeRosters) ReplaceRoster(context.Context, common.TeamID, []competition.RosterMember) error {
	return nil
}
func (f *fakeRosters) Roster(context.Context, common.TeamID) ([]competition.RosterMember, error) {
	return nil, nil
}
func (f *fakeRosters) FindPlayer(_ context.Context, id common.PlayerID) (*competition.Player, error) {
	for _, p := range f.players {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, nil
}
func (f *fakeRosters) SearchPlayers(_ context.Context, query string, _ int, games []competition.GameCode) ([]competition.Player, error) {
	if len(games) == 0 {
		return nil, nil
	}
	var out []competition.Player
	for _, p := range f.players {
		if strings.Contains(strings.ToLower(p.Nickname), strings.ToLower(query)) {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeRosters) TeamsOfPlayer(context.Context, common.PlayerID) ([]common.TeamID, error) {
	return nil, nil
}

// The player entry sits on the same menu as the team one.
func TestPlayers_MenuOffersThePlayerEntry(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "menu:targets")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "targets:psearch") {
		t.Fatalf("no follow-a-player entry on the targets menu: %v", cds)
	}
}

// Search-then-pick for a player, end to end: a nickname reply lists
// candidates, and tapping one creates the subscription under the nickname
// rather than the id.
func TestPlayers_SearchPickAndSubscribe(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	playerID := common.NewPlayerID()
	handler.Rosters = &fakeRosters{players: []competition.Player{
		{ID: playerID, Game: competition.GameCS2, Nickname: "s1mple", FullName: "Oleksandr Kostyliev"},
	}}
	targets := &fakeTargets{}
	handler.Targets = targets

	promptText := stripHTML(handler.Texts.Get("targets.search_player_prompt", settings.Locale))
	replyText := "s1mp"
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
		if strings.HasPrefix(cd, "targets:psub:") {
			subscribeCD = cd
		}
	}
	if subscribeCD == "" {
		t.Fatalf("no player candidate rendered: %v", cds)
	}
	if label := allButtonLabels(*calls)[subscribeCD]; !strings.Contains(label, "s1mple") {
		t.Fatalf("candidate button does not name the player: %q", label)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, subscribeCD)); err != nil {
		t.Fatal(err)
	}
	subs, err := targets.TargetSubscriptions(context.Background(), settings.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Kind != subscription.TargetPlayer || subs[0].TargetName != "s1mple" || !subs[0].Active {
		t.Fatalf("player subscription not created as expected: %+v", subs)
	}
}

// Both kinds show up on the "what this chat follows" screen, each with its
// own unfollow button — a player listed with the team unfollow callback would
// silently fail to remove anything.
func TestPlayers_ListedAlongsideTeamsWithTheirOwnUnfollow(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	playerID := common.NewPlayerID()
	teamID := common.NewTeamID()
	targets := &fakeTargets{subs: []subscription.TargetSubscription{
		{ChatID: settings.ChatID, Kind: subscription.TargetTeam, TargetID: teamID.Value.String(), TargetName: "Astralis", Active: true},
		{ChatID: settings.ChatID, Kind: subscription.TargetPlayer, TargetID: playerID.Value.String(), TargetName: "s1mple", Active: true},
	}}
	handler.Targets = targets

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "targets:mine")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, cbTargetUnsubscribe(teamID.Value.String())) {
		t.Fatalf("no team unfollow button: %v", cds)
	}
	if !slices.Contains(cds, cbPlayerUnsubscribe(playerID.Value.String())) {
		t.Fatalf("no player unfollow button: %v", cds)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, cbPlayerUnsubscribe(playerID.Value.String()))); err != nil {
		t.Fatal(err)
	}
	remaining, err := targets.TargetSubscriptions(context.Background(), settings.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range remaining {
		if s.Kind == subscription.TargetPlayer && s.Active {
			t.Fatalf("player subscription survived the unfollow: %+v", remaining)
		}
	}
}
