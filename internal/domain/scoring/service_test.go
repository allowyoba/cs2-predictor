package scoring

import (
	"context"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/platform/common"
)

// fakeVotes supplies fixed votes for Settle and embeds prediction.Repository
// (nil) so only Votes needs a real implementation; any other method being
// called unexpectedly panics on the nil embed, which is the point — Settle
// shouldn't need them.
type fakeVotes struct {
	prediction.Repository
	votes []prediction.Vote
}

func (f fakeVotes) Votes(context.Context, common.PollID) ([]prediction.Vote, error) {
	return f.votes, nil
}

// fakeAwardsRepo records the last ReplaceAwards call and embeds Repository
// (nil) for the same reason as fakeVotes.
type fakeAwardsRepo struct {
	Repository
	pollID common.PollID
	awards []Award
}

func (f *fakeAwardsRepo) ReplaceAwards(_ context.Context, pollID common.PollID, awards []Award) error {
	f.pollID = pollID
	f.awards = awards
	return nil
}

func TestService_Settle_AwardsCorrectVotesAndSkipsWrongOnes(t *testing.T) {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	score := competition.MatchScore{First: 2, Second: 0}
	match := competition.Match{ID: common.NewMatchID(), Score: &score, Format: format, ActualStartedAt: &now}
	event := competition.Event{ID: common.NewEventID()}
	poll := prediction.Poll{
		ID: common.NewPollID(), ChatID: common.ChatID{Value: -1},
		Options: []prediction.Option{
			{Index: 0, Score: competition.MatchScore{First: 2, Second: 0}}, // exact
			{Index: 1, Score: competition.MatchScore{First: 2, Second: 1}}, // right winner, wrong score
			{Index: 2, Score: competition.MatchScore{First: 0, Second: 2}}, // wrong winner
		},
	}
	right := common.UserID{Value: 1}
	outcome := common.UserID{Value: 2}
	wrong := common.UserID{Value: 3}
	votes := fakeVotes{votes: []prediction.Vote{
		{PollID: poll.ID, UserID: right, OptionIndex: 0},
		{PollID: poll.ID, UserID: outcome, OptionIndex: 1},
		{PollID: poll.ID, UserID: wrong, OptionIndex: 2},
	}}
	repo := &fakeAwardsRepo{}

	svc := NewService(votes, repo, common.FixedClock(now))
	awards, err := svc.Settle(context.Background(), event, match, poll)
	if err != nil {
		t.Fatal(err)
	}
	if len(awards) != 2 {
		t.Fatalf("got %d awards, want 2 (the wrong-winner vote earns nothing): %+v", len(awards), awards)
	}
	byUser := map[common.UserID]Award{}
	for _, a := range awards {
		byUser[a.UserID] = a
	}
	if a, ok := byUser[right]; !ok || a.Kind != AwardExactScore || a.Points != format.ExactPoints() {
		t.Fatalf("exact-score award = %+v, ok=%v", a, ok)
	}
	if a, ok := byUser[outcome]; !ok || a.Kind != AwardOutcome || a.Points != 1 {
		t.Fatalf("outcome award = %+v, ok=%v", a, ok)
	}
	if _, ok := byUser[wrong]; ok {
		t.Fatal("the wrong-winner vote must not receive an award")
	}
	if repo.pollID != poll.ID || len(repo.awards) != 2 {
		t.Fatalf("ReplaceAwards called with pollID=%v awards=%+v", repo.pollID, repo.awards)
	}
}

func TestService_Settle_RejectsAMatchWithNoScore(t *testing.T) {
	svc := NewService(fakeVotes{}, &fakeAwardsRepo{}, common.SystemUTCClock())
	_, err := svc.Settle(context.Background(), competition.Event{}, competition.Match{}, prediction.Poll{})
	if err == nil {
		t.Fatal("expected an error for a match with no recorded score")
	}
}

// Re-settling with the exact same votes must still replace the award set
// (not append to it) — ReplaceAwards is the caller's idempotency
// mechanism, and Settle must always call it with the freshly computed set.
func TestService_Settle_AlwaysReplacesRatherThanAccumulates(t *testing.T) {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	score := competition.MatchScore{First: 2, Second: 0}
	match := competition.Match{ID: common.NewMatchID(), Score: &score, Format: format, ScheduledAt: &now}
	poll := prediction.Poll{
		ID:      common.NewPollID(),
		Options: []prediction.Option{{Index: 0, Score: competition.MatchScore{First: 2, Second: 0}}},
	}
	voter := common.UserID{Value: 1}
	votes := fakeVotes{votes: []prediction.Vote{{PollID: poll.ID, UserID: voter, OptionIndex: 0}}}
	repo := &fakeAwardsRepo{}
	svc := NewService(votes, repo, common.FixedClock(now))

	if _, err := svc.Settle(context.Background(), competition.Event{}, match, poll); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Settle(context.Background(), competition.Event{}, match, poll); err != nil {
		t.Fatal(err)
	}
	if len(repo.awards) != 1 {
		t.Fatalf("second Settle produced %d awards in the replace set, want 1 (not accumulated)", len(repo.awards))
	}
}

// TestService_Settle_CorrectsForATeamOrderThatDriftedSincePollCreation is a
// regression test for a real, repeated production incident: PandaScore's
// opponents order for a match is not stable across two fetches, so
// match.FirstTeam/SecondTeam at settlement can differ from what they were
// when poll.Options was built. G2 was FirstTeam when the poll asked "who
// wins" (poll.FirstTeamID == g2.ID), but by settlement BetBoom had become
// FirstTeam — without correcting for this, every voter who correctly
// predicted "G2 wins 2:1" (option {2,1} under the poll's own G2-first
// convention) would score against the raw, uncorrected actual score and
// come out wrong.
func TestService_Settle_CorrectsForATeamOrderThatDriftedSincePollCreation(t *testing.T) {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	g2 := common.TeamID{Value: common.NameUUID("team:g2")}
	betboom := common.TeamID{Value: common.NameUUID("team:betboom")}

	// Settlement time: match now reports BetBoom first, G2 second — the
	// real recorded outcome is BetBoom 1, G2 2 (G2 won 2:1).
	score := competition.MatchScore{First: 1, Second: 2}
	match := competition.Match{
		ID: common.NewMatchID(), Score: &score, Format: format, ActualStartedAt: &now,
		FirstTeam:  &competition.Team{ID: betboom, Name: "BetBoom Team"},
		SecondTeam: &competition.Team{ID: g2, Name: "G2"},
	}
	event := competition.Event{ID: common.NewEventID()}

	// Poll creation time: the poll was rendered with G2 first (confirmed
	// directly from the sent poll's own question text) — so poll.Options'
	// "first" means G2, not BetBoom.
	poll := prediction.Poll{
		ID: common.NewPollID(), ChatID: common.ChatID{Value: -1},
		FirstTeamID: g2, SecondTeamID: betboom,
		Options: []prediction.Option{
			{Index: 0, Score: competition.MatchScore{First: 2, Second: 0}}, // G2 sweeps
			{Index: 1, Score: competition.MatchScore{First: 2, Second: 1}}, // G2 wins 2:1 — the real outcome
			{Index: 2, Score: competition.MatchScore{First: 1, Second: 2}}, // BetBoom wins 2:1
			{Index: 3, Score: competition.MatchScore{First: 0, Second: 2}}, // BetBoom sweeps
		},
	}
	correct := common.UserID{Value: 1} // predicted "G2 wins 2:1" — right
	wrong := common.UserID{Value: 2}   // predicted "BetBoom wins 2:1" — this raw-matches the uncorrected actual, but is wrong
	votes := fakeVotes{votes: []prediction.Vote{
		{PollID: poll.ID, UserID: correct, OptionIndex: 1},
		{PollID: poll.ID, UserID: wrong, OptionIndex: 2},
	}}
	repo := &fakeAwardsRepo{}
	svc := NewService(votes, repo, common.FixedClock(now))

	awards, err := svc.Settle(context.Background(), event, match, poll)
	if err != nil {
		t.Fatal(err)
	}
	byUser := map[common.UserID]Award{}
	for _, a := range awards {
		byUser[a.UserID] = a
	}
	if a, ok := byUser[correct]; !ok || a.Kind != AwardExactScore || a.Points != format.ExactPoints() {
		t.Fatalf("voter who correctly predicted the real outcome (G2 2:1) = %+v, ok=%v, want an exact-score award", a, ok)
	}
	if _, ok := byUser[wrong]; ok {
		t.Fatalf("voter who predicted the wrong team to win must not be awarded just because it raw-matches the uncorrected score: %+v", byUser[wrong])
	}
}

// TestService_Settle_LeavesScoringUnchangedWhenThePollHasNoAnchor covers
// polls saved before FirstTeamID/SecondTeamID existed (both the zero
// value) — Settle must fall back to the old, uncorrected behavior rather
// than misinterpreting the zero value as "team order confirmed swapped".
func TestService_Settle_LeavesScoringUnchangedWhenThePollHasNoAnchor(t *testing.T) {
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	score := competition.MatchScore{First: 2, Second: 0}
	match := competition.Match{
		ID: common.NewMatchID(), Score: &score, Format: format, ActualStartedAt: &now,
		FirstTeam:  &competition.Team{ID: common.TeamID{Value: common.NameUUID("a")}, Name: "A"},
		SecondTeam: &competition.Team{ID: common.TeamID{Value: common.NameUUID("b")}, Name: "B"},
	}
	poll := prediction.Poll{
		ID:      common.NewPollID(), // FirstTeamID/SecondTeamID left zero-value: no anchor
		Options: []prediction.Option{{Index: 0, Score: competition.MatchScore{First: 2, Second: 0}}},
	}
	voter := common.UserID{Value: 1}
	votes := fakeVotes{votes: []prediction.Vote{{PollID: poll.ID, UserID: voter, OptionIndex: 0}}}
	repo := &fakeAwardsRepo{}
	svc := NewService(votes, repo, common.FixedClock(now))

	awards, err := svc.Settle(context.Background(), competition.Event{}, match, poll)
	if err != nil {
		t.Fatal(err)
	}
	if len(awards) != 1 || awards[0].Kind != AwardExactScore {
		t.Fatalf("expected the exact-score award unaffected by the anchor check, got %+v", awards)
	}
}
