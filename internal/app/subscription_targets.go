package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// CrossSellCooldown spaces tournament offers to one chat.
const CrossSellCooldown = 24 * time.Hour

// SubscriptionScopes answers how much of a tournament a chat may see.
type SubscriptionScopes struct {
	Targets subscription.TargetRepository
}

// ForEvent resolves the chat's scope for one tournament.
func (s SubscriptionScopes) ForEvent(ctx context.Context, chatID common.ChatID, eventID common.EventID) (subscription.Scope, error) {
	if s.Targets == nil {
		return subscription.Scope{Full: true}, nil
	}
	targets, err := s.Targets.Targets(ctx, chatID)
	if err != nil {
		return subscription.Scope{}, err
	}
	resolved, err := s.Targets.ResolveTeams(ctx, targets)
	if err != nil {
		return subscription.Scope{}, err
	}
	return subscription.ScopeFor(eventID, targets, resolved), nil
}

// Narrow applies the scope to a tournament period; other periods and
// periods already narrowed to teams pass through untouched.
func (s SubscriptionScopes) Narrow(ctx context.Context, chatID common.ChatID, period scoring.StatsPeriod) (scoring.StatsPeriod, error) {
	if period.Kind != scoring.PeriodEvent || period.Teams != nil {
		return period, nil
	}
	scope, err := s.ForEvent(ctx, chatID, period.EventID)
	if err != nil {
		return period, err
	}
	if scope.Full {
		return period, nil
	}
	return period.ForTeams(scope.Teams), nil
}

// TargetTeams resolves one team/player target to the teams it covers.
func (s SubscriptionScopes) TargetTeams(ctx context.Context, target subscription.Target) ([]common.TeamID, error) {
	if s.Targets == nil {
		return []common.TeamID{}, nil
	}
	resolved, err := s.Targets.ResolveTeams(ctx, []subscription.Target{target})
	if err != nil {
		return nil, err
	}
	return subscription.TeamsOf([]subscription.Target{target}, resolved), nil
}

// ScopedScoring enforces tournament scoping on every chat leaderboard, so
// no screen can leak a full tournament view to a team/player follower.
type ScopedScoring struct {
	scoring.Repository
	Scopes SubscriptionScopes
}

func (s ScopedScoring) Leaderboard(ctx context.Context, chatID common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	narrowed, err := s.Scopes.Narrow(ctx, chatID, period)
	if err != nil {
		return nil, err
	}
	return s.Repository.Leaderboard(ctx, chatID, narrowed)
}

// ScopedProgression is ScopedScoring for the rating chart.
type ScopedProgression struct {
	Inner interface {
		PointsProgression(ctx context.Context, chatID common.ChatID, period scoring.StatsPeriod) ([]scoring.ProgressionPoint, error)
	}
	Scopes SubscriptionScopes
}

func (s ScopedProgression) PointsProgression(ctx context.Context, chatID common.ChatID, period scoring.StatsPeriod) ([]scoring.ProgressionPoint, error) {
	narrowed, err := s.Scopes.Narrow(ctx, chatID, period)
	if err != nil {
		return nil, err
	}
	return s.Inner.PointsProgression(ctx, chatID, narrowed)
}

// FollowService subscribes chats to teams and players.
type FollowService struct {
	Targets     subscription.TargetRepository
	Catalog     competition.Catalog
	Predictions *prediction.Service
	Chats       chat.Repository
	Clock       common.Clock
	Log         *slog.Logger
}

// Follow subscribes and opens polls for the target's already-known
// upcoming matches, as a tournament subscription does. Returns how many
// polls were opened.
func (f *FollowService) Follow(ctx context.Context, settings chat.Settings, target subscription.Target, topic *int64) (int, error) {
	if err := f.Targets.SubscribeTarget(ctx, settings.ChatID, target, f.Clock.Now()); err != nil {
		return 0, err
	}
	resolved, err := f.Targets.ResolveTeams(ctx, []subscription.Target{target})
	if err != nil {
		return 0, err
	}
	teams := map[common.TeamID]bool{}
	for _, id := range subscription.TeamsOf([]subscription.Target{target}, resolved) {
		teams[id] = true
	}
	if len(teams) == 0 || f.Predictions == nil {
		return 0, nil
	}
	eventIDs, err := f.Targets.FollowedEventIDs(ctx)
	if err != nil {
		return 0, err
	}
	matches, err := f.Catalog.FindUnstartedMatchesForEvents(ctx, eventIDs)
	if err != nil {
		return 0, err
	}
	if topic == nil {
		topic = settings.DefaultTopicID
	}
	opened := 0
	for _, m := range matches {
		if !involves(m, teams) {
			continue
		}
		if _, err := f.Predictions.Create(ctx, m, settings.ChatID, topic); err != nil {
			f.Log.Warn("follow poll creation failed", "chatId", settings.ChatID.Value, "matchId", m.ID.Value, "error", err)
			continue
		}
		opened++
	}
	return opened, nil
}

func involves(m competition.Match, teams map[common.TeamID]bool) bool {
	return (m.FirstTeam != nil && teams[m.FirstTeam.ID]) || (m.SecondTeam != nil && teams[m.SecondTeam.ID])
}

func matchTeamIDs(m competition.Match) []common.TeamID {
	var out []common.TeamID
	for _, t := range []*competition.Team{m.FirstTeam, m.SecondTeam} {
		if t != nil {
			out = append(out, t.ID)
		}
	}
	return out
}

// followerChats lists chats reached by this match only through a
// team/player target, excluding the tournament's own subscribers.
func (s *CompetitionSynchronization) followerChats(ctx context.Context, m competition.Match, subscribed []common.ChatID) ([]common.ChatID, error) {
	if s.Targets == nil {
		return nil, nil
	}
	followers, err := s.Targets.ChatsFollowingTeams(ctx, matchTeamIDs(m))
	if err != nil {
		return nil, err
	}
	already := make(map[common.ChatID]bool, len(subscribed))
	for _, id := range subscribed {
		already[id] = true
	}
	var out []common.ChatID
	for _, id := range followers {
		if !already[id] {
			already[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// offerTournament proposes the tournament to a follower chat through the
// big-event offer channel, gated by the same new-events switch and
// throttled to one offer per event and one per chat per cooldown.
func (s *CompetitionSynchronization) offerTournament(ctx context.Context, settings chat.Settings, event competition.Event, reason string) error {
	if s.Outbox == nil || s.Targets == nil || !event.IsSubscribable(s.Clock.Now()) || !settings.GameEnabled(event.Game) {
		return nil
	}
	wants, err := NotifyGate{Switches: s.Switches}.ChatWants(ctx, settings.ChatID, common.ChatNotifyNewEvents)
	if err != nil || !wants {
		return err
	}
	claimed, err := s.Targets.ClaimCrossSellOffer(ctx, settings.ChatID, event.ID, s.Clock.Now(), CrossSellCooldown)
	if err != nil || !claimed {
		return err
	}
	payload, err := json.Marshal(common.BigEventDiscoveredNotification{
		ChatID: settings.ChatID.Value, TopicID: settings.DefaultTopicID,
		EventID: event.ID.Value.String(), EventName: event.Name, Tier: string(event.Tier), Reason: reason,
	})
	if err != nil {
		return err
	}
	aggregateID := fmt.Sprintf("%d:cross-sell:%s", settings.ChatID.Value, event.ID.Value)
	_, err = s.Outbox.Enqueue(ctx, "TELEGRAM_CHAT", aggregateID, "telegram.follow-cross-sell", string(payload))
	return err
}

// crossSell is best effort: a failed offer never affects the poll.
func (s *CompetitionSynchronization) crossSell(ctx context.Context, settings chat.Settings, m competition.Match) {
	event, err := s.Catalog.FindEvent(ctx, m.EventID)
	if err != nil || event == nil {
		return
	}
	targets, err := s.Targets.Targets(ctx, settings.ChatID)
	if err != nil {
		s.Log.Warn("cross-sell targets lookup failed", "chatId", settings.ChatID.Value, "error", err)
		return
	}
	resolved, err := s.Targets.ResolveTeams(ctx, targets)
	if err != nil {
		s.Log.Warn("cross-sell resolve failed", "chatId", settings.ChatID.Value, "error", err)
		return
	}
	if err := s.offerTournament(ctx, settings, *event, followReason(m, targets, resolved)); err != nil {
		s.Log.Warn("cross-sell offer failed", "chatId", settings.ChatID.Value, "eventId", event.ID.Value, "error", err)
	}
}

func followReason(m competition.Match, targets []subscription.Target, resolved map[string][]common.TeamID) string {
	teams := map[common.TeamID]bool{}
	for _, id := range matchTeamIDs(m) {
		teams[id] = true
	}
	for _, t := range targets {
		for _, id := range resolved[t.Key()] {
			if teams[id] {
				return t.Label
			}
		}
	}
	return ""
}
