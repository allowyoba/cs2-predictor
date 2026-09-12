//go:build integration

package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

func newTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("cs2predictor"),
		tcpostgres.WithUsername("cs2predictor"),
		tcpostgres.WithPassword("cs2predictor"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	if err := pg.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

// TestRun_CorrectsASwappedMatchAndResettles reproduces the exact production
// incident: a poll built while FirstTeam=A/SecondTeam=B, but the match's
// score persisted (and hashed for settlement) under the swapped
// FirstTeam=B/SecondTeam=A — every voter's outcome comparison then fails
// even for a correct prediction. run() must swap the match back, replace
// the (wrong, zero) awards with the correct ones, and enqueue a corrected
// result notification.
func TestRun_CorrectsASwappedMatchAndResettles(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	chats := pg.NewChatRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)
	outbox := pg.NewOutbox(pool)

	chatID := common.ChatID{Value: -100123}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Test", Locale: "RU", Timezone: "UTC", Active: true}); err != nil {
		t.Fatalf("save chat: %v", err)
	}

	eventID := common.NewEventID()
	if _, err := catalog.SaveEvent(ctx, competition.Event{
		ID: eventID, Game: competition.GameCS2, Name: "Test Event", ExternalID: "evt-1",
		Status: competition.EventRunning, Provider: "PANDASCORE",
	}); err != nil {
		t.Fatalf("save event: %v", err)
	}

	teamA := &competition.Team{ID: common.NewTeamID(), Name: "TeamA", ExternalID: "1"}
	teamB := &competition.Team{ID: common.NewTeamID(), Name: "TeamB", ExternalID: "2"}
	format := competition.SeriesFormat{Kind: competition.BestOf, Size: 3}
	matchID := common.NewMatchID()
	scheduledAt := time.Now().Add(-2 * time.Hour)

	// The real result: TeamA won 2:1. The poll (below) is built under
	// FirstTeam=A/SecondTeam=B, matching what actually got rendered to
	// voters. But the persisted match — exactly like the incident — is
	// saved under the SWAPPED labeling (FirstTeam=B/SecondTeam=A), which
	// is what SynchronizeMatches would have stored right before settling.
	wrongMatch := competition.Match{
		ID: matchID, EventID: eventID, ExternalID: "m-1", FirstTeam: teamB, SecondTeam: teamA,
		Status: competition.MatchFinished, Format: format, ScheduledAt: &scheduledAt, ActualStartedAt: &scheduledAt,
		Score: &competition.MatchScore{First: 1, Second: 2}, // "B:A" = 1:2, i.e. A won 2:1 — correct number, wrong labels
	}
	if _, err := catalog.SaveMatch(ctx, wrongMatch); err != nil {
		t.Fatalf("save match: %v", err)
	}

	// Poll built under the ORIGINAL (correct) A-first labeling — exactly
	// what prediction.Service.Create would have produced at creation time,
	// before any drift.
	poll := prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: matchID, Status: prediction.PollClosed,
		ClosesAt: scheduledAt,
		Options: []prediction.Option{
			{Index: 0, Score: competition.MatchScore{First: 2, Second: 0}},
			{Index: 1, Score: competition.MatchScore{First: 2, Second: 1}}, // exact correct prediction: A wins 2:1
			{Index: 2, Score: competition.MatchScore{First: 1, Second: 2}},
			{Index: 3, Score: competition.MatchScore{First: 0, Second: 2}},
		},
	}
	if _, err := predictions.SavePoll(ctx, poll); err != nil {
		t.Fatalf("save poll: %v", err)
	}
	voter := common.UserID{Value: 555}
	if err := predictions.SaveVote(ctx, prediction.Vote{
		PollID: poll.ID, UserID: voter, OptionIndex: 1, DisplayName: "Voter", VotedAt: scheduledAt,
	}); err != nil {
		t.Fatalf("save vote: %v", err)
	}

	// Sanity check the fixture: under the current (wrong) labeling, this
	// exact-correct voter gets nothing — this is the bug being reproduced.
	awardsBefore, err := scoringRepo.Leaderboard(ctx, chatID, scoring.ForEvent(eventID))
	if err != nil {
		t.Fatalf("leaderboard before: %v", err)
	}
	for _, st := range awardsBefore {
		if st.UserID == voter && st.Points != 0 {
			t.Fatalf("fixture invalid: voter already has points (%d) before any settlement ran", st.Points)
		}
	}

	affectedMatches = []affectedMatch{
		{id: matchID.Value.String(), firstTeamName: "TeamB", firstScore: 1, secondTeamName: "TeamA", secondScore: 2},
	}

	log := slog.New(slog.NewJSONHandler(testWriter{t}, nil))

	// Dry run must not touch anything.
	if err := run(ctx, pool, false, log); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	unchanged, err := catalog.FindMatch(ctx, matchID)
	if err != nil {
		t.Fatalf("find match after dry run: %v", err)
	}
	if unchanged.FirstTeam.Name != "TeamB" {
		t.Fatalf("dry run modified the match: %+v", unchanged)
	}

	// The real run must swap the match and re-settle correctly.
	if err := run(ctx, pool, true, log); err != nil {
		t.Fatalf("apply run: %v", err)
	}

	corrected, err := catalog.FindMatch(ctx, matchID)
	if err != nil {
		t.Fatalf("find match after apply: %v", err)
	}
	if corrected.FirstTeam.Name != "TeamA" || corrected.SecondTeam.Name != "TeamB" {
		t.Fatalf("team order not corrected: %+v / %+v", corrected.FirstTeam, corrected.SecondTeam)
	}
	if corrected.Score.First != 2 || corrected.Score.Second != 1 {
		t.Fatalf("score not corrected: %+v", corrected.Score)
	}

	standings, err := scoringRepo.Leaderboard(ctx, chatID, scoring.ForEvent(eventID))
	if err != nil {
		t.Fatalf("leaderboard after: %v", err)
	}
	var voterPoints int
	for _, st := range standings {
		if st.UserID == voter {
			voterPoints = st.Points
		}
	}
	if voterPoints != format.ExactPoints() {
		t.Fatalf("voter points after correction = %d, want %d (exact match)", voterPoints, format.ExactPoints())
	}

	// The corrected result must actually reach the chat, not just the DB —
	// this is the "resend the corrected table" half of the incident.
	pending, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatalf("pending outbox: %v", err)
	}
	found := false
	for _, msg := range pending {
		if msg.Type == "telegram.match-result" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no telegram.match-result outbox message enqueued after correction; pending=%+v", pending)
	}
}

// testWriter adapts *testing.T to io.Writer so the backfill's own log lines
// show up under `go test -v` instead of on stdout.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
