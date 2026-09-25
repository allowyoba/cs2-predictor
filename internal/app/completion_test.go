package app

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

type fakeCatalogForCompletion struct {
	matches map[common.EventID][]competition.Match
}

func (f *fakeCatalogForCompletion) SearchTeams(context.Context, string, int, []competition.GameCode) ([]competition.Team, error) {
	return nil, nil
}

func (f *fakeCatalogForCompletion) SearchEvents(context.Context, string, int, bool, []competition.GameCode) ([]competition.Event, error) {
	return nil, nil
}
func (f *fakeCatalogForCompletion) FindEvent(context.Context, common.EventID) (*competition.Event, error) {
	return nil, nil
}
func (f *fakeCatalogForCompletion) FindEvents(context.Context, []common.EventID) ([]competition.Event, error) {
	return nil, nil
}
func (f *fakeCatalogForCompletion) FindMatch(context.Context, common.MatchID) (*competition.Match, error) {
	return nil, nil
}
func (f *fakeCatalogForCompletion) FindUnstartedMatches(context.Context, common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeCatalogForCompletion) FindUnstartedMatchesForEvents(context.Context, []common.EventID) ([]competition.Match, error) {
	return nil, nil
}
func (f *fakeCatalogForCompletion) FindMatches(_ context.Context, eventID common.EventID) ([]competition.Match, error) {
	return f.matches[eventID], nil
}
func (f *fakeCatalogForCompletion) SaveEvent(_ context.Context, e competition.Event) (competition.Event, error) {
	return e, nil
}
func (f *fakeCatalogForCompletion) SaveMatch(_ context.Context, m competition.Match) (competition.Match, error) {
	return m, nil
}

type fakeSubsForCompletion struct{ chats []common.ChatID }

func (f *fakeSubsForCompletion) Subscribe(_ context.Context, s subscription.EventSubscription) (subscription.EventSubscription, error) {
	return s, nil
}
func (f *fakeSubsForCompletion) Unsubscribe(context.Context, common.ChatID, common.EventID) error {
	return nil
}
func (f *fakeSubsForCompletion) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return f.chats, nil
}
func (f *fakeSubsForCompletion) ActiveEventIDs(context.Context) ([]common.EventID, error) {
	return nil, nil
}
func (f *fakeSubsForCompletion) Subscriptions(context.Context, common.ChatID) ([]subscription.EventSubscription, error) {
	return nil, nil
}

// fakeChatsForCompletion implements chat.Repository with just enough
// behavior for EventCompletionService: no per-event topic override, and a
// default chat with no DefaultTopicID.
type fakeChatsForCompletion struct{}

func (fakeChatsForCompletion) Find(_ context.Context, chatID common.ChatID) (*chat.Settings, error) {
	return &chat.Settings{ChatID: chatID, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}, nil
}
func (fakeChatsForCompletion) Save(_ context.Context, s chat.Settings) (chat.Settings, error) {
	return s, nil
}
func (fakeChatsForCompletion) UserTimezone(context.Context, common.UserID) (*string, error) {
	return nil, nil
}
func (fakeChatsForCompletion) SetUserTimezone(context.Context, common.UserID, string) error {
	return nil
}

func (fakeChatsForCompletion) SetAutoSubscribeGame(context.Context, common.ChatID, competition.GameCode, bool) error {
	return nil
}

func (fakeChatsForCompletion) SetEnabledGames(context.Context, common.ChatID, []competition.GameCode) error {
	return nil
}
func (fakeChatsForCompletion) IsModerator(context.Context, common.ChatID, common.UserID) (bool, error) {
	return false, nil
}
func (fakeChatsForCompletion) AddModerator(context.Context, chat.Moderator) error { return nil }
func (fakeChatsForCompletion) RemoveModerator(context.Context, common.ChatID, common.UserID) error {
	return nil
}
func (fakeChatsForCompletion) EventTopic(context.Context, common.ChatID, common.EventID) (*int64, error) {
	return nil, nil
}
func (fakeChatsForCompletion) SaveEventTopic(context.Context, chat.EventTopic) error { return nil }
func (fakeChatsForCompletion) MigrateChatID(context.Context, common.ChatID, common.ChatID) error {
	return nil
}
func (fakeChatsForCompletion) ModeratorPermissions(context.Context, common.ChatID, common.UserID) ([]chat.Permission, error) {
	return nil, nil
}
func (fakeChatsForCompletion) SetModeratorPermissions(context.Context, common.ChatID, common.UserID, []chat.Permission) error {
	return nil
}
func (fakeChatsForCompletion) UserProfile(context.Context, common.UserID) (*chat.UserProfile, error) {
	return nil, nil
}
func (fakeChatsForCompletion) UserProfiles(context.Context, []common.UserID) (map[common.UserID]chat.UserProfile, error) {
	return nil, nil
}
func (fakeChatsForCompletion) ClearEventTopic(context.Context, common.ChatID, common.EventID) error {
	return nil
}

type fakeScoringForCompletion struct {
	leaderboard      []scoring.UserStanding
	completionHashes map[common.ChatID]string
	medalsAwarded    int
}

func (f *fakeScoringForCompletion) AvailableMonths(context.Context, common.ChatID) ([]scoring.StatsMonth, error) {
	return nil, nil
}
func (f *fakeScoringForCompletion) AvailableEventIDs(context.Context, common.ChatID) ([]common.EventID, error) {
	return nil, nil
}
func (f *fakeScoringForCompletion) ReplaceAwards(context.Context, common.PollID, []scoring.Award) error {
	return nil
}
func (f *fakeScoringForCompletion) Leaderboard(context.Context, common.ChatID, scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	return f.leaderboard, nil
}
func (f *fakeScoringForCompletion) MedalCounts(context.Context, common.ChatID) (map[common.UserID]scoring.MedalCount, error) {
	return nil, nil
}
func (f *fakeScoringForCompletion) AwardMedals(context.Context, common.ChatID, common.EventID, []scoring.UserStanding, time.Time) error {
	f.medalsAwarded++
	return nil
}
func (f *fakeScoringForCompletion) EventCompletionHash(_ context.Context, chatID common.ChatID, _ common.EventID) (string, bool, error) {
	h, ok := f.completionHashes[chatID]
	return h, ok, nil
}
func (f *fakeScoringForCompletion) MarkEventCompleted(_ context.Context, chatID common.ChatID, _ common.EventID, hash string, _ time.Time) error {
	if f.completionHashes == nil {
		f.completionHashes = map[common.ChatID]string{}
	}
	f.completionHashes[chatID] = hash
	return nil
}
func (f *fakeScoringForCompletion) LockEventCompletion(context.Context, common.ChatID, common.EventID) error {
	return nil
}

func newMatch(status competition.MatchStatus) competition.Match {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	return competition.Match{ID: common.NewMatchID(), Status: status, Format: format}
}

func newTestOutboxForCompletion() *fakeSettlementOutbox { return &fakeSettlementOutbox{} }

// Ground truth: completion defers until every match has reached a terminal
// state — a single still-RUNNING match means no medals, no notification.
func TestEventCompletionService_DefersWhileAnyMatchIsNotTerminal(t *testing.T) {
	eventID := common.NewEventID()
	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {newMatch(competition.MatchFinished), newMatch(competition.MatchRunning)},
	}}
	subs := &fakeSubsForCompletion{chats: []common.ChatID{{Value: -1}}}
	scoringRepo := &fakeScoringForCompletion{}
	outbox := newTestOutboxForCompletion()

	svc := NewEventCompletionService(catalog, subs, fakeChatsForCompletion{}, scoringRepo, nil, outbox, common.SystemUTCClock(), identityTx, slog.Default()).WithSwitches(allNotificationsOn{})
	if err := svc.Complete(context.Background(), competition.Event{ID: eventID}); err != nil {
		t.Fatal(err)
	}
	if scoringRepo.medalsAwarded != 0 || len(outbox.enqueued) != 0 {
		t.Fatalf("expected no medals/notification while a match is still running, got medals=%d outbox=%v", scoringRepo.medalsAwarded, outbox.enqueued)
	}
}

func TestEventCompletionService_AwardsMedalsAndNotifiesOnceAllMatchesTerminal(t *testing.T) {
	eventID := common.NewEventID()
	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {newMatch(competition.MatchFinished), newMatch(competition.MatchCancelled), newMatch(competition.MatchForfeit)},
	}}
	chatID := common.ChatID{Value: -1}
	subs := &fakeSubsForCompletion{chats: []common.ChatID{chatID}}
	scoringRepo := &fakeScoringForCompletion{leaderboard: []scoring.UserStanding{{UserID: common.UserID{Value: 1}, DisplayName: "A", Points: 5, Rank: 1, Predictions: 3}}}
	outbox := newTestOutboxForCompletion()

	svc := NewEventCompletionService(catalog, subs, fakeChatsForCompletion{}, scoringRepo, nil, outbox, common.SystemUTCClock(), identityTx, slog.Default()).WithSwitches(allNotificationsOn{})
	if err := svc.Complete(context.Background(), competition.Event{ID: eventID, Name: "Major Final"}); err != nil {
		t.Fatal(err)
	}
	if scoringRepo.medalsAwarded != 1 {
		t.Fatalf("medalsAwarded = %d, want 1", scoringRepo.medalsAwarded)
	}
	if len(outbox.enqueued) != 1 || outbox.enqueued[0] != "telegram.event-finished" {
		t.Fatalf("outbox.enqueued = %v, want [telegram.event-finished]", outbox.enqueued)
	}
	if _, ok := scoringRepo.completionHashes[chatID]; !ok {
		t.Fatal("expected a completion hash to be recorded for the chat")
	}
}

// Enabling a new game in an existing chat pulls in that game's back
// catalogue, and the chats auto-subscribed to those tournaments never voted
// in them. A recap with an empty leaderboard is noise, so the completion is
// recorded silently instead.
func TestEventCompletionService_StaysSilentWhenNobodyInTheChatPredicted(t *testing.T) {
	eventID := common.NewEventID()
	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {newMatch(competition.MatchFinished)},
	}}
	chatID := common.ChatID{Value: -1}
	subs := &fakeSubsForCompletion{chats: []common.ChatID{chatID}}
	// A member on the board with nothing predicted is still nobody who
	// played: the recap has no result to report.
	scoringRepo := &fakeScoringForCompletion{leaderboard: []scoring.UserStanding{{UserID: common.UserID{Value: 1}, DisplayName: "A", Rank: 1}}}
	outbox := newTestOutboxForCompletion()

	svc := NewEventCompletionService(catalog, subs, fakeChatsForCompletion{}, scoringRepo, nil, outbox, common.SystemUTCClock(), identityTx, slog.Default()).WithSwitches(allNotificationsOn{})
	if err := svc.Complete(context.Background(), competition.Event{ID: eventID, Name: "Old Major"}); err != nil {
		t.Fatal(err)
	}
	if len(outbox.enqueued) != 0 || scoringRepo.medalsAwarded != 0 {
		t.Fatalf("expected no recap for a tournament nobody predicted, got medals=%d outbox=%v", scoringRepo.medalsAwarded, outbox.enqueued)
	}
	if _, ok := scoringRepo.completionHashes[chatID]; !ok {
		t.Fatal("the completion must still be recorded, or every sync re-runs this decision")
	}
}

// Ground truth: re-completing with unchanged standings is a no-op per chat.
func TestEventCompletionService_IsIdempotentPerChat(t *testing.T) {
	eventID := common.NewEventID()
	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {newMatch(competition.MatchFinished)},
	}}
	chatID := common.ChatID{Value: -1}
	subs := &fakeSubsForCompletion{chats: []common.ChatID{chatID}}
	scoringRepo := &fakeScoringForCompletion{leaderboard: []scoring.UserStanding{{UserID: common.UserID{Value: 1}, DisplayName: "A", Points: 5, Rank: 1, Predictions: 3}}}
	outbox := newTestOutboxForCompletion()

	svc := NewEventCompletionService(catalog, subs, fakeChatsForCompletion{}, scoringRepo, nil, outbox, common.SystemUTCClock(), identityTx, slog.Default()).WithSwitches(allNotificationsOn{})
	event := competition.Event{ID: eventID, Name: "Major Final"}

	if err := svc.Complete(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if scoringRepo.medalsAwarded != 1 || len(outbox.enqueued) != 1 {
		t.Fatalf("expected a single medal award/notification across two identical Complete calls, got medals=%d outbox=%v", scoringRepo.medalsAwarded, outbox.enqueued)
	}
}

func (fakeChatsForCompletion) ListModerators(context.Context, common.ChatID) ([]chat.ModeratorInfo, error) {
	return nil, nil
}
func (fakeChatsForCompletion) RecordManaged(context.Context, common.ChatID, common.UserID) error {
	return nil
}
func (fakeChatsForCompletion) ManagedChats(context.Context, common.UserID) ([]chat.Settings, error) {
	return nil, nil
}
func (fakeChatsForCompletion) SetDMSession(context.Context, common.UserID, common.ChatID) error {
	return nil
}
func (fakeChatsForCompletion) DMSession(context.Context, common.UserID) (*common.ChatID, error) {
	return nil, nil
}
func (fakeChatsForCompletion) ClearDMSession(context.Context, common.UserID) error { return nil }
func (fakeChatsForCompletion) UserLocale(context.Context, common.UserID) (*common.LocaleCode, error) {
	return nil, nil
}
func (fakeChatsForCompletion) SetUserLocale(context.Context, common.UserID, common.LocaleCode) error {
	return nil
}

func (fakeChatsForCompletion) SetDMReachable(context.Context, common.UserID, bool) error { return nil }
func (fakeChatsForCompletion) FilterDMReachable(context.Context, []common.UserID) ([]common.UserID, error) {
	return nil, nil
}
func (fakeChatsForCompletion) Nickname(context.Context, common.UserID) (*string, error) {
	return nil, nil
}
func (fakeChatsForCompletion) SetNickname(context.Context, common.UserID, string) error { return nil }

// fakeRecapAudience answers which of the candidates asked for recaps.
type fakeRecapAudience struct {
	optedIn map[int64]bool
	asked   []common.NotificationKind
}

func (a *fakeRecapAudience) Recipients(_ context.Context, kind common.NotificationKind, candidates []common.UserID) ([]common.UserID, error) {
	a.asked = append(a.asked, kind)
	var out []common.UserID
	for _, c := range candidates {
		if a.optedIn[c.Value] {
			out = append(out, c)
		}
	}
	return out, nil
}

// The chat's recap is everyone's result and nobody's in particular. This
// is the other half: where the reader finished, out of how many, and what
// they got right — and only for the people who asked for DMs at all.
func TestEventCompletion_DMsEachParticipantTheirOwnResult(t *testing.T) {
	eventID := common.NewEventID()
	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {newMatch(competition.MatchFinished)},
	}}
	chatID := common.ChatID{Value: -1}
	subs := &fakeSubsForCompletion{chats: []common.ChatID{chatID}}
	scoringRepo := &fakeScoringForCompletion{leaderboard: []scoring.UserStanding{
		{UserID: common.UserID{Value: 1}, DisplayName: "Opted in", Rank: 1, Points: 20, Predictions: 8, ExactPredictions: 3, CorrectPredictions: 6},
		{UserID: common.UserID{Value: 2}, DisplayName: "Opted out", Rank: 2, Points: 12, Predictions: 7, ExactPredictions: 1, CorrectPredictions: 4},
		{UserID: common.UserID{Value: 3}, DisplayName: "Never voted", Rank: 3},
	}}
	outbox := newTestOutboxForCompletion()
	audience := &fakeRecapAudience{optedIn: map[int64]bool{1: true, 3: true}}

	svc := NewEventCompletionService(catalog, subs, fakeChatsForCompletion{}, scoringRepo, nil, outbox,
		common.SystemUTCClock(), identityTx, slog.Default()).WithSwitches(allNotificationsOn{}).WithPersonalRecaps(audience)
	if err := svc.Complete(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatal(err)
	}

	var personal int
	for _, eventType := range outbox.enqueued {
		if eventType == "telegram.event-recap-personal" {
			personal++
		}
	}
	if personal != 1 {
		t.Fatalf("expected one personal recap — the opted-in participant — got %d of %v", personal, outbox.enqueued)
	}
	// Its own switch, not the per-match one: a tournament recap every few
	// weeks and a result note several times a day are different appetites.
	if len(audience.asked) != 1 || audience.asked[0] != common.NotifyEventRecaps {
		t.Fatalf("asked about %v, want the tournament recap preference", audience.asked)
	}
	// The chat's own recap still goes out regardless.
	var chatRecap bool
	for _, eventType := range outbox.enqueued {
		if eventType == "telegram.event-finished" {
			chatRecap = true
		}
	}
	if !chatRecap {
		t.Fatal("the chat recap must be unaffected by the personal one")
	}
}

// Without an audience wired the personal half is simply off, and the chat
// recap behaves exactly as it did before.
func TestEventCompletion_SendsNoPersonalRecapsWithoutAnAudience(t *testing.T) {
	eventID := common.NewEventID()
	catalog := &fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
		eventID: {newMatch(competition.MatchFinished)},
	}}
	subs := &fakeSubsForCompletion{chats: []common.ChatID{{Value: -1}}}
	scoringRepo := &fakeScoringForCompletion{leaderboard: []scoring.UserStanding{
		{UserID: common.UserID{Value: 1}, DisplayName: "A", Rank: 1, Points: 5, Predictions: 3},
	}}
	outbox := newTestOutboxForCompletion()

	svc := NewEventCompletionService(catalog, subs, fakeChatsForCompletion{}, scoringRepo, nil, outbox,
		common.SystemUTCClock(), identityTx, slog.Default()).WithSwitches(allNotificationsOn{})
	if err := svc.Complete(context.Background(), competition.Event{ID: eventID, Name: "Major"}); err != nil {
		t.Fatal(err)
	}

	for _, eventType := range outbox.enqueued {
		if eventType == "telegram.event-recap-personal" {
			t.Fatal("no audience means no personal recaps")
		}
	}
}

// FindPlayableMatchesForEvents widens the same fixture by one status; the
// tests here only ever seed unstarted matches, so it answers the same way.
func (f *fakeCatalogForCompletion) FindPlayableMatchesForEvents(ctx context.Context, ids []common.EventID) ([]competition.Match, error) {
	return f.FindUnstartedMatchesForEvents(ctx, ids)
}

func (fakeChatsForCompletion) PrefersHLTVLogos(context.Context, common.UserID) (bool, error) {
	return false, nil
}

func (fakeChatsForCompletion) SetPrefersHLTVLogos(context.Context, common.UserID, bool) error {
	return nil
}
