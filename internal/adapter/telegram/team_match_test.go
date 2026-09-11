package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// --- fakes ---

type fakeTeamMatchRepo struct {
	byID       map[string]*enrichment.TeamMatchRequest
	candidates map[string][]enrichment.TeamMatchCandidate
	responses  map[string]bool
}

func newFakeTeamMatchRepo() *fakeTeamMatchRepo {
	return &fakeTeamMatchRepo{byID: map[string]*enrichment.TeamMatchRequest{}, candidates: map[string][]enrichment.TeamMatchCandidate{}, responses: map[string]bool{}}
}
func (f *fakeTeamMatchRepo) seed(req enrichment.TeamMatchRequest, candidates []enrichment.TeamMatchCandidate) {
	cp := req
	f.byID[req.ID.String()] = &cp
	f.candidates[req.ID.String()] = candidates
}
func (f *fakeTeamMatchRepo) FindPendingByExternalName(context.Context, enrichment.Source, string) (*enrichment.TeamMatchRequest, error) {
	return nil, nil
}
func (f *fakeTeamMatchRepo) CreateRequest(_ context.Context, req enrichment.TeamMatchRequest, candidates []enrichment.TeamMatchCandidate) error {
	f.seed(req, candidates)
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
	r, ok := f.byID[id.String()]
	if !ok {
		return nil, nil, nil
	}
	return r, f.candidates[id.String()], nil
}
func (f *fakeTeamMatchRepo) RecordResponse(_ context.Context, requestID common.RequestID, userID common.UserID, candidateTeamID common.TeamID, answer enrichment.TeamMatchAnswer) error {
	f.responses[requestID.String()] = true
	cands := f.candidates[requestID.String()]
	for i := range cands {
		if cands[i].TeamID == candidateTeamID {
			if answer == enrichment.TeamMatchAnswerYes {
				cands[i].Yes++
			} else {
				cands[i].No++
			}
			cands[i].Score = enrichment.CrowdAdjustedScore(cands[i].Score, answer)
		}
	}
	f.candidates[requestID.String()] = cands
	return nil
}
func (f *fakeTeamMatchRepo) HasResponded(_ context.Context, requestID common.RequestID, _ common.UserID) (bool, error) {
	return f.responses[requestID.String()], nil
}
func (f *fakeTeamMatchRepo) IncrementCrowdAsksSent(context.Context, common.RequestID, int) error {
	return nil
}
func (f *fakeTeamMatchRepo) Resolve(_ context.Context, requestID common.RequestID, status enrichment.TeamMatchStatus, teamID *common.TeamID, at time.Time) error {
	if r, ok := f.byID[requestID.String()]; ok {
		r.Status = status
		r.BestTeamID = teamID
		r.ResolvedAt = &at
	}
	return nil
}

type fakeTeamMatchHelperRepo struct {
	optedOut map[int64]bool
}

func newFakeTeamMatchHelperRepo() *fakeTeamMatchHelperRepo {
	return &fakeTeamMatchHelperRepo{optedOut: map[int64]bool{}}
}
func (f *fakeTeamMatchHelperRepo) EligibleHelpers(context.Context, []common.UserID) ([]common.UserID, error) {
	return nil, nil
}
func (f *fakeTeamMatchHelperRepo) RecordAsk(context.Context, common.UserID, time.Time) error {
	return nil
}
func (f *fakeTeamMatchHelperRepo) SetOptedOut(_ context.Context, userID common.UserID, optedOut bool) error {
	f.optedOut[userID.Value] = optedOut
	return nil
}

type fakeTeamMatchOperatorRepo struct {
	appointed map[int64]bool
}

func newFakeTeamMatchOperatorRepo() *fakeTeamMatchOperatorRepo {
	return &fakeTeamMatchOperatorRepo{appointed: map[int64]bool{}}
}
func (f *fakeTeamMatchOperatorRepo) IsOperator(_ context.Context, userID common.UserID) (bool, error) {
	return f.appointed[userID.Value], nil
}
func (f *fakeTeamMatchOperatorRepo) AddOperator(_ context.Context, userID, _ common.UserID) error {
	f.appointed[userID.Value] = true
	return nil
}
func (f *fakeTeamMatchOperatorRepo) RemoveOperator(_ context.Context, userID common.UserID) error {
	delete(f.appointed, userID.Value)
	return nil
}
func (f *fakeTeamMatchOperatorRepo) ListOperators(context.Context) ([]common.UserID, error) {
	var out []common.UserID
	for id := range f.appointed {
		out = append(out, common.UserID{Value: id})
	}
	return out, nil
}

type fakeIdentityRepoForTelegram struct {
	saved []enrichment.MatchConfidence
}

func (f *fakeIdentityRepoForTelegram) FindTeamByExternalID(context.Context, enrichment.Source, string) (*common.TeamID, error) {
	return nil, nil
}
func (f *fakeIdentityRepoForTelegram) SaveIdentity(_ context.Context, _ common.TeamID, _ enrichment.Source, _, _ string, confidence enrichment.MatchConfidence) error {
	f.saved = append(f.saved, confidence)
	return nil
}
func (f *fakeIdentityRepoForTelegram) Aliases(context.Context, common.TeamID) ([]string, error) {
	return nil, nil
}
func (f *fakeIdentityRepoForTelegram) AllAliases(context.Context) (map[common.TeamID][]string, error) {
	return nil, nil
}

func teamMatchTestHandler(t *testing.T) (*UpdateHandler, *[]map[string]any, *fakeTeamMatchRepo, *fakeTeamMatchOperatorRepo) {
	t.Helper()
	server, calls := newRecordingServer(t)
	t.Cleanup(server.Close)
	handler, _ := newTestHandler(t, server)
	requests := newFakeTeamMatchRepo()
	operators := newFakeTeamMatchOperatorRepo()
	handler.TeamMatches = requests
	handler.TeamMatchHelpers = newFakeTeamMatchHelperRepo()
	handler.TeamMatchOperators = operators
	handler.TeamIdentity = &fakeIdentityRepoForTelegram{}
	handler.TeamMatchOperatorChatIDs = []int64{1}
	return handler, calls, requests, operators
}

func teamMatchPrivateCB(userID int64, data string) *CallbackQuery {
	return &CallbackQuery{ID: "cb", From: User{ID: userID, FirstName: "Alex"},
		Message: &Message{MessageID: 5, Chat: Chat{ID: userID, Type: "private"}}, Data: &data}
}

// --- access control ---

func TestTeamMatches_DeniedForNonOperator(t *testing.T) {
	handler, calls, requests, _ := teamMatchTestHandler(t)
	requests.seed(enrichment.TeamMatchRequest{ID: common.NewRequestID(), ExternalName: "Vitality", Status: enrichment.TeamMatchPending, BestScore: 70}, nil)

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(2, "team_matches:list:0")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "error.forbidden")) {
		t.Fatalf("expected forbidden reply for a non-operator, got %q", lastText(*calls))
	}
}

func TestTeamMatches_RootOperatorSeesPendingQueue(t *testing.T) {
	handler, calls, requests, _ := teamMatchTestHandler(t)
	requests.seed(enrichment.TeamMatchRequest{ID: common.NewRequestID(), ExternalName: "Vitality", Status: enrichment.TeamMatchPending, BestScore: 70}, nil)

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, "team_matches:list:0")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.ContainsFunc(cds, func(d string) bool { return strings.HasPrefix(d, "team_matches:open:") }) {
		t.Fatalf("expected the pending request listed as a button, got %v", cds)
	}
}

func TestTeamMatches_AppointedOperatorGetsAccessAfterAdd(t *testing.T) {
	handler, calls, requests, operators := teamMatchTestHandler(t)
	requests.seed(enrichment.TeamMatchRequest{ID: common.NewRequestID(), ExternalName: "Vitality", Status: enrichment.TeamMatchPending, BestScore: 70}, nil)

	// User 2 is neither root nor appointed yet.
	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(2, "team_matches:list:0")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "error.forbidden")) {
		t.Fatal("expected denial before appointment")
	}

	operators.appointed[2] = true
	*calls = nil
	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(2, "team_matches:list:0")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.ContainsFunc(cds, func(d string) bool { return strings.HasPrefix(d, "team_matches:open:") }) {
		t.Fatalf("expected the appointed operator to see the queue, got %v", cds)
	}
}

// --- /team_match_admin ---

func TestTeamMatchAdmin_OnlyRootMayAppoint(t *testing.T) {
	handler, calls, _, operators := teamMatchTestHandler(t)

	text := "/team_match_admin add 2"
	msg := &Message{MessageID: 1, Chat: Chat{ID: 5, Type: "private"}, From: &User{ID: 5, FirstName: "NotRoot"}, Text: &text}
	if err := handler.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastText(*calls), ru(t, "error.forbidden")) {
		t.Fatalf("expected a non-root user to be denied, got %q", lastText(*calls))
	}
	if operators.appointed[2] {
		t.Fatal("a non-root command must not have appointed anyone")
	}

	*calls = nil
	rootMsg := &Message{MessageID: 2, Chat: Chat{ID: 1, Type: "private"}, From: &User{ID: 1, FirstName: "Root"}, Text: &text}
	if err := handler.handleMessage(context.Background(), rootMsg); err != nil {
		t.Fatal(err)
	}
	if !operators.appointed[2] {
		t.Fatal("expected the root operator's /team_match_admin add to appoint user 2")
	}
}

// --- card / pick / reject ---

func TestTeamMatchCard_PickConfirmsIdentityAndResolves(t *testing.T) {
	handler, calls, requests, _ := teamMatchTestHandler(t)
	teamID := common.NewTeamID()
	req := enrichment.TeamMatchRequest{ID: common.NewRequestID(), ExternalName: "Vitality", Source: enrichment.SourceValveVRS, Status: enrichment.TeamMatchPending, BestScore: 70}
	requests.seed(req, []enrichment.TeamMatchCandidate{{TeamID: teamID, TeamName: "Team Vitality", Score: 70, Kind: enrichment.CandidateKindFuzzy}})

	data := "team_matches:open:" + req.ID.String()
	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, data)); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	pickData := "team_matches:pick:" + req.ID.String() + ":0"
	if !slices.Contains(cds, pickData) {
		t.Fatalf("expected a pick button for candidate 0, got %v", cds)
	}

	*calls = nil
	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, pickData)); err != nil {
		t.Fatal(err)
	}
	identity := handler.TeamIdentity.(*fakeIdentityRepoForTelegram)
	if len(identity.saved) != 1 || identity.saved[0] != enrichment.ConfidenceManual {
		t.Fatalf("expected one manual-confidence identity save, got %+v", identity.saved)
	}
	updated, _, _ := requests.FindRequest(context.Background(), req.ID)
	if updated.Status != enrichment.TeamMatchConfirmed || updated.BestTeamID == nil || *updated.BestTeamID != teamID {
		t.Fatalf("expected the request confirmed with the picked team, got %+v", updated)
	}
}

func TestTeamMatchCard_RejectResolvesWithNoTeam(t *testing.T) {
	handler, _, requests, _ := teamMatchTestHandler(t)
	req := enrichment.TeamMatchRequest{ID: common.NewRequestID(), ExternalName: "Vitality", Status: enrichment.TeamMatchPending, BestScore: 70}
	requests.seed(req, []enrichment.TeamMatchCandidate{{TeamID: common.NewTeamID(), TeamName: "Team Vitality", Score: 70}})

	data := "team_matches:reject:" + req.ID.String()
	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(1, data)); err != nil {
		t.Fatal(err)
	}
	updated, _, _ := requests.FindRequest(context.Background(), req.ID)
	if updated.Status != enrichment.TeamMatchRejected {
		t.Fatalf("expected the request rejected, got %+v", updated)
	}
}

// --- crowd ask / consent ---

func TestTeamMatchAsk_AnswerRecordsResponseAndOffersConsentCheck(t *testing.T) {
	handler, calls, requests, _ := teamMatchTestHandler(t)
	teamID := common.NewTeamID()
	req := enrichment.TeamMatchRequest{ID: common.NewRequestID(), ExternalName: "Vitality", Status: enrichment.TeamMatchPending, BestScore: 60}
	requests.seed(req, []enrichment.TeamMatchCandidate{{TeamID: teamID, TeamName: "Team Vitality", Score: 60}})

	data := "tmatch:ans:" + req.ID.String() + ":yes"
	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(42, data)); err != nil {
		t.Fatal(err)
	}
	if answered, _ := requests.HasResponded(context.Background(), req.ID, common.UserID{Value: 42}); !answered {
		t.Fatal("expected the response to be recorded")
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "tmatch:continue:yes") || !slices.Contains(cds, "tmatch:continue:no") {
		t.Fatalf("expected the follow-up consent-check buttons, got %v", cds)
	}
}

func TestTeamMatchAsk_ConsentNoOptsOut(t *testing.T) {
	handler, _, _, _ := teamMatchTestHandler(t)

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(42, "tmatch:continue:no")); err != nil {
		t.Fatal(err)
	}
	helpers := handler.TeamMatchHelpers.(*fakeTeamMatchHelperRepo)
	if !helpers.optedOut[42] {
		t.Fatal("expected user 42 to be opted out after answering 'no' to the consent check")
	}
}

func TestTeamMatchAsk_ConsentYesStaysOptedIn(t *testing.T) {
	handler, _, _, _ := teamMatchTestHandler(t)

	if err := handler.handleCallback(context.Background(), teamMatchPrivateCB(42, "tmatch:continue:yes")); err != nil {
		t.Fatal(err)
	}
	helpers := handler.TeamMatchHelpers.(*fakeTeamMatchHelperRepo)
	if helpers.optedOut[42] {
		t.Fatal("expected user 42 to remain opted in after answering 'yes'")
	}
}
