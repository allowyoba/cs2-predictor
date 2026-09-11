package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

func intPtr(n int) *int { return &n }

type fakeRankingProvider struct {
	ranked []enrichment.RankedTeam
	err    error
}

func (f *fakeRankingProvider) FetchRankings(context.Context) ([]enrichment.RankedTeam, error) {
	return f.ranked, f.err
}

type fakeTeamLister struct {
	teams []competition.Team
}

func (f *fakeTeamLister) ListTeams(context.Context) ([]competition.Team, error) {
	return f.teams, nil
}

// fakeEnrichmentStore is a single in-memory fake covering
// enrichment.RankingRepository, enrichment.IdentityRepository,
// enrichment.SyncStateRepository, enrichment.FormRepository,
// enrichment.HeadToHeadRepository, and enrichment.TournamentMetadataRepository
// — everything the enrichment sync jobs need besides their own provider/
// team-lister/catalog.
type fakeEnrichmentStore struct {
	rankings    map[common.TeamID]enrichment.TeamRanking
	identities  map[string]common.TeamID
	aliases     map[common.TeamID][]string
	state       map[enrichment.Source]enrichment.SyncState
	forms       map[common.TeamID]enrichment.RecentForm
	h2h         map[[2]common.TeamID]enrichment.HeadToHead
	tournaments map[common.EventID]enrichment.TournamentMetadata
}

func newFakeEnrichmentStore() *fakeEnrichmentStore {
	return &fakeEnrichmentStore{
		rankings:    map[common.TeamID]enrichment.TeamRanking{},
		identities:  map[string]common.TeamID{},
		aliases:     map[common.TeamID][]string{},
		state:       map[enrichment.Source]enrichment.SyncState{},
		forms:       map[common.TeamID]enrichment.RecentForm{},
		h2h:         map[[2]common.TeamID]enrichment.HeadToHead{},
		tournaments: map[common.EventID]enrichment.TournamentMetadata{},
	}
}

func (s *fakeEnrichmentStore) SaveTournamentMetadata(_ context.Context, eventID common.EventID, meta enrichment.TournamentMetadata, _ enrichment.Source) error {
	s.tournaments[eventID] = meta
	return nil
}
func (s *fakeEnrichmentStore) FindTournamentMetadata(_ context.Context, eventID common.EventID, _ enrichment.Source) (*enrichment.TournamentMetadata, error) {
	if m, ok := s.tournaments[eventID]; ok {
		return &m, nil
	}
	return nil, nil
}

func (s *fakeEnrichmentStore) SaveForm(_ context.Context, teamID common.TeamID, form enrichment.RecentForm) error {
	s.forms[teamID] = form
	return nil
}
func (s *fakeEnrichmentStore) FindForm(_ context.Context, teamID common.TeamID, _ enrichment.Source) (*enrichment.RecentForm, error) {
	if f, ok := s.forms[teamID]; ok {
		return &f, nil
	}
	return nil, nil
}

// h2hKey canonicalizes an unordered team pair, mirroring the real
// postgres.EnrichmentRepository's orderedPair so this fake enforces the
// same "lookup order doesn't matter" contract the real implementation is
// tested for.
func h2hKey(teamA, teamB common.TeamID) (key [2]common.TeamID, swapped bool) {
	if teamA.Value.String() <= teamB.Value.String() {
		return [2]common.TeamID{teamA, teamB}, false
	}
	return [2]common.TeamID{teamB, teamA}, true
}

func (s *fakeEnrichmentStore) SaveHeadToHead(_ context.Context, teamA, teamB common.TeamID, h2h enrichment.HeadToHead) error {
	key, swapped := h2hKey(teamA, teamB)
	if swapped {
		h2h.TeamAWins, h2h.TeamBWins = h2h.TeamBWins, h2h.TeamAWins
	}
	s.h2h[key] = h2h
	return nil
}
func (s *fakeEnrichmentStore) FindHeadToHead(_ context.Context, teamA, teamB common.TeamID, _ enrichment.Source) (*enrichment.HeadToHead, error) {
	key, swapped := h2hKey(teamA, teamB)
	h2h, ok := s.h2h[key]
	if !ok {
		return nil, nil
	}
	if swapped {
		h2h.TeamAWins, h2h.TeamBWins = h2h.TeamBWins, h2h.TeamAWins
	}
	return &h2h, nil
}

func (s *fakeEnrichmentStore) SaveRanking(_ context.Context, r enrichment.TeamRanking) error {
	s.rankings[r.TeamID] = r
	return nil
}
func (s *fakeEnrichmentStore) FindRanking(_ context.Context, teamID common.TeamID, _ enrichment.Source) (*enrichment.TeamRanking, error) {
	if r, ok := s.rankings[teamID]; ok {
		return &r, nil
	}
	return nil, nil
}
func (s *fakeEnrichmentStore) FindRankings(_ context.Context, teamIDs []common.TeamID, _ enrichment.Source) (map[common.TeamID]enrichment.TeamRanking, error) {
	out := map[common.TeamID]enrichment.TeamRanking{}
	for _, id := range teamIDs {
		if r, ok := s.rankings[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func identityKey(source enrichment.Source, externalID string) string {
	return string(source) + "|" + externalID
}

func (s *fakeEnrichmentStore) FindTeamByExternalID(_ context.Context, source enrichment.Source, externalID string) (*common.TeamID, error) {
	if id, ok := s.identities[identityKey(source, externalID)]; ok {
		return &id, nil
	}
	return nil, nil
}
func (s *fakeEnrichmentStore) SaveIdentity(_ context.Context, teamID common.TeamID, source enrichment.Source, externalID, externalName string, _ enrichment.MatchConfidence) error {
	s.identities[identityKey(source, externalID)] = teamID
	s.aliases[teamID] = append(s.aliases[teamID], externalName)
	return nil
}
func (s *fakeEnrichmentStore) Aliases(_ context.Context, teamID common.TeamID) ([]string, error) {
	return s.aliases[teamID], nil
}
func (s *fakeEnrichmentStore) AllAliases(context.Context) (map[common.TeamID][]string, error) {
	return s.aliases, nil
}

func (s *fakeEnrichmentStore) RecordSuccess(_ context.Context, provider enrichment.Source) error {
	st := s.state[provider]
	st.Provider = provider
	now := time.Now()
	st.LastSuccessAt = &now
	st.ConsecutiveFailures = 0
	s.state[provider] = st
	return nil
}
func (s *fakeEnrichmentStore) RecordFailure(_ context.Context, provider enrichment.Source, errText string) error {
	st := s.state[provider]
	st.Provider = provider
	now := time.Now()
	st.LastErrorAt = &now
	st.LastError = errText
	st.ConsecutiveFailures++
	s.state[provider] = st
	return nil
}
func (s *fakeEnrichmentStore) State(_ context.Context, provider enrichment.Source) (*enrichment.SyncState, error) {
	st := s.state[provider]
	return &st, nil
}

func TestValveVRSSync_MatchesTeamByExactNameAndCachesRanking(t *testing.T) {
	teamID := common.NewTeamID()
	store := newFakeEnrichmentStore()
	sync := &RankingSync{
		Source: enrichment.SourceValveVRS,
		Provider: &fakeRankingProvider{ranked: []enrichment.RankedTeam{
			{Identity: enrichment.TeamIdentity{Name: "Spirit"}, GlobalRank: intPtr(1), Points: intPtr(2011), PublishedAt: time.Now(), Source: enrichment.SourceValveVRS},
		}},
		Teams:    &fakeTeamLister{teams: []competition.Team{{ID: teamID, Name: "Spirit"}}},
		Rankings: store, Identity: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	got, err := store.FindRanking(context.Background(), teamID, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.GlobalRank == nil || *got.GlobalRank != 1 {
		t.Fatalf("expected cached ranking with GlobalRank=1, got %+v", got)
	}
	st, err := store.State(context.Background(), enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil {
		t.Fatalf("expected RecordSuccess to be called, state = %+v", st)
	}
}

func TestValveVRSSync_SkipsUnmatchedTeamsWithoutFailingTheSync(t *testing.T) {
	store := newFakeEnrichmentStore()
	sync := &RankingSync{
		Source: enrichment.SourceValveVRS,
		Provider: &fakeRankingProvider{ranked: []enrichment.RankedTeam{
			{Identity: enrichment.TeamIdentity{Name: "Completely Unknown Team"}, GlobalRank: intPtr(1), PublishedAt: time.Now(), Source: enrichment.SourceValveVRS},
		}},
		Teams:    &fakeTeamLister{teams: []competition.Team{{ID: common.NewTeamID(), Name: "Spirit"}}},
		Rankings: store, Identity: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	if len(store.rankings) != 0 {
		t.Fatalf("expected no ranking saved for an unmatched team, got %+v", store.rankings)
	}
	st, err := store.State(context.Background(), enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil {
		t.Fatalf("an unmatched team must not fail the whole sync run, state = %+v", st)
	}
}

func TestValveVRSSync_RecordsFailureWhenProviderFetchErrors(t *testing.T) {
	store := newFakeEnrichmentStore()
	sync := &RankingSync{
		Source:   enrichment.SourceValveVRS,
		Provider: &fakeRankingProvider{err: errors.New("valve unreachable")},
		Teams:    &fakeTeamLister{},
		Rankings: store, Identity: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if st.ConsecutiveFailures != 1 || st.LastError != "valve unreachable" {
		t.Fatalf("state = %+v, want ConsecutiveFailures=1 LastError=%q", st, "valve unreachable")
	}
	if st.LastSuccessAt != nil {
		t.Fatalf("RecordSuccess must not be called on a fetch failure, state = %+v", st)
	}
}

func TestValveVRSSync_ReusesConfirmedExternalIDMappingOnSubsequentRuns(t *testing.T) {
	teamID := common.NewTeamID()
	store := newFakeEnrichmentStore()
	provider := &fakeRankingProvider{ranked: []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Natus Vincere"}, GlobalRank: intPtr(2), PublishedAt: time.Now(), Source: enrichment.SourceValveVRS},
	}}
	sync := &RankingSync{
		Source:   enrichment.SourceValveVRS,
		Provider: provider,
		Teams:    &fakeTeamLister{teams: []competition.Team{{ID: teamID, Name: "Natus Vincere"}}},
		Rankings: store, Identity: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())
	if len(store.identities) != 1 {
		t.Fatalf("expected the first run to persist a confirmed external-id mapping, got %v", store.identities)
	}

	// Second run: the local team is renamed, so name/alias matching alone
	// would no longer find it — but the external-id mapping confirmed on
	// the first run must still resolve it straight away.
	sync.Teams = &fakeTeamLister{teams: []competition.Team{{ID: teamID, Name: "NAVI (renamed)"}}}
	sync.Dispatch(context.Background())

	got, err := store.FindRanking(context.Background(), teamID, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.GlobalRank == nil || *got.GlobalRank != 2 {
		t.Fatalf("expected the second run to still resolve via the confirmed external-id mapping, got %+v", got)
	}
}
