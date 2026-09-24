package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// fakeTargetsForScope implements subscription.TargetRepository with a
// fixed, per-test list of subscriptions for one chat.
type fakeTargetsForScope struct {
	subs []subscription.TargetSubscription
}

func (f *fakeTargetsForScope) SubscribeTarget(_ context.Context, s subscription.TargetSubscription) (subscription.TargetSubscription, error) {
	return s, nil
}
func (f *fakeTargetsForScope) UnsubscribeTarget(context.Context, common.ChatID, subscription.TargetKind, string) error {
	return nil
}
func (f *fakeTargetsForScope) TargetSubscriptions(context.Context, common.ChatID) ([]subscription.TargetSubscription, error) {
	return f.subs, nil
}
func (f *fakeTargetsForScope) ChatsForTarget(context.Context, subscription.TargetKind, string) ([]common.ChatID, error) {
	return nil, nil
}

// fakeScoringForScope implements just enough of scoring.Repository for
// TournamentScopeService: a canned Leaderboard keyed by whether the period
// asked for the full tournament or one team's cut of it.
type fakeScoringForScope struct {
	full   []scoring.UserStanding
	byTeam map[string][]scoring.UserStanding
	calls  []scoring.StatsPeriod
}

func (f *fakeScoringForScope) AvailableMonths(context.Context, common.ChatID) ([]scoring.StatsMonth, error) {
	return nil, nil
}
func (f *fakeScoringForScope) AvailableEventIDs(context.Context, common.ChatID) ([]common.EventID, error) {
	return nil, nil
}
func (f *fakeScoringForScope) ReplaceAwards(context.Context, common.PollID, []scoring.Award) error {
	return nil
}
func (f *fakeScoringForScope) Leaderboard(_ context.Context, _ common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	f.calls = append(f.calls, period)
	if period.TeamID == nil {
		return f.full, nil
	}
	return f.byTeam[period.TeamID.Value.String()], nil
}
func (f *fakeScoringForScope) MedalCounts(context.Context, common.ChatID) (map[common.UserID]scoring.MedalCount, error) {
	return nil, nil
}
func (f *fakeScoringForScope) AwardMedals(context.Context, common.ChatID, common.EventID, []scoring.UserStanding, time.Time) error {
	return nil
}
func (f *fakeScoringForScope) EventCompletionHash(context.Context, common.ChatID, common.EventID) (string, bool, error) {
	return "", false, nil
}
func (f *fakeScoringForScope) MarkEventCompleted(context.Context, common.ChatID, common.EventID, string, time.Time) error {
	return nil
}
func (f *fakeScoringForScope) LockEventCompletion(context.Context, common.ChatID, common.EventID) error {
	return nil
}

func newScopeService(t *testing.T, subscribedEvent *common.EventID, targets []subscription.TargetSubscription,
	matches map[common.EventID][]competition.Match, scoringRepo *fakeScoringForScope) (*TournamentScopeService, common.ChatID) {
	t.Helper()
	chatID := common.ChatID{Value: 1}
	var eventSubs []subscription.EventSubscription
	if subscribedEvent != nil {
		eventSubs = []subscription.EventSubscription{{ChatID: chatID, EventID: *subscribedEvent, Active: true}}
	}
	subs := &fakeSubsForScopeWithSubscriptions{eventSubs: eventSubs}
	return &TournamentScopeService{
		Subscriptions: subs,
		Targets:       &fakeTargetsForScope{subs: targets},
		Catalog:       &fakeCatalogForCompletion{matches: matches},
		Scoring:       scoringRepo,
	}, chatID
}

// fakeSubsForScopeWithSubscriptions is fakeSubsForCompletion plus a working
// Subscriptions() — the scope service needs to read the chat's actual
// tournament subscriptions, which fakeSubsForCompletion always returns nil
// for.
type fakeSubsForScopeWithSubscriptions struct {
	eventSubs []subscription.EventSubscription
}

func (f *fakeSubsForScopeWithSubscriptions) Subscribe(_ context.Context, s subscription.EventSubscription) (subscription.EventSubscription, error) {
	return s, nil
}
func (f *fakeSubsForScopeWithSubscriptions) Unsubscribe(context.Context, common.ChatID, common.EventID) error {
	return nil
}
func (f *fakeSubsForScopeWithSubscriptions) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (f *fakeSubsForScopeWithSubscriptions) ActiveEventIDs(context.Context) ([]common.EventID, error) {
	return nil, nil
}
func (f *fakeSubsForScopeWithSubscriptions) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return f.eventSubs, nil
}

func matchBetween(eventID common.EventID, teamA, teamB common.TeamID) competition.Match {
	return competition.Match{
		ID:         common.MatchID{Value: uuid.New()},
		EventID:    eventID,
		FirstTeam:  &competition.Team{ID: teamA, Name: "A"},
		SecondTeam: &competition.Team{ID: teamB, Name: "B"},
		Status:     competition.MatchFinished,
	}
}

// Team subscription only, no tournament subscription: the chat gets the
// team-scoped merge, never the full leaderboard.
func TestTournamentScopeService_TeamOnly_ReturnsScopedNotFull(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	matches := map[common.EventID][]competition.Match{eventID: {matchBetween(eventID, teamA, teamB)}}

	scoringRepo := &fakeScoringForScope{
		full: []scoring.UserStanding{{UserID: common.UserID{Value: 9}, Points: 100}},
		byTeam: map[string][]scoring.UserStanding{
			teamA.Value.String(): {{UserID: common.UserID{Value: 1}, Points: 10, Predictions: 2}},
		},
	}
	targets := []subscription.TargetSubscription{{Kind: subscription.TargetTeam, TargetID: teamA.Value.String(), Active: true}}

	svc, chatID := newScopeService(t, nil, targets, matches, scoringRepo)
	view, err := svc.Resolve(context.Background(), chatID, eventID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Scope.FullTournament {
		t.Fatal("team subscription alone must not resolve to a full tournament view")
	}
	if len(view.Standings) != 1 || view.Standings[0].UserID.Value != 1 {
		t.Fatalf("expected the team-scoped standings, got %+v", view.Standings)
	}
	// It must never have asked the scoring repo for the unscoped full
	// leaderboard.
	for _, call := range scoringRepo.calls {
		if call.TeamID == nil {
			t.Fatalf("must not query the full leaderboard when only team-scoped, period=%+v", call)
		}
	}
}

// Team subscription AND tournament subscription for that tournament: full
// leaderboard, fetched via the unscoped period.
func TestTournamentScopeService_TeamPlusTournamentSub_ReturnsFull(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	matches := map[common.EventID][]competition.Match{eventID: {matchBetween(eventID, teamA, teamB)}}

	scoringRepo := &fakeScoringForScope{full: []scoring.UserStanding{{UserID: common.UserID{Value: 9}, Points: 100}}}
	targets := []subscription.TargetSubscription{{Kind: subscription.TargetTeam, TargetID: teamA.Value.String(), Active: true}}

	svc, chatID := newScopeService(t, &eventID, targets, matches, scoringRepo)
	view, err := svc.Resolve(context.Background(), chatID, eventID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !view.Scope.FullTournament {
		t.Fatal("expected full tournament view")
	}
	if len(view.Standings) != 1 || view.Standings[0].UserID.Value != 9 {
		t.Fatalf("expected the full standings, got %+v", view.Standings)
	}
}

// No subscription of any kind for this tournament: no access, no query
// issued at all.
func TestTournamentScopeService_NoSubscription_NoAccess(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	matches := map[common.EventID][]competition.Match{eventID: {matchBetween(eventID, teamA, teamB)}}

	scoringRepo := &fakeScoringForScope{full: []scoring.UserStanding{{UserID: common.UserID{Value: 9}, Points: 100}}}
	svc, chatID := newScopeService(t, nil, nil, matches, scoringRepo)
	view, err := svc.Resolve(context.Background(), chatID, eventID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Scope.HasAnyAccess() {
		t.Fatalf("expected no access, got %+v", view.Scope)
	}
	if len(view.Standings) != 0 {
		t.Fatalf("expected no standings, got %+v", view.Standings)
	}
	if len(scoringRepo.calls) != 0 {
		t.Fatalf("expected no leaderboard query when the chat has no access at all, got %d calls", len(scoringRepo.calls))
	}
}

// A team subscription for a team that does not play in this tournament
// must not leak any access.
func TestTournamentScopeService_SubscribedTeamNotInTournament_NoAccess(t *testing.T) {
	eventID := common.EventID{Value: uuid.New()}
	teamA := common.TeamID{Value: uuid.New()}
	teamB := common.TeamID{Value: uuid.New()}
	otherTeam := common.TeamID{Value: uuid.New()}
	matches := map[common.EventID][]competition.Match{eventID: {matchBetween(eventID, teamA, teamB)}}

	scoringRepo := &fakeScoringForScope{}
	targets := []subscription.TargetSubscription{{Kind: subscription.TargetTeam, TargetID: otherTeam.Value.String(), Active: true}}
	svc, chatID := newScopeService(t, nil, targets, matches, scoringRepo)

	view, err := svc.Resolve(context.Background(), chatID, eventID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.Scope.HasAnyAccess() {
		t.Fatalf("expected no access for a team not in this tournament's field, got %+v", view.Scope)
	}
}
