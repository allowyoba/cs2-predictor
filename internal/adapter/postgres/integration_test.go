//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
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
		ChatID: chatID, EventID: event.ID, MatchID: match.ID, PollID: poll.ID, UserID: voter,
		Points: 2, Kind: scoring.AwardExactScore, MatchStartedAt: now, AwardedAt: now.Add(time.Minute),
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
		ChatID: otherChatID, EventID: event.ID, MatchID: match.ID, PollID: otherPoll.ID, UserID: voter,
		Points: 2, Kind: scoring.AwardExactScore, MatchStartedAt: now, AwardedAt: now.Add(time.Minute),
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

func TestPendingUnsubscribeRepository_CreateFindResolveRoundTrips(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	pending := pg.NewPendingUnsubscribeRepository(pool)

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
	p := chat.PendingUnsubscribe{
		ID: requestID, ChatID: chatID, EventID: event.ID, RequestedBy: userID,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), SelfConfirmable: true,
	}
	if err := pending.Create(ctx, p); err != nil {
		t.Fatal(err)
	}

	got, err := pending.Find(ctx, requestID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ChatID != chatID || got.EventID != event.ID || got.RequestedBy != userID || !got.SelfConfirmable {
		t.Fatalf("Find = %+v, want a round trip of %+v", got, p)
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

	if err := outbox.Failed(ctx, id, "telegram unavailable"); err != nil {
		t.Fatal(err)
	}
	pendingAfterFailure, err := outbox.Pending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingAfterFailure) != 0 {
		t.Fatalf("expected the failed message to be backed off (not immediately pending again), got %+v", pendingAfterFailure)
	}

	if err := outbox.Published(ctx, id); err != nil {
		t.Fatal(err)
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

	notStarted := competition.Match{
		ID: common.NewMatchID(), EventID: event.ID, ExternalID: "m1", Status: competition.MatchNotStarted,
		Format: format, FirstTeam: &firstTeam, SecondTeam: &secondTeam, Stage: &stage, ScheduledAt: &future,
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

	for _, id := range []common.UserID{optedInReachable, optedInUnreachable, reachableNoOptIn, otherKindOnly} {
		if err := chats.SetNotificationPref(ctx, id, common.NotifyResultRecaps, false); err != nil {
			t.Fatal(err) // creates the row
		}
	}
	for _, id := range []common.UserID{optedInReachable, reachableNoOptIn, otherKindOnly} {
		if err := chats.SetDMReachable(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []common.UserID{optedInReachable, optedInUnreachable} {
		if err := chats.SetNotificationPref(ctx, id, common.NotifyResultRecaps, true); err != nil {
			t.Fatal(err)
		}
	}
	// Opted into the other kind only: must not receive recaps.
	if err := chats.SetNotificationPref(ctx, otherKindOnly, common.NotifyPollReminders, true); err != nil {
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

	// The two kinds are independent switches.
	prefs, err := chats.NotificationPrefs(ctx, otherKindOnly)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.ResultRecaps || !prefs.PollReminders {
		t.Fatalf("prefs = %+v, want reminders on and recaps off", prefs)
	}

	// An unknown kind must be an error, never a query.
	if _, err := chats.Recipients(ctx, common.NotificationKind("everything"), []common.UserID{optedInReachable}); err == nil {
		t.Fatal("an unknown notification kind was accepted")
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
		ChatID: chatID, EventID: event.ID, MatchID: match.ID, PollID: poll.ID, UserID: voter,
		Points: 2, Kind: scoring.AwardExactScore, MatchStartedAt: now, AwardedAt: now.Add(time.Minute),
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
