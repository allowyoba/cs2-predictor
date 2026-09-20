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
	calls  int
}

func (f *fakeRankingProvider) FetchRankings(context.Context) ([]enrichment.RankedTeam, error) {
	f.calls++
	return f.ranked, f.err
}

// fakeTeamLister records which games it was asked for: a ranking sync
// asking for the wrong ones is the whole bug this filter exists for.
type fakeTeamLister struct {
	teams      []competition.Team
	byGame     map[competition.GameCode][]competition.Team
	askedGames []competition.GameCode
}

func (f *fakeTeamLister) ListTeams(_ context.Context, games ...competition.GameCode) ([]competition.Team, error) {
	f.askedGames = games
	if f.byGame == nil {
		return f.teams, nil
	}
	var out []competition.Team
	for _, g := range games {
		out = append(out, f.byGame[g]...)
	}
	return out, nil
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

// fakeRankingSyncGate lets a test dictate ShouldRun's answer (and,
// optionally, an error) without needing a real ApifyRankingGate.
type fakeRankingSyncGate struct {
	allow bool
	err   error
}

func (g fakeRankingSyncGate) ShouldRun(context.Context, enrichment.Source) (bool, error) {
	return g.allow, g.err
}

func TestRankingSync_GateFalseSkipsTheFetchEntirely(t *testing.T) {
	store := newFakeEnrichmentStore()
	provider := &fakeRankingProvider{ranked: []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Spirit"}, GlobalRank: intPtr(1), PublishedAt: time.Now(), Source: enrichment.SourceHLTV},
	}}
	sync := &RankingSync{
		Source: enrichment.SourceHLTV, Provider: provider,
		Teams:    &fakeTeamLister{},
		Rankings: store, Identity: store, State: store,
		Gate: fakeRankingSyncGate{allow: false},
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	if provider.calls != 0 {
		t.Fatalf("expected the provider never to be called when the gate says no, calls = %d", provider.calls)
	}
	st, err := store.State(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt != nil || st.LastErrorAt != nil {
		t.Fatalf("a gated-off tick must not touch sync state either way, state = %+v", st)
	}
}

func TestRankingSync_GateTrueRunsNormally(t *testing.T) {
	teamID := common.NewTeamID()
	store := newFakeEnrichmentStore()
	sync := &RankingSync{
		Source: enrichment.SourceHLTV,
		Provider: &fakeRankingProvider{ranked: []enrichment.RankedTeam{
			{Identity: enrichment.TeamIdentity{Name: "Spirit"}, GlobalRank: intPtr(1), PublishedAt: time.Now(), Source: enrichment.SourceHLTV},
		}},
		Teams:    &fakeTeamLister{teams: []competition.Team{{ID: teamID, Name: "Spirit"}}},
		Rankings: store, Identity: store, State: store,
		Gate: fakeRankingSyncGate{allow: true},
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	if got, err := store.FindRanking(context.Background(), teamID, enrichment.SourceHLTV); err != nil || got == nil {
		t.Fatalf("expected a cached ranking when the gate allows the run, got %+v, err %v", got, err)
	}
}

func TestRankingSync_GateErrorSkipsWithoutFailingDispatch(t *testing.T) {
	store := newFakeEnrichmentStore()
	provider := &fakeRankingProvider{ranked: []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Spirit"}, PublishedAt: time.Now(), Source: enrichment.SourceHLTV},
	}}
	sync := &RankingSync{
		Source: enrichment.SourceHLTV, Provider: provider,
		Teams:    &fakeTeamLister{},
		Rankings: store, Identity: store, State: store,
		Gate: fakeRankingSyncGate{err: errors.New("catalog unreachable")},
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background()) // must not panic or block on the lock

	if provider.calls != 0 {
		t.Fatalf("expected no fetch when the gate itself fails to answer, calls = %d", provider.calls)
	}
}

// A provider whose fetch is a remote job still running is neither healthy-
// and-done nor broken: recording either would misreport it in
// /provider_status and, for a success, close the weekly gate on data that
// never arrived.
func TestRankingSync_PendingFetchIsNeitherSuccessNorFailure(t *testing.T) {
	store := newFakeEnrichmentStore()
	sync := &RankingSync{
		Source:   enrichment.SourceHLTV,
		Provider: &fakeRankingProvider{err: enrichment.ErrFetchPending},
		Teams:    &fakeTeamLister{},
		Rankings: store, Identity: store, State: store,
		Gate: fakeRankingSyncGate{allow: true},
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
	sync.Dispatch(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt != nil || st.LastErrorAt != nil || st.ConsecutiveFailures != 0 {
		t.Fatalf("a pending fetch must leave sync state untouched, got %+v", st)
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

// cachedProvider is a RankingProvider that also holds a finished result,
// the way the Apify provider holds a run that completed while the bot was
// down.
type cachedProvider struct {
	fakeRankingProvider
	cached      []enrichment.RankedTeam
	cachedErr   error
	cachedCalls int
	// onFetch runs inside FetchLatestCached, so a test can observe what
	// the caller was holding at that moment.
	onFetch func()
}

func (c *cachedProvider) FetchLatestCached(context.Context) ([]enrichment.RankedTeam, error) {
	c.cachedCalls++
	if c.onFetch != nil {
		c.onFetch()
	}
	return c.cached, c.cachedErr
}

func newCachedSync(store *fakeEnrichmentStore, provider enrichment.RankingProvider, teamID common.TeamID) *RankingSync {
	return &RankingSync{
		Source: enrichment.SourceHLTV, Provider: provider,
		Teams:    &fakeTeamLister{teams: []competition.Team{{ID: teamID, Name: "Spirit"}}},
		Rankings: store, Identity: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}
}

// A run that finished while the process was down is adopted at startup
// rather than waiting for the weekly window — the data already exists and
// re-reading it costs nothing.
func TestRankingSync_RefreshFromCacheAdoptsANewerFinishedRun(t *testing.T) {
	teamID := common.NewTeamID()
	store := newFakeEnrichmentStore()
	provider := &cachedProvider{cached: []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Spirit"}, GlobalRank: intPtr(1),
			PublishedAt: time.Now().Add(-time.Hour), Source: enrichment.SourceHLTV},
	}}
	sync := newCachedSync(store, provider, teamID)

	sync.RefreshFromCache(context.Background())

	if got, err := store.FindRanking(context.Background(), teamID, enrichment.SourceHLTV); err != nil || got == nil {
		t.Fatalf("expected the cached ranking to be applied, got %+v, err %v", got, err)
	}
	if provider.calls != 0 {
		t.Fatalf("the startup refresh must never start new remote work, got %d fetches", provider.calls)
	}
	st, err := store.State(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSuccessAt == nil {
		t.Fatal("adopting a result is a successful sync and must be recorded as one")
	}
}

// Data we already hold is not re-applied: the comparison is on the
// ranking's own published date, so a restart does not rewrite the same
// snapshot on every boot.
func TestRankingSync_RefreshFromCacheSkipsDataAlreadyHeld(t *testing.T) {
	teamID := common.NewTeamID()
	store := newFakeEnrichmentStore()
	if err := store.RecordSuccess(context.Background(), enrichment.SourceHLTV); err != nil {
		t.Fatal(err)
	}
	provider := &cachedProvider{cached: []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Spirit"}, GlobalRank: intPtr(1),
			PublishedAt: time.Now().Add(-48 * time.Hour), Source: enrichment.SourceHLTV},
	}}
	sync := newCachedSync(store, provider, teamID)

	sync.RefreshFromCache(context.Background())

	if got, _ := store.FindRanking(context.Background(), teamID, enrichment.SourceHLTV); got != nil {
		t.Fatalf("expected older cached data to be ignored, got %+v", got)
	}
}

// A ranking is a snapshot; a team's crest and country are not. Skipping a
// snapshot we already hold is right, and it used to skip the appearance
// with it — which is why the flag column stayed empty for a week after
// shipping, on a feed that had already published every flag.
func TestRankingSync_RefreshFromCacheBackfillsAppearanceFromDataAlreadyHeld(t *testing.T) {
	teamID := common.NewTeamID()
	store := newFakeEnrichmentStore()
	if err := store.RecordSuccess(context.Background(), enrichment.SourceHLTV); err != nil {
		t.Fatal(err)
	}
	appearance := &recordingAppearance{}
	provider := &cachedProvider{cached: []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Spirit", LogoURL: "https://img-cdn.hltv.org/s.png", Country: "Russia"},
			GlobalRank: intPtr(1), PublishedAt: time.Now().Add(-48 * time.Hour), Source: enrichment.SourceHLTV},
	}}
	sync := newCachedSync(store, provider, teamID)
	sync.TeamLogos = appearance

	sync.RefreshFromCache(context.Background())

	// The ranking itself is still not rewritten: that part of the guard was
	// right and stays.
	if got, _ := store.FindRanking(context.Background(), teamID, enrichment.SourceHLTV); got != nil {
		t.Fatalf("the held snapshot was re-applied after all: %+v", got)
	}
	if len(appearance.calls) != 1 {
		t.Fatalf("expected the crest and country to be backfilled, got %d writes", len(appearance.calls))
	}
	got := appearance.calls[0]
	if got.logo != "https://img-cdn.hltv.org/s.png" || got.country != "RU" {
		t.Fatalf("backfilled %+v, want the feed's crest and its country as a code", got)
	}
}

type appearanceCall struct {
	teamID  common.TeamID
	source  enrichment.Source
	logo    string
	country string
}

type recordingAppearance struct{ calls []appearanceCall }

func (r *recordingAppearance) SetRankingAppearance(_ context.Context, teamID common.TeamID, source enrichment.Source, logo, country string) error {
	r.calls = append(r.calls, appearanceCall{teamID, source, logo, country})
	return nil
}

// A provider that cannot answer right now must not turn a startup into a
// failure, nor mark the source as broken: its scheduled run is still ahead
// of it.
func TestRankingSync_RefreshFromCacheSwallowsProviderErrors(t *testing.T) {
	store := newFakeEnrichmentStore()
	provider := &cachedProvider{cachedErr: errors.New("apify unreachable")}
	sync := newCachedSync(store, provider, common.NewTeamID())

	sync.RefreshFromCache(context.Background())

	st, err := store.State(context.Background(), enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastErrorAt != nil || st.ConsecutiveFailures != 0 {
		t.Fatalf("an opportunistic refresh must not mark the provider broken, got %+v", st)
	}
}

// Providers without anything cached (the free feeds) are simply skipped.
func TestRankingSync_RefreshFromCacheIgnoresPlainProviders(t *testing.T) {
	store := newFakeEnrichmentStore()
	provider := &fakeRankingProvider{}
	sync := newCachedSync(store, provider, common.NewTeamID())

	sync.RefreshFromCache(context.Background())

	if provider.calls != 0 {
		t.Fatalf("expected no fetch at all, got %d", provider.calls)
	}
}

// lockProbe records whether anything was still being fetched while the
// cluster lock was held. The lock pins a pooled database connection, so a
// provider call inside it starves every other job — which is exactly what
// took production's background jobs down on the deploy that shipped the
// startup refresh.
type lockProbe struct {
	held     bool
	heldWhen func() bool
	violated bool
}

func (l *lockProbe) Execute(ctx context.Context, _ string, action func(ctx context.Context) error) (bool, error) {
	l.held = true
	defer func() { l.held = false }()
	if l.heldWhen != nil && l.heldWhen() {
		l.violated = true
	}
	return true, action(ctx)
}

func TestRankingSync_RefreshFromCacheFetchesOutsideTheClusterLock(t *testing.T) {
	store := newFakeEnrichmentStore()
	probe := &lockProbe{}
	provider := &cachedProvider{cached: []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Spirit"}, GlobalRank: intPtr(1),
			PublishedAt: time.Now().Add(-time.Hour), Source: enrichment.SourceHLTV},
	}}
	// The provider reports whether the lock was held at fetch time.
	provider.onFetch = func() {
		if probe.held {
			probe.violated = true
		}
	}
	sync := newCachedSync(store, provider, common.NewTeamID())
	sync.Lock = probe

	sync.RefreshFromCache(context.Background())

	if probe.violated {
		t.Fatal("the provider must be called before the cluster lock is taken: the lock holds a pooled connection")
	}
	if provider.cachedCalls != 1 {
		t.Fatalf("expected exactly one cached fetch, got %d", provider.cachedCalls)
	}
}

// The bug this prevents, at the layer where it happened: "BetBoom Team"
// exists twice — a Counter-Strike roster and a Dota 2 roster under one
// organisation — and the ranking sync used to match the CS2 feed against
// both, attaching a VRS ranking to a Dota 2 team that never played a map
// of Counter-Strike.
func TestRankingSync_NeverMatchesATeamFromAGameTheFeedDoesNotRank(t *testing.T) {
	cs2Team, dotaTeam := common.NewTeamID(), common.NewTeamID()
	teams := &fakeTeamLister{byGame: map[competition.GameCode][]competition.Team{
		competition.GameCS2:   {{ID: cs2Team, Name: "BetBoom Team"}},
		competition.GameDota2: {{ID: dotaTeam, Name: "BetBoom Team"}},
	}}
	store := newFakeEnrichmentStore()
	sync := &RankingSync{
		Source: enrichment.SourceValveVRS,
		Provider: &fakeRankingProvider{ranked: []enrichment.RankedTeam{
			{Identity: enrichment.TeamIdentity{Name: "BetBoom Team"}, GlobalRank: intPtr(7), PublishedAt: time.Now(), Source: enrichment.SourceValveVRS},
		}},
		Teams: teams, Rankings: store, Identity: store, State: store,
		Lock: fakeClusterLock{}, Log: slog.Default(),
	}

	sync.Dispatch(context.Background())

	if len(teams.askedGames) != 1 || teams.askedGames[0] != competition.GameCS2 {
		t.Fatalf("the sync asked for %v, want only the games its feed ranks", teams.askedGames)
	}
	if _, ok := store.rankings[cs2Team]; !ok {
		t.Fatal("the Counter-Strike team must still get its ranking")
	}
	if _, ok := store.rankings[dotaTeam]; ok {
		t.Fatal("the Dota 2 team shares an organisation, not a ranking")
	}
}
