package app

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeRosterCatalog answers the three reads RosterSync makes: which events
// are followed, what game each is, and which matches are still to be played.
type fakeRosterCatalog struct {
	fakeCatalogForCompletion
	events   []competition.Event
	playable []competition.Match
}

func (f *fakeRosterCatalog) FindEvents(context.Context, []common.EventID) ([]competition.Event, error) {
	return f.events, nil
}
func (f *fakeRosterCatalog) FindPlayableMatchesForEvents(context.Context, []common.EventID) ([]competition.Match, error) {
	return f.playable, nil
}

type fakeRosterSubs struct {
	fakeOfferSubs
	active []common.EventID
}

func (f *fakeRosterSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) {
	return f.active, nil
}

// fakeRosterProvider reports a fixed roster per external team id, and records
// what it was asked about.
type fakeRosterProvider struct {
	byExternalID map[string][]competition.ProviderPlayer
	// appearance is the crest and country the full team object carries,
	// keyed by external team id.
	appearance map[string][2]string
	asked      []string
}

func (f *fakeRosterProvider) Rosters(_ context.Context, _ competition.GameCode, externalTeamIDs []string) ([]competition.ProviderRoster, error) {
	f.asked = append(f.asked, externalTeamIDs...)
	var out []competition.ProviderRoster
	for _, id := range externalTeamIDs {
		players, ok := f.byExternalID[id]
		if !ok {
			continue // the provider knows nothing about this team
		}
		roster := competition.ProviderRoster{ExternalTeamID: id, Players: players}
		if look, ok := f.appearance[id]; ok {
			roster.LogoURL, roster.Location = look[0], look[1]
		}
		out = append(out, roster)
	}
	return out, nil
}

// fakePlayerStore is the player catalogue in memory, keyed the way the real
// one is: by each feed's own external id.
type fakePlayerStore struct {
	byExternalID map[string]competition.Player
	rosters      map[common.TeamID][]competition.RosterMember
	identities   map[string]common.PlayerID // source+":"+externalID
}

func newPlayerStore() *fakePlayerStore {
	return &fakePlayerStore{
		byExternalID: map[string]competition.Player{},
		rosters:      map[common.TeamID][]competition.RosterMember{},
		identities:   map[string]common.PlayerID{},
	}
}

func (f *fakePlayerStore) SavePlayer(_ context.Context, game competition.GameCode, source, externalID string,
	player competition.Player) (competition.Player, error) {
	key := source + ":" + externalID
	existing, ok := f.byExternalID[key]
	if !ok {
		player.ID = common.NewPlayerID()
	} else {
		player.ID = existing.ID
	}
	player.Game = game
	f.byExternalID[key] = player
	f.identities[key] = player.ID
	return player, nil
}

func (f *fakePlayerStore) ReplaceRoster(_ context.Context, teamID common.TeamID, members []competition.RosterMember) error {
	f.rosters[teamID] = members
	return nil
}

func (f *fakePlayerStore) Roster(_ context.Context, teamID common.TeamID) ([]competition.RosterMember, error) {
	return f.rosters[teamID], nil
}

func (f *fakePlayerStore) FindPlayer(context.Context, common.PlayerID) (*competition.Player, error) {
	return nil, nil
}
func (f *fakePlayerStore) SearchPlayers(context.Context, string, int, []competition.GameCode) ([]competition.Player, error) {
	return nil, nil
}
func (f *fakePlayerStore) TeamsOfPlayer(context.Context, common.PlayerID) ([]common.TeamID, error) {
	return nil, nil
}

func (f *fakePlayerStore) SavePlayerIdentity(_ context.Context, id common.PlayerID, source, externalID,
	_, _ string) (bool, error) {
	key := source + ":" + externalID
	if owner, taken := f.identities[key]; taken && owner != id {
		return false, nil
	}
	f.identities[key] = id
	return true, nil
}

// fakeHLTVRankings reports HLTV's roster for a team, which is the only place
// HLTV's player nicknames come from.
type fakeHLTVRankings struct {
	rosters map[common.TeamID][]string
}

func (f *fakeHLTVRankings) SaveRanking(context.Context, enrichment.TeamRanking) error { return nil }
func (f *fakeHLTVRankings) FindRanking(context.Context, common.TeamID, enrichment.Source) (*enrichment.TeamRanking, error) {
	return nil, nil
}
func (f *fakeHLTVRankings) FindRankings(_ context.Context, teamIDs []common.TeamID,
	source enrichment.Source) (map[common.TeamID]enrichment.TeamRanking, error) {
	out := map[common.TeamID]enrichment.TeamRanking{}
	if source != enrichment.SourceHLTV {
		return out, nil
	}
	for _, id := range teamIDs {
		if roster, ok := f.rosters[id]; ok {
			out[id] = enrichment.TeamRanking{TeamID: id, Source: source, Roster: roster}
		}
	}
	return out, nil
}

func rosterMatch(eventID common.EventID, first, second competition.Team) competition.Match {
	return competition.Match{
		ID: common.NewMatchID(), EventID: eventID,
		FirstTeam: &first, SecondTeam: &second,
	}
}

func newRosterSync(catalog competition.Catalog, subs subscription.Repository, provider competition.RosterProvider,
	players PlayerStore, rankings enrichment.RankingRepository) *RosterSync {
	return &RosterSync{
		Catalog: catalog, Subscriptions: subs, Provider: provider, Players: players,
		Rankings: rankings, Lock: fakeClusterLock{}, Log: slog.Default(),
	}
}

// The whole point: PandaScore's players get stored, and HLTV's nicknames for
// the same people get attached to them rather than becoming separate records.
func TestRosterSync_PairsPandaScorePlayersWithHLTVNicknames(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	navi := competition.Team{ID: common.NewTeamID(), Name: "Natus Vincere", ExternalID: "100"}
	vitality := competition.Team{ID: common.NewTeamID(), Name: "Vitality", ExternalID: "200"}

	catalog := &fakeRosterCatalog{
		events:   []competition.Event{{ID: eventID, Game: competition.GameCS2}},
		playable: []competition.Match{rosterMatch(eventID, navi, vitality)},
	}
	provider := &fakeRosterProvider{byExternalID: map[string][]competition.ProviderPlayer{
		"100": {{ExternalID: "1", Nickname: "s1mple"}, {ExternalID: "2", Nickname: "b1t"}},
		"200": {{ExternalID: "3", Nickname: "ZywOo"}},
	}}
	players := newPlayerStore()
	// HLTV spells them differently in case, and lists a stand-in PandaScore
	// has not caught up with.
	rankings := &fakeHLTVRankings{rosters: map[common.TeamID][]string{
		navi.ID:     {"S1MPLE", "b1t", "someStandIn"},
		vitality.ID: {"ZywOo"},
	}}

	newRosterSync(catalog, &fakeRosterSubs{active: []common.EventID{eventID}}, provider, players, rankings).
		Dispatch(context.Background())

	if len(players.rosters[navi.ID]) != 2 || len(players.rosters[vitality.ID]) != 1 {
		t.Fatalf("expected both rosters stored, got %+v", players.rosters)
	}
	for _, nickname := range []string{"s1mple", "b1t", "zywoo"} {
		if _, paired := players.identities["HLTV:"+nickname]; !paired {
			t.Fatalf("expected HLTV's %q paired with a stored player, got %v", nickname, players.identities)
		}
	}
	// The player PandaScore has never heard of must not be invented.
	if _, invented := players.identities["HLTV:somestandin"]; invented {
		t.Fatal("an unmatched HLTV nickname must not create an identity")
	}
	// And the pairing must point at the player it belongs to, not just exist.
	naviPlayers := map[string]common.PlayerID{}
	for _, member := range players.rosters[navi.ID] {
		naviPlayers[member.Player.Nickname] = member.Player.ID
	}
	if players.identities["HLTV:s1mple"] != naviPlayers["s1mple"] {
		t.Fatal("HLTV's s1mple is attached to the wrong player")
	}
}

// HLTV ranks Counter-Strike and nothing else, so a Dota 2 roster must never
// be run past it — the same rule the team pipeline follows.
func TestRosterSync_DoesNotAskHLTVAboutDota(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	team := competition.Team{ID: common.NewTeamID(), Name: "Team Spirit", ExternalID: "300"}
	other := competition.Team{ID: common.NewTeamID(), Name: "Falcons", ExternalID: "301"}

	catalog := &fakeRosterCatalog{
		events:   []competition.Event{{ID: eventID, Game: competition.GameDota2}},
		playable: []competition.Match{rosterMatch(eventID, team, other)},
	}
	provider := &fakeRosterProvider{byExternalID: map[string][]competition.ProviderPlayer{
		"300": {{ExternalID: "9", Nickname: "Yatoro"}},
	}}
	players := newPlayerStore()
	// A nickname collision waiting to happen if HLTV were consulted here.
	rankings := &fakeHLTVRankings{rosters: map[common.TeamID][]string{team.ID: {"Yatoro"}}}

	newRosterSync(catalog, &fakeRosterSubs{active: []common.EventID{eventID}}, provider, players, rankings).
		Dispatch(context.Background())

	if len(players.rosters[team.ID]) != 1 {
		t.Fatalf("expected the Dota 2 roster stored, got %+v", players.rosters)
	}
	if _, paired := players.identities["HLTV:yatoro"]; paired {
		t.Fatal("HLTV must not be consulted about a Dota 2 roster")
	}
}

// A team the provider has no answer for keeps whatever roster is on record:
// "nothing published" is not "nobody on the team".
func TestRosterSync_KeepsAStoredRosterWhenTheProviderSaysNothing(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	team := competition.Team{ID: common.NewTeamID(), Name: "Astralis", ExternalID: "400"}
	other := competition.Team{ID: common.NewTeamID(), Name: "MOUZ", ExternalID: "401"}

	catalog := &fakeRosterCatalog{
		events:   []competition.Event{{ID: eventID, Game: competition.GameCS2}},
		playable: []competition.Match{rosterMatch(eventID, team, other)},
	}
	players := newPlayerStore()
	existing := []competition.RosterMember{{Player: competition.Player{ID: common.NewPlayerID(), Nickname: "device"}}}
	players.rosters[team.ID] = existing

	newRosterSync(catalog, &fakeRosterSubs{active: []common.EventID{eventID}},
		&fakeRosterProvider{byExternalID: map[string][]competition.ProviderPlayer{}}, players,
		&fakeHLTVRankings{}).Dispatch(context.Background())

	if len(players.rosters[team.ID]) != 1 || players.rosters[team.ID][0].Player.Nickname != "device" {
		t.Fatalf("expected the stored roster untouched, got %+v", players.rosters[team.ID])
	}
}

// Nobody is followed, nothing is played, no request is made.
func TestRosterSync_AsksNothingWithNoSubscriptions(t *testing.T) {
	provider := &fakeRosterProvider{}
	newRosterSync(&fakeRosterCatalog{}, &fakeRosterSubs{}, provider, newPlayerStore(), &fakeHLTVRankings{}).
		Dispatch(context.Background())

	if len(provider.asked) != 0 {
		t.Fatalf("expected no provider call, asked about %v", provider.asked)
	}
}

// The same team in two matches is one request, not two.
func TestRosterSync_AsksAboutEachTeamOnce(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	navi := competition.Team{ID: common.NewTeamID(), Name: "Natus Vincere", ExternalID: "100"}
	vitality := competition.Team{ID: common.NewTeamID(), Name: "Vitality", ExternalID: "200"}
	mouz := competition.Team{ID: common.NewTeamID(), Name: "MOUZ", ExternalID: "300"}

	catalog := &fakeRosterCatalog{
		events: []competition.Event{{ID: eventID, Game: competition.GameCS2}},
		playable: []competition.Match{
			rosterMatch(eventID, navi, vitality),
			rosterMatch(eventID, navi, mouz),
		},
	}
	provider := &fakeRosterProvider{byExternalID: map[string][]competition.ProviderPlayer{}}
	newRosterSync(catalog, &fakeRosterSubs{active: []common.EventID{eventID}}, provider, newPlayerStore(),
		&fakeHLTVRankings{}).Dispatch(context.Background())

	if len(provider.asked) != 3 {
		t.Fatalf("expected each of the three teams asked about once, got %v", provider.asked)
	}
}

// fakeAppearance records the crests handed to the catalogue, and refuses to
// overwrite one it already has — the same "fill the gap, never take over the
// column" rule the real UPDATE follows.
type fakeAppearance struct {
	logos     map[common.TeamID]string
	locations map[common.TeamID]string
}

func newAppearance() *fakeAppearance {
	return &fakeAppearance{logos: map[common.TeamID]string{}, locations: map[common.TeamID]string{}}
}

func (f *fakeAppearance) FillTeamAppearance(_ context.Context, teamID common.TeamID, logoURL, location string) error {
	if logoURL != "" && f.logos[teamID] == "" {
		f.logos[teamID] = logoURL
	}
	if location != "" && f.locations[teamID] == "" {
		f.locations[teamID] = location
	}
	return nil
}

// The crest gap this closes: 71 of the 100 CS2 teams most recently in play
// had a country stored and no picture at all, because the opponent object
// inside a match payload carried no image_url. The provider's full team
// object — the same response the roster already comes from — has one.
func TestRosterSync_FillsTheCrestFromTheFullTeamObject(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	astralis := competition.Team{ID: common.NewTeamID(), Name: "Astralis", ExternalID: "500"}
	nip := competition.Team{ID: common.NewTeamID(), Name: "NIP", ExternalID: "501"}

	catalog := &fakeRosterCatalog{
		events:   []competition.Event{{ID: eventID, Game: competition.GameCS2}},
		playable: []competition.Match{rosterMatch(eventID, astralis, nip)},
	}
	provider := &fakeRosterProvider{
		byExternalID: map[string][]competition.ProviderPlayer{
			"500": {{ExternalID: "11", Nickname: "device"}},
			// NIP answers with a crest and an empty roster: the two are
			// independent, and the picture is still worth having.
			"501": {},
		},
		appearance: map[string][2]string{
			"500": {"https://cdn.pandascore.co/astralis.png", "DK"},
			"501": {"https://cdn.pandascore.co/nip.png", "SE"},
		},
	}
	appearance := newAppearance()
	sync := newRosterSync(catalog, &fakeRosterSubs{active: []common.EventID{eventID}}, provider, newPlayerStore(),
		&fakeHLTVRankings{})
	sync.Appearance = appearance

	sync.Dispatch(context.Background())

	if got := appearance.logos[astralis.ID]; got != "https://cdn.pandascore.co/astralis.png" {
		t.Fatalf("expected Astralis's crest filled in, got %q", got)
	}
	if got := appearance.logos[nip.ID]; got != "https://cdn.pandascore.co/nip.png" {
		t.Fatalf("a team with no roster must still get its crest, got %q", got)
	}
	if got := appearance.locations[astralis.ID]; got != "DK" {
		t.Fatalf("expected the country filled in too, got %q", got)
	}
}

// With no appearance writer wired the rosters still sync — the crest fill is
// a feature that is off, not a sync that fails.
func TestRosterSync_WorksWithoutAnAppearanceWriter(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	team := competition.Team{ID: common.NewTeamID(), Name: "Astralis", ExternalID: "500"}
	other := competition.Team{ID: common.NewTeamID(), Name: "NIP", ExternalID: "501"}

	catalog := &fakeRosterCatalog{
		events:   []competition.Event{{ID: eventID, Game: competition.GameCS2}},
		playable: []competition.Match{rosterMatch(eventID, team, other)},
	}
	provider := &fakeRosterProvider{
		byExternalID: map[string][]competition.ProviderPlayer{"500": {{ExternalID: "11", Nickname: "device"}}},
		appearance:   map[string][2]string{"500": {"https://cdn.pandascore.co/astralis.png", "DK"}},
	}
	players := newPlayerStore()
	newRosterSync(catalog, &fakeRosterSubs{active: []common.EventID{eventID}}, provider, players,
		&fakeHLTVRankings{}).Dispatch(context.Background())

	if len(players.rosters[team.ID]) != 1 {
		t.Fatalf("expected the roster stored regardless, got %+v", players.rosters)
	}
}
