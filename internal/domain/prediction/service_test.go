package prediction

import (
	"context"
	"errors"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

type inMemoryRepo struct {
	polls map[common.PollID]Poll
	votes map[[2]string]Vote // key: pollID.String()+userID
}

func newInMemoryRepo() *inMemoryRepo {
	return &inMemoryRepo{polls: map[common.PollID]Poll{}, votes: map[[2]string]Vote{}}
}

func voteKey(pollID common.PollID, userID common.UserID) [2]string {
	return [2]string{pollID.Value.String(), userID.String()}
}

func (r *inMemoryRepo) FindPoll(_ context.Context, id common.PollID) (*Poll, error) {
	if p, ok := r.polls[id]; ok {
		return &p, nil
	}
	return nil, nil
}
func (r *inMemoryRepo) FindByTelegramPollID(_ context.Context, telegramPollID string) (*Poll, error) {
	for _, p := range r.polls {
		if p.TelegramPollID != nil && *p.TelegramPollID == telegramPollID {
			cp := p
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *inMemoryRepo) FindByMatchAndChat(_ context.Context, matchID common.MatchID, chatID common.ChatID) (*Poll, error) {
	for _, p := range r.polls {
		if p.MatchID == matchID && p.ChatID == chatID {
			cp := p
			return &cp, nil
		}
	}
	return nil, nil
}
func (r *inMemoryRepo) OpenPollsForMatch(_ context.Context, matchID common.MatchID) ([]Poll, error) {
	var out []Poll
	for _, p := range r.polls {
		if p.MatchID == matchID && p.Status == PollOpen {
			out = append(out, p)
		}
	}
	return out, nil
}
func (r *inMemoryRepo) PollsForMatch(_ context.Context, matchID common.MatchID) ([]Poll, error) {
	var out []Poll
	for _, p := range r.polls {
		if p.MatchID == matchID {
			out = append(out, p)
		}
	}
	return out, nil
}
func (r *inMemoryRepo) SavePoll(_ context.Context, poll Poll) (Poll, error) {
	r.polls[poll.ID] = poll
	return poll, nil
}
func (r *inMemoryRepo) OpenPollsDue(_ context.Context, at time.Time) ([]Poll, error) {
	var out []Poll
	for _, p := range r.polls {
		if p.Status == PollOpen && !p.ClosesAt.After(at) {
			out = append(out, p)
		}
	}
	return out, nil
}
func (r *inMemoryRepo) Votes(_ context.Context, pollID common.PollID) ([]Vote, error) {
	var out []Vote
	for k, v := range r.votes {
		if k[0] == pollID.Value.String() {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *inMemoryRepo) SaveVote(_ context.Context, vote Vote) error {
	r.votes[voteKey(vote.PollID, vote.UserID)] = vote
	return nil
}
func (r *inMemoryRepo) RemoveVote(_ context.Context, pollID common.PollID, userID common.UserID) error {
	delete(r.votes, voteKey(pollID, userID))
	return nil
}

type stubGateway struct {
	closed    int
	cancelled int
	// failAfter, if > 0, makes the (closed+cancelled)'th call to
	// Close/Cancel return failErr instead of succeeding — used to test
	// that a batch aborts partway through on a gateway failure.
	failAfter int
	failErr   error
}

func (g *stubGateway) Send(context.Context, Poll) (SentPoll, error) {
	return SentPoll{PollID: "tg-poll", MessageID: 42}, nil
}
func (g *stubGateway) Close(context.Context, Poll) error {
	g.closed++
	if g.failAfter > 0 && g.closed+g.cancelled == g.failAfter {
		return g.failErr
	}
	return nil
}
func (g *stubGateway) Cancel(context.Context, Poll) error {
	g.cancelled++
	if g.failAfter > 0 && g.closed+g.cancelled == g.failAfter {
		return g.failErr
	}
	return nil
}

func testMatch(scheduledAt time.Time) competition.Match {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	return competition.Match{
		ID:          common.NewMatchID(),
		EventID:     common.NewEventID(),
		ExternalID:  "m1",
		FirstTeam:   &competition.Team{ID: common.NewTeamID(), Name: "A", ExternalID: "a"},
		SecondTeam:  &competition.Team{ID: common.NewTeamID(), Name: "B", ExternalID: "b"},
		ScheduledAt: &scheduledAt,
		Status:      competition.MatchNotStarted,
		Format:      format,
	}
}

// A repeat vote replaces the
// previous vote; withdrawal (empty option list) removes it entirely.
func TestRecordVote_LatestVoteReplacesPreviousAndWithdrawalRemoves(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repo := newInMemoryRepo()
	clock := common.FixedClock(now)
	gateway := &stubGateway{}
	svc := NewService(repo, gateway, clock)

	match := testMatch(now.Add(time.Hour))
	poll, err := svc.Create(context.Background(), match, common.ChatID{Value: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}

	user := common.UserID{Value: 7}
	username := "alex"
	if err := svc.RecordVote(context.Background(), "tg-poll", user, []int{0}, &username, "Alex"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordVote(context.Background(), "tg-poll", user, []int{2}, &username, "Alex"); err != nil {
		t.Fatal(err)
	}
	votes, _ := repo.Votes(context.Background(), poll.ID)
	if len(votes) != 1 || votes[0].OptionIndex != 2 {
		t.Fatalf("expected single vote with optionIndex=2, got %+v", votes)
	}

	if err := svc.RecordVote(context.Background(), "tg-poll", user, []int{}, &username, "Alex"); err != nil {
		t.Fatal(err)
	}
	votes, _ = repo.Votes(context.Background(), poll.ID)
	if len(votes) != 0 {
		t.Fatalf("expected withdrawal to remove the vote, got %+v", votes)
	}
}

func TestCreate_FailsWhenParticipantMissing(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repo := newInMemoryRepo()
	svc := NewService(repo, &stubGateway{}, common.FixedClock(now))

	match := testMatch(now.Add(time.Minute))
	match.SecondTeam = nil
	if _, err := svc.Create(context.Background(), match, common.ChatID{Value: -1}, nil); err == nil {
		t.Fatal("expected error when a participant is missing")
	}
}

// Ground truth: CloseDue is idempotent — a second call at the same "now"
// processes zero and the gateway's Close is invoked exactly once total.
func TestCloseDue_IsIdempotent(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repo := newInMemoryRepo()
	gateway := &stubGateway{}
	svc := NewService(repo, gateway, common.FixedClock(now.Add(-time.Minute)))

	match := testMatch(now)
	if _, err := svc.Create(context.Background(), match, common.ChatID{Value: -1}, nil); err != nil {
		t.Fatal(err)
	}

	count1, err := svc.CloseDue(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	count2, err := svc.CloseDue(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if count1 != 1 || count2 != 0 {
		t.Fatalf("expected (1, 0), got (%d, %d)", count1, count2)
	}
	if gateway.closed != 1 {
		t.Fatalf("expected gateway.Close called once, got %d", gateway.closed)
	}
}

func (r *inMemoryRepo) PollsAwaitingReminder(context.Context, time.Time, int) ([]Poll, error) {
	return nil, nil
}
func (r *inMemoryRepo) MarkReminded(context.Context, common.PollID, time.Time) error { return nil }
func (r *inMemoryRepo) ChatParticipants(context.Context, common.ChatID, time.Time) ([]common.UserID, error) {
	return nil, nil
}

func TestCloseForRunningMatch_ClosesEveryOpenPollForTheMatch(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repo := newInMemoryRepo()
	gateway := &stubGateway{}
	svc := NewService(repo, gateway, common.FixedClock(now))

	match := testMatch(now.Add(time.Hour))
	pollA, err := svc.Create(context.Background(), match, common.ChatID{Value: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pollB, err := svc.Create(context.Background(), match, common.ChatID{Value: -2}, nil)
	if err != nil {
		t.Fatal(err)
	}

	count, err := svc.CloseForRunningMatch(context.Background(), match.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || gateway.closed != 2 {
		t.Fatalf("count=%d gateway.closed=%d, want 2 and 2", count, gateway.closed)
	}
	for _, id := range []common.PollID{pollA.ID, pollB.ID} {
		got, _ := repo.FindPoll(context.Background(), id)
		if got.Status != PollClosed {
			t.Fatalf("poll %v status = %s, want CLOSED", id, got.Status)
		}
	}
}

func TestCancelForMatch_CancelsEveryOpenPollForTheMatch(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repo := newInMemoryRepo()
	gateway := &stubGateway{}
	svc := NewService(repo, gateway, common.FixedClock(now))

	match := testMatch(now.Add(time.Hour))
	poll, err := svc.Create(context.Background(), match, common.ChatID{Value: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}

	count, err := svc.CancelForMatch(context.Background(), match.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || gateway.cancelled != 1 || gateway.closed != 0 {
		t.Fatalf("count=%d cancelled=%d closed=%d, want 1, 1, 0", count, gateway.cancelled, gateway.closed)
	}
	got, _ := repo.FindPoll(context.Background(), poll.ID)
	if got.Status != PollCancelled {
		t.Fatalf("poll status = %s, want CANCELLED", got.Status)
	}
}

// A gateway failure partway through a batch aborts the rest: the polls
// already processed keep their new status, and the ones not yet reached
// stay untouched for the next scheduler tick to retry.
func TestCancelForMatch_AbortsRemainingPollsOnGatewayFailure(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repo := newInMemoryRepo()
	failure := errors.New("telegram unavailable")
	gateway := &stubGateway{failAfter: 1, failErr: failure}
	svc := NewService(repo, gateway, common.FixedClock(now))

	match := testMatch(now.Add(time.Hour))
	// Two chats, two separate polls for the same match.
	if _, err := svc.Create(context.Background(), match, common.ChatID{Value: -1}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), match, common.ChatID{Value: -2}, nil); err != nil {
		t.Fatal(err)
	}

	count, err := svc.CancelForMatch(context.Background(), match.ID)
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want %v", err, failure)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0 (the failing poll doesn't count as processed)", count)
	}
}

func TestReschedule_MovesClosesAtWithoutTouchingTelegram(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	repo := newInMemoryRepo()
	gateway := &stubGateway{}
	svc := NewService(repo, gateway, common.FixedClock(now))

	match := testMatch(now.Add(time.Hour))
	poll, err := svc.Create(context.Background(), match, common.ChatID{Value: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}

	newClose := now.Add(3 * time.Hour)
	count, err := svc.Reschedule(context.Background(), match.ID, newClose)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if gateway.closed != 0 || gateway.cancelled != 0 {
		t.Fatalf("Reschedule must never touch the gateway, got closed=%d cancelled=%d", gateway.closed, gateway.cancelled)
	}
	got, _ := repo.FindPoll(context.Background(), poll.ID)
	if got.Status != PollOpen {
		t.Fatalf("poll status = %s, want it to stay OPEN", got.Status)
	}
	if !got.ClosesAt.Equal(newClose) {
		t.Fatalf("ClosesAt = %v, want %v", got.ClosesAt, newClose)
	}
}
