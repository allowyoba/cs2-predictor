package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

const (
	// DefaultEventEveLead is how far ahead of the first match the nudge
	// goes out. A day is the span that still leaves room to act on it —
	// the 30-minutes-before nudge is already covered by
	// PollReminderScheduler, and repeating that here would be noise.
	DefaultEventEveLead = 24 * time.Hour
	// eventEveMatchesShown bounds the opening matches listed by name. The
	// message is a hook, not a schedule: the full list is one tap away
	// behind the "upcoming matches" button the publisher attaches.
	eventEveMatchesShown = 3
	eventEveReportType   = "EVENT_EVE"
)

// EventEveScheduler posts one message to a subscribed chat the day before a
// tournament it follows begins.
//
// It deliberately leads with substance — when the first match is, how many
// there are that day, who opens, and who won the chat's last tournament —
// rather than a bare "are you ready?". A notification that carries no
// information is the kind people learn to swipe away, and this is a chat
// message rather than a push: it has to earn its place in the room. One
// message per tournament per chat, claimed through ScheduledReportStore, so
// a restart or a second instance cannot repeat it.
type EventEveScheduler struct {
	Chats         chat.ActiveChatLister
	ChatSettings  EventTopicLookup
	Subscriptions subscription.Repository
	Catalog       competition.Catalog
	Scoring       scoring.Repository
	Store         ScheduledReportStore
	Outbox        common.Outbox
	Lock          common.ClusterLock
	Clock         common.Clock
	Log           *slog.Logger

	// Lead is how far ahead of the first match to post; zero uses
	// DefaultEventEveLead. Negative disables the job.
	Lead time.Duration
}

// EventTopicLookup resolves which forum topic a chat's tournament messages
// belong in — the same two-step (per-event binding, then the chat default)
// EventCompletionService applies.
type EventTopicLookup interface {
	EventTopic(ctx context.Context, chatID common.ChatID, eventID common.EventID) (*int64, error)
	Find(ctx context.Context, chatID common.ChatID) (*chat.Settings, error)
}

func (s *EventEveScheduler) lead() time.Duration {
	if s.Lead == 0 {
		return DefaultEventEveLead
	}
	return s.Lead
}

func (s *EventEveScheduler) Dispatch(ctx context.Context) {
	if s.lead() < 0 {
		return
	}
	_, err := s.Lock.Execute(ctx, "cs2predictor:event-eve", func(ctx context.Context) error {
		return s.dispatch(ctx)
	})
	if err != nil {
		s.Log.Error("event eve dispatch failed", "error", err)
	}
}

func (s *EventEveScheduler) dispatch(ctx context.Context) error {
	chats, err := s.Chats.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, settings := range chats {
		if err := s.dispatchChat(ctx, settings); err != nil {
			// One chat's failure must not cost every other chat its nudge.
			s.Log.Error("event eve failed for chat", "chatId", settings.ChatID.Value, "error", err)
		}
	}
	return nil
}

func (s *EventEveScheduler) dispatchChat(ctx context.Context, settings chat.Settings) error {
	subs, err := s.Subscriptions.Subscriptions(ctx, settings.ChatID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	eventIDs := make([]common.EventID, 0, len(subs))
	for _, sub := range subs {
		eventIDs = append(eventIDs, sub.EventID)
	}
	matches, err := s.Catalog.FindUnstartedMatchesForEvents(ctx, eventIDs)
	if err != nil {
		return err
	}

	now := s.Clock.Now()
	byEvent := map[common.EventID][]competition.Match{}
	for _, m := range matches {
		if m.ScheduledAt == nil || !m.ScheduledAt.After(now) {
			continue
		}
		byEvent[m.EventID] = append(byEvent[m.EventID], m)
	}

	for _, eventID := range eventIDs {
		if err := s.notifyIfDue(ctx, settings, eventID, byEvent[eventID], now); err != nil {
			return err
		}
	}
	return nil
}

// notifyIfDue posts the nudge for one event once its first match is inside
// the lead window. Claimed/Claim are the same cheap-check-then-atomic-claim
// pair the digests use: the pre-check skips the payload work for a
// tournament already announced, the claim is what actually makes it once.
func (s *EventEveScheduler) notifyIfDue(ctx context.Context, settings chat.Settings, eventID common.EventID,
	upcoming []competition.Match, now time.Time) error {
	if len(upcoming) == 0 {
		return nil
	}
	sort.Slice(upcoming, func(i, j int) bool { return upcoming[i].ScheduledAt.Before(*upcoming[j].ScheduledAt) })
	if upcoming[0].ScheduledAt.Sub(now) > s.lead() {
		return nil // still too far out
	}

	periodKey := eventID.Value.String()
	if claimed, err := s.Store.Claimed(ctx, settings.ChatID, eventEveReportType, periodKey); err != nil || claimed {
		return err
	}
	event, err := s.Catalog.FindEvent(ctx, eventID)
	if err != nil || event == nil {
		return err
	}
	claimed, err := s.Store.Claim(ctx, settings.ChatID, eventEveReportType, periodKey)
	if err != nil || !claimed {
		return err
	}

	notification, err := s.build(ctx, settings, *event, upcoming)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	_, err = s.Outbox.Enqueue(ctx, "EVENT", eventID.Value.String(), "telegram.event-eve", string(payload))
	return err
}

func (s *EventEveScheduler) build(ctx context.Context, settings chat.Settings, event competition.Event,
	upcoming []competition.Match) (common.EventEveNotification, error) {
	loc := chat.ZoneOrDefault(settings.Timezone)
	first := upcoming[0].ScheduledAt.In(loc)

	// "The opening day" is the chat's own calendar day, not a 24-hour
	// window: a tournament that starts at 22:00 local has a two-match
	// evening, and saying "12 matches" there would be a different day's
	// number.
	firstDay := 0
	var shown []common.EventEveMatchNotification
	for _, m := range upcoming {
		local := m.ScheduledAt.In(loc)
		if local.Year() != first.Year() || local.YearDay() != first.YearDay() {
			continue
		}
		firstDay++
		if len(shown) < eventEveMatchesShown {
			shown = append(shown, common.EventEveMatchNotification{
				LocalTime:  local.Format("15:04"),
				FirstTeam:  teamName(m.FirstTeam),
				SecondTeam: teamName(m.SecondTeam),
			})
		}
	}

	notification := common.EventEveNotification{
		ChatID: settings.ChatID.Value, EventName: event.Name,
		StartsAt: first.Format("02.01 15:04"), FirstDayMatches: firstDay, Matches: shown,
	}
	topicID, err := s.topic(ctx, settings, event.ID)
	if err != nil {
		return notification, err
	}
	notification.TopicID = topicID
	champion, err := s.previousChampion(ctx, settings.ChatID, event.ID)
	if err != nil {
		// A missing champion is a missing line, not a missing message.
		s.Log.Error("event eve champion lookup failed", "chatId", settings.ChatID.Value, "error", err)
		return notification, nil
	}
	notification.Champion = champion
	return notification, nil
}

func (s *EventEveScheduler) topic(ctx context.Context, settings chat.Settings, eventID common.EventID) (*int64, error) {
	if s.ChatSettings == nil {
		return settings.DefaultTopicID, nil
	}
	topic, err := s.ChatSettings.EventTopic(ctx, settings.ChatID, eventID)
	if err != nil {
		return nil, err
	}
	if topic != nil {
		return topic, nil
	}
	return settings.DefaultTopicID, nil
}

// previousChampion is whoever topped this chat's most recent finished
// tournament — the reigning title the new one is played for. Empty when the
// chat has no finished tournament yet, which is simply a chat that has not
// crowned anybody.
func (s *EventEveScheduler) previousChampion(ctx context.Context, chatID common.ChatID, upcomingEventID common.EventID) (string, error) {
	past, err := s.Scoring.AvailableEventIDs(ctx, chatID)
	if err != nil {
		return "", err
	}
	for _, id := range past {
		if id == upcomingEventID {
			continue
		}
		standings, err := s.Scoring.Leaderboard(ctx, chatID, scoring.ForEvent(id))
		if err != nil {
			return "", err
		}
		if len(standings) == 0 {
			continue
		}
		return standings[0].DisplayName, nil
	}
	return "", nil
}

func teamName(team *competition.Team) string {
	if team == nil {
		return ""
	}
	return team.Name
}
