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

// The reported failure: there was no way to add a team or a player without
// knowing the exact spelling and replying to a ForceReply. Both lists now
// open straight from the menu.
func TestBrowse_TeamListOpensFromTheMenuAndSubscribes(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	spirit := common.NewTeamID()
	handler.Catalog = &dataCatalog{teams: []competition.Team{
		{ID: spirit, Name: "Team Spirit"}, {ID: common.NewTeamID(), Name: "Natus Vincere"},
	}}
	targets := &fakeTargets{}
	handler.Targets = targets

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "targets:browse:0")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	want := "targets:sub:" + compactUUIDString(spirit.Value.String())
	if !slices.Contains(cds, want) {
		t.Fatalf("the team list has no subscribe button for Spirit: %v", cds)
	}
	// Typing stays available, one tap away rather than being the only way.
	if !slices.Contains(cds, "targets:search") {
		t.Fatalf("the list must still offer search: %v", cds)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, want)); err != nil {
		t.Fatal(err)
	}
	subs, err := targets.TargetSubscriptions(context.Background(), settings.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].TargetName != "Team Spirit" {
		t.Fatalf("browsing did not create the subscription: %+v", subs)
	}
}

func TestBrowse_PlayerListOpensFromTheMenuAndSubscribes(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	ropz := common.NewPlayerID()
	handler.Rosters = &fakeRosters{players: []competition.Player{
		{ID: ropz, Game: competition.GameCS2, Nickname: "r0pz", FullName: "Robin Kool"},
	}}
	targets := &fakeTargets{}
	handler.Targets = targets

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "targets:pbrowse:0")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	want := "targets:psub:" + compactUUIDString(ropz.Value.String())
	if !slices.Contains(cds, want) {
		t.Fatalf("the player list has no subscribe button for r0pz: %v", cds)
	}
	if label := allButtonLabels(*calls)[want]; !strings.Contains(label, "r0pz") {
		t.Fatalf("the entry does not name the player: %q", label)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, want)); err != nil {
		t.Fatal(err)
	}
	subs, err := targets.TargetSubscriptions(context.Background(), settings.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Kind != subscription.TargetPlayer || subs[0].TargetName != "r0pz" {
		t.Fatalf("browsing did not create the player subscription: %+v", subs)
	}
}

// Something already followed is shown as followed rather than dropped from
// the list — seeing it is how you notice you already have it.
func TestBrowse_MarksWhatIsAlreadyFollowed(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	spirit := common.NewTeamID()
	handler.Catalog = &dataCatalog{teams: []competition.Team{{ID: spirit, Name: "Team Spirit"}}}
	handler.Targets = &fakeTargets{subs: []subscription.TargetSubscription{{
		ChatID: settings.ChatID, Kind: subscription.TargetTeam,
		TargetID: spirit.Value.String(), TargetName: "Team Spirit", Active: true,
	}}}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "targets:browse:0")); err != nil {
		t.Fatal(err)
	}
	label := allButtonLabels(*calls)["targets:sub:"+compactUUIDString(spirit.Value.String())]
	if !strings.HasPrefix(label, "✅") {
		t.Fatalf("an already-followed team must be ticked, got %q", label)
	}
}

// An empty catalogue says why it is empty and still offers a way forward,
// rather than rendering a page with nothing on it.
func TestBrowse_EmptyListExplainsItselfAndStillOffersSearch(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})
	handler.Rosters = &fakeRosters{}
	handler.Targets = &fakeTargets{}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "targets:pbrowse:0")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "targets:psearch") {
		t.Fatalf("an empty list must still offer search: %v", cds)
	}
	var body string
	for _, c := range *calls {
		if text, ok := c["text"].(string); ok {
			body += text + "\n"
		}
	}
	if !strings.Contains(body, stripHTML(handler.Texts.Get("targets.browse_players_empty", settings.Locale))) {
		t.Fatalf("an empty list must say why it is empty: %q", body)
	}
}
