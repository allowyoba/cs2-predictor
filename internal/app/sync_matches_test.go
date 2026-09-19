package app

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// The match-sync pipeline — SynchronizeMatches → processMatch →
// fanOutNewPolls — is what turns a provider snapshot into polls and
// closures. These tests drive it through the real prediction.Service, since
// the decision under test ("what should happen to this match's polls") only
// exists in the interaction between the two.

// memPolls is an in-memory prediction.Repository: enough of one to let a
// real prediction.Service create, find and close polls.
type memPolls struct {
	polls    map[common.PollID]prediction.Poll
	saveFail bool
}

func newMemPolls() *memPolls { return &memPolls{polls: map[common.PollID]prediction.Poll{}} }

func (m *memPolls) FindPoll(_ context.Context, id common.PollID) (*prediction.Poll, error) {
	if p, ok := m.polls[id]; ok {
		return &p, nil
	}
	return nil, nil
}
func (m *memPolls) FindByTelegramPollID(context.Context, string) (*prediction.Poll, error) {
	return nil, nil
}
func (m *memPolls) FindByMatchAndChat(_ context.Context, matchID common.MatchID, chatID common.ChatID) (*prediction.Poll, error) {
	for _, p := range m.polls {
		if p.MatchID == matchID && p.ChatID == chatID {
			return &p, nil
		}
	}
	return nil, nil
}
func (m *memPolls) OpenPollsForMatch(_ context.Context, matchID common.MatchID) ([]prediction.Poll, error) {
	var out []prediction.Poll
	for _, p := range m.polls {
		if p.MatchID == matchID && p.Status == prediction.PollOpen {
			out = append(out, p)
		}
	}
	return out, nil
}
func (m *memPolls) PollsForMatch(_ context.Context, matchID common.MatchID) ([]prediction.Poll, error) {
	return m.OpenPollsForMatch(context.Background(), matchID)
}
func (m *memPolls) SavePoll(_ context.Context, p prediction.Poll) (prediction.Poll, error) {
	if m.saveFail {
		return prediction.Poll{}, errors.New("poll store unavailable")
	}
	m.polls[p.ID] = p
	return p, nil
}
func (m *memPolls) OpenPollsDue(_ context.Context, at time.Time) ([]prediction.Poll, error) {
	var out []prediction.Poll
	for _, p := range m.polls {
		if p.Status == prediction.PollOpen && !p.ClosesAt.After(at) {
			out = append(out, p)
		}
	}
	return out, nil
}
func (m *memPolls) Votes(context.Context, common.PollID) ([]prediction.Vote, error) { return nil, nil }
func (m *memPolls) PollsAwaitingReminder(context.Context, time.Time, int) ([]prediction.Poll, error) {
	return nil, nil
}
func (m *memPolls) MarkReminded(context.Context, common.PollID, time.Time) error { return nil }
func (m *memPolls) ChatParticipants(context.Context, common.ChatID, time.Time) ([]common.UserID, error) {
	return nil, nil
}
func (m *memPolls) SaveVote(context.Context, prediction.Vote) error                { return nil }
func (m *memPolls) RemoveVote(context.Context, common.PollID, common.UserID) error { return nil }

// recordingGateway is the Telegram side of a poll, reduced to a log of what
// it was asked to do.
type recordingGateway struct {
	sent      int
	closed    []common.PollID
	cancelled []common.PollID
}

func (g *recordingGateway) Send(_ context.Context, p prediction.Poll) (prediction.SentPoll, error) {
	g.sent++
	id := "tg-poll"
	return prediction.SentPoll{PollID: id, MessageID: int64(g.sent), TopicID: p.TopicID}, nil
}
func (g *recordingGateway) Close(_ context.Context, p prediction.Poll) error {
	g.closed = append(g.closed, p.ID)
	return nil
}
func (g *recordingGateway) Cancel(_ context.Context, p prediction.Poll) error {
	g.cancelled = append(g.cancelled, p.ID)
	return nil
}

// matchSyncFixture wires the whole pipeline over one event with one
// subscribed chat, and hands back the moving parts a test asserts on.
type matchSyncFixture struct {
	sync    *CompetitionSynchronization
	polls   *memPolls
	gateway *recordingGateway
	event   competition.Event
	chatID  common.ChatID
}

func newMatchSyncFixture(t *testing.T) *matchSyncFixture {
	t.Helper()
	event := topTierEvent("IEM Katowice")
	chatID := common.ChatID{Value: -1}
	catalog := newFakeSyncCatalog()
	catalog.events[event.ID] = event
	subs := &fakeSyncSubs{
		activeEvents:      []common.EventID{event.ID, event.ID}, // deliberately repeated: the same event must be fetched once
		subscribedByEvent: map[common.EventID][]common.ChatID{event.ID: {chatID}},
	}
	chats := &fakeSyncChats{active: []chat.Settings{{ChatID: chatID, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}}}}
	provider := &fixedProvider{name: "PANDASCORE"}
	polls, gw := newMemPolls(), &recordingGateway{}
	sync := newTestSync(t, provider, catalog, subs, chats, &fakeSyncOutbox{})
	sync.Predictions = prediction.NewService(polls, gw, common.SystemUTCClock())
	sync.Log = slog.Default()
	return &matchSyncFixture{sync: sync, polls: polls, gateway: gw, event: event, chatID: chatID}
}

// syncMatch builds an unstarted match for the fixture's event; the tests
// that need a later status take this one and move it on.
func syncMatch(eventID common.EventID, scheduledAt *time.Time) competition.Match {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	return competition.Match{
		ID: common.NewMatchID(), EventID: eventID, ExternalID: "m-1", Status: competition.MatchNotStarted, Format: format,
		FirstTeam:   &competition.Team{ID: common.NewTeamID(), Name: "G2"},
		SecondTeam:  &competition.Team{ID: common.NewTeamID(), Name: "NAVI"},
		ScheduledAt: scheduledAt,
	}
}

// The point of the whole job: a match whose participants have just been
// resolved becomes a poll in every subscribed chat.
func TestSynchronizeMatches_CreatesAPollForEachSubscribedChat(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	fx := newMatchSyncFixture(t)
	match := syncMatch(fx.event.ID, &soon)
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{match})

	fx.sync.SynchronizeMatches(context.Background())

	if fx.gateway.sent != 1 {
		t.Fatalf("expected one poll sent to the subscribed chat, got %d", fx.gateway.sent)
	}
	if len(fx.polls.polls) != 1 {
		t.Fatalf("expected one stored poll, got %d", len(fx.polls.polls))
	}
	for _, p := range fx.polls.polls {
		if p.ChatID != fx.chatID || p.MatchID != match.ID {
			t.Fatalf("poll = %+v, want it anchored to the subscribed chat and this match", p)
		}
	}
}

// A second run over the same match must not produce a second poll: the
// fan-out trigger is one-shot, and Create is idempotent per (match, chat).
func TestSynchronizeMatches_DoesNotDuplicateAPollOnTheNextRun(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	fx := newMatchSyncFixture(t)
	match := syncMatch(fx.event.ID, &soon)
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{match})

	fx.sync.SynchronizeMatches(context.Background())
	fx.sync.SynchronizeMatches(context.Background())

	if fx.gateway.sent != 1 {
		t.Fatalf("expected exactly one poll across two runs, got %d", fx.gateway.sent)
	}
}

// A match that has started closes its polls: voting after the first round
// is played would be guessing at a result already on screen.
func TestSynchronizeMatches_ClosesPollsOnceTheMatchIsRunning(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	fx := newMatchSyncFixture(t)
	match := syncMatch(fx.event.ID, &soon)
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{match})
	fx.sync.SynchronizeMatches(context.Background())

	running := match
	running.Status = competition.MatchRunning
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{running})
	fx.sync.SynchronizeMatches(context.Background())

	if len(fx.gateway.closed) != 1 {
		t.Fatalf("expected the open poll to be closed, got %v", fx.gateway.closed)
	}
}

// A cancelled match cancels rather than closes: there will be no result to
// settle, so nobody should be scored on it.
func TestSynchronizeMatches_CancelsPollsForACancelledMatch(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	fx := newMatchSyncFixture(t)
	match := syncMatch(fx.event.ID, &soon)
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{match})
	fx.sync.SynchronizeMatches(context.Background())

	cancelled := match
	cancelled.Status = competition.MatchCancelled
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{cancelled})
	fx.sync.SynchronizeMatches(context.Background())

	if len(fx.gateway.cancelled) != 1 || len(fx.gateway.closed) != 0 {
		t.Fatalf("expected the poll to be cancelled, not closed: cancelled=%v closed=%v", fx.gateway.cancelled, fx.gateway.closed)
	}
}

// A postponed match moves its polls' deadline instead of ending them —
// the match is still going to be played.
func TestSynchronizeMatches_ReschedulesPollsForAPostponedMatch(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	fx := newMatchSyncFixture(t)
	match := syncMatch(fx.event.ID, &soon)
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{match})
	fx.sync.SynchronizeMatches(context.Background())

	later := soon.Add(24 * time.Hour)
	postponed := match
	postponed.Status, postponed.ScheduledAt = competition.MatchPostponed, &later
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{postponed})
	fx.sync.SynchronizeMatches(context.Background())

	if len(fx.gateway.closed) != 0 || len(fx.gateway.cancelled) != 0 {
		t.Fatal("a postponed match must leave its polls open")
	}
	for _, p := range fx.polls.polls {
		if !p.ClosesAt.Equal(later) {
			t.Fatalf("ClosesAt = %s, want the new kick-off %s", p.ClosesAt, later)
		}
	}
}

// One bad match in a provider snapshot must not cost every match after it
// in the same batch — the run is reported as partial, not abandoned.
func TestSynchronizeMatches_KeepsGoingAfterOneMatchFails(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	fx := newMatchSyncFixture(t)
	bad := syncMatch(fx.event.ID, &soon)
	good := syncMatch(fx.event.ID, &soon)
	fx.sync.Catalog.(*fakeSyncCatalog).failSaveMatchFor = &bad.ID
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{bad, good})

	fx.sync.SynchronizeMatches(context.Background())

	if fx.gateway.sent != 1 {
		t.Fatalf("expected the healthy match to still produce its poll, got %d sends", fx.gateway.sent)
	}
}

// CloseDuePolls closes everything past its deadline. A failure inside the
// batch is logged, not turned into a failed run — the polls that did close
// are closed for good.
func TestCloseDuePolls_ClosesEverythingPastItsDeadline(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	fx := newMatchSyncFixture(t)
	match := syncMatch(fx.event.ID, &soon)
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{match})
	fx.sync.SynchronizeMatches(context.Background())

	// Move every poll's deadline into the past.
	for id, p := range fx.polls.polls {
		p.ClosesAt = time.Now().Add(-time.Minute)
		fx.polls.polls[id] = p
	}

	fx.sync.CloseDuePolls(context.Background())

	if len(fx.gateway.closed) != 1 {
		t.Fatalf("expected the overdue poll to be closed, got %v", fx.gateway.closed)
	}
	for _, p := range fx.polls.polls {
		if p.Status != prediction.PollClosed {
			t.Fatalf("poll status = %s, want closed", p.Status)
		}
	}
}
