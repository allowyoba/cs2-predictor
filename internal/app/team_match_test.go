package app

import (
	"context"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// --- fakes ---

type fakeSnapshotRepo struct {
	entries []enrichment.RankedTeam
}

func (f *fakeSnapshotRepo) SaveSnapshot(context.Context, []enrichment.RankedTeam) error { return nil }
func (f *fakeSnapshotRepo) AllSnapshot(_ context.Context, source enrichment.Source) ([]enrichment.RankedTeam, error) {
	var out []enrichment.RankedTeam
	for _, e := range f.entries {
		if e.Source == source {
			out = append(out, e)
		}
	}
	return out, nil
}

// rankingKey composes the (team, source) pair fakeRankingRepoForMatch keys
// by — a bare TeamID key would conflate two different sources' rankings
// for the same team, which the real repository never does (see
// EnrichmentRepository's SQL, always filtered by source).
type rankingKey struct {
	team   common.TeamID
	source enrichment.Source
}

type fakeRankingRepoForMatch struct {
	byTeam map[rankingKey]enrichment.TeamRanking
	saved  []enrichment.TeamRanking
}

func newFakeRankingRepoForMatch() *fakeRankingRepoForMatch {
	return &fakeRankingRepoForMatch{byTeam: map[rankingKey]enrichment.TeamRanking{}}
}
func (f *fakeRankingRepoForMatch) SaveRanking(_ context.Context, r enrichment.TeamRanking) error {
	f.saved = append(f.saved, r)
	f.byTeam[rankingKey{r.TeamID, r.Source}] = r
	return nil
}
func (f *fakeRankingRepoForMatch) FindRanking(_ context.Context, teamID common.TeamID, source enrichment.Source) (*enrichment.TeamRanking, error) {
	if r, ok := f.byTeam[rankingKey{teamID, source}]; ok {
		return &r, nil
	}
	return nil, nil
}
func (f *fakeRankingRepoForMatch) FindRankings(_ context.Context, teamIDs []common.TeamID, source enrichment.Source) (map[common.TeamID]enrichment.TeamRanking, error) {
	out := map[common.TeamID]enrichment.TeamRanking{}
	for _, id := range teamIDs {
		if r, ok := f.byTeam[rankingKey{id, source}]; ok {
			out[id] = r
		}
	}
	return out, nil
}

type savedIdentity struct {
	teamID     common.TeamID
	externalID string
	confidence enrichment.MatchConfidence
}
type fakeIdentityRepoForMatch struct {
	saved []savedIdentity
}

func (f *fakeIdentityRepoForMatch) FindTeamByExternalID(context.Context, enrichment.Source, string) (*common.TeamID, error) {
	return nil, nil
}
func (f *fakeIdentityRepoForMatch) SaveIdentity(_ context.Context, teamID common.TeamID, _ enrichment.Source, externalID, _ string, confidence enrichment.MatchConfidence) error {
	f.saved = append(f.saved, savedIdentity{teamID: teamID, externalID: externalID, confidence: confidence})
	return nil
}
func (f *fakeIdentityRepoForMatch) Aliases(context.Context, common.TeamID) ([]string, error) {
	return nil, nil
}
func (f *fakeIdentityRepoForMatch) AllAliases(context.Context) (map[common.TeamID][]string, error) {
	return nil, nil
}

// fakeTeamMatchRepo is a simple in-memory TeamMatchRepository, keyed like
// the real schema (unique per source+externalName).
type fakeTeamMatchRepo struct {
	byID         map[common.RequestID]*enrichment.TeamMatchRequest
	candidates   map[common.RequestID][]enrichment.TeamMatchCandidate
	responded    map[string]bool // requestID.String()+":"+userID
	askIncrement []int
}

func newFakeTeamMatchRepo() *fakeTeamMatchRepo {
	return &fakeTeamMatchRepo{
		byID: map[common.RequestID]*enrichment.TeamMatchRequest{}, candidates: map[common.RequestID][]enrichment.TeamMatchCandidate{},
		responded: map[string]bool{},
	}
}
func (f *fakeTeamMatchRepo) FindPendingByExternalName(_ context.Context, source enrichment.Source, externalName string) (*enrichment.TeamMatchRequest, error) {
	for _, r := range f.byID {
		if r.Source == source && r.ExternalName == externalName && r.Status == enrichment.TeamMatchPending {
			return r, nil
		}
	}
	return nil, nil
}
func (f *fakeTeamMatchRepo) CreateRequest(_ context.Context, req enrichment.TeamMatchRequest, candidates []enrichment.TeamMatchCandidate) error {
	cp := req
	f.byID[req.ID] = &cp
	f.candidates[req.ID] = candidates
	return nil
}
func (f *fakeTeamMatchRepo) AddCandidate(_ context.Context, requestID common.RequestID, candidate enrichment.TeamMatchCandidate) error {
	for _, c := range f.candidates[requestID] {
		if c.TeamID == candidate.TeamID {
			return nil
		}
	}
	f.candidates[requestID] = append(f.candidates[requestID], candidate)
	return nil
}
func (f *fakeTeamMatchRepo) ListPending(context.Context, int) ([]enrichment.TeamMatchRequest, error) {
	var out []enrichment.TeamMatchRequest
	for _, r := range f.byID {
		if r.Status == enrichment.TeamMatchPending {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *fakeTeamMatchRepo) FindRequest(_ context.Context, id common.RequestID) (*enrichment.TeamMatchRequest, []enrichment.TeamMatchCandidate, error) {
	r, ok := f.byID[id]
	if !ok {
		return nil, nil, nil
	}
	return r, f.candidates[id], nil
}
func (f *fakeTeamMatchRepo) RecordResponse(_ context.Context, requestID common.RequestID, userID common.UserID, candidateTeamID common.TeamID, answer enrichment.TeamMatchAnswer) error {
	f.responded[requestID.String()+":"+requestIDUserKey(userID)] = true
	cands := f.candidates[requestID]
	for i := range cands {
		if cands[i].TeamID == candidateTeamID {
			cands[i].Score = enrichment.CrowdAdjustedScore(cands[i].Score, answer)
		}
	}
	f.candidates[requestID] = cands
	return nil
}
func (f *fakeTeamMatchRepo) HasResponded(_ context.Context, requestID common.RequestID, userID common.UserID) (bool, error) {
	return f.responded[requestID.String()+":"+requestIDUserKey(userID)], nil
}
func (f *fakeTeamMatchRepo) IncrementCrowdAsksSent(_ context.Context, requestID common.RequestID, n int) error {
	f.askIncrement = append(f.askIncrement, n)
	if r, ok := f.byID[requestID]; ok {
		r.CrowdAsksSent += n
	}
	return nil
}
func (f *fakeTeamMatchRepo) Resolve(_ context.Context, requestID common.RequestID, status enrichment.TeamMatchStatus, teamID *common.TeamID, at time.Time) error {
	if r, ok := f.byID[requestID]; ok {
		r.Status = status
		r.BestTeamID = teamID
		r.ResolvedAt = &at
	}
	return nil
}

func requestIDUserKey(userID common.UserID) string {
	return strconv.FormatInt(userID.Value, 10)
}

type fakeHelperRepo struct {
	optedOut  map[int64]bool
	askedN    map[int64]int
	recordAsk []int64
}

func newFakeHelperRepo() *fakeHelperRepo {
	return &fakeHelperRepo{optedOut: map[int64]bool{}, askedN: map[int64]int{}}
}
func (f *fakeHelperRepo) EligibleHelpers(_ context.Context, candidates []common.UserID) ([]common.UserID, error) {
	var out []common.UserID
	for _, c := range candidates {
		if f.optedOut[c.Value] || f.askedN[c.Value] >= enrichment.MaxLifetimeAsksPerUser {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}
func (f *fakeHelperRepo) RecordAsk(_ context.Context, userID common.UserID, _ time.Time) error {
	f.askedN[userID.Value]++
	f.recordAsk = append(f.recordAsk, userID.Value)
	return nil
}
func (f *fakeHelperRepo) SetOptedOut(_ context.Context, userID common.UserID, optedOut bool) error {
	f.optedOut[userID.Value] = optedOut
	return nil
}

// fakePredictionsForMatch is a minimal prediction.Repository — only
// ChatParticipants is used by TeamMatchService.
type fakePredictionsForMatch struct {
	prediction.Repository
	participants []common.UserID
}

func (f *fakePredictionsForMatch) ChatParticipants(context.Context, common.ChatID, time.Time) ([]common.UserID, error) {
	return f.participants, nil
}

// fakeChatsForMatch is a minimal chat.Repository — only FilterDMReachable
// is used by TeamMatchService.
type fakeChatsForMatch struct {
	chat.Repository
	reachable []common.UserID
}

func (f *fakeChatsForMatch) FilterDMReachable(context.Context, []common.UserID) ([]common.UserID, error) {
	return f.reachable, nil
}

func newTestTeamMatchService(snapshot []enrichment.RankedTeam) (*TeamMatchService, *fakeTeamMatchRepo, *fakeRankingRepoForMatch, *fakeIdentityRepoForMatch) {
	requests := newFakeTeamMatchRepo()
	rankings := newFakeRankingRepoForMatch()
	identity := &fakeIdentityRepoForMatch{}
	svc := &TeamMatchService{
		Requests: requests, Helpers: newFakeHelperRepo(), Snapshots: &fakeSnapshotRepo{entries: snapshot},
		Rankings: rankings, Identity: identity, Sources: []enrichment.Source{enrichment.SourceValveVRS},
		Predictions: &fakePredictionsForMatch{}, Chats: &fakeChatsForMatch{},
		Outbox: &recordingOutbox{fakeOutbox: fakeOutbox{failed: map[uuid.UUID]string{}}},
		Clock:  common.FixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), Log: slog.Default(),
	}
	return svc, requests, rankings, identity
}

// ensureRequestSingle calls EnsureRequests (which now checks every
// svc.Sources independently) and asserts exactly the single-source shape
// most of these tests exercise, returning the one request id or nil the
// same way the old single-source EnsureRequest used to.
func ensureRequestSingle(t *testing.T, svc *TeamMatchService, team competition.Team) *common.RequestID {
	t.Helper()
	ids := svc.EnsureRequests(context.Background(), team)
	if len(ids) > 1 {
		t.Fatalf("expected at most one request id for a single-source test, got %v", ids)
	}
	if len(ids) == 0 {
		return nil
	}
	return &ids[0]
}

// --- EnsureRequest ---

func TestEnsureRequest_AutoAcceptsHighFuzzyScore(t *testing.T) {
	rank := 3
	snapshot := []enrichment.RankedTeam{{Identity: enrichment.TeamIdentity{Name: "Team Vitality"}, GlobalRank: &rank, Source: enrichment.SourceValveVRS}}
	svc, requests, rankings, identity := newTestTeamMatchService(snapshot)
	team := competition.Team{ID: common.NewTeamID(), Name: "Vitality"}

	reqID := ensureRequestSingle(t, svc, team)
	if reqID != nil {
		t.Fatalf("expected auto-accept (nil request id), got %v", reqID)
	}
	if len(identity.saved) != 1 || identity.saved[0].confidence != enrichment.ConfidenceFuzzyName {
		t.Fatalf("expected one fuzzy-name identity save, got %+v", identity.saved)
	}
	if len(rankings.saved) != 1 || *rankings.saved[0].GlobalRank != 3 {
		t.Fatalf("expected the ranking saved immediately with rank 3, got %+v", rankings.saved)
	}
	if pending, _ := requests.ListPending(context.Background(), 10); len(pending) != 0 {
		t.Fatalf("auto-accept must not also create a review request, got %+v", pending)
	}
}

func TestEnsureRequest_SkipsWhenAlreadyRanked(t *testing.T) {
	svc, requests, _, identity := newTestTeamMatchService(nil)
	team := competition.Team{ID: common.NewTeamID(), Name: "Vitality"}
	rank := 3
	if err := svc.Rankings.SaveRanking(context.Background(), enrichment.TeamRanking{TeamID: team.ID, GlobalRank: &rank, Source: enrichment.SourceValveVRS}); err != nil {
		t.Fatal(err)
	}

	reqID := ensureRequestSingle(t, svc, team)
	if reqID != nil {
		t.Fatalf("expected nil (already resolved), got %v", reqID)
	}
	if len(identity.saved) != 0 {
		t.Fatalf("must not touch identity for an already-ranked team, got %+v", identity.saved)
	}
	if pending, _ := requests.ListPending(context.Background(), 10); len(pending) != 0 {
		t.Fatalf("must not create a request for an already-ranked team, got %+v", pending)
	}
}

func TestEnsureRequest_SkipsWhenNoPlausibleMatchAtAll(t *testing.T) {
	snapshot := []enrichment.RankedTeam{{Identity: enrichment.TeamIdentity{Name: "NAVI"}, Source: enrichment.SourceValveVRS}}
	svc, requests, _, _ := newTestTeamMatchService(snapshot)
	team := competition.Team{ID: common.NewTeamID(), Name: "Some Unranked Regional Team"}

	reqID := ensureRequestSingle(t, svc, team)
	if reqID != nil {
		t.Fatalf("expected nil (not plausibly ranked), got %v", reqID)
	}
	if pending, _ := requests.ListPending(context.Background(), 10); len(pending) != 0 {
		t.Fatalf("a genuinely-unranked team must not create review noise, got %+v", pending)
	}
}

// A name that's similar-but-not-obviously-the-same (below auto-accept, above
// the request floor) must open exactly one reviewable request.
func TestEnsureRequest_CreatesRequestForAmbiguousScore(t *testing.T) {
	snapshot := []enrichment.RankedTeam{{Identity: enrichment.TeamIdentity{Name: "Avangar"}, Source: enrichment.SourceValveVRS}}
	svc, requests, _, identity := newTestTeamMatchService(snapshot)
	team := competition.Team{ID: common.NewTeamID(), Name: "Avangarr"}

	score := enrichment.FuzzyNameScore(team.Name, "Avangar")
	if score < enrichment.FuzzyRequestThreshold || score >= enrichment.FuzzyAutoAcceptThreshold {
		t.Fatalf("test fixture invalid: score %d must sit strictly between the two thresholds", score)
	}

	reqID := ensureRequestSingle(t, svc, team)
	if reqID == nil {
		t.Fatal("expected a review request to be opened")
	}
	if len(identity.saved) != 0 {
		t.Fatalf("an ambiguous score must never save an identity mapping directly, got %+v", identity.saved)
	}
	req, candidates, err := requests.FindRequest(context.Background(), *reqID)
	if err != nil {
		t.Fatal(err)
	}
	if req.ExternalName != "Avangar" || req.Status != enrichment.TeamMatchPending {
		t.Fatalf("unexpected request: %+v", req)
	}
	if len(candidates) != 1 || candidates[0].TeamID != team.ID {
		t.Fatalf("expected the local team as the sole initial candidate, got %+v", candidates)
	}
}

func TestEnsureRequest_ReusesExistingPendingRequestForSameExternalName(t *testing.T) {
	snapshot := []enrichment.RankedTeam{{Identity: enrichment.TeamIdentity{Name: "Avangar"}, Source: enrichment.SourceValveVRS}}
	svc, requests, _, _ := newTestTeamMatchService(snapshot)
	teamA := competition.Team{ID: common.NewTeamID(), Name: "Avangarr"}
	teamB := competition.Team{ID: common.NewTeamID(), Name: "Avangaar"}

	firstID := ensureRequestSingle(t, svc, teamA)
	if firstID == nil {
		t.Fatal("expected a request from the first call")
	}
	secondID := ensureRequestSingle(t, svc, teamB)
	if secondID == nil {
		t.Fatal("expected a request from the second call")
	}
	if firstID.String() != secondID.String() {
		t.Fatalf("expected the same request to be reused for the same external name, got %s and %s", firstID.String(), secondID.String())
	}
	if pending, _ := requests.ListPending(context.Background(), 10); len(pending) != 1 {
		t.Fatalf("expected exactly one pending request, got %+v", pending)
	}
	_, candidates, err := requests.FindRequest(context.Background(), *firstID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected the second team to be added as a competing candidate rather than silently dropped, got %+v", candidates)
	}
	var sawA, sawB bool
	for _, c := range candidates {
		sawA = sawA || c.TeamID == teamA.ID
		sawB = sawB || c.TeamID == teamB.ID
	}
	if !sawA || !sawB {
		t.Fatalf("expected both competing teams among the candidates, got %+v", candidates)
	}
}

// The same team matching the same external name again (e.g. in a later
// match) must not be added as a duplicate candidate.
func TestEnsureRequest_SameTeamMatchingAgainDoesNotDuplicateCandidate(t *testing.T) {
	snapshot := []enrichment.RankedTeam{{Identity: enrichment.TeamIdentity{Name: "Avangar"}, Source: enrichment.SourceValveVRS}}
	svc, requests, _, _ := newTestTeamMatchService(snapshot)
	team := competition.Team{ID: common.NewTeamID(), Name: "Avangarr"}

	firstID := ensureRequestSingle(t, svc, team)
	if firstID == nil {
		t.Fatal("expected a request from the first call")
	}
	secondID := ensureRequestSingle(t, svc, team)
	if secondID == nil || secondID.String() != firstID.String() {
		t.Fatalf("expected the same request reused, got %v and %v", firstID, secondID)
	}
	_, candidates, err := requests.FindRequest(context.Background(), *firstID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected exactly one candidate — the same team matching again must not duplicate it, got %+v", candidates)
	}
}

// Once a request already has MaxCandidatesPerRequest candidates, a further
// competing team must not push it over that cap.
func TestEnsureRequest_DoesNotExceedMaxCandidatesPerRequest(t *testing.T) {
	snapshot := []enrichment.RankedTeam{{Identity: enrichment.TeamIdentity{Name: "Avangar"}, Source: enrichment.SourceValveVRS}}
	svc, requests, _, _ := newTestTeamMatchService(snapshot)

	// Each name below is "Avangar" with one letter doubled — a single
	// insertion, scoring consistently in the ambiguous fuzzy-request band
	// (well under FuzzyAutoAcceptThreshold) regardless of which letter.
	names := []string{"Avangarr", "Avangaar", "Avanggar", "Avanngar", "Aavangar"}
	var lastID *common.RequestID
	for _, name := range names {
		team := competition.Team{ID: common.NewTeamID(), Name: name}
		id := ensureRequestSingle(t, svc, team)
		if id == nil {
			t.Fatalf("expected a request id for %q", name)
		}
		lastID = id
	}
	_, candidates, err := requests.FindRequest(context.Background(), *lastID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != enrichment.MaxCandidatesPerRequest {
		t.Fatalf("expected exactly %d candidates (the cap), got %d: %+v", enrichment.MaxCandidatesPerRequest, len(candidates), candidates)
	}
}

// A team can be ambiguous on one ranking source while already resolved (or
// simply unranked) on another — EnsureRequests must check every configured
// Source independently and only report the ones that actually opened a
// request, never conflating two different feeds' identities.
func TestEnsureRequests_ChecksEachConfiguredSourceIndependently(t *testing.T) {
	valveSnapshot := enrichment.RankedTeam{Identity: enrichment.TeamIdentity{Name: "Avangar"}, Source: enrichment.SourceValveVRS}
	hltvSnapshot := enrichment.RankedTeam{Identity: enrichment.TeamIdentity{Name: "Avangar"}, Source: enrichment.SourceHLTV}
	svc, requests, rankings, _ := newTestTeamMatchService([]enrichment.RankedTeam{valveSnapshot, hltvSnapshot})
	svc.Sources = []enrichment.Source{enrichment.SourceValveVRS, enrichment.SourceHLTV}
	team := competition.Team{ID: common.NewTeamID(), Name: "Avangarr"}

	// The team is already resolved on HLTV, but not on Valve VRS — only
	// the Valve side should end up with a pending request.
	rank := 5
	if err := rankings.SaveRanking(context.Background(), enrichment.TeamRanking{TeamID: team.ID, GlobalRank: &rank, Source: enrichment.SourceHLTV}); err != nil {
		t.Fatal(err)
	}

	ids := svc.EnsureRequests(context.Background(), team)
	if len(ids) != 1 {
		t.Fatalf("expected exactly one pending request (Valve only), got %v", ids)
	}
	req, _, err := requests.FindRequest(context.Background(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if req.Source != enrichment.SourceValveVRS {
		t.Fatalf("expected the pending request's source to be Valve VRS, got %s", req.Source)
	}
}

// --- AskChatHelpers ---

func TestAskChatHelpers_AsksEligibleReachableUsersAndRecordsCounts(t *testing.T) {
	svc, requests, _, _ := newTestTeamMatchService(nil)
	team := competition.Team{ID: common.NewTeamID(), Name: "Avangarr"}
	req := enrichment.TeamMatchRequest{ID: common.NewRequestID(), ExternalName: "Avangar", Source: enrichment.SourceValveVRS, Status: enrichment.TeamMatchPending, CreatedAt: svc.Clock.Now()}
	candidate := enrichment.TeamMatchCandidate{TeamID: team.ID, TeamName: team.Name, Score: 60, Kind: enrichment.CandidateKindFuzzy}
	if err := requests.CreateRequest(context.Background(), req, []enrichment.TeamMatchCandidate{candidate}); err != nil {
		t.Fatal(err)
	}

	users := []common.UserID{{Value: 1}, {Value: 2}, {Value: 3}}
	svc.Predictions = &fakePredictionsForMatch{participants: users}
	svc.Chats = &fakeChatsForMatch{reachable: users}
	outbox := &recordingOutbox{fakeOutbox: fakeOutbox{failed: map[uuid.UUID]string{}}}
	svc.Outbox = outbox

	if err := svc.AskChatHelpers(context.Background(), req.ID, common.ChatID{Value: -1}, common.LocaleRU); err != nil {
		t.Fatal(err)
	}

	// teamMatchAskBatchSize caps a single trigger at 2 asks, even though 3
	// users were eligible.
	askEvents := 0
	for _, e := range outbox.enqueued {
		if e.eventType == "telegram.team-match-ask" {
			askEvents++
		}
	}
	if askEvents != teamMatchAskBatchSize {
		t.Fatalf("sent %d asks, want %d (the per-trigger batch cap)", askEvents, teamMatchAskBatchSize)
	}
	updated, _, err := requests.FindRequest(context.Background(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CrowdAsksSent != teamMatchAskBatchSize {
		t.Fatalf("CrowdAsksSent = %d, want %d", updated.CrowdAsksSent, teamMatchAskBatchSize)
	}
}

func TestAskChatHelpers_NoOpWhenRequestAlreadyResolved(t *testing.T) {
	svc, requests, _, _ := newTestTeamMatchService(nil)
	team := competition.Team{ID: common.NewTeamID(), Name: "AVANGAR"}
	req := enrichment.TeamMatchRequest{ID: common.NewRequestID(), ExternalName: "Avangar", Source: enrichment.SourceValveVRS, Status: enrichment.TeamMatchConfirmed, CreatedAt: svc.Clock.Now()}
	candidate := enrichment.TeamMatchCandidate{TeamID: team.ID, TeamName: team.Name, Score: 60, Kind: enrichment.CandidateKindFuzzy}
	if err := requests.CreateRequest(context.Background(), req, []enrichment.TeamMatchCandidate{candidate}); err != nil {
		t.Fatal(err)
	}
	users := []common.UserID{{Value: 1}}
	svc.Predictions = &fakePredictionsForMatch{participants: users}
	svc.Chats = &fakeChatsForMatch{reachable: users}
	outbox := &recordingOutbox{fakeOutbox: fakeOutbox{failed: map[uuid.UUID]string{}}}
	svc.Outbox = outbox

	if err := svc.AskChatHelpers(context.Background(), req.ID, common.ChatID{Value: -1}, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	if len(outbox.enqueued) != 0 {
		t.Fatalf("expected no asks for an already-resolved request, got %+v", outbox.enqueued)
	}
}
