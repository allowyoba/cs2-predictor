package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeTeamStatsSubs is a subscription.Repository fake exposing a fixed set
// of active event ids — everything else is unused by TeamStatsSync.
type fakeTeamStatsSubs struct {
	eventIDs []common.EventID
}

func (f *fakeTeamStatsSubs) Subscribe(context.Context, subscription.EventSubscription) (subscription.EventSubscription, error) {
	return subscription.EventSubscription{}, nil
}
func (f *fakeTeamStatsSubs) Unsubscribe(context.Context, common.ChatID, common.EventID) error {
	return nil
}
func (f *fakeTeamStatsSubs) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (f *fakeTeamStatsSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) {
	return f.eventIDs, nil
}
func (f *fakeTeamStatsSubs) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return nil, nil
}

// fakeTeamStatsCatalog is a competition.Catalog fake returning a fixed set
// of unstarted matches for FindUnstartedMatchesForEvents — everything else
// is unused by TeamStatsSync.
type fakeTeamStatsCatalog struct {
	matches []competition.Match
	err     error
}

func (f *fakeTeamStatsCatalog) SearchEvents(context.Context, string, int, bool, []competition.GameCode) ([]competition.Event, error) {
	return nil, nil
}
func (f *fakeTeamStatsCatalog) FindEvent(context.Context, common.EventID) (*competition.Event, error) {
	return nil, nil
}
func (f *fakeTeamStatsCatalog) FindEvents(context.Context, []common.EventID) ([]competition.Event, error) {
	return nil, nil
}
func (f *fakeTeamStatsCatalog) FindMatch(context.Context, common.MatchID) (*competition.Match, error) {
	return nil, nil
}
func (f *fakeTeamStatsCatalog) FindUnstartedMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeTeamStatsCatalog) FindUnstartedMatchesForEvents(context.Context, []common.EventID) ([]competition.Match, error) {
	return f.matches, f.err
}
func (f *fakeTeamStatsCatalog) FindMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeTeamStatsCatalog) SaveEvent(_ context.Context, e competition.Event) (competition.Event, error) {
	return e, nil
}
func (f *fakeTeamStatsCatalog) SaveMatch(_ context.Context, m competition.Match) (competition.Match, error) {
	return m, nil
}

type fakeTeamStatsProvider struct {
	forms map[string]enrichment.RecentForm // keyed by team name
	err   error
}

func (f *fakeTeamStatsProvider) GetTeamStats(_ context.Context, team enrichment.TeamIdentity) (*enrichment.RecentForm, error) {
	if f.err != nil {
		return nil, f.err
	}
	if form, ok := f.forms[team.Name]; ok {
		return &form, nil
	}
	return nil, nil
}

type fakeMatchStatsProvider struct {
	h2h *enrichment.HeadToHead
	err error
}

func (f *fakeMatchStatsProvider) GetHeadToHead(context.Context, enrichment.TeamIdentity, enrichment.TeamIdentity) (*enrichment.HeadToHead, error) {
	return f.h2h, f.err
}

func TestTeamStatsSync_EnrichesBothTeamsAndH2HForUpcomingSubscribedMatches(t *testing.T) {
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	navi := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	eventID := common.NewEventID()
	match := competition.Match{ID: common.NewMatchID(), EventID: eventID, FirstTeam: &spirit, SecondTeam: &navi}

	store := newFakeEnrichmentStore()
	sync := &TeamStatsSync{
		TeamStats: &fakeTeamStatsProvider{forms: map[string]enrichment.RecentForm{
			"Spirit": {Wins: 4, Losses: 1, Sample: 5, Source: enrichment.SourceGRID},
			"NAVI":   {Wins: 3, Losses: 2, Sample: 5, Source: enrichment.SourceGRID},
		}},
		MatchStats:    &fakeMatchStatsProvider{h2h: &enrichment.HeadToHead{TeamAWins: 6, TeamBWins: 4, Sample: 10, Source: enrichment.SourceGRID}},
		Catalog:       &fakeTeamStatsCatalog{matches: []competition.Match{match}},
		Subscriptions: &fakeTeamStatsSubs{eventIDs: []common.EventID{eventID}},
		Form:          store, H2H: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	gotSpirit, err := store.FindForm(context.Background(), spirit.ID, enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSpirit == nil || gotSpirit.Wins != 4 {
		t.Fatalf("Spirit form = %+v, want Wins=4", gotSpirit)
	}
	gotH2H, err := store.FindHeadToHead(context.Background(), spirit.ID, navi.ID, enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if gotH2H == nil || gotH2H.TeamAWins != 6 || gotH2H.TeamBWins != 4 {
		t.Fatalf("H2H = %+v, want TeamAWins=6 TeamBWins=4", gotH2H)
	}
	st, err := store.State(context.Background(), enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil {
		t.Fatalf("expected RecordSuccess to be called, state = %+v", st)
	}
}

func TestTeamStatsSync_NoActiveSubscriptionsIsANoOp(t *testing.T) {
	store := newFakeEnrichmentStore()
	sync := &TeamStatsSync{
		TeamStats:     &fakeTeamStatsProvider{},
		MatchStats:    &fakeMatchStatsProvider{},
		Catalog:       &fakeTeamStatsCatalog{},
		Subscriptions: &fakeTeamStatsSubs{},
		Form:          store, H2H: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil {
		t.Fatalf("an empty subscription set should still record a successful (no-op) run, state = %+v", st)
	}
}

func TestTeamStatsSync_MissingGRIDDataForATeamIsNotAFailure(t *testing.T) {
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	navi := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	eventID := common.NewEventID()
	match := competition.Match{ID: common.NewMatchID(), EventID: eventID, FirstTeam: &spirit, SecondTeam: &navi}

	store := newFakeEnrichmentStore()
	sync := &TeamStatsSync{
		// No forms configured — GRID simply has nothing for either team yet.
		TeamStats:     &fakeTeamStatsProvider{},
		MatchStats:    &fakeMatchStatsProvider{},
		Catalog:       &fakeTeamStatsCatalog{matches: []competition.Match{match}},
		Subscriptions: &fakeTeamStatsSubs{eventIDs: []common.EventID{eventID}},
		Form:          store, H2H: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil || st.ConsecutiveFailures != 0 {
		t.Fatalf("no cached GRID data yet must not count as a sync failure, state = %+v", st)
	}
}

func TestTeamStatsSync_EveryLookupErroringRecordsFailure(t *testing.T) {
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit"}
	navi := competition.Team{ID: common.NewTeamID(), Name: "NAVI"}
	eventID := common.NewEventID()
	match := competition.Match{ID: common.NewMatchID(), EventID: eventID, FirstTeam: &spirit, SecondTeam: &navi}

	store := newFakeEnrichmentStore()
	sync := &TeamStatsSync{
		TeamStats:     &fakeTeamStatsProvider{err: errors.New("grid unreachable")},
		MatchStats:    &fakeMatchStatsProvider{err: errors.New("grid unreachable")},
		Catalog:       &fakeTeamStatsCatalog{matches: []competition.Match{match}},
		Subscriptions: &fakeTeamStatsSubs{eventIDs: []common.EventID{eventID}},
		Form:          store, H2H: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveFailures == 0 {
		t.Fatalf("every lookup erroring should record a sync failure, state = %+v", st)
	}
}

func TestTeamStatsSync_RecordsFailureWhenCatalogLookupErrors(t *testing.T) {
	store := newFakeEnrichmentStore()
	sync := &TeamStatsSync{
		TeamStats:     &fakeTeamStatsProvider{},
		MatchStats:    &fakeMatchStatsProvider{},
		Catalog:       &fakeTeamStatsCatalog{err: errors.New("db down")},
		Subscriptions: &fakeTeamStatsSubs{eventIDs: []common.EventID{common.NewEventID()}},
		Form:          store, H2H: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceGRID)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveFailures != 1 || st.LastError != "db down" {
		t.Fatalf("state = %+v, want ConsecutiveFailures=1 LastError=\"db down\"", st)
	}
}
