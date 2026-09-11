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
func (f *fakeSnapshotRepo) AllSnapshot(context.Context) ([]enrichment.RankedTeam, error) {
	return f.entries, nil
}

type fakeRankingRepoForMatch struct {
	byTeam map[common.TeamID]enrichment.TeamRanking
	saved  []enrichment.TeamRanking
}

func newFakeRankingRepoForMatch() *fakeRankingRepoForMatch {
	return &fakeRankingRepoForMatch{byTeam: map[common.TeamID]enrichment.TeamRanking{}}
}
func (f *fakeRankingRepoForMatch) SaveRanking(_ context.Context, r enrichment.TeamRanking) error {
	f.saved = append(f.saved, r)
	f.byTeam[r.TeamID] = r
	return nil
}
func (f *fakeRankingRepoForMatch) FindRanking(_ context.Context, teamID common.TeamID, _ enrichment.Source) (*enrichment.TeamRanking, error) {
	if r, ok := f.byTeam[teamID]; ok {
		return &r, nil
	}
	return nil, nil
}
func (f *fakeRankingRepoForMatch) FindRankings(context.Context, []common.TeamID, enrichment.Source) (map[common.TeamID]enrichment.TeamRanking, error) {
	return f.byTeam, nil
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
		Rankings: rankings, Identity: identity,
		Predictions: &fakePredictionsForMatch{}, Chats: &fakeChatsForMatch{},
		Outbox: &recordingOutbox{fakeOutbox: fakeOutbox{failed: map[uuid.UUID]string{}}},
		Clock:  common.FixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), Log: slog.Default(),
	}
	return svc, requests, rankings, identity
}

// --- EnsureRequest ---

func TestEnsureRequest_AutoAcceptsHighFuzzyScore(t *testing.T) {
	rank := 3
	snapshot := []enrichment.RankedTeam{{Identity: enrichment.TeamIdentity{Name: "Team Vitality"}, GlobalRank: &rank, Source: enrichment.SourceValveVRS}}
	svc, requests, rankings, identity := newTestTeamMatchService(snapshot)
	team := competition.Team{ID: common.NewTeamID(), Name: "Vitality"}

	reqID, err := svc.EnsureRequest(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
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

	reqID, err := svc.EnsureRequest(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
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

	reqID, err := svc.EnsureRequest(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
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

	reqID, err := svc.EnsureRequest(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
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

	firstID, err := svc.EnsureRequest(context.Background(), teamA)
	if err != nil || firstID == nil {
		t.Fatalf("expected a request from the first call, err=%v id=%v", err, firstID)
	}
	secondID, err := svc.EnsureRequest(context.Background(), teamB)
	if err != nil || secondID == nil {
		t.Fatalf("expected a request from the second call, err=%v id=%v", err, secondID)
	}
	if firstID.String() != secondID.String() {
		t.Fatalf("expected the same request to be reused for the same external name, got %s and %s", firstID.String(), secondID.String())
	}
	if pending, _ := requests.ListPending(context.Background(), 10); len(pending) != 1 {
		t.Fatalf("expected exactly one pending request, got %+v", pending)
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
