//go:build integration

package postgres_test

import (
	"testing"
	"time"

	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// A chat following a team and two players (one on the same team) but not
// subscribed to the tournament sees only the followed teams' matches, each
// counted once; subscribing to the tournament widens it to everything.
func TestSubscriptionTargets_OverlappingFollowsScopeTournamentStats(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	predictions := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)
	subs := pg.NewSubscriptionRepository(pool)
	rankings := pg.NewEnrichmentRepository(pool)

	now := time.Now().UTC()
	playedAt := now.Add(-2 * time.Hour)
	chatID := common.ChatID{Value: -300100}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Followers", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	newEvent := func(external string) competition.Event {
		event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: external,
			ExternalID: external, Status: competition.EventRunning, Provider: "PANDASCORE", Tier: competition.TierS}
		if _, err := catalog.SaveEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
		return event
	}
	event, other := newEvent("major"), newEvent("other")

	team := func(name string) *competition.Team {
		return &competition.Team{ID: common.NewTeamID(), Name: name, ExternalID: "t-" + name}
	}
	a, b, x, y := team("Alpha"), team("Bravo"), team("Xray"), team("Yankee")

	format, _ := competition.NewSeriesFormat(competition.BestOf, 3)
	var options []prediction.Option
	for i, score := range format.PossibleScores() {
		options = append(options, prediction.Option{Index: i, Score: score})
	}
	voter := common.UserID{Value: 9101}
	seed := func(external string, first, second *competition.Team, points int) {
		score := competition.MatchScore{First: 2, Second: 0}
		match := competition.Match{ID: common.NewMatchID(), EventID: event.ID, ExternalID: external,
			FirstTeam: first, SecondTeam: second, ScheduledAt: &playedAt, ActualStartedAt: &playedAt,
			Status: competition.MatchFinished, Format: format, Score: &score}
		if _, err := catalog.SaveMatch(ctx, match); err != nil {
			t.Fatal(err)
		}
		pollTG, msg := external+"-poll", int64(len(external))
		saved, err := predictions.SavePoll(ctx, prediction.Poll{ID: common.NewPollID(), ChatID: chatID, MatchID: match.ID,
			TelegramPollID: &pollTG, TelegramMessageID: &msg, Options: options, Status: prediction.PollClosed, ClosesAt: playedAt})
		if err != nil {
			t.Fatal(err)
		}
		if err := predictions.SaveVote(ctx, prediction.Vote{PollID: saved.ID, UserID: voter, OptionIndex: 0, DisplayName: "V", VotedAt: playedAt.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := scoringRepo.ReplaceAwards(ctx, saved.ID, []scoring.Award{{PollID: saved.ID, UserID: voter, Points: points, Kind: scoring.AwardExactScore, AwardedAt: playedAt}}); err != nil {
			t.Fatal(err)
		}
	}
	seed("m-ab", a, b, 3)
	seed("m-xy", x, y, 5)

	rank := 1
	for _, r := range []struct {
		team   *competition.Team
		roster []string
	}{{a, []string{"Ace", "Anchor"}}, {b, []string{"Bolt"}}} {
		rk := rank
		if err := rankings.SaveRanking(ctx, enrichment.TeamRanking{TeamID: r.team.ID, Source: enrichment.SourceHLTV, GlobalRank: &rk, Roster: r.roster, PublishedAt: now}); err != nil {
			t.Fatal(err)
		}
		rank++
	}

	for _, target := range []subscription.Target{
		subscription.TournamentTarget(other.ID),
		subscription.TeamTarget(a.ID, a.Name),
		subscription.PlayerTarget("ace"), // also on Alpha: overlaps the team follow
		subscription.PlayerTarget("BOLT"),
	} {
		if err := subs.SubscribeTarget(ctx, chatID, target, now); err != nil {
			t.Fatal(err)
		}
	}

	scopeOf := func() subscription.Scope {
		targets, err := subs.Targets(ctx, chatID)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := subs.ResolveTeams(ctx, targets)
		if err != nil {
			t.Fatal(err)
		}
		return subscription.ScopeFor(event.ID, targets, resolved)
	}
	board := func(scope subscription.Scope) scoring.UserStanding {
		period := scoring.ForEvent(event.ID)
		if !scope.Full {
			period = period.ForTeams(scope.Teams)
		}
		rows, err := scoringRepo.Leaderboard(ctx, chatID, period)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("leaderboard rows = %d, want 1", len(rows))
		}
		return rows[0]
	}

	sliced := scopeOf()
	if sliced.Full || len(sliced.Teams) != 2 {
		t.Fatalf("scope = %+v, want the Alpha/Bravo slice", sliced)
	}
	if got := board(sliced); got.Points != 3 || got.Predictions != 1 {
		t.Fatalf("sliced board = %d points over %d predictions, want 3 over 1 (no double count, no Xray match)", got.Points, got.Predictions)
	}

	followers, err := subs.ChatsFollowingTeams(ctx, []common.TeamID{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(followers) != 1 || followers[0] != chatID {
		t.Fatalf("followers = %v, want the chat exactly once", followers)
	}
	search, err := subs.SearchTargets(ctx, "an", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(search) == 0 {
		t.Fatal("search found neither Yankee nor Anchor")
	}

	if err := subs.SubscribeTarget(ctx, chatID, subscription.TournamentTarget(event.ID), now); err != nil {
		t.Fatal(err)
	}
	full := scopeOf()
	if !full.Full {
		t.Fatalf("a tournament subscription must widen the scope, got %+v", full)
	}
	if got := board(full); got.Points != 8 || got.Predictions != 2 {
		t.Fatalf("full board = %d/%d, want 8/2", got.Points, got.Predictions)
	}

	if err := subs.UnsubscribeTarget(ctx, chatID, subscription.PlayerTarget("Ace")); err != nil {
		t.Fatal(err)
	}
	targets, err := subs.Targets(ctx, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 4 {
		t.Fatalf("targets after unfollowing Ace = %d, want 4", len(targets))
	}
}

func TestSubscriptionTargets_CrossSellOfferIsThrottled(t *testing.T) {
	pool, ctx := newTestPool(t)
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	subs := pg.NewSubscriptionRepository(pool)

	chatID := common.ChatID{Value: -300200}
	if _, err := chats.Save(ctx, chat.Settings{ChatID: chatID, Title: "Offers", Locale: common.LocaleRU, Timezone: chat.DefaultTimezone, Active: true}); err != nil {
		t.Fatal(err)
	}
	var events []common.EventID
	for _, ext := range []string{"e1", "e2"} {
		event := competition.Event{ID: common.NewEventID(), Game: competition.GameCS2, Name: ext, ExternalID: ext, Status: competition.EventRunning, Provider: "PANDASCORE"}
		if _, err := catalog.SaveEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event.ID)
	}
	now := time.Now().UTC()
	day := 24 * time.Hour
	claim := func(event common.EventID, at time.Time) bool {
		ok, err := subs.ClaimCrossSellOffer(ctx, chatID, event, at, day)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !claim(events[0], now) {
		t.Fatal("first offer must be allowed")
	}
	if claim(events[0], now.Add(2*day)) {
		t.Fatal("the same event must never be offered twice")
	}
	if claim(events[1], now.Add(time.Hour)) {
		t.Fatal("a second offer inside the cooldown must wait")
	}
	if !claim(events[1], now.Add(2*day)) {
		t.Fatal("after the cooldown another event may be offered")
	}
}
