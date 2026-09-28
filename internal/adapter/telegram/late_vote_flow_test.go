package telegram

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// fakePollReads is the poll history the picker walks.
type fakePollReads struct {
	polls        []prediction.Poll
	votes        []prediction.Vote
	participants []common.UserID
}

func (f *fakePollReads) RecentClosedPolls(context.Context, common.ChatID, int) ([]prediction.Poll, error) {
	return f.polls, nil
}
func (f *fakePollReads) FindPoll(_ context.Context, id common.PollID) (*prediction.Poll, error) {
	for i := range f.polls {
		if f.polls[i].ID == id {
			return &f.polls[i], nil
		}
	}
	return nil, nil
}
func (f *fakePollReads) Votes(context.Context, common.PollID) ([]prediction.Vote, error) {
	return f.votes, nil
}
func (f *fakePollReads) ChatParticipants(context.Context, common.ChatID, time.Time) ([]common.UserID, error) {
	return f.participants, nil
}

func latePoll(chatID common.ChatID, matchID common.MatchID) prediction.Poll {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	var options []prediction.Option
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	return prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: matchID,
		Options: options, Status: prediction.PollClosed,
	}
}

// The whole path by button: the entry sits on the events menu, the match
// list leads to the people, and the people lead to the scorelines the poll
// itself offered.
func TestLateVote_WalksFromTheMenuToAScoreline(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	matchID := common.NewMatchID()
	poll := latePoll(settings.ChatID, matchID)
	handler.PollReads = &fakePollReads{polls: []prediction.Poll{poll}, participants: []common.UserID{{Value: 4242}}}
	handler.LateVotes = lateVoteServiceStub()

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "menu:events")); err != nil {
		t.Fatal(err)
	}
	cds, _ := findKeyboardButtons(*calls)
	if !slices.Contains(cds, "lv:polls") {
		t.Fatalf("no late-prediction entry on the events menu: %v", cds)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, "lv:polls")); err != nil {
		t.Fatal(err)
	}
	cds, _ = findKeyboardButtons(*calls)
	wantPoll := "lv:who:" + compactUUID(poll.ID.Value) + ":0"
	if !slices.Contains(cds, wantPoll) {
		t.Fatalf("the match list does not offer the closed poll: %v", cds)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, wantPoll)); err != nil {
		t.Fatal(err)
	}
	cds, _ = findKeyboardButtons(*calls)
	var scorePick string
	for _, cd := range cds {
		if strings.HasPrefix(cd, "lv:score:") {
			scorePick = cd
		}
	}
	if scorePick == "" {
		t.Fatalf("no participant to pick: %v", cds)
	}

	if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, scorePick)); err != nil {
		t.Fatal(err)
	}
	cds, _ = findKeyboardButtons(*calls)
	var offered int
	for _, cd := range cds {
		if strings.HasPrefix(cd, "lv:do:") {
			offered++
		}
	}
	if offered != len(poll.Options) {
		t.Fatalf("offered %d scorelines, want the poll's own %d: %v", offered, len(poll.Options), cds)
	}
}

// Every callback data the flow produces has to fit Telegram's 64-byte limit,
// or the button silently does nothing when tapped.
func TestLateVote_CallbackDataFitsTelegramsLimit(t *testing.T) {
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, chats := newTestHandler(t, server)
	settings := targetTestChat(t, handler, chats, common.ChatID{Value: -100})

	poll := latePoll(settings.ChatID, common.NewMatchID())
	handler.PollReads = &fakePollReads{polls: []prediction.Poll{poll}, participants: []common.UserID{{Value: 4242}}}
	handler.LateVotes = lateVoteServiceStub()

	for _, data := range []string{"lv:polls", "lv:who:" + compactUUID(poll.ID.Value) + ":0"} {
		if err := handler.handleCallback(context.Background(), targetCallback(settings.ChatID.Value, data)); err != nil {
			t.Fatal(err)
		}
	}
	cds, _ := findKeyboardButtons(*calls)
	for _, cd := range cds {
		if len(cd) > 64 {
			t.Fatalf("callback data %q is %d bytes, over Telegram's 64", cd, len(cd))
		}
	}
	// And the longest one the flow can build — poll, user and option.
	longest := "lv:do:" + compactUUID(poll.ID.Value) + ":" + "zzzzzzzzzzzzz" + ":99"
	if len(longest) > 64 {
		t.Fatalf("the write callback can reach %d bytes, over Telegram's 64", len(longest))
	}
}

// lateVoteServiceStub is enough to get past the "is this wired?" check; no
// test here reaches the write itself, which is covered against real SQL.
func lateVoteServiceStub() *app.LateVoteService {
	return &app.LateVoteService{}
}
