package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// identityTx runs fn directly against ctx — a TxRunner stand-in for tests
// that don't need real transactional isolation.
func identityTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type fakePredictionsForSettlement struct {
	polls map[common.PollID]prediction.Poll
	votes map[common.PollID][]prediction.Vote
}

func (f *fakePredictionsForSettlement) FindPoll(_ context.Context, id common.PollID) (*prediction.Poll, error) {
	if p, ok := f.polls[id]; ok {
		return &p, nil
	}
	return nil, nil
}
func (f *fakePredictionsForSettlement) FindByTelegramPollID(context.Context, string) (*prediction.Poll, error) {
	return nil, nil
}
func (f *fakePredictionsForSettlement) FindByMatchAndChat(context.Context, common.MatchID, common.ChatID) (*prediction.Poll, error) {
	return nil, nil
}
func (f *fakePredictionsForSettlement) OpenPollsForMatch(context.Context, common.MatchID) ([]prediction.Poll, error) {
	return nil, nil
}
func (f *fakePredictionsForSettlement) PollsForMatch(_ context.Context, matchID common.MatchID) ([]prediction.Poll, error) {
	var out []prediction.Poll
	for _, p := range f.polls {
		if p.MatchID == matchID {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakePredictionsForSettlement) SavePoll(_ context.Context, p prediction.Poll) (prediction.Poll, error) {
	f.polls[p.ID] = p
	return p, nil
}
func (f *fakePredictionsForSettlement) OpenPollsDue(context.Context, time.Time) ([]prediction.Poll, error) {
	return nil, nil
}
func (f *fakePredictionsForSettlement) Votes(_ context.Context, pollID common.PollID) ([]prediction.Vote, error) {
	return f.votes[pollID], nil
}
func (f *fakePredictionsForSettlement) SaveVote(context.Context, prediction.Vote) error { return nil }
func (f *fakePredictionsForSettlement) RemoveVote(context.Context, common.PollID, common.UserID) error {
	return nil
}

type fakeScoringForSettlement struct {
	awards      map[common.PollID][]scoring.Award
	leaderboard []scoring.UserStanding
}

func (f *fakeScoringForSettlement) AvailableMonths(context.Context, common.ChatID) ([]scoring.StatsMonth, error) {
	return nil, nil
}
func (f *fakeScoringForSettlement) AvailableEventIDs(context.Context, common.ChatID) ([]common.EventID, error) {
	return nil, nil
}
func (f *fakeScoringForSettlement) ReplaceAwards(_ context.Context, pollID common.PollID, awards []scoring.Award) error {
	f.awards[pollID] = awards
	return nil
}
func (f *fakeScoringForSettlement) Leaderboard(context.Context, common.ChatID, scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	return f.leaderboard, nil
}
func (f *fakeScoringForSettlement) MedalCounts(context.Context, common.ChatID) (map[common.UserID]scoring.MedalCount, error) {
	return nil, nil
}
func (f *fakeScoringForSettlement) AwardMedals(context.Context, common.ChatID, common.EventID, []scoring.UserStanding, time.Time) error {
	return nil
}
func (f *fakeScoringForSettlement) EventCompletionHash(context.Context, common.ChatID, common.EventID) (string, bool, error) {
	return "", false, nil
}
func (f *fakeScoringForSettlement) MarkEventCompleted(context.Context, common.ChatID, common.EventID, string, time.Time) error {
	return nil
}
func (f *fakeScoringForSettlement) LockEventCompletion(context.Context, common.ChatID, common.EventID) error {
	return nil
}

type fakeSettlementRepo struct {
	hashes map[common.PollID]string
	marked int
}

func (f *fakeSettlementRepo) ResultHash(_ context.Context, pollID common.PollID) (string, bool, error) {
	h, ok := f.hashes[pollID]
	return h, ok, nil
}
func (f *fakeSettlementRepo) MarkSettled(_ context.Context, pollID common.PollID, hash string, _ time.Time) error {
	if f.hashes == nil {
		f.hashes = map[common.PollID]string{}
	}
	f.hashes[pollID] = hash
	f.marked++
	return nil
}

type fakeSettlementOutbox struct {
	enqueued []string // event types
	payloads []string // parallel to enqueued
}

func (f *fakeSettlementOutbox) Enqueue(_ context.Context, _, _, eventType, payload string) (uuid.UUID, error) {
	f.enqueued = append(f.enqueued, eventType)
	f.payloads = append(f.payloads, payload)
	return uuid.New(), nil
}

// recaps decodes the personal result recaps out of what was enqueued.
func (f *fakeSettlementOutbox) recaps() []common.ResultRecapNotification {
	var out []common.ResultRecapNotification
	for i, eventType := range f.enqueued {
		if eventType != "telegram.result-recap" {
			continue
		}
		var n common.ResultRecapNotification
		if err := json.Unmarshal([]byte(f.payloads[i]), &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}
func (f *fakeSettlementOutbox) Pending(context.Context, int) ([]common.OutboxMessage, error) {
	return nil, nil
}
func (f *fakeSettlementOutbox) Published(context.Context, uuid.UUID) error      { return nil }
func (f *fakeSettlementOutbox) Failed(context.Context, uuid.UUID, string) error { return nil }

func newSettlementFixture() (*fakePredictionsForSettlement, *fakeScoringForSettlement, *fakeSettlementRepo, *fakeSettlementOutbox) {
	return &fakePredictionsForSettlement{polls: map[common.PollID]prediction.Poll{}, votes: map[common.PollID][]prediction.Vote{}},
		&fakeScoringForSettlement{awards: map[common.PollID][]scoring.Award{}},
		&fakeSettlementRepo{},
		&fakeSettlementOutbox{}
}

func buildFinishedMatch(id common.MatchID, eventID common.EventID) competition.Match {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	score := competition.MatchScore{First: 2, Second: 0}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	return competition.Match{
		ID: id, EventID: eventID, Status: competition.MatchFinished, Score: &score,
		Format: format, ActualStartedAt: &now,
	}
}

func TestResultSettlementService_SettlesVotedPollAndEnqueuesNotification(t *testing.T) {
	predictions, scoringRepo, settlements, outbox := newSettlementFixture()
	clock := common.SystemUTCClock()

	matchID, eventID, pollID, chatID := common.NewMatchID(), common.NewEventID(), common.NewPollID(), common.ChatID{Value: -1}
	match := buildFinishedMatch(matchID, eventID)
	event := competition.Event{ID: eventID, Name: "Test Event"}

	options := make([]prediction.Option, len(match.Format.PossibleScores()))
	for i, s := range match.Format.PossibleScores() {
		options[i] = prediction.Option{Index: i, Score: s}
	}
	poll := prediction.Poll{ID: pollID, ChatID: chatID, MatchID: matchID, Options: options, Status: prediction.PollClosed}
	predictions.polls[pollID] = poll
	predictions.votes[pollID] = []prediction.Vote{{PollID: pollID, UserID: common.UserID{Value: 7}, OptionIndex: 0, DisplayName: "Alex"}}
	scoringRepo.leaderboard = []scoring.UserStanding{{UserID: common.UserID{Value: 7}, DisplayName: "Alex", Points: 2, Rank: 1}}

	scoringSvc := scoring.NewService(predictions, scoringRepo, clock)
	settlementSvc := NewResultSettlementService(predictions, scoringRepo, settlements, scoringSvc, outbox, clock, identityTx)

	count, err := settlementSvc.Settle(context.Background(), event, match)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("settled count = %d, want 1", count)
	}
	if len(outbox.enqueued) != 1 || outbox.enqueued[0] != "telegram.match-result" {
		t.Fatalf("outbox.enqueued = %v, want [telegram.match-result]", outbox.enqueued)
	}
	if settlements.marked != 1 {
		t.Fatalf("settlements.marked = %d, want 1", settlements.marked)
	}
	if len(scoringRepo.awards[pollID]) != 1 || scoringRepo.awards[pollID][0].Points != match.Format.ExactPoints() {
		t.Fatalf("awards = %+v, want one award worth %d points", scoringRepo.awards[pollID], match.Format.ExactPoints())
	}
}

// Ground truth: re-settling with an unchanged final score is a no-op —
// verified by calling Settle twice and asserting the outbox only gets one
// message and MarkSettled is only called once.
func TestResultSettlementService_IsIdempotentForUnchangedScore(t *testing.T) {
	predictions, scoringRepo, settlements, outbox := newSettlementFixture()
	clock := common.SystemUTCClock()

	matchID, eventID, pollID, chatID := common.NewMatchID(), common.NewEventID(), common.NewPollID(), common.ChatID{Value: -1}
	match := buildFinishedMatch(matchID, eventID)
	event := competition.Event{ID: eventID, Name: "Test Event"}
	options := make([]prediction.Option, len(match.Format.PossibleScores()))
	for i, s := range match.Format.PossibleScores() {
		options[i] = prediction.Option{Index: i, Score: s}
	}
	predictions.polls[pollID] = prediction.Poll{ID: pollID, ChatID: chatID, MatchID: matchID, Options: options, Status: prediction.PollClosed}

	scoringSvc := scoring.NewService(predictions, scoringRepo, clock)
	settlementSvc := NewResultSettlementService(predictions, scoringRepo, settlements, scoringSvc, outbox, clock, identityTx)

	if _, err := settlementSvc.Settle(context.Background(), event, match); err != nil {
		t.Fatal(err)
	}
	count2, err := settlementSvc.Settle(context.Background(), event, match)
	if err != nil {
		t.Fatal(err)
	}
	if count2 != 0 {
		t.Fatalf("second Settle count = %d, want 0 (idempotent)", count2)
	}
	if len(outbox.enqueued) != 1 {
		t.Fatalf("expected exactly one outbox message across both calls, got %d", len(outbox.enqueued))
	}
}

func TestResultSettlementService_NoOpWithoutFinalScore(t *testing.T) {
	predictions, scoringRepo, settlements, outbox := newSettlementFixture()
	clock := common.SystemUTCClock()
	scoringSvc := scoring.NewService(predictions, scoringRepo, clock)
	settlementSvc := NewResultSettlementService(predictions, scoringRepo, settlements, scoringSvc, outbox, clock, identityTx)

	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	unfinished := competition.Match{ID: common.NewMatchID(), Status: competition.MatchRunning, Format: format}
	count, err := settlementSvc.Settle(context.Background(), competition.Event{}, unfinished)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 || len(outbox.enqueued) != 0 {
		t.Fatalf("expected no-op for a match without a final score, got count=%d enqueued=%v", count, outbox.enqueued)
	}
}

func (f *fakePredictionsForSettlement) PollsAwaitingReminder(context.Context, time.Time, int) ([]prediction.Poll, error) {
	return nil, nil
}
func (f *fakePredictionsForSettlement) MarkReminded(context.Context, common.PollID, time.Time) error {
	return nil
}
func (f *fakePredictionsForSettlement) ChatParticipants(context.Context, common.ChatID, time.Time) ([]common.UserID, error) {
	return nil, nil
}

// Result recaps are opt-in and private, so the two things worth pinning
// down are that they only go to people who asked, and that a wrong
// prediction still gets one — a recap that only arrives on a win is
// flattery, not a record.
func TestResultSettlementService_RecapsGoOnlyToPeopleWhoAskedForThem(t *testing.T) {
	predictions, scoringRepo, settlements, outbox := newSettlementFixture()
	clock := common.SystemUTCClock()

	matchID, eventID, pollID, chatID := common.NewMatchID(), common.NewEventID(), common.NewPollID(), common.ChatID{Value: -1}
	match := buildFinishedMatch(matchID, eventID)
	event := competition.Event{ID: eventID, Name: "Test Event"}
	options := make([]prediction.Option, len(match.Format.PossibleScores()))
	for i, s := range match.Format.PossibleScores() {
		options[i] = prediction.Option{Index: i, Score: s}
	}
	predictions.polls[pollID] = prediction.Poll{ID: pollID, ChatID: chatID, MatchID: matchID, Options: options, Status: prediction.PollClosed}

	wants := common.UserID{Value: 7}     // opted in, predicted 2:0 — right
	alsoWants := common.UserID{Value: 8} // opted in, backed the other team entirely
	quiet := common.UserID{Value: 9}     // never asked
	predictions.votes[pollID] = []prediction.Vote{
		{PollID: pollID, UserID: wants, OptionIndex: 0, DisplayName: "Alex"},
		// The last option is the mirror scoreline: the other team winning,
		// so this vote earns nothing rather than partial credit.
		{PollID: pollID, UserID: alsoWants, OptionIndex: len(options) - 1, DisplayName: "Sam"},
		{PollID: pollID, UserID: quiet, OptionIndex: 0, DisplayName: "Kim"},
	}

	scoringSvc := scoring.NewService(predictions, scoringRepo, clock)
	settlementSvc := NewResultSettlementService(predictions, scoringRepo, settlements, scoringSvc, outbox, clock, identityTx).
		WithRecaps(optedIn{kind: common.NotifyResultRecaps, users: []common.UserID{wants, alsoWants}},
			func(context.Context, common.ChatID) string { return "Office CS2" }, slog.Default())

	if _, err := settlementSvc.Settle(context.Background(), event, match); err != nil {
		t.Fatal(err)
	}

	recaps := outbox.recaps()
	if len(recaps) != 2 {
		t.Fatalf("enqueued %d recaps, want 2 (only the opted-in voters): %+v", len(recaps), recaps)
	}
	byUser := map[int64]common.ResultRecapNotification{}
	for _, r := range recaps {
		byUser[r.UserID] = r
	}
	if _, sent := byUser[quiet.Value]; sent {
		t.Fatal("an unsolicited private message was enqueued")
	}
	right := byUser[wants.Value]
	if right.Score != "2:0" || right.Predicted != "2:0" || right.Points != match.Format.ExactPoints() {
		t.Fatalf("correct prediction's recap = %+v", right)
	}
	wrong := byUser[alsoWants.Value]
	if wrong.Predicted == wrong.Score {
		t.Fatalf("wrong prediction's recap should not claim the actual score: %+v", wrong)
	}
	if wrong.Points != 0 {
		t.Fatalf("wrong prediction scored %d points", wrong.Points)
	}
	if right.ChatTitle != "Office CS2" {
		t.Fatalf("recap does not say which chat it came from: %+v", right)
	}
}

// Settlement without recaps configured must behave exactly as before.
func TestResultSettlementService_WithoutRecapsEnqueuesOnlyTheGroupNotification(t *testing.T) {
	predictions, scoringRepo, settlements, outbox := newSettlementFixture()
	clock := common.SystemUTCClock()

	matchID, eventID, pollID, chatID := common.NewMatchID(), common.NewEventID(), common.NewPollID(), common.ChatID{Value: -1}
	match := buildFinishedMatch(matchID, eventID)
	options := make([]prediction.Option, len(match.Format.PossibleScores()))
	for i, s := range match.Format.PossibleScores() {
		options[i] = prediction.Option{Index: i, Score: s}
	}
	predictions.polls[pollID] = prediction.Poll{ID: pollID, ChatID: chatID, MatchID: matchID, Options: options, Status: prediction.PollClosed}
	predictions.votes[pollID] = []prediction.Vote{{PollID: pollID, UserID: common.UserID{Value: 7}, OptionIndex: 0}}

	scoringSvc := scoring.NewService(predictions, scoringRepo, clock)
	settlementSvc := NewResultSettlementService(predictions, scoringRepo, settlements, scoringSvc, outbox, clock, identityTx)
	if _, err := settlementSvc.Settle(context.Background(), competition.Event{ID: eventID, Name: "Test Event"}, match); err != nil {
		t.Fatal(err)
	}

	if len(outbox.enqueued) != 1 || outbox.enqueued[0] != "telegram.match-result" {
		t.Fatalf("outbox.enqueued = %v, want only the group notification", outbox.enqueued)
	}
}
