//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/feedback"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// newTestPool starts a fresh postgres:17-alpine container, migrates it, and
// returns a connected pool. Each test gets its own container so tests stay
// independent (and safe to run with -parallel).
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
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pg.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool, ctx
}

// TestPredictionIsPersistedScoredAndRankedWithinItsChat exercises the full
// path against a real Postgres via testcontainers-go: chat ->
// event/team/match -> poll+options (write-once) -> vote (auto-creates
// user) -> award replace -> leaderboard SQL join -> medal upsert-by-rank.
func TestPredictionIsPersistedScoredAndRankedWithinItsChat(t *testing.T) {
	pool, ctx := newTestPool(t)

	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	chatID := common.ChatID{Value: -100123}

	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Test chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatalf("save chat: %v", err)
	}

	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	event := competition.Event{
		ID: common.NewEventID(), Game: competition.GameCS2, Name: "IEM Test", ExternalID: "event-1",
		Status: competition.EventRunning, StartsAt: &now, Provider: "PANDASCORE",
	}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatalf("save event: %v", err)
	}

	firstTeam := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "team-1"}
	secondTeam := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "team-2"}
	stage := "Final"
	score := competition.MatchScore{First: 2, Second: 1}
	match := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "match-1",
		FirstTeam: &firstTeam, SecondTeam: &secondTeam, Stage: &stage,
		ScheduledAt: &now, ActualStartedAt: &now, Status: competition.MatchFinished,
		Format: format, Score: &score,
	}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatalf("save match: %v", err)
	}

	closesAt := now
	telegramPollID := "tg-poll"
	telegramMessageID := int64(10)
	options := make([]prediction.Option, 0)
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	poll := prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID,
		TelegramPollID: &telegramPollID, TelegramMessageID: &telegramMessageID,
		Options: options, Status: prediction.PollClosed, ClosesAt: closesAt,
	}
	if _, err := predictions.SavePoll(ctx, poll); err != nil {
		t.Fatalf("save poll: %v", err)
	}

	voter := common.UserID{Value: 7}
	username := "alex"
	if err := predictions.SaveVote(ctx, prediction.Vote{
		PollID: poll.ID, UserID: voter, OptionIndex: 1, Username: &username, DisplayName: "Alex", VotedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("save vote: %v", err)
	}

	if err := scoringRepo.ReplaceAwards(ctx, poll.ID, []scoring.Award{{
		PollID: poll.ID, UserID: voter,
		Points: 2, Kind: scoring.AwardExactScore, AwardedAt: now.Add(time.Minute),
	}}); err != nil {
		t.Fatalf("replace awards: %v", err)
	}

	standings, err := scoringRepo.Leaderboard(ctx, chatID, scoring.ForEvent(event.ID))
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}
	if len(standings) != 1 {
		t.Fatalf("expected 1 standing, got %d: %+v", len(standings), standings)
	}
	s := standings[0]
	if s.Rank != 1 || s.Points != 2 || s.ExactPredictions != 1 || s.CorrectPredictions != 1 || s.Predictions != 1 || s.Tournaments != 1 || s.AccuracyPercent() != 100 {
		t.Fatalf("unexpected standing: %+v", s)
	}

	// The same Telegram user may vote in many chats. Leaderboards and every
	// derived metric must stay scoped to the requested chat rather than merge
	// votes globally by telegram_user.id.
	otherChatID := common.ChatID{Value: -100456}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: otherChatID, Title: "Other chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatalf("save other chat: %v", err)
	}
	otherTelegramPollID := "tg-poll-other"
	otherTelegramMessageID := int64(11)
	otherPoll := prediction.Poll{
		ID: common.NewPollID(), ChatID: otherChatID, MatchID: match.ID,
		TelegramPollID: &otherTelegramPollID, TelegramMessageID: &otherTelegramMessageID,
		Options: options, Status: prediction.PollClosed, ClosesAt: closesAt,
	}
	if _, err := predictions.SavePoll(ctx, otherPoll); err != nil {
		t.Fatalf("save other poll: %v", err)
	}
	if err := predictions.SaveVote(ctx, prediction.Vote{
		PollID: otherPoll.ID, UserID: voter, OptionIndex: 1, Username: &username, DisplayName: "Alex", VotedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("save other-chat vote: %v", err)
	}
	if err := scoringRepo.ReplaceAwards(ctx, otherPoll.ID, []scoring.Award{{
		PollID: otherPoll.ID, UserID: voter,
		Points: 2, Kind: scoring.AwardExactScore, AwardedAt: now.Add(time.Minute),
	}}); err != nil {
		t.Fatalf("replace other-chat awards: %v", err)
	}

	standings, err = scoringRepo.Leaderboard(ctx, chatID, scoring.AllTime())
	if err != nil {
		t.Fatalf("chat-scoped leaderboard: %v", err)
	}
	if len(standings) != 1 || standings[0].Points != 2 || standings[0].Predictions != 1 || standings[0].Tournaments != 1 {
		t.Fatalf("other chat leaked into leaderboard: %+v", standings)
	}

	// Personal statistics intentionally do the opposite: they aggregate the
	// same Telegram user across every chat. Poll participations are distinct
	// (two votes), while the same tournament is counted once globally.
	personal, err := scoringRepo.UserStats(ctx, voter, scoring.AllTime())
	if err != nil {
		t.Fatalf("personal all-time stats: %v", err)
	}
	if personal == nil || personal.Points != 4 || personal.Predictions != 2 || personal.CorrectPredictions != 2 || personal.Tournaments != 1 || personal.AccuracyPercent() != 100 {
		t.Fatalf("unexpected cross-chat personal stats: %+v", personal)
	}
	personalYear, err := scoringRepo.UserStats(ctx, voter, scoring.ForYear(2026))
	if err != nil {
		t.Fatalf("personal yearly stats: %v", err)
	}
	if personalYear == nil || personalYear.Predictions != 2 || personalYear.Points != 4 {
		t.Fatalf("unexpected cross-chat yearly stats: %+v", personalYear)
	}
	months, err := scoringRepo.AvailableUserMonths(ctx, voter)
	if err != nil {
		t.Fatalf("personal available months: %v", err)
	}
	if len(months) != 1 || months[0].Year != 2026 || months[0].Month != time.September {
		t.Fatalf("unexpected personal months: %+v", months)
	}
	chatStats, err := scoringRepo.UserChatStats(ctx, voter)
	if err != nil {
		t.Fatalf("personal per-chat stats: %v", err)
	}
	if len(chatStats) != 2 {
		t.Fatalf("expected stats for 2 chats, got %d: %+v", len(chatStats), chatStats)
	}
	for _, row := range chatStats {
		if row.Points != 2 || row.Predictions != 1 || row.Tournaments != 1 || row.AccuracyPercent() != 100 {
			t.Fatalf("unexpected per-chat personal stats row: %+v", row)
		}
	}

	if err := scoringRepo.AwardMedals(ctx, chatID, event.ID, standings, now); err != nil {
		t.Fatalf("award medals: %v", err)
	}
	medals, err := scoringRepo.MedalCounts(ctx, chatID)
	if err != nil {
		t.Fatalf("medal counts: %v", err)
	}
	if medals[voter].Gold != 1 {
		t.Fatalf("expected gold=1 for voter, got %+v", medals[voter])
	}
}

// TestClusterLock_MutualExclusion verifies pg_try_advisory_lock actually
// excludes a concurrent holder of the same lock name, and that the lock is
// released (available again) once the first Execute call returns.
func TestClusterLock_MutualExclusion(t *testing.T) {
	pool, ctx := newTestPool(t)
	lock := pg.NewClusterLock(pool)

	holding := make(chan struct{})
	release := make(chan struct{})
	firstAcquired := make(chan bool, 1)

	go func() {
		acquired, err := lock.Execute(ctx, "test-lock", func(ctx context.Context) error {
			close(holding)
			<-release
			return nil
		})
		if err != nil {
			t.Errorf("first Execute: %v", err)
		}
		firstAcquired <- acquired
	}()

	<-holding // wait until the first goroutine actually holds the lock

	acquired, err := lock.Execute(ctx, "test-lock", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("expected the second Execute to find the lock already held")
	}

	close(release)
	if !<-firstAcquired {
		t.Fatal("expected the first Execute to have acquired the lock")
	}

	acquired, err = lock.Execute(ctx, "test-lock", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("expected the lock to be available again after the first holder released it")
	}
}

// TestClusterLock_DifferentNamesDoNotContend verifies two different lock
// names don't block each other.
func TestClusterLock_DifferentNamesDoNotContend(t *testing.T) {
	pool, ctx := newTestPool(t)
	lock := pg.NewClusterLock(pool)

	holding := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = lock.Execute(ctx, "lock-a", func(context.Context) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	defer close(release)

	acquired, err := lock.Execute(ctx, "lock-b", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("expected a differently-named lock to be independently acquirable")
	}
}

// TestUpdateDeduplicator_ClaimIsExclusiveThenReleasable verifies claim
// returns true only for the first caller, and that release lets a later
// claim of the same update id succeed again (used to un-claim after a
// failed webhook handler, so Telegram's retry can reprocess it).
func TestUpdateDeduplicator_ClaimIsExclusiveThenReleasable(t *testing.T) {
	pool, ctx := newTestPool(t)
	dedup := pg.NewUpdateDeduplicator(pool)

	first, err := dedup.Claim(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !first {
		t.Fatal("expected the first claim to succeed")
	}

	second, err := dedup.Claim(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Fatal("expected a duplicate claim to fail")
	}

	if err := dedup.Release(ctx, 42); err != nil {
		t.Fatal(err)
	}

	third, err := dedup.Claim(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !third {
		t.Fatal("expected a claim after release to succeed again")
	}
}

// TestSubscriptionRepository_UnsubscribeSoftDeletes verifies Unsubscribe
// sets active=false rather than deleting the row — SubscribedChats and
// Subscriptions must both stop returning it, but the subscription history
// itself is preserved.
func TestSubscriptionRepository_UnsubscribeSoftDeletes(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	subs := pg.NewSubscriptionRepository(pool)

	chatID := common.ChatID{Value: -555}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	if _, err := subs.Subscribe(ctx, subscription.EventSubscription{ChatID: chatID, EventID: event.ID, SubscribedAt: now, Active: true}); err != nil {
		t.Fatal(err)
	}

	active, err := subs.Subscriptions(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active subscription before unsubscribe, got %d", len(active))
	}

	if err := subs.Unsubscribe(ctx, chatID, event.ID); err != nil {
		t.Fatal(err)
	}

	activeAfter, err := subs.Subscriptions(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(activeAfter) != 0 {
		t.Fatalf("expected 0 active subscriptions after unsubscribe, got %d", len(activeAfter))
	}
	chatsSubscribed, err := subs.SubscribedChats(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chatsSubscribed) != 0 {
		t.Fatalf("expected SubscribedChats to exclude the unsubscribed chat, got %v", chatsSubscribed)
	}
}

// TestChatRepository_ModeratorLifecycle verifies AddModerator auto-creates
// the referenced telegram_user rows (needed to satisfy the FK), and that
// RemoveModerator reverses IsModerator.
func TestChatRepository_ModeratorLifecycle(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)

	chatID := common.ChatID{Value: -777}
	actor, target := common.UserID{Value: 1}, common.UserID{Value: 2}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	if isMod, err := chats.IsModerator(ctx, chatID, target); err != nil || isMod {
		t.Fatalf("expected target not to be a moderator yet, isMod=%v err=%v", isMod, err)
	}

	if err := chats.AddModerator(ctx, chat.Moderator{ChatID: chatID, UserID: target, AppointedBy: actor}); err != nil {
		t.Fatal(err)
	}
	if isMod, err := chats.IsModerator(ctx, chatID, target); err != nil || !isMod {
		t.Fatalf("expected target to be a moderator, isMod=%v err=%v", isMod, err)
	}

	if err := chats.RemoveModerator(ctx, chatID, target); err != nil {
		t.Fatal(err)
	}
	if isMod, err := chats.IsModerator(ctx, chatID, target); err != nil || isMod {
		t.Fatalf("expected target to no longer be a moderator, isMod=%v err=%v", isMod, err)
	}
}

// TestChatRepository_ModeratorPermissionsRoundTrip exercises the
// chat_moderator_permission table against a real Postgres: AddModerator's
// initial grant, ListModerators/ModeratorPermissions reading it back,
// SetModeratorPermissions replacing (not merging) the set, and — the one
// behavior no fake-backed unit test can verify — RemoveModerator's
// ON DELETE CASCADE actually clearing the permission rows with it rather
// than leaving them orphaned.
func TestChatRepository_ModeratorPermissionsRoundTrip(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)

	chatID := common.ChatID{Value: -778}
	actor, target := common.UserID{Value: 1}, common.UserID{Value: 2}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	if err := chats.AddModerator(ctx, chat.Moderator{
		ChatID: chatID, UserID: target, AppointedBy: actor,
		Username: "mod", DisplayName: "Mod", Permissions: chat.PresetStatsOnly(),
	}); err != nil {
		t.Fatal(err)
	}

	perms, err := chats.ModeratorPermissions(ctx, chatID, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(perms) != 1 || perms[0] != chat.PermissionViewStats {
		t.Fatalf("ModeratorPermissions = %v, want exactly [view_stats]", perms)
	}

	mods, err := chats.ListModerators(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 1 || len(mods[0].Permissions) != 1 || mods[0].Permissions[0] != chat.PermissionViewStats {
		t.Fatalf("ListModerators = %+v, want exactly one moderator with [view_stats]", mods)
	}

	// SetModeratorPermissions replaces the set — content preset has no
	// overlap with the stats-only preset it started from, so a merge bug
	// (INSERT without clearing first) would leave view_stats behind too.
	if err := chats.SetModeratorPermissions(ctx, chatID, target, chat.PresetContent()); err != nil {
		t.Fatal(err)
	}
	perms, err = chats.ModeratorPermissions(ctx, chatID, target)
	if err != nil {
		t.Fatal(err)
	}
	if !chat.HasPermission(perms, chat.PermissionManageEvents) || !chat.HasPermission(perms, chat.PermissionManageMatches) || chat.HasPermission(perms, chat.PermissionViewStats) {
		t.Fatalf("ModeratorPermissions after replace = %v, want exactly the content preset", perms)
	}

	// Clearing the set entirely (the "0 permissions" state a fresh
	// appointment can transiently be in) must round-trip to empty, not nil
	// vs. empty ambiguity or a leftover row.
	if err := chats.SetModeratorPermissions(ctx, chatID, target, nil); err != nil {
		t.Fatal(err)
	}
	if perms, err := chats.ModeratorPermissions(ctx, chatID, target); err != nil || len(perms) != 0 {
		t.Fatalf("ModeratorPermissions after clearing = %v, err=%v, want empty", perms, err)
	}

	if err := chats.SetModeratorPermissions(ctx, chatID, target, chat.PresetFullAccess()); err != nil {
		t.Fatal(err)
	}
	if err := chats.RemoveModerator(ctx, chatID, target); err != nil {
		t.Fatal(err)
	}
	if perms, err := chats.ModeratorPermissions(ctx, chatID, target); err != nil || len(perms) != 0 {
		t.Fatalf("expected ON DELETE CASCADE to clear permission rows with the moderator, got %v, err=%v", perms, err)
	}
}

// TestChatRepository_UserProfile checks the one read UserProfile has —
// nothing known yet returns nil, and AddModerator's telegram_user upsert
// (via ensureUser) makes the profile resolvable afterward.
func TestChatRepository_UserProfile(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)

	unknown := common.UserID{Value: 999999}
	if profile, err := chats.UserProfile(ctx, unknown); err != nil || profile != nil {
		t.Fatalf("expected nil profile for an unknown user, got %+v, err=%v", profile, err)
	}

	chatID := common.ChatID{Value: -779}
	actor, target := common.UserID{Value: 1}, common.UserID{Value: 2}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.AddModerator(ctx, chat.Moderator{ChatID: chatID, UserID: target, AppointedBy: actor, Username: "newmod", DisplayName: "New Mod"}); err != nil {
		t.Fatal(err)
	}

	profile, err := chats.UserProfile(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if profile == nil || profile.Username != "newmod" || profile.DisplayName != "New Mod" {
		t.Fatalf("UserProfile = %+v, want username=newmod displayName=\"New Mod\"", profile)
	}
}

// TestInvitationRepository_CreateAcceptAndRevokeLifecycle exercises
// moderator_invitation against a real Postgres, including the one property
// a fake can only simulate rather than prove: UseInvitation's
// compare-and-swap WHERE clause actually enforces one-time use under the
// database's own concurrency guarantees, not just in application code.
func TestInvitationRepository_CreateAcceptAndRevokeLifecycle(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	invitations := pg.NewInvitationRepository(pool)

	chatID := common.ChatID{Value: -780}
	creator := common.UserID{Value: 1}
	acceptor := common.UserID{Value: 2}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	if inv, err := invitations.Invitation(ctx, "does-not-exist"); err != nil || inv != nil {
		t.Fatalf("expected nil for an unknown token, got %+v, err=%v", inv, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	inv := chat.ModeratorInvitation{
		Token: "test-token-1", ChatID: chatID, Permissions: chat.PresetContent(),
		CreatedBy: creator, CreatedAt: now, ExpiresAt: now.Add(72 * time.Hour),
	}
	if err := invitations.CreateInvitation(ctx, inv); err != nil {
		t.Fatal(err)
	}

	got, err := invitations.Invitation(ctx, inv.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ChatID != chatID || got.CreatedBy != creator || got.UsedAt != nil || got.RevokedAt != nil {
		t.Fatalf("Invitation() = %+v, want a fresh, unused, unrevoked invitation", got)
	}
	if !chat.HasPermission(got.Permissions, chat.PermissionManageEvents) || !chat.HasPermission(got.Permissions, chat.PermissionManageMatches) {
		t.Fatalf("Invitation().Permissions = %v, want the content preset", got.Permissions)
	}
	if got.Status(now) != chat.InvitationPending {
		t.Fatalf("Status() = %v, want pending", got.Status(now))
	}

	ok, err := invitations.UseInvitation(ctx, inv.Token, acceptor, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected the first UseInvitation to succeed")
	}
	used, err := invitations.Invitation(ctx, inv.Token)
	if err != nil {
		t.Fatal(err)
	}
	if used.UsedAt == nil || used.UsedBy == nil || *used.UsedBy != acceptor {
		t.Fatalf("expected the invitation to record its acceptor, got %+v", used)
	}

	// The one-time-use guarantee: a second accept must not succeed, and
	// must not overwrite who actually claimed it.
	ok, err = invitations.UseInvitation(ctx, inv.Token, common.UserID{Value: 3}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected a second UseInvitation on an already-used token to fail")
	}
	stillUsedByFirst, err := invitations.Invitation(ctx, inv.Token)
	if err != nil {
		t.Fatal(err)
	}
	if stillUsedByFirst.UsedBy == nil || *stillUsedByFirst.UsedBy != acceptor {
		t.Fatalf("expected UsedBy to remain the first acceptor, got %+v", stillUsedByFirst.UsedBy)
	}

	// A separate, never-used invitation: revoke makes it permanently
	// unusable, and revoking it twice is a harmless no-op.
	inv2 := chat.ModeratorInvitation{
		Token: "test-token-2", ChatID: chatID, Permissions: chat.PresetStatsOnly(),
		CreatedBy: creator, CreatedAt: now, ExpiresAt: now.Add(72 * time.Hour),
	}
	if err := invitations.CreateInvitation(ctx, inv2); err != nil {
		t.Fatal(err)
	}
	if err := invitations.RevokeInvitation(ctx, inv2.Token); err != nil {
		t.Fatal(err)
	}
	if err := invitations.RevokeInvitation(ctx, inv2.Token); err != nil {
		t.Fatalf("expected revoking an already-revoked invitation to be a no-op, got err=%v", err)
	}
	revoked, err := invitations.Invitation(ctx, inv2.Token)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("expected RevokedAt to be set")
	}
	if revoked.Status(now) != chat.InvitationRevoked {
		t.Fatalf("Status() = %v, want revoked", revoked.Status(now))
	}
	ok, err = invitations.UseInvitation(ctx, inv2.Token, acceptor, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected UseInvitation on a revoked invitation to fail")
	}
}

func TestChatRepository_ManagedChatsIndexRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)

	chatA := common.ChatID{Value: -901}
	chatB := common.ChatID{Value: -902}
	userID := common.UserID{Value: 501}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatA, Title: "Chat A", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatB, Title: "Chat B", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	empty, err := chats.ManagedChats(ctx, userID)
	if err != nil || len(empty) != 0 {
		t.Fatalf("ManagedChats before any RecordManaged = %v, %v, want empty", empty, err)
	}

	if err := chats.RecordManaged(ctx, chatA, userID); err != nil {
		t.Fatal(err)
	}
	if err := chats.RecordManaged(ctx, chatB, userID); err != nil {
		t.Fatal(err)
	}

	got, err := chats.ManagedChats(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ManagedChats = %+v, want 2 entries", got)
	}

	// Recording again (e.g. a second admin action) must upsert, not
	// duplicate — still exactly 2 rows for this user.
	if err := chats.RecordManaged(ctx, chatA, userID); err != nil {
		t.Fatal(err)
	}
	got2, err := chats.ManagedChats(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 2 {
		t.Fatalf("ManagedChats after re-recording chatA = %+v, want still 2 entries", got2)
	}

	// A deactivated chat must not appear in the picker.
	settingsA, err := chats.Find(ctx, chatA)
	if err != nil {
		t.Fatal(err)
	}
	settingsA.Active = false
	if _, err := chats.Save(ctx, *settingsA); err != nil {
		t.Fatal(err)
	}
	got3, err := chats.ManagedChats(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got3) != 1 || got3[0].ChatID != chatB {
		t.Fatalf("ManagedChats after deactivating chatA = %+v, want only chatB", got3)
	}
}

func TestChatRepository_DMSessionLifecycle(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)

	chatA := common.ChatID{Value: -903}
	chatB := common.ChatID{Value: -904}
	userID := common.UserID{Value: 502}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatA, Title: "Chat A", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatB, Title: "Chat B", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	none, err := chats.DMSession(ctx, userID)
	if err != nil || none != nil {
		t.Fatalf("DMSession before any SetDMSession = %v, %v, want nil, nil", none, err)
	}

	if err := chats.SetDMSession(ctx, userID, chatA); err != nil {
		t.Fatal(err)
	}
	got, err := chats.DMSession(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != chatA {
		t.Fatalf("DMSession = %v, want %v", got, chatA)
	}

	// Opening a second chat must overwrite, not add a second session — one
	// row per user by design.
	if err := chats.SetDMSession(ctx, userID, chatB); err != nil {
		t.Fatal(err)
	}
	got2, err := chats.DMSession(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if got2 == nil || *got2 != chatB {
		t.Fatalf("DMSession after switching = %v, want %v", got2, chatB)
	}

	if err := chats.ClearDMSession(ctx, userID); err != nil {
		t.Fatal(err)
	}
	cleared, err := chats.DMSession(ctx, userID)
	if err != nil || cleared != nil {
		t.Fatalf("DMSession after ClearDMSession = %v, %v, want nil, nil", cleared, err)
	}
}

func TestPendingApprovalRepository_CreateFindResolveRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	pending := pg.NewPendingApprovalRepository(pool)

	chatID := common.ChatID{Value: -905}
	userID := common.UserID{Value: 503}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e-pending", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	// RecordManaged (like requireManager's wrapper does in production, before
	// a pending-unsubscribe row referencing this user is ever created) is
	// what ensures the telegram_user row pending_unsubscribe.requested_by FKs
	// against actually exists.
	if err := chats.RecordManaged(ctx, chatID, userID); err != nil {
		t.Fatal(err)
	}

	missing, err := pending.Find(ctx, common.NewRequestID())
	if err != nil || missing != nil {
		t.Fatalf("Find of an unknown id = %v, %v, want nil, nil", missing, err)
	}

	requestID := common.NewRequestID()
	now := timeMustParse(t, "2026-08-03")
	p := chat.PendingApproval{
		ID: requestID, ChatID: chatID, Kind: chat.ApprovalUnsubscribe, EventID: &event.ID, RequestedBy: userID,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), SelfConfirmable: true,
	}
	if err := pending.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	got, err := pending.Find(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ChatID != chatID || got.RequestedBy != userID || !got.SelfConfirmable {
		t.Fatalf("Find = %+v, want a round trip of %+v", got, p)
	}
	if got.Kind != chat.ApprovalUnsubscribe || got.EventID == nil || *got.EventID != event.ID {
		t.Fatalf("Find = %+v, want the unsubscribe's own event back", got)
	}
	if !got.CreatedAt.Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}

	if err := pending.Resolve(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	resolved, err := pending.Find(ctx, requestID)
	if err != nil || resolved != nil {
		t.Fatalf("Find after Resolve = %v, %v, want nil, nil (resolved rows are excluded)", resolved, err)
	}

	// Resolving an already-resolved (or unknown) id must be a no-op, not an
	// error — a second tap on the same button must not fail loudly.
	if err := pending.Resolve(ctx, requestID); err != nil {
		t.Fatalf("Resolve of an already-resolved id should be a no-op, got %v", err)
	}
}

// TestChatRepository_DefaultTopTierOnlyRoundTrips covers the new
// default_top_tier_only column (migration 0007): it must persist true
// across a save/find round trip, default to false for a chat that never
// set it, and toggle back to false correctly (not just "truthy").
func TestChatRepository_DefaultTopTierOnlyRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	chatID := common.ChatID{Value: -888}

	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	got, err := chats.Find(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultTopTierOnly {
		t.Fatal("expected DefaultTopTierOnly to default to false")
	}

	got.DefaultTopTierOnly = true
	if _, err := chats.Save(ctx, *got); err != nil {
		t.Fatal(err)
	}
	got, err = chats.Find(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.DefaultTopTierOnly {
		t.Fatal("expected DefaultTopTierOnly = true after saving it as true")
	}

	got.DefaultTopTierOnly = false
	if _, err := chats.Save(ctx, *got); err != nil {
		t.Fatal(err)
	}
	got, err = chats.Find(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultTopTierOnly {
		t.Fatal("expected DefaultTopTierOnly = false after toggling it back off")
	}
}

// Every switch the bot has starts off — for chats and people that already
// exist, not only for new ones. The store answers that without a row
// having to be written first, which is what makes "off by default" a
// property of the schema rather than of whatever seeded it.
func TestChatRepository_EverySwitchStartsOffAndRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	chatID := common.ChatID{Value: -995}

	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range common.ChatNotificationKinds {
		on, err := chats.NotifyEnabled(ctx, common.ScopeChat, chatID.Value, string(kind))
		if err != nil {
			t.Fatal(err)
		}
		if on {
			t.Fatalf("%s is on for a chat that never asked for it", kind)
		}
	}

	if err := chats.SetNotifyEnabled(ctx, common.ScopeChat, chatID.Value, string(common.ChatNotifyStreams), true); err != nil {
		t.Fatal(err)
	}
	prefs, err := chats.NotifySettings(ctx, common.ScopeChat, chatID.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !prefs[string(common.ChatNotifyStreams)] {
		t.Fatal("expected the chat's choice to survive the round trip")
	}
	if prefs[string(common.ChatNotifyDigests)] {
		t.Fatal("turning one switch on turned another on as well")
	}

	// An operator chat id never has to exist anywhere else: the alert
	// contacts come from configuration, not from a row the bot wrote.
	if err := chats.SetNotifyEnabled(ctx, common.ScopeOperator, 424242, string(common.AdminAlertHostPressure), true); err != nil {
		t.Fatal(err)
	}
	on, err := chats.NotifyEnabled(ctx, common.ScopeOperator, 424242, string(common.AdminAlertHostPressure))
	if err != nil || !on {
		t.Fatalf("operator switch = %v, %v; want it stored", on, err)
	}
	// Scopes do not leak into one another.
	if on, _ := chats.NotifyEnabled(ctx, common.ScopeChat, 424242, string(common.AdminAlertHostPressure)); on {
		t.Fatal("an operator switch answered for a chat with the same id")
	}
}

// A chat with no explicit broadcast language follows its UI language; an
// explicit one survives the round trip and overrides it.
func TestChatRepository_StreamLanguageRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	chatID := common.ChatID{Value: -890}

	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	got, err := chats.Find(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StreamLanguage != "" || got.StreamLocale() != common.LocaleRU {
		t.Fatalf("expected an unset StreamLanguage following the chat's RU locale, got %q", got.StreamLanguage)
	}

	got.StreamLanguage = common.LocaleEN
	if _, err := chats.Save(ctx, *got); err != nil {
		t.Fatal(err)
	}
	got, err = chats.Find(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StreamLocale() != common.LocaleEN {
		t.Fatalf("StreamLocale() = %q, want EN after saving it", got.StreamLocale())
	}
}

// The retention sweep deletes from the approvals table by name, so a
// rename that misses this query leaves the sweep failing on every run —
// with nothing but a log line to say so. This exercises the real delete
// against the real schema.
func TestRetentionRepository_SweepsResolvedApprovals(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	approvals := pg.NewPendingApprovalRepository(pool)
	retention := pg.NewRetentionRepository(pool)

	chatID := common.ChatID{Value: -7171}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	userID := common.UserID{Value: 7171}
	if err := chats.RecordManaged(ctx, chatID, userID); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "ev-retention", Status: competition.EventRunning, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	requestID := common.NewRequestID()
	if err := approvals.Create(ctx, chat.PendingApproval{
		ID: requestID, Kind: chat.ApprovalUnsubscribe, ChatID: chatID, EventID: &event.ID,
		RequestedBy: userID, CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// Unresolved requests are never swept, however old.
	if deleted, err := retention.DeleteResolvedUnsubscribesBefore(ctx, now); err != nil || deleted != 0 {
		t.Fatalf("DeleteResolvedUnsubscribesBefore = %d, %v; want nothing deleted while unresolved", deleted, err)
	}
	if err := approvals.Resolve(ctx, requestID); err != nil {
		t.Fatal(err)
	}
	deleted, err := retention.DeleteResolvedUnsubscribesBefore(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteResolvedUnsubscribesBefore = %d, want the resolved request gone", deleted)
	}
}

// Partition maintenance must never queue for a lock. In PostgreSQL a
// pending ACCESS EXCLUSIVE request blocks every reader that arrives after
// it, so a sweep that waits (behind a backup's pg_dump, say) takes the
// whole application down with it — which is exactly what happened in
// production before this bound existed.
func TestRetentionRepository_PartitionDropGivesUpRatherThanQueueingForALock(t *testing.T) {
	pool, ctx := newTestPool(t)
	retention := pg.NewRetentionRepository(pool)
	day := time.Now().UTC().AddDate(0, 0, -30)

	if err := retention.EnsureOutboxPartition(ctx, day); err != nil {
		t.Fatal(err)
	}

	// Hold a reader's lock on the partition, the way a dump would.
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, "SELECT count(*) FROM outbox_event_"+day.Format("20060102")); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, "LOCK TABLE outbox_event_"+day.Format("20060102")+" IN ACCESS SHARE MODE"); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	dropped, err := retention.DropOutboxPartitionIfEmpty(ctx, day)
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("a contended drop must be a quiet no-op, got %v", err)
	}
	if dropped {
		t.Fatal("expected the drop to be skipped while the table is locked")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the drop waited %s; it must give up after about %s", elapsed, "2s")
	}

	// Once the reader is gone, the same call succeeds.
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	dropped, err = retention.DropOutboxPartitionIfEmpty(ctx, day)
	if err != nil || !dropped {
		t.Fatalf("expected the drop to succeed once uncontended, got %v, %v", dropped, err)
	}
}

// The tournament nominations depend on numbers no aggregate can produce:
// who went against the chat's majority, who was the only one right, how
// long a correct run lasted, and who voted in every single match. All four
// come out of one query, so all four are checked against one real dataset.
func TestScoringRepository_EventSpecialsCountsCrowdRelativeAchievements(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	chatID := common.ChatID{Value: -4242}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Major", ExternalID: "ev-specials", Status: competition.EventRunning, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	options := make([]prediction.Option, 0)
	for i, sc := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: sc})
	}
	// Index 0 is 2:0 (first team wins), the last option is 0:2 (second
	// team wins) — the two sides of the poll.
	firstWins, secondWins := 0, len(options)-1

	crowd := common.UserID{Value: 501}
	rebel := common.UserID{Value: 502}
	quiet := common.UserID{Value: 503}

	started := time.Now().UTC().Add(-72 * time.Hour)
	// Three matches: the crowd is right on the first, the rebel alone on
	// the second and third, having gone against the majority both times.
	type plan struct {
		crowdPick, rebelPick, quietPick int
		correct                         []common.UserID
	}
	plans := []plan{
		{crowdPick: firstWins, rebelPick: secondWins, quietPick: firstWins, correct: []common.UserID{crowd, quiet}},
		{crowdPick: firstWins, rebelPick: secondWins, quietPick: firstWins, correct: []common.UserID{rebel}},
		{crowdPick: firstWins, rebelPick: secondWins, quietPick: firstWins, correct: []common.UserID{rebel}},
	}
	for i, p := range plans {
		match := competition.Match{
			ID: common.NewMatchID(), EventID: event.ID, ExternalID: fmt.Sprintf("m-spec-%d", i),
			Status: competition.MatchFinished, Format: format,
		}
		playedAt := started.Add(time.Duration(i) * time.Hour)
		match.ActualStartedAt = &playedAt
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		poll := prediction.Poll{ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID, Options: options, Status: prediction.PollClosed, ClosesAt: started}
		saved, err := predictions.SavePoll(ctx, poll)
		if err != nil {
			t.Fatal(err)
		}
		for user, pick := range map[common.UserID]int{crowd: p.crowdPick, rebel: p.rebelPick, quiet: p.quietPick} {
			name := fmt.Sprintf("user-%d", user.Value)
			if err := predictions.SaveVote(ctx, prediction.Vote{
				PollID: saved.ID, UserID: user, OptionIndex: pick, DisplayName: name, VotedAt: started,
			}); err != nil {
				t.Fatal(err)
			}
		}
		var awards []scoring.Award
		for _, user := range p.correct {
			awards = append(awards, scoring.Award{
				PollID: saved.ID, UserID: user,
				Points: 1, Kind: scoring.AwardOutcome, AwardedAt: started,
			})
		}
		if err := scoringRepo.ReplaceAwards(ctx, saved.ID, awards); err != nil {
			t.Fatal(err)
		}
	}

	// A fourth match nobody but one person voted on: being "the only one
	// right" there means being the only one there, which is not a
	// nomination.
	soloMatch := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m-spec-solo",
		Status: competition.MatchFinished, Format: format,
	}
	soloPlayedAt := started.Add(4 * time.Hour)
	soloMatch.ActualStartedAt = &soloPlayedAt
	if _, err := catalog.SaveMatch(ctx, soloMatch); err != nil {
		t.Fatal(err)
	}
	soloPoll, err := predictions.SavePoll(ctx, prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: soloMatch.ID, Options: options,
		Status: prediction.PollClosed, ClosesAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := predictions.SaveVote(ctx, prediction.Vote{
		PollID: soloPoll.ID, UserID: quiet, OptionIndex: firstWins, DisplayName: "user-503", VotedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	if err := scoringRepo.ReplaceAwards(ctx, soloPoll.ID, []scoring.Award{{
		PollID: soloPoll.ID, UserID: quiet,
		Points: 1, Kind: scoring.AwardOutcome, AwardedAt: started,
	}}); err != nil {
		t.Fatal(err)
	}

	specials, err := scoringRepo.EventSpecials(ctx, chatID, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	byUserOf := func(s scoring.EventSpecials, id int64) scoring.EventUserSpecials {
		for _, u := range s.Users {
			if u.UserID.Value == id {
				return u
			}
		}
		return scoring.EventUserSpecials{}
	}
	if specials.TotalPolls != 4 {
		t.Fatalf("TotalPolls = %d, want 4", specials.TotalPolls)
	}
	if solo := byUserOf(specials, quiet.Value); solo.LoneCorrect != 0 {
		t.Fatalf("a poll with a single voter must not produce a lone-correct award, got %+v", solo)
	}
	byUser := map[int64]scoring.EventUserSpecials{}
	for _, u := range specials.Users {
		byUser[u.UserID.Value] = u
	}
	got := byUser[rebel.Value]
	if got.ContrarianWins != 2 {
		t.Fatalf("rebel ContrarianWins = %d, want 2 (right twice against the majority)", got.ContrarianWins)
	}
	if got.LoneCorrect != 2 {
		t.Fatalf("rebel LoneCorrect = %d, want 2 (the only one right both times)", got.LoneCorrect)
	}
	if got.LongestStreak != 2 {
		t.Fatalf("rebel LongestStreak = %d, want 2 (matches two and three)", got.LongestStreak)
	}
	if got.VotedPolls != 3 {
		t.Fatalf("rebel VotedPolls = %d, want 3", got.VotedPolls)
	}
	// The crowd was right only when it was the majority, which earns
	// nothing crowd-relative.
	if c := byUser[crowd.Value]; c.ContrarianWins != 0 || c.LoneCorrect != 0 || c.LongestStreak != 1 {
		t.Fatalf("crowd specials = %+v, want no contrarian credit and a streak of one", c)
	}
}

// The in-flight Apify run has to survive a restart — that persistence is
// the whole reason a timed-out fetch no longer pays for a second run.
func TestEnrichmentRepository_ProviderRunRoundTripsAndClears(t *testing.T) {
	pool, ctx := newTestPool(t)
	repo := pg.NewEnrichmentRepository(pool)

	if got, err := repo.Run(ctx, enrichment.SourceHLTV, "hltv"); err != nil || got != nil {
		t.Fatalf("expected no run before one is saved, got %+v, err %v", got, err)
	}

	period := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	started := period.Add(23 * time.Hour)
	run := enrichment.ProviderRun{
		Provider: enrichment.SourceHLTV, Key: "hltv", RunID: "abc123",
		Status: "RUNNING", Attempts: 1, PeriodStart: period, StartedAt: started,
	}
	if err := repo.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Run(ctx, enrichment.SourceHLTV, "hltv")
	if err != nil || got == nil {
		t.Fatalf("expected the run back, got %+v, err %v", got, err)
	}
	if got.RunID != "abc123" || got.Attempts != 1 || !got.PeriodStart.Equal(period) || !got.StartedAt.Equal(started) {
		t.Fatalf("round-tripped run = %+v, want %+v", *got, run)
	}

	// The other ranking mode of the same actor is tracked separately.
	if other, err := repo.Run(ctx, enrichment.SourceValveVRS, "valve"); err != nil || other != nil {
		t.Fatalf("expected (provider, key) to be independent, got %+v, err %v", other, err)
	}

	run.Attempts = 2
	run.RunID = "def456"
	if err := repo.SaveRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Run(ctx, enrichment.SourceHLTV, "hltv"); got.RunID != "def456" || got.Attempts != 2 {
		t.Fatalf("expected the second save to replace the first, got %+v", *got)
	}

	if err := repo.ClearRun(ctx, enrichment.SourceHLTV, "hltv"); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.Run(ctx, enrichment.SourceHLTV, "hltv"); err != nil || got != nil {
		t.Fatalf("expected the run to be gone after ClearRun, got %+v, err %v", got, err)
	}
}

// TestOutbox_PendingPublishedAndBackoff verifies the enqueue -> pending ->
// published lifecycle, and that Failed schedules a future retry (so a
// second Pending call right after a failure doesn't return the same
// message again).
func TestOutbox_PendingPublishedAndBackoff(t *testing.T) {
	pool, ctx := newTestPool(t)
	outbox := pg.NewOutbox(pool)

	id, err := outbox.Enqueue(ctx, "MATCH_POLL", "poll-1", "telegram.match-result", `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}

	pending, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("expected the enqueued message to be pending, got %+v", pending)
	}
	occurredAt := pending[0].OccurredAt

	if err := outbox.Failed(ctx, id, occurredAt, "telegram unavailable"); err != nil {
		t.Fatal(err)
	}
	pendingAfterFailure, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingAfterFailure) != 0 {
		t.Fatalf("expected the failed message to be backed off (not immediately pending again), got %+v", pendingAfterFailure)
	}

	if err := outbox.Published(ctx, id, occurredAt); err != nil {
		t.Fatal(err)
	}
}

// A message that keeps failing must stop being returned by Pending once it
// hits common.OutboxMaxAttempts — the cutoff app.OutboxDispatcher relies on
// to know when to stop treating a failure as "will retry" and start
// treating it as "gave up for good" (see its exhausted-metric logic).
func TestOutbox_StopsBeingPendingOnceItHitsMaxAttempts(t *testing.T) {
	pool, ctx := newTestPool(t)
	outbox := pg.NewOutbox(pool)

	id, err := outbox.Enqueue(ctx, "MATCH_POLL", "poll-2", "telegram.match-result", `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	initialPending, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var occurredAt time.Time
	for _, m := range initialPending {
		if m.ID == id {
			occurredAt = m.OccurredAt
		}
	}
	for range common.OutboxMaxAttempts {
		if err := outbox.Failed(ctx, id, occurredAt, "still unavailable"); err != nil {
			t.Fatal(err)
		}
	}

	pending, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range pending {
		if m.ID == id {
			t.Fatalf("message hit %d attempts but is still pending: %+v", common.OutboxMaxAttempts, m)
		}
	}
}

// TestOutboxPartitions_EnsureCreatesAndDropOnlyRemovesWhenEmpty covers
// migration 0031's whole point: EnsureOutboxPartition must make a day's
// partition available for inserts, and DropOutboxPartitionIfEmpty must
// refuse to remove one still holding a row — only an empty one, or a day
// with no partition at all, goes.
func TestOutboxPartitions_EnsureCreatesAndDropOnlyRemovesWhenEmpty(t *testing.T) {
	pool, ctx := newTestPool(t)
	retention := pg.NewRetentionRepository(pool)
	outbox := pg.NewOutbox(pool)

	today := time.Now().UTC().Truncate(24 * time.Hour)

	id, err := outbox.Enqueue(ctx, "chat", "-1", "telegram.match-result", "{}")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var occurredAt time.Time
	for _, m := range pending {
		if m.ID == id {
			occurredAt = m.OccurredAt
		}
	}
	if occurredAt.IsZero() {
		t.Fatal("enqueued message must come back from Pending")
	}

	// Today's partition already exists (migration 0031 creates it) and
	// holds the row just enqueued — it must not be dropped.
	dropped, err := retention.DropOutboxPartitionIfEmpty(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Fatal("a partition holding a row must never be dropped")
	}

	// A day with no partition at all is a safe no-op, not an error.
	farFuture := today.AddDate(0, 0, 365)
	dropped, err = retention.DropOutboxPartitionIfEmpty(ctx, farFuture)
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Fatal("a nonexistent partition must never report as dropped")
	}

	// Ensure creates it; with nothing routed into it, it must then drop.
	if err := retention.EnsureOutboxPartition(ctx, farFuture); err != nil {
		t.Fatal(err)
	}
	dropped, err = retention.DropOutboxPartitionIfEmpty(ctx, farFuture)
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Fatal("an empty partition with no rows must be dropped")
	}
}

// TestCompetitionRepository_FindMatchesBatchFetchesStageAndBothTeams covers
// the join-based FindMatches/FindUnstartedMatches path directly (the main
// end-to-end test above only ever fetches a single match via FindMatch):
// multiple matches for one event, one of them with a stage set, must all
// come back fully hydrated (teams + stage) in one shot.
func TestCompetitionRepository_FindMatchesBatchFetchesStageAndBothTeams(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	firstTeam := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "t1"}
	secondTeam := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "t2"}
	stage := "Final"
	future := time.Now().Add(time.Hour).UTC()

	streams := []competition.Stream{
		{Language: "ru", URL: "https://www.twitch.tv/betboom_cs_ru3"},
		{Language: "en", URL: "https://kick.com/cct_cs2", Main: true, Official: true},
	}
	notStarted := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m1", Status: competition.MatchNotStarted,
		Format: format, FirstTeam: &firstTeam, SecondTeam: &secondTeam, Stage: &stage, ScheduledAt: &future,
		Streams: streams,
	}
	if _, err := catalog.SaveMatch(ctx, notStarted); err != nil {
		t.Fatal(err)
	}
	finished := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m2", Status: competition.MatchFinished, Format: format,
	}
	if _, err := catalog.SaveMatch(ctx, finished); err != nil {
		t.Fatal(err)
	}

	all, err := catalog.FindMatches(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 matches for the event, got %d", len(all))
	}

	unstarted, err := catalog.FindUnstartedMatches(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unstarted) != 1 || unstarted[0].ID != notStarted.ID {
		t.Fatalf("expected exactly the NOT_STARTED match, got %+v", unstarted)
	}
	got := unstarted[0]
	if got.FirstTeam == nil || got.FirstTeam.Name != "Spirit" || got.SecondTeam == nil || got.SecondTeam.Name != "NAVI" {
		t.Fatalf("expected both teams hydrated, got first=%+v second=%+v", got.FirstTeam, got.SecondTeam)
	}
	if got.Stage == nil || *got.Stage != "Final" {
		t.Fatalf("expected stage hydrated, got %v", got.Stage)
	}
	if stream, ok := got.StreamFor(common.LocaleEN); !ok || stream.URL != "https://kick.com/cct_cs2" {
		t.Fatalf("expected streams round-tripped through the jsonb column, StreamFor(EN) = %q, %v", stream.URL, ok)
	}
}

// TestCompetitionRepository_FindEventsBatchFetchesAndPreservesTier covers
// FindEvents (the N+1-avoidance batch lookup backing the Telegram
// subscribed-events/event-stats menus) and the tier column round trip:
// SaveEvent persists it, FindEvents/FindEvent read it back, and an unknown
// tier stays NULL rather than the empty string being stored literally.
func TestCompetitionRepository_FindEventsBatchFetchesAndPreservesTier(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)

	major := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Major", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE", Tier: competition.TierS}
	minor := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Minor", ExternalID: "e2", Status: competition.EventUpcoming, Provider: "PANDASCORE", Tier: competition.TierC}
	unranked := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Pickup Cup", ExternalID: "e3", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	for _, e := range []competition.Event{major, minor, unranked} {
		if _, err := catalog.SaveEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := catalog.FindEvents(ctx, []common.EventID{major.ID, minor.ID, unranked.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(got), got)
	}
	byID := map[common.EventID]competition.Event{}
	for _, e := range got {
		byID[e.ID] = e
	}
	if byID[major.ID].Tier != competition.TierS {
		t.Fatalf("major tier = %q, want %q", byID[major.ID].Tier, competition.TierS)
	}
	if byID[minor.ID].Tier != competition.TierC {
		t.Fatalf("minor tier = %q, want %q", byID[minor.ID].Tier, competition.TierC)
	}
	if byID[unranked.ID].Tier != competition.TierUnknown {
		t.Fatalf("unranked tier = %q, want empty/unknown", byID[unranked.ID].Tier)
	}

	empty, err := catalog.FindEvents(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("FindEvents(nil) = %+v, %v, want empty/nil", empty, err)
	}
}

// TestCompetitionRepository_FindUnstartedMatchesForEventsBatchesAcrossEvents
// covers the batch lookup backing the Telegram "upcoming" menu: matches
// from two different events must both come back from a single call, and a
// FINISHED match must still be excluded.
func TestCompetitionRepository_FindUnstartedMatchesForEventsBatchesAcrossEvents(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)

	eventA := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "A", ExternalID: "ea", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	eventB := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "B", ExternalID: "eb", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	for _, e := range []competition.Event{eventA, eventB} {
		if _, err := catalog.SaveEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	matchA := competition.Match{ID: common.NewMatchID(), EventID: eventA.ID, ExternalID: "ma", Status: competition.MatchNotStarted, Format: format}
	matchB := competition.Match{ID: common.NewMatchID(), EventID: eventB.ID, ExternalID: "mb", Status: competition.MatchNotStarted, Format: format}
	finishedInA := competition.Match{ID: common.NewMatchID(), EventID: eventA.ID, ExternalID: "mc", Status: competition.MatchFinished, Format: format}
	for _, m := range []competition.Match{matchA, matchB, finishedInA} {
		if _, err := catalog.SaveMatch(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	got, err := catalog.FindUnstartedMatchesForEvents(ctx, []common.EventID{eventA.ID, eventB.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 not-started matches across both events, got %d: %+v", len(got), got)
	}
	ids := map[common.MatchID]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	if !ids[matchA.ID] || !ids[matchB.ID] {
		t.Fatalf("expected matchA and matchB, got %+v", got)
	}

	empty, err := catalog.FindUnstartedMatchesForEvents(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("FindUnstartedMatchesForEvents(nil) = %+v, %v, want empty/nil", empty, err)
	}
}

// A match does not stop existing when it kicks off. Between the poll
// closing and the result landing it is the most interesting thing on the
// schedule, and the screen that lists what is on has to keep showing it —
// while the poll pipeline, which reads the narrower set, must not start
// offering votes on a match already in play.
func TestCompetitionRepository_PlayableMatchesKeepTheOnesInPlay(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Live",
		ExternalID: "e-live", Status: competition.EventRunning, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().UTC().Add(time.Hour)
	started := time.Now().UTC().Add(-time.Hour)
	upcoming := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m-soon",
		Status: competition.MatchNotStarted, Format: format, ScheduledAt: &soon}
	running := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m-running",
		Status: competition.MatchRunning, Format: format, ScheduledAt: &started, ActualStartedAt: &started}
	finished := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m-done",
		Status: competition.MatchFinished, Format: format, ScheduledAt: &started, ActualStartedAt: &started}
	for _, m := range []competition.Match{upcoming, running, finished} {
		if _, err := catalog.SaveMatch(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	playable, err := catalog.FindPlayableMatchesForEvents(ctx, []common.EventID{event.ID})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[common.MatchID]bool{}
	for _, m := range playable {
		seen[m.ID] = true
	}
	if !seen[upcoming.ID] || !seen[running.ID] {
		t.Fatalf("expected the upcoming and the running match, got %+v", playable)
	}
	if seen[finished.ID] {
		t.Fatal("a finished match is history, not something still on")
	}

	// The poll pipeline's own view stays exactly as narrow as it was.
	unstarted, err := catalog.FindUnstartedMatchesForEvents(ctx, []common.EventID{event.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(unstarted) != 1 || unstarted[0].ID != upcoming.ID {
		t.Fatalf("polls would now be offered on a match in play: %+v", unstarted)
	}
}

// TestPredictionRepository_OpenPollsDueBatchFetchesOptionsPerPoll covers the
// batch poll-fetch path (queryPolls, backing OpenPollsDue/OpenPollsForMatch/
// PollsForMatch): two due polls with DIFFERENT option sets must each get
// back exactly their own options, not a mix-up from the ANY($1) batch join.
func TestPredictionRepository_OpenPollsDueBatchFetchesOptionsPerPoll(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)

	chatID := common.ChatID{Value: -999}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "E", ExternalID: "e1", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	bo3, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	bo5, _ := competition.NewSeriesFormat(competition.BestOf, 5)
	now := time.Now().UTC()

	makePoll := func(externalID string, format competition.SeriesFormat) prediction.Poll {
		match := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: externalID, Status: competition.MatchNotStarted, Format: format}
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		var options []prediction.Option
		for i, s := range format.PossibleScores() {
			options = append(options, prediction.Option{Index: i, Score: s})
		}
		poll := prediction.Poll{ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID, Options: options, Status: prediction.PollOpen, ClosesAt: now}
		if _, err := predictions.SavePoll(ctx, poll); err != nil {
			t.Fatal(err)
		}
		return poll
	}

	poll1 := makePoll("m1", bo3) // 4 options
	poll2 := makePoll("m2", bo5) // 6 options

	due, err := predictions.OpenPollsDue(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("expected 2 due polls, got %d", len(due))
	}

	byID := map[string][]prediction.Option{}
	for _, p := range due {
		byID[p.ID.Value.String()] = p.Options
	}
	if len(byID[poll1.ID.Value.String()]) != 4 {
		t.Fatalf("poll1 (BO3) expected 4 options, got %d", len(byID[poll1.ID.Value.String()]))
	}
	if len(byID[poll2.ID.Value.String()]) != 6 {
		t.Fatalf("poll2 (BO5) expected 6 options, got %d", len(byID[poll2.ID.Value.String()]))
	}
}

// TestPredictionRepository_PollTeamAnchorRoundTripsAndIsWriteOnce covers the
// fix for a real, repeated production incident: a poll's FirstTeamID/
// SecondTeamID must survive exactly as saved through every read path
// (FindPoll and the batch OpenPollsDue path both), and must never change on
// a later SavePoll (e.g. the one that fills in TelegramMessageID right
// after sending) — the whole point of the anchor is that it stays fixed
// even if the match's own team order drifts afterward.
func TestPredictionRepository_PollTeamAnchorRoundTripsAndIsWriteOnce(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)

	chatID := common.ChatID{Value: -998}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Anchor Cup", ExternalID: "anchor-event", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	g2 := competition.Team{ID: common.NewTeamID(), Name: "G2", ExternalID: "anchor-team-g2"}
	betboom := competition.Team{ID: common.NewTeamID(), Name: "BetBoom Team", ExternalID: "anchor-team-betboom"}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	now := time.Now().UTC()
	match := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "anchor-match", Status: competition.MatchNotStarted,
		Format: format, FirstTeam: &g2, SecondTeam: &betboom,
	}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}

	poll := prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID, Status: prediction.PollOpen, ClosesAt: now,
		FirstTeamID: g2.ID, SecondTeamID: betboom.ID,
		Options: []prediction.Option{{Index: 0, Score: competition.MatchScore{First: 2, Second: 0}}},
	}
	saved, err := predictions.SavePoll(ctx, poll)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FirstTeamID != g2.ID || saved.SecondTeamID != betboom.ID {
		t.Fatalf("SavePoll's own return value lost the anchor: %+v", saved)
	}

	found, err := predictions.FindPoll(ctx, poll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.FirstTeamID != g2.ID || found.SecondTeamID != betboom.ID {
		t.Fatalf("FindPoll did not round-trip the anchor: %+v", found)
	}

	due, err := predictions.OpenPollsDue(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].FirstTeamID != g2.ID || due[0].SecondTeamID != betboom.ID {
		t.Fatalf("OpenPollsDue's batch path did not round-trip the anchor: %+v", due)
	}

	// Simulate the real "fill in TelegramMessageID after sending" save —
	// a different team anchor passed here must be silently ignored, not
	// overwrite the original.
	messageID := int64(123)
	other := common.NewTeamID()
	resent := found
	resent.TelegramMessageID = &messageID
	resent.FirstTeamID = other
	if _, err := predictions.SavePoll(ctx, *resent); err != nil {
		t.Fatal(err)
	}
	after, err := predictions.FindPoll(ctx, poll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.FirstTeamID != g2.ID {
		t.Fatalf("a later SavePoll must not overwrite the original anchor, got %+v", after.FirstTeamID)
	}
	if after.TelegramMessageID == nil || *after.TelegramMessageID != messageID {
		t.Fatalf("expected the later save's TelegramMessageID to still take effect, got %+v", after.TelegramMessageID)
	}
}

// The announced broadcast has to survive a round trip through the real
// schema: it is the only thing standing between a retried poll closure and
// the same stream link being posted to a chat twice.
func TestPredictionRepository_AnnouncedStreamRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)

	chatID := common.ChatID{Value: -997}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Stream Cup", ExternalID: "stream-event", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	match := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "stream-match",
		Status: competition.MatchNotStarted, Format: format,
	}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}
	poll := prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID, Status: prediction.PollOpen,
		ClosesAt: time.Now().UTC(),
		Options:  []prediction.Option{{Index: 0, Score: competition.MatchScore{First: 2, Second: 0}}},
	}
	if _, err := predictions.SavePoll(ctx, poll); err != nil {
		t.Fatal(err)
	}

	fresh, err := predictions.FindPoll(ctx, poll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.StreamURL != "" {
		t.Fatalf("a poll starts with no announced broadcast, got %q", fresh.StreamURL)
	}
	const url = "https://twitch.tv/major_ru"
	if err := predictions.MarkPollStreamAnnounced(ctx, poll.ID, url); err != nil {
		t.Fatal(err)
	}
	marked, err := predictions.FindPoll(ctx, poll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if marked.StreamURL != url {
		t.Fatalf("StreamURL = %q, want the announced link", marked.StreamURL)
	}
	// The save that follows a closure must not erase it.
	if _, err := predictions.SavePoll(ctx, *marked); err != nil {
		t.Fatal(err)
	}
	after, err := predictions.FindPoll(ctx, poll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.StreamURL != url {
		t.Fatalf("a later SavePoll dropped the announced link: %q", after.StreamURL)
	}
}

// Auto-subscription lives on the chat's row for one game, so it has to
// survive both a reload and the chat turning other games on and off.
func TestChatRepository_AutoSubscribeIsPerGame(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)

	chatID := common.ChatID{Value: -996}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(ctx, chatID, []competition.GameCode{competition.GameCS2, competition.GameDota2}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetAutoSubscribeGame(ctx, chatID, competition.GameCS2, true); err != nil {
		t.Fatal(err)
	}

	found, err := chats.Find(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if !found.AutoSubscribesTo(competition.GameCS2) || found.AutoSubscribesTo(competition.GameDota2) {
		t.Fatalf("expected CS2 only, got %v", found.AutoSubscribeGames)
	}

	// A game switched off and back on starts from the default again —
	// re-enabling a game must not silently resurrect a choice made before
	// the chat stopped following it.
	if err := chats.SetEnabledGames(ctx, chatID, []competition.GameCode{competition.GameDota2}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(ctx, chatID, []competition.GameCode{competition.GameCS2, competition.GameDota2}); err != nil {
		t.Fatal(err)
	}
	after, err := chats.Find(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.AutoSubscribeGames) != 0 {
		t.Fatalf("expected the flag to be back at its default, got %v", after.AutoSubscribeGames)
	}
}

func TestRetentionRepository_PrunesOnlyEligibleRows(t *testing.T) {
	pool, ctx := newTestPool(t)
	retention := pg.NewRetentionRepository(pool)
	dedup := pg.NewUpdateDeduplicator(pool)
	outbox := pg.NewOutbox(pool)

	// Two dedup rows: one aged past the cutoff, one fresh.
	if _, err := dedup.Claim(ctx, 1001); err != nil {
		t.Fatal(err)
	}
	if _, err := dedup.Claim(ctx, 1002); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE processed_telegram_update SET processed_at = now() - interval '10 days' WHERE update_id = $1`, 1001); err != nil {
		t.Fatal(err)
	}

	deleted, err := retention.DeleteProcessedUpdatesBefore(ctx, time.Now().Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d dedup rows, want exactly the aged one", deleted)
	}
	// The fresh one must survive: it still has a retry to suppress.
	claimed, err := dedup.Claim(ctx, 1002)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("the fresh dedup row was deleted — a retried update would now be processed twice")
	}

	// An unpublished outbox row is never eligible, however old.
	if _, err := outbox.Enqueue(ctx, "chat", "-1", "telegram.match-result", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_event SET occurred_at = now() - interval '90 days'`); err != nil {
		t.Fatal(err)
	}
	deletedOutbox, err := retention.DeletePublishedOutboxBefore(ctx, time.Now().Add(-14*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deletedOutbox != 0 {
		t.Fatalf("deleted %d unpublished outbox rows, want 0 — those are still owed to someone", deletedOutbox)
	}

	// Once published and aged, it goes.
	if _, err := pool.Exec(ctx, `UPDATE outbox_event SET published_at = now() - interval '90 days'`); err != nil {
		t.Fatal(err)
	}
	deletedOutbox, err = retention.DeletePublishedOutboxBefore(ctx, time.Now().Add(-14*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deletedOutbox != 1 {
		t.Fatalf("deleted %d published outbox rows, want 1", deletedOutbox)
	}
}

// TestRetentionRepository_RowCountCapKeepsOnlyTheNewestRows covers the
// row-count backstop added alongside the TTL sweep: it must keep exactly
// the newest maxRows rows regardless of how young the deleted ones are —
// unlike the TTL delete, age plays no part in which rows survive.
func TestRetentionRepository_RowCountCapKeepsOnlyTheNewestRows(t *testing.T) {
	pool, ctx := newTestPool(t)
	retention := pg.NewRetentionRepository(pool)
	dedup := pg.NewUpdateDeduplicator(pool)
	outbox := pg.NewOutbox(pool)

	// Five dedup rows, all fresh (well within any TTL), timestamped an hour
	// apart so their relative order is unambiguous.
	for i := int64(1); i <= 5; i++ {
		if _, err := dedup.Claim(ctx, 2000+i); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE processed_telegram_update SET processed_at = now() - ($2 * interval '1 hour') WHERE update_id = $1`,
			2000+i, 5-i); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := retention.DeleteProcessedUpdatesExceeding(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted %d rows, want exactly the 2 oldest beyond the cap of 3", deleted)
	}
	// The 3 newest (2003, 2004, 2005) must survive; the 2 oldest (2001, 2002)
	// must not — re-claiming an id that survived must report "already seen".
	for id, wantClaimed := range map[int64]bool{2001: true, 2002: true, 2003: false, 2004: false, 2005: false} {
		claimed, err := dedup.Claim(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if claimed != wantClaimed {
			t.Fatalf("update %d: claimed=%v, want %v", id, claimed, wantClaimed)
		}
	}

	// maxRows <= 0 is the "keep everything" escape hatch, not "cap at zero".
	if _, err := dedup.Claim(ctx, 3001); err != nil {
		t.Fatal(err)
	}
	deleted, err = retention.DeleteProcessedUpdatesExceeding(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("deleted %d rows with maxRows<=0, want 0 (cap disabled)", deleted)
	}

	// Same behavior for published outbox rows: only unpublished-never-
	// touched and published-beyond-the-cap distinctions matter, not age.
	var ids []uuid.UUID
	for i := 0; i < 4; i++ {
		id, err := outbox.Enqueue(ctx, "chat", fmt.Sprintf("-%d", i+1), "telegram.match-result", "{}")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for i, id := range ids {
		if _, err := pool.Exec(ctx,
			`UPDATE outbox_event SET published_at = now() - ($2 * interval '1 hour') WHERE id = $1`,
			id, len(ids)-i); err != nil {
			t.Fatal(err)
		}
	}
	deletedOutbox, err := retention.DeletePublishedOutboxExceeding(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if deletedOutbox != 2 {
		t.Fatalf("deleted %d published outbox rows, want exactly the 2 oldest beyond the cap of 2", deletedOutbox)
	}
}

func TestAdminActionRepository_ReturnsNewestFirstPerChat(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	actions := pg.NewAdminActionRepository(pool)

	chatA := common.ChatID{Value: -8001}
	chatB := common.ChatID{Value: -8002}
	for id, title := range map[common.ChatID]string{chatA: "Chat A", chatB: "Chat B"} {
		if _, err := chats.Save(ctx, chat.Settings{ChatID: id, Title: title, Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
			t.Fatal(err)
		}
	}

	for _, a := range []chat.AdminAction{
		{ChatID: chatA, ActorID: common.UserID{Value: 1}, ActorName: "First", Kind: "locale", Detail: "en"},
		{ChatID: chatA, ActorID: common.UserID{Value: 2}, ActorName: "Second", Kind: "timezone", Detail: "Europe/Berlin"},
		{ChatID: chatB, ActorID: common.UserID{Value: 3}, ActorName: "Elsewhere", Kind: "subscribe", Detail: "Major"},
	} {
		if err := actions.Record(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	recent, err := actions.Recent(ctx, chatA, chat.AdminActionHistorySize)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("got %d actions for chat A, want 2 (chat B's must not leak in)", len(recent))
	}
	if recent[0].Kind != "timezone" || recent[0].ActorName != "Second" {
		t.Fatalf("newest = %q by %q, want timezone by Second", recent[0].Kind, recent[0].ActorName)
	}
	if recent[0].CreatedAt.IsZero() {
		t.Fatal("CreatedAt was not read back — the history screen has nothing to date entries by")
	}

	// The limit is what bounds the screen; 0 must not be read as "no limit".
	if capped, err := actions.Recent(ctx, chatA, 1); err != nil || len(capped) != 1 {
		t.Fatalf("Recent(limit=1) = %d rows, %v; want 1 row", len(capped), err)
	}
	if none, err := actions.Recent(ctx, chatA, 0); err != nil || len(none) != 0 {
		t.Fatalf("Recent(limit=0) = %d rows, %v; want none", len(none), err)
	}
}

// UserPredictions is the only query that reads votes individually rather
// than as an aggregate, and the team it reports is derived from the
// scoreline the person picked — worth pinning down against real SQL.
func TestScoringRepository_UserPredictionsReportsTheTeamBackedAndWhetherItWon(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	chatID := common.ChatID{Value: -100789}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Insights chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{
		ID: common.NewEventID(), Game: competition.GameCS2, Name: "Insights Cup", ExternalID: "insights-event",
		Status: competition.EventRunning, StartsAt: &now, Provider: "PANDASCORE",
	}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	options := make([]prediction.Option, 0)
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "insights-team-1"}
	navi := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "insights-team-2"}
	voter := common.UserID{Value: 77}

	// Two matches with the same two teams: the voter backs the first team
	// in both, and is right once.
	type seeded struct {
		playedAt time.Time
		score    competition.MatchScore
		external string
		message  int64
	}
	for i, s := range []seeded{
		{now.Add(-24 * time.Hour), competition.MatchScore{First: 2, Second: 0}, "insights-match-1", 21},
		{now.Add(-48 * time.Hour), competition.MatchScore{First: 0, Second: 2}, "insights-match-2", 22},
	} {
		playedAt, score := s.playedAt, s.score
		match := competition.Match{
			ID: common.NewMatchID(), EventID: event.ID, ExternalID: s.external,
			FirstTeam: &spirit, SecondTeam: &navi,
			ScheduledAt: &playedAt, ActualStartedAt: &playedAt, Status: competition.MatchFinished,
			Format: format, Score: &score,
		}
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		telegramPollID := fmt.Sprintf("insights-poll-%d", i)
		messageID := s.message
		poll := prediction.Poll{
			ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID,
			TelegramPollID: &telegramPollID, TelegramMessageID: &messageID,
			Options: options, Status: prediction.PollClosed, ClosesAt: playedAt,
		}
		if _, err := predictions.SavePoll(ctx, poll); err != nil {
			t.Fatal(err)
		}
		// Option 0 is 2:0 — a win for the first team, Spirit.
		if err := predictions.SaveVote(ctx, prediction.Vote{
			PollID: poll.ID, UserID: voter, OptionIndex: 0, DisplayName: "Alex", VotedAt: playedAt.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := scoringRepo.UserPredictions(ctx, voter, scoring.InsightsMaxPredictions)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d predictions, want 2: %+v", len(got), got)
	}
	// Newest first.
	if !got[0].PlayedAt.After(got[1].PlayedAt) {
		t.Fatalf("predictions are not newest-first: %+v", got)
	}
	for _, p := range got {
		if p.TeamName != "Spirit" {
			t.Fatalf("team = %q, want the team whose win was predicted (Spirit)", p.TeamName)
		}
		if p.TeamID != spirit.ID {
			t.Fatalf("teamID = %v, want Spirit's real id %v — grouping in teamAccuracy relies on this being a real, stable identity", p.TeamID, spirit.ID)
		}
	}
	if !got[0].Correct || got[1].Correct {
		t.Fatalf("correctness = %v/%v, want the 2:0 win right and the 0:2 loss wrong", got[0].Correct, got[1].Correct)
	}

	// The insights the screen shows are built from exactly these rows.
	insights := scoring.BuildPersonalInsights(got, now)
	if insights.CurrentStreak != 1 || insights.LongestStreak != 1 {
		t.Fatalf("streaks = %d/%d, want 1/1", insights.CurrentStreak, insights.LongestStreak)
	}
}

// UserBets backs the "my bets" DM screen: it must report the exact
// predicted scoreline (not just who was picked to win), the exact actual
// scoreline, the points a settled award earned, and — the point of the
// chatID filter — narrow to one chat without touching the other.
func TestScoringRepository_UserBetsReportsScorelinesAndFiltersByChat(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	chatA := common.ChatID{Value: -200781}
	chatB := common.ChatID{Value: -200782}
	for _, c := range []common.ChatID{chatA, chatB} {
		if _, err := chats.Save(ctx, chat.Settings{ChatID: c, Title: fmt.Sprintf("Bets chat %d", c.Value), Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	event := competition.Event{
		ID: common.NewEventID(), Game: competition.GameCS2, Name: "Bets Cup", ExternalID: "bets-event",
		Status: competition.EventRunning, StartsAt: &now, Provider: "PANDASCORE",
	}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	options := make([]prediction.Option, 0)
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "bets-team-1"}
	navi := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "bets-team-2"}
	voter := common.UserID{Value: 78}

	seedBet := func(c common.ChatID, external string, messageID int64, playedAt time.Time, actual competition.MatchScore, predictedOption int, points int) common.PollID {
		match := competition.Match{
			ID: common.NewMatchID(), EventID: event.ID, ExternalID: external,
			FirstTeam: &spirit, SecondTeam: &navi,
			ScheduledAt: &playedAt, ActualStartedAt: &playedAt, Status: competition.MatchFinished,
			Format: format, Score: &actual,
		}
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		telegramPollID := "bets-poll-" + external
		poll := prediction.Poll{
			ID: common.NewPollID(), ChatID: c, MatchID: match.ID,
			TelegramPollID: &telegramPollID, TelegramMessageID: &messageID,
			Options: options, Status: prediction.PollClosed, ClosesAt: playedAt,
		}
		saved, err := predictions.SavePoll(ctx, poll)
		if err != nil {
			t.Fatal(err)
		}
		if err := predictions.SaveVote(ctx, prediction.Vote{
			PollID: saved.ID, UserID: voter, OptionIndex: predictedOption, DisplayName: "Alex", VotedAt: playedAt.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		if points > 0 {
			if err := scoringRepo.ReplaceAwards(ctx, saved.ID, []scoring.Award{{
				PollID: saved.ID, UserID: voter,
				Points: points, Kind: scoring.AwardExactScore, AwardedAt: playedAt,
			}}); err != nil {
				t.Fatal(err)
			}
		}
		return saved.ID
	}

	// Chat A: predicted 2:0, actual 2:0 — an exact, awarded hit.
	seedBet(chatA, "bets-match-a", 31, now.Add(-time.Hour), competition.MatchScore{First: 2, Second: 0}, 0, 3)
	// Chat B: predicted 2:1, actual 0:2 — a miss, no award.
	seedBet(chatB, "bets-match-b", 32, now.Add(-2*time.Hour), competition.MatchScore{First: 0, Second: 2}, optionIndexFor(options, competition.MatchScore{First: 2, Second: 1}), 0)

	all, err := scoringRepo.UserBets(ctx, voter, nil, scoring.UserBetsMaxRows)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d bets across both chats, want 2: %+v", len(all), all)
	}
	if !all[0].PlayedAt.After(all[1].PlayedAt) {
		t.Fatalf("bets are not newest-first: %+v", all)
	}

	onlyA, err := scoringRepo.UserBets(ctx, voter, &chatA, scoring.UserBetsMaxRows)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyA) != 1 || onlyA[0].ChatID != chatA {
		t.Fatalf("chat filter leaked or dropped rows, got %+v", onlyA)
	}
	bet := onlyA[0]
	if bet.PredictedScore != (competition.MatchScore{First: 2, Second: 0}) || bet.ActualScore != (competition.MatchScore{First: 2, Second: 0}) {
		t.Fatalf("scorelines = predicted %v actual %v, want 2:0/2:0", bet.PredictedScore, bet.ActualScore)
	}
	if !bet.Correct || bet.Points != 3 {
		t.Fatalf("correct/points = %v/%d, want true/3", bet.Correct, bet.Points)
	}
	if bet.FirstTeamName != "Spirit" || bet.SecondTeamName != "NAVI" {
		t.Fatalf("team names = %q/%q, want Spirit/NAVI", bet.FirstTeamName, bet.SecondTeamName)
	}

	onlyB, err := scoringRepo.UserBets(ctx, voter, &chatB, scoring.UserBetsMaxRows)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyB) != 1 {
		t.Fatalf("got %d bets for chat B, want 1: %+v", len(onlyB), onlyB)
	}
	if onlyB[0].Correct || onlyB[0].Points != 0 {
		t.Fatalf("chat B's bet correct/points = %v/%d, want false/0", onlyB[0].Correct, onlyB[0].Points)
	}
}

// TestScoringRepository_UserBetsForEventFiltersByTournamentAndOrdersOldestFirst
// covers the tournament-results table: two events in the same chat must not
// leak into each other, and — unlike UserBets' newest-first "recent
// activity" ordering — a tournament reads chronologically, oldest match
// first.
func TestScoringRepository_UserBetsForEventFiltersByTournamentAndOrdersOldestFirst(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	chatID := common.ChatID{Value: -200790}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Event bets chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	eventA := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Cup A", ExternalID: "evbets-event-a", Status: competition.EventRunning, StartsAt: &now, Provider: "PANDASCORE"}
	eventB := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Cup B", ExternalID: "evbets-event-b", Status: competition.EventRunning, StartsAt: &now, Provider: "PANDASCORE"}
	for _, e := range []competition.Event{eventA, eventB} {
		if _, err := catalog.SaveEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	options := make([]prediction.Option, 0)
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "evbets-team-1"}
	navi := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "evbets-team-2"}
	voter := common.UserID{Value: 78}

	seed := func(eventID common.EventID, external string, messageID int64, playedAt time.Time) {
		actual := competition.MatchScore{First: 2, Second: 0}
		match := competition.Match{
			ID: common.NewMatchID(), EventID: eventID, ExternalID: external,
			FirstTeam: &spirit, SecondTeam: &navi,
			ScheduledAt: &playedAt, ActualStartedAt: &playedAt, Status: competition.MatchFinished,
			Format: format, Score: &actual,
		}
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		telegramPollID := "evbets-poll-" + external
		poll := prediction.Poll{
			ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID,
			TelegramPollID: &telegramPollID, TelegramMessageID: &messageID,
			Options: options, Status: prediction.PollClosed, ClosesAt: playedAt,
		}
		saved, err := predictions.SavePoll(ctx, poll)
		if err != nil {
			t.Fatal(err)
		}
		if err := predictions.SaveVote(ctx, prediction.Vote{
			PollID: saved.ID, UserID: voter, OptionIndex: 0, DisplayName: "Alex", VotedAt: playedAt.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Two matches in event A (oldest first when read back), one in event B.
	seed(eventA.ID, "evbets-match-a1", 41, now.Add(-2*time.Hour))
	seed(eventA.ID, "evbets-match-a2", 42, now.Add(-time.Hour))
	seed(eventB.ID, "evbets-match-b1", 43, now.Add(-3*time.Hour))

	got, err := scoringRepo.UserBetsForEvent(ctx, voter, chatID, eventA.ID, scoring.UserBetsMaxRows)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d bets for event A, want 2 (event B's match must not leak in): %+v", len(got), got)
	}
	if !got[0].PlayedAt.Before(got[1].PlayedAt) {
		t.Fatalf("event bets are not oldest-first: %+v", got)
	}
}

// TestScoringRepository_PointsProgressionOrdersEventsAndSumsPerUser is the
// raw material a rating chart cumulates client-side: one row per settled
// prediction, oldest first, so two participants' interleaved match
// histories come back correctly ordered and separable by user id.
func TestScoringRepository_PointsProgressionOrdersEventsAndSumsPerUser(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	chatID := common.ChatID{Value: -200795}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Progression chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Progression Cup", ExternalID: "progression-event", Status: competition.EventRunning, StartsAt: &now, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	options := make([]prediction.Option, 0)
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	spirit := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "progression-team-1"}
	navi := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "progression-team-2"}
	alex := common.UserID{Value: 81}
	sam := common.UserID{Value: 82}

	seed := func(external string, messageID int64, playedAt time.Time, actual competition.MatchScore, alexOption, samOption, alexPoints, samPoints int) {
		match := competition.Match{
			ID: common.NewMatchID(), EventID: event.ID, ExternalID: external,
			FirstTeam: &spirit, SecondTeam: &navi,
			ScheduledAt: &playedAt, ActualStartedAt: &playedAt, Status: competition.MatchFinished,
			Format: format, Score: &actual,
		}
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		telegramPollID := "progression-poll-" + external
		poll := prediction.Poll{
			ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID,
			TelegramPollID: &telegramPollID, TelegramMessageID: &messageID,
			Options: options, Status: prediction.PollClosed, ClosesAt: playedAt,
		}
		saved, err := predictions.SavePoll(ctx, poll)
		if err != nil {
			t.Fatal(err)
		}
		if err := predictions.SaveVote(ctx, prediction.Vote{PollID: saved.ID, UserID: alex, OptionIndex: alexOption, DisplayName: "Alex", VotedAt: playedAt.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := predictions.SaveVote(ctx, prediction.Vote{PollID: saved.ID, UserID: sam, OptionIndex: samOption, DisplayName: "Sam", VotedAt: playedAt.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
		var awards []scoring.Award
		if alexPoints > 0 {
			awards = append(awards, scoring.Award{PollID: saved.ID, UserID: alex, Points: alexPoints, Kind: scoring.AwardExactScore, AwardedAt: playedAt})
		}
		if samPoints > 0 {
			awards = append(awards, scoring.Award{PollID: saved.ID, UserID: sam, Points: samPoints, Kind: scoring.AwardExactScore, AwardedAt: playedAt})
		}
		if len(awards) > 0 {
			if err := scoringRepo.ReplaceAwards(ctx, saved.ID, awards); err != nil {
				t.Fatal(err)
			}
		}
	}

	exact := optionIndexFor(options, competition.MatchScore{First: 2, Second: 0})
	wrong := optionIndexFor(options, competition.MatchScore{First: 0, Second: 2})
	// Match 1 (earliest): Alex predicts right (+3), Sam wrong (+0).
	seed("progression-match-1", 51, now.Add(-2*time.Hour), competition.MatchScore{First: 2, Second: 0}, exact, wrong, 3, 0)
	// Match 2 (latest): reversed — Sam catches up, Alex doesn't score.
	seed("progression-match-2", 52, now.Add(-time.Hour), competition.MatchScore{First: 0, Second: 2}, wrong, exact, 0, 3)

	rows, err := scoringRepo.PointsProgression(ctx, chatID, scoring.AllTime())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d progression rows, want 4 (2 matches x 2 users): %+v", len(rows), rows)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].PlayedAt.Before(rows[i-1].PlayedAt) {
			t.Fatalf("progression rows are not oldest-first: %+v", rows)
		}
	}
	var alexTotal, samTotal int
	for _, r := range rows {
		switch r.UserID {
		case alex:
			alexTotal += r.Points
		case sam:
			samTotal += r.Points
		default:
			t.Fatalf("unexpected user id in progression row: %+v", r)
		}
	}
	if alexTotal != 3 || samTotal != 3 {
		t.Fatalf("alexTotal=%d samTotal=%d, want 3/3 (each won exactly one match)", alexTotal, samTotal)
	}
}

// optionIndexFor finds the poll option index matching score s, so the test
// can vote for a specific predicted scoreline by name instead of a bare
// index into format.PossibleScores()'s own ordering.
func optionIndexFor(options []prediction.Option, s competition.MatchScore) int {
	for _, o := range options {
		if o.Score == s {
			return o.Index
		}
	}
	panic(fmt.Sprintf("no option for score %v", s))
}

// Both nudges are gated on the same two conditions, and getting either
// wrong means messaging someone who never asked or repeatedly failing to
// reach someone who left.
func TestChatRepository_NotificationRecipientsNeedBothOptInAndReachability(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)

	optedInReachable := common.UserID{Value: 9001}
	optedInUnreachable := common.UserID{Value: 9002}
	reachableNoOptIn := common.UserID{Value: 9003}
	otherKindOnly := common.UserID{Value: 9004}

	for _, id := range []common.UserID{optedInReachable, reachableNoOptIn, otherKindOnly} {
		if err := chats.SetDMReachable(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []common.UserID{optedInReachable, optedInUnreachable} {
		if err := chats.SetNotifyEnabled(ctx, common.ScopeUser, id.Value, string(common.NotifyResultRecaps), true); err != nil {
			t.Fatal(err)
		}
	}
	// Opted into the other kind only: must not receive recaps.
	if err := chats.SetNotifyEnabled(ctx, common.ScopeUser, otherKindOnly.Value, string(common.NotifyPollReminders), true); err != nil {
		t.Fatal(err)
	}

	got, err := chats.Recipients(ctx, common.NotifyResultRecaps,
		[]common.UserID{optedInReachable, optedInUnreachable, reachableNoOptIn, otherKindOnly})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != optedInReachable {
		t.Fatalf("recipients = %v, want only the opted-in reachable user %v", got, optedInReachable)
	}

	// The kinds are independent switches.
	prefs, err := chats.NotifySettings(ctx, common.ScopeUser, otherKindOnly.Value)
	if err != nil {
		t.Fatal(err)
	}
	if prefs[string(common.NotifyResultRecaps)] || !prefs[string(common.NotifyPollReminders)] {
		t.Fatalf("prefs = %+v, want reminders on and recaps off", prefs)
	}

	// An unknown kind must be an error, never a query.
	if _, err := chats.Recipients(ctx, common.NotificationKind("everything"), []common.UserID{optedInReachable}); err == nil {
		t.Fatal("an unknown notification kind was accepted")
	}
}

// pgxTxCloser lets a test hold a transaction open across goroutines and
// release it (rolling back — nothing in these tests needs the write kept)
// from more than one place (an explicit release plus a deferred safety net)
// without a second Rollback call's "already closed" error needing handling.
type pgxTxCloser struct{ tx pgx.Tx }

func (c pgxTxCloser) rollback(ctx context.Context) { _ = c.tx.Rollback(ctx) }

// LockEventCompletion is app.EventCompletionService.completeForChat's guard
// against two different scheduled jobs (DiscoverEvents and
// SynchronizeMatches) both reaching event completion for the same
// (chat, event) pair around the same time: a second transaction attempting
// the same pair must block until the first transaction holding it commits
// or rolls back, while two DIFFERENT pairs must not serialize against each
// other at all (or every unrelated event completing in the same chat would
// contend needlessly).
func TestScoringRepository_LockEventCompletionSerializesOnlyTheSamePair(t *testing.T) {
	pool, ctx := newTestPool(t)
	scoringRepo := pg.NewScoringRepository(pool)

	chatID := common.ChatID{Value: -300901}
	eventA, eventB := common.NewEventID(), common.NewEventID()

	// beginLocked starts a transaction, acquires the lock for (chatID, id)
	// inside it, and returns the still-open transaction — the caller
	// decides when to commit, simulating "this job's completion pass is
	// still in progress".
	beginLocked := func(t *testing.T, id common.EventID) pgxTxCloser {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin tx: %v", err)
		}
		if err := scoringRepo.LockEventCompletion(pg.WithTx(ctx, tx), chatID, id); err != nil {
			t.Fatalf("lock: %v", err)
		}
		return pgxTxCloser{tx}
	}

	t.Run("same pair blocks until released", func(t *testing.T) {
		first := beginLocked(t, eventA)
		defer first.rollback(ctx)

		acquired := make(chan error, 1)
		go func() {
			tx, err := pool.Begin(ctx)
			if err != nil {
				acquired <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			acquired <- scoringRepo.LockEventCompletion(pg.WithTx(ctx, tx), chatID, eventA)
		}()

		select {
		case err := <-acquired:
			t.Fatalf("second lock attempt on the same pair acquired immediately (err=%v), want it blocked", err)
		case <-time.After(300 * time.Millisecond):
			// Still blocked, as expected.
		}

		first.rollback(ctx) // releases the advisory lock

		select {
		case err := <-acquired:
			if err != nil {
				t.Fatalf("second lock attempt failed after release: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("second lock attempt never unblocked after the first transaction ended")
		}
	})

	t.Run("different pair does not block", func(t *testing.T) {
		first := beginLocked(t, eventA)
		defer first.rollback(ctx)

		done := make(chan error, 1)
		go func() {
			tx, err := pool.Begin(ctx)
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			done <- scoringRepo.LockEventCompletion(pg.WithTx(ctx, tx), chatID, eventB)
		}()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("lock on a different event id failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a different (chat, event) pair must not be blocked by an unrelated lock")
		}
	})
}

// EnrichmentRepository's team-match-request lifecycle end to end: the
// snapshot cache, creating a request with an initial candidate, a crowd
// response nudging that candidate's score and re-electing the request's
// best candidate, the per-user ask/opt-out bookkeeping, and finally
// resolving the request.
func TestEnrichmentRepository_TeamMatchRequestLifecycle(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	chats := pg.NewChatRepository(pool)
	repo := pg.NewEnrichmentRepository(pool)

	// Seed two local teams the normal way (via a match), and one Telegram
	// user (via any upsert path — SetUserLocale is the simplest).
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "TM Cup", ExternalID: "tm-event", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	teamA := competition.Team{ID: common.NewTeamID(), Name: "Avangarr", ExternalID: "tm-team-a"}
	teamB := competition.Team{ID: common.NewTeamID(), Name: "Avangaar", ExternalID: "tm-team-b"}
	match := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "tm-match-1",
		FirstTeam: &teamA, SecondTeam: &teamB, Status: competition.MatchNotStarted, Format: format,
	}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}
	helper := common.UserID{Value: 90001}
	if err := chats.SetUserLocale(ctx, helper, common.LocaleRU); err != nil {
		t.Fatal(err)
	}

	// Snapshot cache.
	rank := 12
	if err := repo.SaveSnapshot(ctx, []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Avangar"}, GlobalRank: &rank, Source: enrichment.SourceValveVRS},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.AllSnapshot(ctx, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 || snapshot[0].Identity.Name != "Avangar" || *snapshot[0].GlobalRank != 12 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}

	// Create a request with one candidate.
	req := enrichment.TeamMatchRequest{
		ID: common.NewRequestID(), ExternalName: "Avangar", Source: enrichment.SourceValveVRS,
		Status: enrichment.TeamMatchPending, BestTeamID: &teamA.ID, BestScore: 70, CreatedAt: time.Now().UTC(),
	}
	candidate := enrichment.TeamMatchCandidate{TeamID: teamA.ID, Score: 70, Kind: enrichment.CandidateKindFuzzy}
	if err := repo.CreateRequest(ctx, req, []enrichment.TeamMatchCandidate{candidate}); err != nil {
		t.Fatal(err)
	}

	if existing, err := repo.FindPendingByExternalName(ctx, enrichment.SourceValveVRS, "Avangar"); err != nil {
		t.Fatal(err)
	} else if existing == nil || existing.ID.String() != req.ID.String() {
		t.Fatalf("expected to find the just-created pending request, got %+v", existing)
	}
	if pending, err := repo.ListPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if len(pending) != 1 {
		t.Fatalf("expected exactly one pending request, got %+v", pending)
	}

	// A "yes" response must nudge the candidate's score up (capped below
	// auto-accept) and re-elect it as the request's best candidate.
	if err := repo.RecordResponse(ctx, req.ID, helper, teamA.ID, enrichment.TeamMatchAnswerYes); err != nil {
		t.Fatal(err)
	}
	gotReq, gotCandidates, err := repo.FindRequest(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantScore := enrichment.CrowdAdjustedScore(70, enrichment.TeamMatchAnswerYes)
	if gotReq.BestScore != wantScore || gotReq.BestTeamID == nil || *gotReq.BestTeamID != teamA.ID {
		t.Fatalf("request not re-elected after response: %+v", gotReq)
	}
	if len(gotCandidates) != 1 || gotCandidates[0].Score != wantScore || gotCandidates[0].Yes != 1 || gotCandidates[0].No != 0 {
		t.Fatalf("candidate tally wrong after one 'yes': %+v", gotCandidates)
	}

	if answered, err := repo.HasResponded(ctx, req.ID, helper); err != nil {
		t.Fatal(err)
	} else if !answered {
		t.Fatal("expected HasResponded to be true after RecordResponse")
	}

	// Helper prefs: not eligible until asked-count/opt-out say otherwise.
	if eligible, err := repo.EligibleHelpers(ctx, []common.UserID{helper}); err != nil {
		t.Fatal(err)
	} else if len(eligible) != 1 {
		t.Fatalf("expected helper to be eligible before any asks, got %+v", eligible)
	}
	for range enrichment.MaxLifetimeAsksPerUser {
		if err := repo.RecordAsk(ctx, helper, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if eligible, err := repo.EligibleHelpers(ctx, []common.UserID{helper}); err != nil {
		t.Fatal(err)
	} else if len(eligible) != 0 {
		t.Fatalf("expected helper to be ineligible after hitting the lifetime cap, got %+v", eligible)
	}

	otherHelper := common.UserID{Value: 90002}
	if err := chats.SetUserLocale(ctx, otherHelper, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetOptedOut(ctx, otherHelper, true); err != nil {
		t.Fatal(err)
	}
	if eligible, err := repo.EligibleHelpers(ctx, []common.UserID{otherHelper}); err != nil {
		t.Fatal(err)
	} else if len(eligible) != 0 {
		t.Fatalf("expected the opted-out helper to be excluded, got %+v", eligible)
	}

	if err := repo.IncrementCrowdAsksSent(ctx, req.ID, 2); err != nil {
		t.Fatal(err)
	}
	if gotReq, _, err := repo.FindRequest(ctx, req.ID); err != nil {
		t.Fatal(err)
	} else if gotReq.CrowdAsksSent != 2 {
		t.Fatalf("CrowdAsksSent = %d, want 2", gotReq.CrowdAsksSent)
	}

	if err := repo.Resolve(ctx, req.ID, enrichment.TeamMatchConfirmed, &teamA.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resolved, _, err := repo.FindRequest(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != enrichment.TeamMatchConfirmed || resolved.ResolvedAt == nil {
		t.Fatalf("expected the request resolved as confirmed, got %+v", resolved)
	}
	if pending, err := repo.ListPending(ctx, 10); err != nil {
		t.Fatal(err)
	} else if len(pending) != 0 {
		t.Fatalf("a resolved request must no longer be pending, got %+v", pending)
	}
}

// RecordResponse used to read a candidate's score, adjust it in Go, then
// write it back with no row lock — two people answering about the same
// candidate at nearly the same time could read the same starting score and
// have one's effect silently overwritten by the other's (a lost update).
// This drives two concurrent "yes" votes from different helpers at real
// Postgres and asserts both boosts land — CrowdYesBoost is small enough
// (well under half of CrowdScoreCap starting from a mid-range score) that
// "both applied" is unambiguous regardless of which transaction's SELECT ...
// FOR UPDATE wins the row lock first.
func TestEnrichmentRepository_ConcurrentCrowdResponsesDoNotLoseAnUpdate(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	chats := pg.NewChatRepository(pool)
	repo := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Concurrency Cup", ExternalID: "cc-event", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	teamA := competition.Team{ID: common.NewTeamID(), Name: "Avangarr", ExternalID: "cc-team-a"}
	teamB := competition.Team{ID: common.NewTeamID(), Name: "Avangaar", ExternalID: "cc-team-b"}
	match := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "cc-match-1",
		FirstTeam: &teamA, SecondTeam: &teamB, Status: competition.MatchNotStarted, Format: format,
	}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}
	helper1 := common.UserID{Value: 90101}
	helper2 := common.UserID{Value: 90102}
	if err := chats.SetUserLocale(ctx, helper1, common.LocaleRU); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetUserLocale(ctx, helper2, common.LocaleRU); err != nil {
		t.Fatal(err)
	}

	req := enrichment.TeamMatchRequest{
		ID: common.NewRequestID(), ExternalName: "Avangar", Source: enrichment.SourceValveVRS,
		Status: enrichment.TeamMatchPending, BestTeamID: &teamA.ID, BestScore: 50, CreatedAt: time.Now().UTC(),
	}
	candidate := enrichment.TeamMatchCandidate{TeamID: teamA.ID, Score: 50, Kind: enrichment.CandidateKindFuzzy}
	if err := repo.CreateRequest(ctx, req, []enrichment.TeamMatchCandidate{candidate}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, h := range []common.UserID{helper1, helper2} {
		wg.Add(1)
		go func(helper common.UserID) {
			defer wg.Done()
			errs <- repo.RecordResponse(ctx, req.ID, helper, teamA.ID, enrichment.TeamMatchAnswerYes)
		}(h)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	_, candidates, err := repo.FindRequest(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := 50 + 2*enrichment.CrowdYesBoost
	if len(candidates) != 1 || candidates[0].Score != want {
		t.Fatalf("expected both concurrent 'yes' votes to apply (score = %d), got %+v", want, candidates)
	}
	if candidates[0].Yes != 2 {
		t.Fatalf("expected both responses recorded in the yes tally, got %+v", candidates[0])
	}
}

// Two different ranking sources (Valve VRS, HLTV) reporting a
// similarly-named team must never collide in ranking_snapshot — this is
// exactly the bug migration 0028 fixes (the table used to key solely on
// normalized_name, shared across every source).
func TestEnrichmentRepository_SnapshotIsScopedPerSource(t *testing.T) {
	pool, ctx := newTestPool(t)
	repo := pg.NewEnrichmentRepository(pool)

	valveRank, hltvRank := 3, 7
	if err := repo.SaveSnapshot(ctx, []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Vitality"}, GlobalRank: &valveRank, Source: enrichment.SourceValveVRS},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveSnapshot(ctx, []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Vitality"}, GlobalRank: &hltvRank, Source: enrichment.SourceHLTV},
	}); err != nil {
		t.Fatal(err)
	}

	valveSnapshot, err := repo.AllSnapshot(ctx, enrichment.SourceValveVRS)
	if err != nil {
		t.Fatal(err)
	}
	if len(valveSnapshot) != 1 || *valveSnapshot[0].GlobalRank != valveRank {
		t.Fatalf("Valve VRS snapshot was overwritten by the HLTV save, got %+v", valveSnapshot)
	}

	hltvSnapshot, err := repo.AllSnapshot(ctx, enrichment.SourceHLTV)
	if err != nil {
		t.Fatal(err)
	}
	if len(hltvSnapshot) != 1 || *hltvSnapshot[0].GlobalRank != hltvRank {
		t.Fatalf("unexpected HLTV snapshot: %+v", hltvSnapshot)
	}

	// A save for one source must not clear or affect the other's rows —
	// re-saving Valve's alone shouldn't touch the HLTV row from above.
	newValveRank := 4
	if err := repo.SaveSnapshot(ctx, []enrichment.RankedTeam{
		{Identity: enrichment.TeamIdentity{Name: "Vitality"}, GlobalRank: &newValveRank, Source: enrichment.SourceValveVRS},
	}); err != nil {
		t.Fatal(err)
	}
	if hltvSnapshot, err := repo.AllSnapshot(ctx, enrichment.SourceHLTV); err != nil {
		t.Fatal(err)
	} else if len(hltvSnapshot) != 1 || *hltvSnapshot[0].GlobalRank != hltvRank {
		t.Fatalf("HLTV snapshot changed after an unrelated Valve VRS save, got %+v", hltvSnapshot)
	}
}

// A chosen nickname must win over the Telegram-sourced display_name on
// every result list, and clearing it (SetNickname with "") must fall back
// to that Telegram name again — the whole point of keeping it a separate
// column from the one SaveVote refreshes on every vote.
func TestScoringRepository_LeaderboardPrefersTheChosenNickname(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	chatID := common.ChatID{Value: -100999}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Nickname chat", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Nickname Cup", ExternalID: "nickname-event", Status: competition.EventRunning, StartsAt: &now, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	firstTeam := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "nickname-team-1"}
	secondTeam := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "nickname-team-2"}
	score := competition.MatchScore{First: 2, Second: 1}
	match := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "nickname-match",
		FirstTeam: &firstTeam, SecondTeam: &secondTeam, ScheduledAt: &now, ActualStartedAt: &now,
		Status: competition.MatchFinished, Format: format, Score: &score,
	}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}
	options := make([]prediction.Option, 0)
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	telegramPollID, messageID := "nickname-poll", int64(99)
	poll := prediction.Poll{ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID, TelegramPollID: &telegramPollID, TelegramMessageID: &messageID, Options: options, Status: prediction.PollClosed, ClosesAt: now}
	if _, err := predictions.SavePoll(ctx, poll); err != nil {
		t.Fatal(err)
	}
	voter := common.UserID{Value: 4242}
	if err := predictions.SaveVote(ctx, prediction.Vote{PollID: poll.ID, UserID: voter, OptionIndex: 1, DisplayName: "Alex From Telegram", VotedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := scoringRepo.ReplaceAwards(ctx, poll.ID, []scoring.Award{{
		PollID: poll.ID, UserID: voter,
		Points: 2, Kind: scoring.AwardExactScore, AwardedAt: now.Add(time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}

	standings, err := scoringRepo.Leaderboard(ctx, chatID, scoring.AllTime())
	if err != nil {
		t.Fatal(err)
	}
	if len(standings) != 1 || standings[0].DisplayName != "Alex From Telegram" {
		t.Fatalf("before setting a nickname, DisplayName = %+v, want the Telegram name", standings)
	}

	if err := chats.SetNickname(ctx, voter, "Captain Clutch"); err != nil {
		t.Fatal(err)
	}
	standings, err = scoringRepo.Leaderboard(ctx, chatID, scoring.AllTime())
	if err != nil {
		t.Fatal(err)
	}
	if len(standings) != 1 || standings[0].DisplayName != "Captain Clutch" {
		t.Fatalf("DisplayName = %+v, want the chosen nickname", standings)
	}

	// A vote cast after the nickname was set must not clobber it — this is
	// the entire reason nickname is a separate column from display_name.
	if err := predictions.SaveVote(ctx, prediction.Vote{PollID: poll.ID, UserID: voter, OptionIndex: 0, DisplayName: "Alex From Telegram (renamed again)", VotedAt: now.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	nickname, err := chats.Nickname(ctx, voter)
	if err != nil || nickname == nil || *nickname != "Captain Clutch" {
		t.Fatalf("Nickname() after a vote = %v, %v; a vote must never overwrite it", nickname, err)
	}

	if err := chats.SetNickname(ctx, voter, ""); err != nil {
		t.Fatal(err)
	}
	if nickname, err := chats.Nickname(ctx, voter); err != nil || nickname != nil {
		t.Fatalf("Nickname() after clearing = %v, %v; want nil", nickname, err)
	}
	standings, err = scoringRepo.Leaderboard(ctx, chatID, scoring.AllTime())
	if err != nil {
		t.Fatal(err)
	}
	if len(standings) != 1 || standings[0].DisplayName != "Alex From Telegram (renamed again)" {
		t.Fatalf("after clearing the nickname, DisplayName = %+v, want the latest Telegram name", standings)
	}
}

// TestChatRepository_EventTopicLifecycle exercises the event_topic table —
// including its FK on tournament_event, which only a real Postgres (not the
// fakes the telegram package's unit tests use) can enforce or violate.
func TestChatRepository_EventTopicLifecycle(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)

	chatID := common.ChatID{Value: -781}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{
		ID: common.NewEventID(), Game: competition.GameCS2, Name: "Topic Test Event", ExternalID: "topic-event-1",
		Status: competition.EventRunning, Provider: "PANDASCORE",
	}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	if topicID, err := chats.EventTopic(ctx, chatID, event.ID); err != nil || topicID != nil {
		t.Fatalf("expected no topic bound yet, got %v, err=%v", topicID, err)
	}

	if err := chats.SaveEventTopic(ctx, chat.EventTopic{ChatID: chatID, EventID: event.ID, TopicID: 42}); err != nil {
		t.Fatal(err)
	}
	topicID, err := chats.EventTopic(ctx, chatID, event.ID)
	if err != nil || topicID == nil || *topicID != 42 {
		t.Fatalf("EventTopic() = %v, %v, want 42", topicID, err)
	}

	// Re-binding the same (chat, event) pair to a different topic replaces
	// it rather than erroring or creating a second row.
	if err := chats.SaveEventTopic(ctx, chat.EventTopic{ChatID: chatID, EventID: event.ID, TopicID: 99}); err != nil {
		t.Fatal(err)
	}
	topicID, err = chats.EventTopic(ctx, chatID, event.ID)
	if err != nil || topicID == nil || *topicID != 99 {
		t.Fatalf("EventTopic() after rebind = %v, %v, want 99", topicID, err)
	}

	if err := chats.ClearEventTopic(ctx, chatID, event.ID); err != nil {
		t.Fatal(err)
	}
	if topicID, err := chats.EventTopic(ctx, chatID, event.ID); err != nil || topicID != nil {
		t.Fatalf("expected the binding to be cleared, got %v, err=%v", topicID, err)
	}
}

// TestPredictionRepository_VotesAndRemoveVote covers the two prediction
// repository methods no existing integration test touches: reading back a
// poll's raw votes, and RemoveVote (the "cancel my prediction" feature) —
// including that it only removes the intended (poll, user) pair.
func TestPredictionRepository_VotesAndRemoveVote(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)

	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	chatID := common.ChatID{Value: -782}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Vote Test Event", ExternalID: "vote-event-1", Status: competition.EventRunning, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	firstTeam := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "vote-team-1"}
	secondTeam := competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: "vote-team-2"}
	match := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "vote-match-1",
		FirstTeam: &firstTeam, SecondTeam: &secondTeam, ScheduledAt: &now, Status: competition.MatchNotStarted, Format: format,
	}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}
	options := make([]prediction.Option, 0)
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	poll := prediction.Poll{ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID, Options: options, Status: prediction.PollOpen, ClosesAt: now}
	if _, err := predictions.SavePoll(ctx, poll); err != nil {
		t.Fatal(err)
	}

	voterA, voterB := common.UserID{Value: 1}, common.UserID{Value: 2}
	usernameA := "alex"
	if err := predictions.SaveVote(ctx, prediction.Vote{PollID: poll.ID, UserID: voterA, OptionIndex: 0, Username: &usernameA, DisplayName: "Alex", VotedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := predictions.SaveVote(ctx, prediction.Vote{PollID: poll.ID, UserID: voterB, OptionIndex: 1, DisplayName: "Bo", VotedAt: now}); err != nil {
		t.Fatal(err)
	}

	votes, err := predictions.Votes(ctx, poll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 2 {
		t.Fatalf("Votes() = %+v, want 2 votes", votes)
	}

	if err := predictions.RemoveVote(ctx, poll.ID, voterA); err != nil {
		t.Fatal(err)
	}
	votes, err = predictions.Votes(ctx, poll.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(votes) != 1 || votes[0].UserID != voterB {
		t.Fatalf("Votes() after removing voterA = %+v, want only voterB left", votes)
	}

	// Removing an already-removed (or never-existing) vote is a no-op, not
	// an error.
	if err := predictions.RemoveVote(ctx, poll.ID, voterA); err != nil {
		t.Fatalf("expected removing an absent vote to be a no-op, got %v", err)
	}
}

// TestScheduledReportRepository_ClaimIsExclusiveThenIdempotent mirrors
// TestClusterLock_MutualExclusion for the digest idempotency store: Claim
// must be the exclusive "was this the first claim" signal Postgres's own
// ON CONFLICT DO NOTHING guarantees, not something a fake could get subtly
// wrong (e.g. by not actually being atomic under concurrent callers).
func TestScheduledReportRepository_ClaimIsExclusiveThenIdempotent(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	reports := pg.NewScheduledReportRepository(pool)

	chatID := common.ChatID{Value: -783}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "C", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	if claimed, err := reports.Claimed(ctx, chatID, "monthly", "2026-09"); err != nil || claimed {
		t.Fatalf("expected unclaimed initially, got %v, err=%v", claimed, err)
	}

	const attempts = 8
	results := make(chan bool, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			ok, err := reports.Claim(ctx, chatID, "monthly", "2026-09")
			if err != nil {
				t.Error(err)
				results <- false
				return
			}
			results <- ok
		}()
	}
	successes := 0
	for i := 0; i < attempts; i++ {
		if <-results {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one of %d concurrent Claim calls to succeed, got %d", attempts, successes)
	}

	if claimed, err := reports.Claimed(ctx, chatID, "monthly", "2026-09"); err != nil || !claimed {
		t.Fatalf("expected Claimed to report true after a successful Claim, got %v, err=%v", claimed, err)
	}
	// A different report_type/period_key for the same chat is independent.
	if claimed, err := reports.Claimed(ctx, chatID, "annual", "2026"); err != nil || claimed {
		t.Fatalf("expected a different report type/period to remain unclaimed, got %v, err=%v", claimed, err)
	}
}

// TestChatRepository_MigrateChatIDCascadesEverywhere is the regression test
// for Telegram's basic-group -> supergroup migration: MigrateChatID must
// rename the chat's id everywhere in one shot — including through the
// chat_moderator -> chat_moderator_permission transitive foreign key —
// which only a real Postgres (enforcing the actual ON UPDATE CASCADE
// constraints from migration 0025) can verify; a fake repository would
// just simulate "it worked" without ever exercising the constraints.
func TestChatRepository_MigrateChatIDCascadesEverywhere(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)

	oldID := common.ChatID{Value: -100900001}
	newID := common.ChatID{Value: -100900002}
	manager := common.UserID{Value: 1}
	moderator := common.UserID{Value: 2}

	if _, err := chats.Save(ctx, chat.Settings{ChatID: oldID, Title: "Migrating Group", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.RecordManaged(ctx, oldID, manager); err != nil {
		t.Fatal(err)
	}
	if err := chats.AddModerator(ctx, chat.Moderator{ChatID: oldID, UserID: moderator, AppointedBy: manager, Permissions: chat.PresetContent()}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Migration Test Event", ExternalID: "migrate-event-1", Status: competition.EventRunning, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := chats.SaveEventTopic(ctx, chat.EventTopic{ChatID: oldID, EventID: event.ID, TopicID: 7}); err != nil {
		t.Fatal(err)
	}

	if err := chats.MigrateChatID(ctx, oldID, newID); err != nil {
		t.Fatal(err)
	}

	if settings, err := chats.Find(ctx, oldID); err != nil || settings != nil {
		t.Fatalf("expected the old id to no longer exist, got %+v, err=%v", settings, err)
	}
	settings, err := chats.Find(ctx, newID)
	if err != nil || settings == nil || settings.Title != "Migrating Group" {
		t.Fatalf("expected the new id to hold the migrated chat, got %+v, err=%v", settings, err)
	}

	managed, err := chats.ManagedChats(ctx, manager)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 || managed[0].ChatID != newID {
		t.Fatalf("expected exactly one managed chat under the new id, got %+v", managed)
	}

	isMod, err := chats.IsModerator(ctx, newID, moderator)
	if err != nil || !isMod {
		t.Fatalf("expected the moderator row to have followed the migration, isMod=%v err=%v", isMod, err)
	}
	perms, err := chats.ModeratorPermissions(ctx, newID, moderator)
	if err != nil {
		t.Fatal(err)
	}
	if !chat.HasPermission(perms, chat.PermissionManageEvents) || !chat.HasPermission(perms, chat.PermissionManageMatches) {
		// This is the transitive hop: chat_moderator_permission references
		// chat_moderator, not telegram_chat, so it only survives if THAT
		// foreign key also cascades.
		t.Fatalf("expected the moderator's permissions to have survived via the transitive cascade, got %v", perms)
	}

	topicID, err := chats.EventTopic(ctx, newID, event.ID)
	if err != nil || topicID == nil || *topicID != 7 {
		t.Fatalf("expected the event topic binding to have followed the migration, got %v, err=%v", topicID, err)
	}
}

// TestChatRepository_MigrateChatIDDeletesOldRowWhenNewIDAlreadyExists covers
// the race where the bot was somehow already contacted under the new id
// before the migration service message arrived: the new id's row is
// already the source of truth, so the old id's orphaned row is dropped
// rather than the migration failing on a duplicate key.
func TestChatRepository_MigrateChatIDDeletesOldRowWhenNewIDAlreadyExists(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)

	oldID := common.ChatID{Value: -100900003}
	newID := common.ChatID{Value: -100900004}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: oldID, Title: "Old", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: newID, Title: "Already Migrated", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}

	if err := chats.MigrateChatID(ctx, oldID, newID); err != nil {
		t.Fatal(err)
	}
	if settings, err := chats.Find(ctx, oldID); err != nil || settings != nil {
		t.Fatalf("expected the old id's row to be deleted, got %+v, err=%v", settings, err)
	}
	settings, err := chats.Find(ctx, newID)
	if err != nil || settings == nil || settings.Title != "Already Migrated" {
		t.Fatalf("expected the new id's existing row to be left untouched, got %+v, err=%v", settings, err)
	}
}

// A message that exhausts its retries disappears from every other query
// here, which is exactly why the operator view and the replay have to work
// against the real schema — nothing else would ever notice them again.
func TestOutbox_DeadLettersAreVisibleAndReplayable(t *testing.T) {
	pool, ctx := newTestPool(t)
	outbox := pg.NewOutbox(pool)

	id, err := outbox.Enqueue(ctx, "TELEGRAM_CHAT", "-1:recap", "telegram.result-recap", `{"chatId":-1}`)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var occurredAt time.Time
	for _, m := range pending {
		if m.ID == id {
			occurredAt = m.OccurredAt
		}
	}
	if occurredAt.IsZero() {
		t.Fatal("expected the new message to be pending")
	}

	// Burn the whole retry budget the way a permanently failing publisher
	// would.
	for i := 0; i < common.OutboxMaxAttempts; i++ {
		if err := outbox.Failed(ctx, id, occurredAt, "chat not found"); err != nil {
			t.Fatal(err)
		}
	}

	groups, err := outbox.DeadLetters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *common.DeadLetterGroup
	for i := range groups {
		if groups[i].EventType == "telegram.result-recap" {
			found = &groups[i]
		}
	}
	if found == nil || found.Count < 1 {
		t.Fatalf("expected the exhausted message to show up as a dead letter, got %+v", groups)
	}
	if found.LastError == "" {
		t.Fatal("the operator needs the error that killed it, not just a count")
	}

	replayed, err := outbox.ReplayDeadLetters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if replayed < 1 {
		t.Fatalf("expected at least the one message to be released, got %d", replayed)
	}
	after, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var back bool
	for _, m := range after {
		if m.ID == id {
			back = true
		}
	}
	if !back {
		t.Fatal("a replayed message must be selectable for delivery again")
	}
}

// The Ideas channel's rate limits are computed from what this table
// stores, so the two reads behind them have to work against the real
// schema: what has this person tried lately, and have they sent this exact
// idea before.
func TestFeedbackRepository_RecordsAttemptsAndFindsDuplicates(t *testing.T) {
	pool, ctx := newTestPool(t)
	repo := pg.NewFeedbackRepository(pool)
	userID := common.UserID{Value: 4242}
	now := time.Now().UTC()

	suggestion := &feedback.Suggestion{
		ID: common.NewRequestID(), UserID: userID,
		Text: "Добавьте статистику по картам", Fingerprint: feedback.Fingerprint("Добавьте статистику по картам"),
		CreatedAt: now,
	}
	if err := repo.RecordAttempt(ctx, feedback.Attempt{UserID: userID, Outcome: feedback.OutcomeAccepted, CreatedAt: now}, suggestion); err != nil {
		t.Fatal(err)
	}
	// A refusal is recorded too — it is what the flood threshold counts.
	if err := repo.RecordAttempt(ctx, feedback.Attempt{UserID: userID, Outcome: feedback.OutcomeTooLong, CreatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}

	attempts, err := repo.RecentAttempts(ctx, userID, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected both attempts, got %+v", attempts)
	}
	var accepted, refused int
	for _, a := range attempts {
		switch a.Outcome {
		case feedback.OutcomeAccepted:
			accepted++
		case feedback.OutcomeTooLong:
			refused++
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("outcomes did not round-trip: %+v", attempts)
	}

	dupe, err := repo.HasFingerprint(ctx, userID, suggestion.Fingerprint, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !dupe {
		t.Fatal("expected the stored idea to be recognized as already sent")
	}
	other, err := repo.HasFingerprint(ctx, userID, feedback.Fingerprint("что-то совсем другое"), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if other {
		t.Fatal("a different idea must not look like a duplicate")
	}
	// Yesterday's copy does not block today's send.
	stale, err := repo.HasFingerprint(ctx, userID, suggestion.Fingerprint, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("the duplicate check has to respect its window")
	}
}

// Teams are per game, and so are the rankings that may be attached to
// them: the ranking sync asks for the games its feed covers, and this is
// the query that has to honour that.
func TestEnrichmentRepository_ListTeamsFiltersByGame(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	enrich := pg.NewEnrichmentRepository(pool)

	cs2Event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "CS Cup", ExternalID: "cs-cup", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	dotaEvent := competition.Event{ID: common.NewEventID(), Game: competition.GameDota2, Name: "Dota Cup", ExternalID: "dota-cup", Status: competition.EventUpcoming, Provider: "PANDASCORE"}
	for _, e := range []competition.Event{cs2Event, dotaEvent} {
		if _, err := catalog.SaveEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	// The same organisation in both games, which is exactly the case that
	// used to produce a wrong ranking.
	cs2Team := competition.Team{ID: common.NewTeamID(), Name: "BetBoom Team", ExternalID: "bb-cs2"}
	dotaTeam := competition.Team{ID: common.NewTeamID(), Name: "BetBoom Team", ExternalID: "bb-dota"}
	other := competition.Team{ID: common.NewTeamID(), Name: "Spirit", ExternalID: "spirit-cs2"}
	if _, err := catalog.SaveMatch(ctx, competition.Match{
		ID: common.NewMatchID(), EventID: cs2Event.ID, ExternalID: "cs-m1", Status: competition.MatchNotStarted,
		Format: format, FirstTeam: &cs2Team, SecondTeam: &other,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.SaveMatch(ctx, competition.Match{
		ID: common.NewMatchID(), EventID: dotaEvent.ID, ExternalID: "dota-m1", Status: competition.MatchNotStarted,
		Format: format, FirstTeam: &dotaTeam, SecondTeam: &competition.Team{ID: common.NewTeamID(), Name: "Falcons", ExternalID: "falcons-dota"},
	}); err != nil {
		t.Fatal(err)
	}

	cs2Teams, err := enrich.ListTeams(ctx, competition.GameCS2)
	if err != nil {
		t.Fatal(err)
	}
	found := map[common.TeamID]bool{}
	for _, team := range cs2Teams {
		found[team.ID] = true
	}
	if !found[cs2Team.ID] {
		t.Fatal("expected the Counter-Strike team in a CS2-scoped listing")
	}
	if found[dotaTeam.ID] {
		t.Fatal("a Dota 2 team must never be offered to a Counter-Strike ranking feed")
	}

	all, err := enrich.ListTeams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) <= len(cs2Teams) {
		t.Fatalf("an unscoped listing still returns every team, got %d vs %d", len(all), len(cs2Teams))
	}
}

// The reader's own timezone: absent until they choose one, and then
// exactly what they chose.
func TestChatRepository_UserTimezoneRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	userID := common.UserID{Value: 5150}

	zone, err := chats.UserTimezone(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if zone != nil {
		t.Fatalf("a person who never chose has no zone of their own, got %v", *zone)
	}

	if err := chats.SetUserTimezone(ctx, userID, "Europe/Berlin"); err != nil {
		t.Fatal(err)
	}
	zone, err = chats.UserTimezone(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if zone == nil || *zone != "Europe/Berlin" {
		t.Fatalf("zone = %v, want Europe/Berlin", zone)
	}

	// Clearing it puts them back on the chat's zone rather than on UTC.
	if err := chats.SetUserTimezone(ctx, userID, ""); err != nil {
		t.Fatal(err)
	}
	zone, err = chats.UserTimezone(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if zone != nil {
		t.Fatalf("expected the choice to be cleared, got %v", *zone)
	}
}

// Holding a message and failing to deliver it are different things in the
// schema too: Defer moves the next attempt without spending one.
func TestOutbox_DeferHoldsWithoutSpendingAnAttempt(t *testing.T) {
	pool, ctx := newTestPool(t)
	outbox := pg.NewOutbox(pool)

	id, err := outbox.Enqueue(ctx, "TELEGRAM_CHAT", "-1:big-event", "telegram.big-event-discovered", `{"chatId":-1}`)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var occurredAt time.Time
	for _, m := range pending {
		if m.ID == id {
			occurredAt = m.OccurredAt
		}
	}
	if occurredAt.IsZero() {
		t.Fatal("expected the message to be pending")
	}

	if err := outbox.Defer(ctx, id, occurredAt, time.Now().Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}

	after, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range after {
		if m.ID == id {
			t.Fatal("a held message must not be selected for delivery before its time")
		}
	}
	// And it is still unspent: a nine-hour night must not cost nine
	// attempts, or the retry budget would run out before morning.
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT attempts FROM outbox_event WHERE id = $1`, id).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want the hold to have cost none", attempts)
	}
}

// The game filter has to reach the SQL, and it has to compose with a date
// range rather than fight it for placeholders — hand-numbered $2/$3 is
// exactly what would break there.
func TestScoringRepository_LeaderboardNarrowsToOneGame(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	playedAt := time.Now().UTC().Add(-2 * time.Hour)
	chatID := common.ChatID{Value: -200796}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Two games", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	var options []prediction.Option
	for i, score := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: score})
	}
	player := common.UserID{Value: 8401}

	// One finished match per game, each worth a different number of points
	// so the two slices cannot be confused for one another.
	seed := func(game competition.GameCode, external string, points int) {
		event := competition.Event{
			ID: common.NewEventID(), Game: game, Name: string(game) + " Cup",
			ExternalID: external + "-event", Status: competition.EventRunning, Provider: "PANDASCORE",
		}
		if _, err := catalog.SaveEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
		score := competition.MatchScore{First: 2, Second: 0}
		match := competition.Match{
			ID: common.NewMatchID(), EventID: event.ID, ExternalID: external + "-match",
			FirstTeam:   &competition.Team{ID: common.NewTeamID(), Name: "A", ExternalID: external + "-a"},
			SecondTeam:  &competition.Team{ID: common.NewTeamID(), Name: "B", ExternalID: external + "-b"},
			ScheduledAt: &playedAt, ActualStartedAt: &playedAt, Status: competition.MatchFinished,
			Format: format, Score: &score,
		}
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		telegramPollID := external + "-poll"
		messageID := int64(len(external))
		saved, err := predictions.SavePoll(ctx, prediction.Poll{
			ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID,
			TelegramPollID: &telegramPollID, TelegramMessageID: &messageID,
			Options: options, Status: prediction.PollClosed, ClosesAt: playedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := predictions.SaveVote(ctx, prediction.Vote{PollID: saved.ID, UserID: player, OptionIndex: 0, DisplayName: "Player", VotedAt: playedAt.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := scoringRepo.ReplaceAwards(ctx, saved.ID, []scoring.Award{{
			PollID: saved.ID, UserID: player,
			Points: points, Kind: scoring.AwardExactScore, AwardedAt: playedAt,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	seed(competition.GameCS2, "cs2", 3)
	seed(competition.GameDota2, "dota", 5)

	points := func(period scoring.StatsPeriod) int {
		rows, err := scoringRepo.Leaderboard(ctx, chatID, period)
		if err != nil {
			t.Fatal(err)
		}
		sum := 0
		for _, row := range rows {
			sum += row.Points
		}
		return sum
	}

	if got := points(scoring.AllTime()); got != 8 {
		t.Fatalf("the unfiltered board = %d points, want both games (8)", got)
	}
	if got := points(scoring.AllTime().ForGame(competition.GameCS2)); got != 3 {
		t.Fatalf("the CS2 board = %d points, want 3", got)
	}
	if got := points(scoring.AllTime().ForGame(competition.GameDota2)); got != 5 {
		t.Fatalf("the Dota 2 board = %d points, want 5", got)
	}
	// Composed with a date range — the case that needs the placeholders to
	// be numbered as they are appended rather than assumed.
	if got := points(scoring.ForYear(playedAt.Year()).ForGame(competition.GameDota2)); got != 5 {
		t.Fatalf("this year's Dota 2 board = %d points, want 5", got)
	}
}

// Crests come from two places and are kept in two columns: the match
// provider's (every game, arriving with the ordinary sync) and HLTV's
// ranking (Counter-Strike, ranked teams only). Which one is shown is a
// preference, so neither write may clobber the other.
func TestCompetitionRepository_TeamCrestsFromBothSources(t *testing.T) {
	pool, ctx := newTestPool(t)
	catalog := pg.NewCompetitionRepository(pool)
	enrich := pg.NewEnrichmentRepository(pool)

	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Crest Cup",
		ExternalID: "crest-event", Status: competition.EventRunning, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	ranked := competition.Team{ID: common.NewTeamID(), Name: "Vitality", ExternalID: "crest-vit",
		LogoURL: "https://cdn-api.pandascore.co/vitality.png"}
	unranked := competition.Team{ID: common.NewTeamID(), Name: "Qualifier Five", ExternalID: "crest-five",
		LogoURL: "https://cdn-api.pandascore.co/five.png"}
	if _, err := catalog.SaveMatch(ctx, competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "crest-match",
		Status: competition.MatchNotStarted, Format: format, FirstTeam: &ranked, SecondTeam: &unranked,
	}); err != nil {
		t.Fatal(err)
	}

	// HLTV's ranking then adds its own picture and its own country for the
	// ranked team. Both travel together: they come from the same feed row.
	if err := enrich.SetRankingAppearance(ctx, ranked.ID, enrichment.SourceHLTV,
		"https://img-cdn.hltv.org/vitality.png", "FR"); err != nil {
		t.Fatal(err)
	}
	// A source that is not HLTV must not write into HLTV's columns.
	if err := enrich.SetRankingAppearance(ctx, unranked.ID, enrichment.SourceValveVRS,
		"https://example.invalid/vrs.png", "SE"); err != nil {
		t.Fatal(err)
	}

	teams, err := catalog.TeamsForGame(ctx, competition.GameCS2, 50)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]competition.Team{}
	for _, team := range teams {
		byName[team.Name] = team
	}
	vit, five := byName["Vitality"], byName["Qualifier Five"]
	if vit.LogoURL != "https://cdn-api.pandascore.co/vitality.png" || vit.HLTVLogoURL != "https://img-cdn.hltv.org/vitality.png" {
		t.Fatalf("expected both crests side by side, got %+v", vit)
	}
	if five.HLTVLogoURL != "" {
		t.Fatalf("Valve's feed publishes no crest and must not write one, got %q", five.HLTVLogoURL)
	}
	// The preference picks, and falls back for the teams HLTV never ranked.
	if vit.LogoFor(true) != vit.HLTVLogoURL || vit.LogoFor(false) != vit.LogoURL {
		t.Fatalf("LogoFor ignored the preference: %+v", vit)
	}
	if five.LogoFor(true) != five.LogoURL {
		t.Fatalf("an unranked team must keep the provider's crest, got %q", five.LogoFor(true))
	}
	// The flag follows the same rule as the crest, on its own switch: a
	// chat may well want the provider's picture with HLTV's country.
	if vit.HLTVLocation != "FR" {
		t.Fatalf("HLTV's country was not stored: %+v", vit)
	}
	if five.HLTVLocation != "" {
		t.Fatalf("a non-HLTV source wrote into HLTV's country column: %q", five.HLTVLocation)
	}
	if vit.LocationFor(true) != "FR" {
		t.Fatalf("LocationFor ignored the preference: %+v", vit)
	}

	// A crest-only refresh must not blank the country it stored last week.
	if err := enrich.SetRankingAppearance(ctx, ranked.ID, enrichment.SourceHLTV,
		"https://img-cdn.hltv.org/vitality-2.png", ""); err != nil {
		t.Fatal(err)
	}
	refreshed, err := catalog.TeamsForGame(ctx, competition.GameCS2, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range refreshed {
		if team.ID == ranked.ID && team.HLTVLocation != "FR" {
			t.Fatalf("a crest-only refresh erased the country: %+v", team)
		}
	}

	// A later match sync that carries no crest must not erase the stored one.
	if _, err := catalog.SaveMatch(ctx, competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "crest-match-2",
		Status: competition.MatchNotStarted, Format: format,
		FirstTeam:  &competition.Team{ID: ranked.ID, Name: ranked.Name, ExternalID: ranked.ExternalID},
		SecondTeam: &unranked,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := catalog.TeamsForGame(ctx, competition.GameCS2, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range after {
		if team.ID == ranked.ID && (team.LogoURL == "" || team.HLTVLogoURL == "") {
			t.Fatalf("a sync without crests erased the stored ones: %+v", team)
		}
	}
}

// Mini App access is an access grant: closed until somebody opens it,
// recorded with who decided, and not reopened by asking twice.
func TestChatRepository_MiniAppAccessLifecycle(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	person := common.UserID{Value: 9001}
	operator := common.UserID{Value: 9002}
	now := time.Now().UTC()

	access, err := chats.MiniAppAccess(ctx, person)
	if err != nil {
		t.Fatal(err)
	}
	if access != nil {
		t.Fatalf("nobody has access until they are given it, got %+v", access)
	}

	if err := chats.RequestMiniAppAccess(ctx, person, now); err != nil {
		t.Fatal(err)
	}
	// Asking again must not reset the queue position or reopen anything.
	if err := chats.RequestMiniAppAccess(ctx, person, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	access, err = chats.MiniAppAccess(ctx, person)
	if err != nil {
		t.Fatal(err)
	}
	// The point is that the second ask did not reset the first, not that
	// the timestamp round-trips to the nanosecond: Postgres stores
	// microseconds, so equality here is a flake waiting for a machine
	// whose clock has a finer tail than the last one's.
	if access == nil || access.Status != chat.MiniAppPending {
		t.Fatalf("access = %+v, want a pending request", access)
	}
	if !access.RequestedAt.Before(now.Add(time.Hour)) {
		t.Fatalf("requested at %s, want the first request preserved rather than the later one", access.RequestedAt)
	}

	pending, err := chats.PendingMiniAppRequests(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var listed bool
	for _, row := range pending {
		if row.UserID == person {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("expected the request in the pending list, got %+v", pending)
	}

	if err := chats.DecideMiniAppAccess(ctx, person, chat.MiniAppGranted, operator, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	access, err = chats.MiniAppAccess(ctx, person)
	if err != nil {
		t.Fatal(err)
	}
	if access.Status != chat.MiniAppGranted || access.DecidedBy == nil || *access.DecidedBy != operator {
		t.Fatalf("access = %+v, want granted and signed by the operator", access)
	}
	// A decided request leaves the queue.
	pending, err = chats.PendingMiniAppRequests(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range pending {
		if row.UserID == person {
			t.Fatal("a decided request must not stay in the pending list")
		}
	}

	// Asking again after a decision changes nothing: the decision stands
	// until an operator changes it.
	if err := chats.RequestMiniAppAccess(ctx, person, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	access, err = chats.MiniAppAccess(ctx, person)
	if err != nil {
		t.Fatal(err)
	}
	if access.Status != chat.MiniAppGranted {
		t.Fatalf("status = %s, want the decision to stand", access.Status)
	}
}

// The unsettled half of a record: what somebody has riding right now, with
// the broadcast to watch it on. Written against the real schema because
// the stream lives in a JSON column and the language preference lives on
// the chat — two things a fake would quietly get right.
func TestScoringRepository_ActivePredictionsCarryStreamsAndSkipFinishedMatches(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	chatID := common.ChatID{Value: -300100}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Активные", Locale: common.LocaleRU,
		Timezone: chat.DefaultTimezone, Active: true, StreamLanguage: common.LocaleRU}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Active Cup",
		ExternalID: "active-event", Status: competition.EventRunning, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	// A prediction only counts as pending while the chat is still
	// following the tournament and still has the game switched on — see
	// the dropped-tournament case at the end of this test.
	subs := pg.NewSubscriptionRepository(pool)
	if _, err := subs.Subscribe(ctx, subscription.EventSubscription{ChatID: chatID, EventID: event.ID,
		SubscribedAt: time.Now(), Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(ctx, chatID, []competition.GameCode{competition.GameCS2}); err != nil {
		t.Fatal(err)
	}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	var options []prediction.Option
	for i, score := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: score})
	}
	player := common.UserID{Value: 7301}
	soon := time.Now().UTC().Add(3 * time.Hour)

	seed := func(external string, status competition.MatchStatus, streams []competition.Stream) {
		match := competition.Match{
			ID: common.NewMatchID(), EventID: event.ID, ExternalID: external, Status: status, Format: format,
			FirstTeam:   &competition.Team{ID: common.NewTeamID(), Name: "G2", ExternalID: external + "-a"},
			SecondTeam:  &competition.Team{ID: common.NewTeamID(), Name: "NAVI", ExternalID: external + "-b"},
			ScheduledAt: &soon, Streams: streams,
		}
		if status == competition.MatchFinished {
			score := competition.MatchScore{First: 2, Second: 0}
			match.Score = &score
			match.ActualStartedAt = &soon
		}
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		telegramPollID := external + "-poll"
		messageID := int64(len(external))
		saved, err := predictions.SavePoll(ctx, prediction.Poll{
			ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID,
			TelegramPollID: &telegramPollID, TelegramMessageID: &messageID,
			Options: options, Status: prediction.PollOpen, ClosesAt: soon,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := predictions.SaveVote(ctx, prediction.Vote{PollID: saved.ID, UserID: player,
			OptionIndex: 0, DisplayName: "Player", VotedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	seed("active-upcoming", competition.MatchNotStarted, []competition.Stream{
		{Language: "ru", URL: "https://twitch.tv/major_ru", Main: true, Official: true},
		{Language: "en", URL: "https://twitch.tv/major_en", Official: true},
	})
	seed("active-no-stream", competition.MatchRunning, nil)
	seed("active-finished", competition.MatchFinished, nil)

	rows, err := scoringRepo.ActivePredictions(ctx, player, scoring.ActivePredictionsMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected only the unsettled matches, got %d", len(rows))
	}
	var withStream, withoutStream int
	for _, row := range rows {
		if row.PredictedScore.String() == "" {
			t.Fatalf("a prediction without its call is not worth showing: %+v", row)
		}
		switch row.StreamURL {
		case "https://twitch.tv/major_ru":
			withStream++
		case "":
			withoutStream++
		default:
			t.Fatalf("stream = %q, want the chat's own language or none", row.StreamURL)
		}
	}
	if withStream != 1 || withoutStream != 1 {
		t.Fatalf("expected one match with a broadcast and one without, got %d/%d", withStream, withoutStream)
	}

	// The chat drops the tournament. Nobody in that room is waiting on
	// these polls any more — they will never be settled there or talked
	// about again — so they stop being "what you have riding right now".
	if err := subs.Unsubscribe(ctx, chatID, event.ID); err != nil {
		t.Fatal(err)
	}
	dropped, err := scoringRepo.ActivePredictions(ctx, player, scoring.ActivePredictionsMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 0 {
		t.Fatalf("a dropped tournament still shows %d pending predictions", len(dropped))
	}

	// Same for a discipline the chat has switched off.
	if _, err := subs.Subscribe(ctx, subscription.EventSubscription{ChatID: chatID, EventID: event.ID,
		SubscribedAt: time.Now(), Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := chats.SetEnabledGames(ctx, chatID, []competition.GameCode{competition.GameDota2}); err != nil {
		t.Fatal(err)
	}
	offGame, err := scoringRepo.ActivePredictions(ctx, player, scoring.ActivePredictionsMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(offGame) != 0 {
		t.Fatalf("a switched-off discipline still shows %d pending predictions", len(offGame))
	}
}

// Both ranks in a comparison have to come from the same feed.
//
// Valve's standings run to about 390 places and HLTV's to about 100, so a
// team's position means a different thing in each. Taking whichever number
// is smaller — which is what this did at first — compares a top-ten place
// on one scale against a hundred-and-twentieth on the other and calls the
// difference a gap. On production data that mis-sorted a quarter of the
// matches into the wrong bucket.
func TestScoringRepository_RankGapComesFromOneFeed(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	enrich := pg.NewEnrichmentRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)

	chatID := common.ChatID{Value: -770100}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Ranks", Locale: common.LocaleRU,
		Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: "Rank Cup",
		ExternalID: "rank-event", Status: competition.EventRunning, Provider: "PANDASCORE"}
	if _, err := catalog.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}

	// Two teams both feeds know, ranked so that the smallest number for
	// each comes from a DIFFERENT feed. That is what makes this fixture
	// able to tell the two implementations apart: picking the minimum per
	// team silently reads one side off HLTV and the other off Valve.
	picked := competition.Team{ID: common.NewTeamID(), Name: "Near", ExternalID: "rank-near"}
	other := competition.Team{ID: common.NewTeamID(), Name: "Far", ExternalID: "rank-far"}
	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	started := time.Now().UTC().Add(-2 * time.Hour)
	score := competition.MatchScore{First: 2, Second: 0}
	match := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "rank-match",
		Status: competition.MatchFinished, Format: format, FirstTeam: &picked, SecondTeam: &other,
		ScheduledAt: &started, ActualStartedAt: &started, Score: &score,
	}
	if _, err := catalog.SaveMatch(ctx, match); err != nil {
		t.Fatal(err)
	}

	rank := func(v int) *int { return &v }
	for _, r := range []enrichment.TeamRanking{
		{TeamID: picked.ID, Source: enrichment.SourceHLTV, GlobalRank: rank(40), PublishedAt: started},
		{TeamID: other.ID, Source: enrichment.SourceHLTV, GlobalRank: rank(9), PublishedAt: started},
		{TeamID: picked.ID, Source: enrichment.SourceValveVRS, GlobalRank: rank(11), PublishedAt: started},
		{TeamID: other.ID, Source: enrichment.SourceValveVRS, GlobalRank: rank(240), PublishedAt: started},
	} {
		if err := enrich.SaveRanking(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	player := common.UserID{Value: 7711}
	var options []prediction.Option
	for i, s := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: s})
	}
	telegramPollID, messageID := "rank-poll", int64(11)
	saved, err := predictions.SavePoll(ctx, prediction.Poll{
		ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID,
		TelegramPollID: &telegramPollID, TelegramMessageID: &messageID,
		Options: options, Status: prediction.PollClosed, ClosesAt: started,
		FirstTeamID: picked.ID, SecondTeamID: other.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Option 0 of a BO3 is 2:0 — backing the first team, which is picked.
	if err := predictions.SaveVote(ctx, prediction.Vote{PollID: saved.ID, UserID: player,
		OptionIndex: 0, DisplayName: "Player", VotedAt: started}); err != nil {
		t.Fatal(err)
	}

	facts, err := scoringRepo.UserPredictionFacts(ctx, player, scoring.PredictionFactsMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want the one settled prediction", len(facts))
	}
	fact := facts[0]
	if !fact.Ranked() {
		t.Fatal("both teams are ranked by both feeds; this must be readable")
	}
	// HLTV is preferred and ranks both: 9 − 40 = −31, a heavy underdog
	// call. Taking each team's smallest number instead reads the picked
	// side off Valve (11) and the other off HLTV (9), giving −2 and
	// calling the same match even.
	if gap := fact.RankGap(); gap != -31 {
		t.Fatalf("gap = %d, want −31 from HLTV alone — mixing feeds gives −2", gap)
	}
	if bucket := scoring.ClassifyRankGap(fact.RankGap()); bucket != scoring.RankHeavyUnderdog {
		t.Fatalf("bucket = %q, want a heavy underdog call", bucket)
	}
}
