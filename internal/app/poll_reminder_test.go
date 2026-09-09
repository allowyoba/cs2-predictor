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
	"cs2predictor/internal/platform/common"
)

// recordingOutbox keeps what was enqueued, which is the whole observable
// effect of the reminder job.
type recordingOutbox struct {
	fakeOutbox
	enqueued []struct {
		eventType string
		payload   string
	}
}

func (o *recordingOutbox) Enqueue(_ context.Context, _, _, eventType, payload string) (uuid.UUID, error) {
	o.enqueued = append(o.enqueued, struct {
		eventType string
		payload   string
	}{eventType, payload})
	return uuid.New(), nil
}

func (o *recordingOutbox) reminders() []common.PollReminderNotification {
	var out []common.PollReminderNotification
	for _, e := range o.enqueued {
		if e.eventType != "telegram.poll-reminder" {
			continue
		}
		var n common.PollReminderNotification
		if err := json.Unmarshal([]byte(e.payload), &n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

type reminderPredictions struct {
	prediction.Repository
	polls        []prediction.Poll
	votes        map[common.PollID][]prediction.Vote
	participants []common.UserID
	marked       []common.PollID
}

func (r *reminderPredictions) PollsAwaitingReminder(_ context.Context, _ time.Time, _ int) ([]prediction.Poll, error) {
	return r.polls, nil
}
func (r *reminderPredictions) MarkReminded(_ context.Context, id common.PollID, _ time.Time) error {
	r.marked = append(r.marked, id)
	return nil
}
func (r *reminderPredictions) ChatParticipants(context.Context, common.ChatID, time.Time) ([]common.UserID, error) {
	return r.participants, nil
}
func (r *reminderPredictions) Votes(_ context.Context, id common.PollID) ([]prediction.Vote, error) {
	return r.votes[id], nil
}

type reminderCatalog struct {
	competition.Catalog
	match *competition.Match
}

func (c reminderCatalog) FindMatch(context.Context, common.MatchID) (*competition.Match, error) {
	return c.match, nil
}

// optedIn accepts exactly the listed users for the listed kind.
type optedIn struct {
	kind  common.NotificationKind
	users []common.UserID
}

func (a optedIn) Recipients(_ context.Context, kind common.NotificationKind, candidates []common.UserID) ([]common.UserID, error) {
	if kind != a.kind {
		return nil, nil
	}
	allowed := map[common.UserID]bool{}
	for _, id := range a.users {
		allowed[id] = true
	}
	var out []common.UserID
	for _, id := range candidates {
		if allowed[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func setupReminderTest(t *testing.T, participants, optIns []common.UserID, voters []common.UserID) (*PollReminderScheduler, *reminderPredictions, *recordingOutbox) {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	pollID := common.PollID{Value: uuid.New()}
	matchID := common.MatchID{Value: uuid.New()}
	votes := make([]prediction.Vote, 0, len(voters))
	for _, v := range voters {
		votes = append(votes, prediction.Vote{PollID: pollID, UserID: v})
	}
	predictions := &reminderPredictions{
		polls: []prediction.Poll{{
			ID: pollID, ChatID: common.ChatID{Value: -1}, MatchID: matchID,
			Status: prediction.PollOpen, ClosesAt: now.Add(20 * time.Minute),
		}},
		votes:        map[common.PollID][]prediction.Vote{pollID: votes},
		participants: participants,
	}
	first := competition.Team{Name: "Spirit"}
	second := competition.Team{Name: "NAVI"}
	outbox := &recordingOutbox{}
	return &PollReminderScheduler{
		Predictions: predictions,
		Catalog:     reminderCatalog{match: &competition.Match{ID: matchID, FirstTeam: &first, SecondTeam: &second}},
		Audience:    optedIn{kind: common.NotifyPollReminders, users: optIns},
		ChatTitles:  func(context.Context, common.ChatID) string { return "Office CS2" },
		Outbox:      outbox, Lock: fakeClusterLock{}, Clock: fixedClock{now: now},
		RunTx: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
		Log:   slog.Default(), Lead: 30 * time.Minute,
	}, predictions, outbox
}

func TestPollReminder_RemindsOnlyOptedInParticipantsWhoHaveNotVoted(t *testing.T) {
	waiting := common.UserID{Value: 1} // opted in, hasn't voted — the one case
	voted := common.UserID{Value: 2}   // opted in but already voted
	notOptedIn := common.UserID{Value: 3}

	scheduler, _, outbox := setupReminderTest(t,
		[]common.UserID{waiting, voted, notOptedIn},
		[]common.UserID{waiting, voted},
		[]common.UserID{voted})
	scheduler.Dispatch(context.Background())

	reminders := outbox.reminders()
	if len(reminders) != 1 {
		t.Fatalf("enqueued %d reminders, want exactly 1: %+v", len(reminders), reminders)
	}
	got := reminders[0]
	if got.UserID != waiting.Value {
		t.Fatalf("reminded user %d, want %d", got.UserID, waiting.Value)
	}
	if got.FirstTeam != "Spirit" || got.SecondTeam != "NAVI" || got.ChatTitle != "Office CS2" {
		t.Fatalf("reminder does not say what or where: %+v", got)
	}
	if got.MinutesLeft != 20 {
		t.Fatalf("MinutesLeft = %d, want 20", got.MinutesLeft)
	}
}

// A poll is marked whether or not anybody needed reminding, so the job
// stops re-examining it every minute until it closes.
func TestPollReminder_MarksThePollEvenWhenNobodyIsReminded(t *testing.T) {
	scheduler, predictions, outbox := setupReminderTest(t, nil, nil, nil)
	scheduler.Dispatch(context.Background())

	if len(predictions.marked) != 1 {
		t.Fatalf("marked %d polls, want 1", len(predictions.marked))
	}
	if len(outbox.reminders()) != 0 {
		t.Fatal("reminded somebody with no participants at all")
	}
}

// Nothing is sent to anyone who hasn't opted in, however active they are.
func TestPollReminder_SendsNothingWithoutOptIns(t *testing.T) {
	active := common.UserID{Value: 9}
	scheduler, _, outbox := setupReminderTest(t, []common.UserID{active}, nil, nil)
	scheduler.Dispatch(context.Background())

	if len(outbox.reminders()) != 0 {
		t.Fatal("an unsolicited private message was enqueued")
	}
}

// A zero lead is how an operator turns the whole feature off.
func TestPollReminder_ZeroLeadDisablesTheJob(t *testing.T) {
	scheduler, predictions, outbox := setupReminderTest(t,
		[]common.UserID{{Value: 1}}, []common.UserID{{Value: 1}}, nil)
	scheduler.Lead = 0
	scheduler.Dispatch(context.Background())

	if len(predictions.marked) != 0 || len(outbox.enqueued) != 0 {
		t.Fatal("the job ran despite being disabled")
	}
}
