package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// CompetitionSynchronization runs the three scheduled jobs that keep the
// Postgres catalog in sync with the configured competition providers and
// react to state changes. Every run is guarded by ClusterLock so only one
// instance in a multi-instance deployment executes it at a time.
type CompetitionSynchronization struct {
	Gateway       *CompetitionProviderGateway
	Catalog       competition.Catalog
	Subscriptions subscription.Repository
	Chats         chat.Repository
	// ActiveChats is the chat-wide fan-out view (announceBigEvent). Declared
	// as its own narrow dependency rather than type-asserted off Chats at
	// call time, so a wiring mistake is a compile error instead of a feature
	// that silently announces nothing.
	ActiveChats     chat.ActiveChatLister
	Predictions     *prediction.Service
	Settlement      *ResultSettlementService
	EventCompletion *EventCompletionService
	Outbox          common.Outbox
	Lock            common.ClusterLock
	Clock           common.Clock
	Metrics         *Metrics
	Log             *slog.Logger
}

func (s *CompetitionSynchronization) guarded(ctx context.Context, name string, action func(ctx context.Context) error) {
	acquired, err := s.Lock.Execute(ctx, "cs2predictor:"+name, action)
	if err != nil {
		s.Metrics.SyncRuns.WithLabelValues(name, "failure").Inc()
		s.Log.Error("scheduled job failed", "job", name, "error", err)
		return
	}
	if !acquired {
		s.Log.Debug("scheduled job skipped, lock held elsewhere", "job", name)
	}
}

func (s *CompetitionSynchronization) DiscoverEvents(ctx context.Context) {
	s.guarded(ctx, "discover-events", func(ctx context.Context) error {
		loaded, err := s.Gateway.UpcomingEvents(ctx)
		if err != nil {
			return err
		}
		// One event's processing failing (a transient DB blip, or bad data
		// for that one event) must not stop every event ordered after it in
		// this batch from being discovered — same per-item resilience as
		// announceBigEvent/fanOutNewPolls below. Each event is independent
		// and a retried next run safely reprocesses whatever didn't finish.
		for _, event := range loaded {
			if err := s.discoverOneEvent(ctx, event); err != nil {
				s.Log.Error("event discovery failed for one event, continuing with the rest", "eventId", event.ID.Value, "error", err)
				continue
			}
		}
		s.Metrics.SyncRuns.WithLabelValues("events", "success").Inc()
		s.Metrics.SyncEntities.WithLabelValues("events").Observe(float64(len(loaded)))
		s.Log.Info("event catalog synchronized", "count", len(loaded))
		return nil
	})
}

func (s *CompetitionSynchronization) discoverOneEvent(ctx context.Context, event competition.Event) error {
	previous, err := s.Catalog.FindEvent(ctx, event.ID)
	if err != nil {
		return err
	}
	if _, err := s.Catalog.SaveEvent(ctx, event); err != nil {
		return err
	}
	if event.Status == competition.EventFinished && (previous == nil || previous.Status != competition.EventFinished) {
		if err := s.EventCompletion.Complete(ctx, event); err != nil {
			return err
		}
	}
	if previous == nil && event.Tier.IsTopTier() {
		if err := s.announceBigEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// SynchronizeMatches fetches a single global, distinct match snapshot for
// the union of every actively-subscribed event — fan-out to chats happens
// only after persistence, never a second external call per chat.
func (s *CompetitionSynchronization) SynchronizeMatches(ctx context.Context) {
	s.guarded(ctx, "synchronize-matches", func(ctx context.Context) error {
		eventIDs, err := s.Subscriptions.ActiveEventIDs(ctx)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		var activeEvents []competition.Event
		for _, id := range eventIDs {
			key := id.Value.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			event, err := s.Catalog.FindEvent(ctx, id)
			if err != nil {
				return err
			}
			if event != nil {
				activeEvents = append(activeEvents, *event)
			}
		}
		if len(activeEvents) == 0 {
			return nil
		}

		// Matches can return both a non-empty slice AND an error: one
		// provider group failing (e.g. an open circuit breaker) must not
		// discard matches successfully fetched for a different, healthy
		// provider's events — so whatever did come back is processed
		// (and persisted) regardless, before the error is surfaced. Each
		// match is independent of the others, so — same per-item
		// resilience as announceBigEvent/fanOutNewPolls below — one match's
		// processing failure must not stop every match ordered after it in
		// this batch from being processed too.
		loaded, err := s.Gateway.Matches(ctx, activeEvents)
		for _, m := range loaded {
			if procErr := s.processMatch(ctx, m); procErr != nil {
				s.Log.Error("match processing failed for one match, continuing with the rest", "matchId", m.ID.Value, "error", procErr)
				continue
			}
		}
		if err != nil {
			return err
		}
		s.Metrics.SyncRuns.WithLabelValues("matches", "success").Inc()
		s.Metrics.SyncEntities.WithLabelValues("matches").Observe(float64(len(loaded)))
		s.Log.Info("global match snapshot synchronized", "events", len(activeEvents), "matches", len(loaded))
		return nil
	})
}

func (s *CompetitionSynchronization) CloseDuePolls(ctx context.Context) {
	s.guarded(ctx, "close-due-polls", func(ctx context.Context) error {
		count, err := s.Predictions.CloseDue(ctx, s.Clock.Now())
		if err != nil {
			return err
		}
		if count > 0 {
			s.Log.Info("closed due prediction polls", "count", count)
		}
		return nil
	})
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (s *CompetitionSynchronization) processMatch(ctx context.Context, incoming competition.Match) error {
	previous, err := s.Catalog.FindMatch(ctx, incoming.ID)
	if err != nil {
		return err
	}
	if _, err := s.Catalog.SaveMatch(ctx, incoming); err != nil {
		return err
	}

	switch {
	case incoming.ShouldCancelPrediction():
		if _, err := s.Predictions.CancelForMatch(ctx, incoming.ID); err != nil {
			return err
		}
	case incoming.Status == competition.MatchPostponed && incoming.ScheduledAt != nil:
		if _, err := s.Predictions.Reschedule(ctx, incoming.ID, *incoming.ScheduledAt); err != nil {
			return err
		}
	case incoming.Status == competition.MatchRunning:
		if _, err := s.Predictions.CloseForRunningMatch(ctx, incoming.ID); err != nil {
			return err
		}
	case incoming.Status == competition.MatchFinished:
		event, err := s.Catalog.FindEvent(ctx, incoming.EventID)
		if err != nil {
			return err
		}
		if event != nil {
			if _, err := s.Settlement.Settle(ctx, *event, incoming); err != nil {
				return err
			}
			if event.Status == competition.EventFinished {
				if err := s.EventCompletion.Complete(ctx, *event); err != nil {
					return err
				}
			}
		}
	}

	if incoming.Status == competition.MatchNotStarted && incoming.ParticipantsKnown() &&
		incoming.ScheduledAt != nil && incoming.ScheduledAt.After(s.Clock.Now()) &&
		(previous == nil || !previous.ParticipantsKnown()) {
		return s.fanOutNewPolls(ctx, incoming)
	}
	return nil
}

// announceBigEvent fans out a "new big event" suggestion, via the
// transactional outbox, to every active chat not already subscribed to a
// newly discovered S/A tier tournament — so it surfaces immediately instead
// of only being reachable through an explicit /events search.
func (s *CompetitionSynchronization) announceBigEvent(ctx context.Context, event competition.Event) error {
	if s.ActiveChats == nil {
		return nil
	}
	chats, err := s.ActiveChats.ListActive(ctx)
	if err != nil {
		return err
	}
	if len(chats) == 0 {
		return nil
	}
	subscribed, err := s.Subscriptions.SubscribedChats(ctx, event.ID)
	if err != nil {
		return err
	}
	alreadySubscribed := make(map[common.ChatID]bool, len(subscribed))
	for _, chatID := range subscribed {
		alreadySubscribed[chatID] = true
	}

	for _, settings := range chats {
		if alreadySubscribed[settings.ChatID] {
			continue
		}
		n := common.BigEventDiscoveredNotification{
			ChatID: settings.ChatID.Value, TopicID: settings.DefaultTopicID,
			EventID: event.ID.Value.String(), EventName: event.Name, Tier: string(event.Tier),
		}
		payload, err := json.Marshal(n)
		if err != nil {
			return err
		}
		aggregateID := fmt.Sprintf("%d:big-event:%s", settings.ChatID.Value, event.ID.Value)
		if _, err := s.Outbox.Enqueue(ctx, "TELEGRAM_CHAT", aggregateID, "telegram.big-event-discovered", string(payload)); err != nil {
			// One chat's enqueue failure (a transient DB blip) shouldn't
			// stop the rest of the batch from being notified — same
			// per-item resilience as fanOutNewPolls below.
			s.Log.Error("big event announcement enqueue failed", "chatId", settings.ChatID.Value, "eventId", event.ID.Value, "error", err)
			continue
		}
	}
	return nil
}

func (s *CompetitionSynchronization) fanOutNewPolls(ctx context.Context, incoming competition.Match) error {
	chatIDs, err := s.Subscriptions.SubscribedChats(ctx, incoming.EventID)
	if err != nil {
		return err
	}
	for _, chatID := range chatIDs {
		settings, err := s.Chats.Find(ctx, chatID)
		if err != nil {
			return err
		}
		if settings == nil {
			continue
		}
		topic, err := s.Chats.EventTopic(ctx, chatID, incoming.EventID)
		if err != nil {
			return err
		}
		if topic == nil {
			topic = settings.DefaultTopicID
		}
		if _, err := s.Predictions.Create(ctx, incoming, chatID, topic); err != nil {
			s.Log.Error("poll creation failed", "chatId", chatID.Value, "matchId", incoming.ID.Value, "error", err)
		} else {
			s.Metrics.PredictionPolls.WithLabelValues("created").Inc()
		}
	}
	return nil
}
