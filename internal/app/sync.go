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
	// TeamMatch resolves a team with no cached Valve VRS ranking against
	// Valve's feed (auto-accepting a near-certain name match, or opening a
	// review request) — nil disables the whole pipeline, same as any other
	// optional enrichment dependency here.
	TeamMatch *TeamMatchService
	Outbox    common.Outbox
	Lock      common.ClusterLock
	Clock     common.Clock
	Metrics   *Metrics
	Log       *slog.Logger
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
		failed := 0
		for _, event := range loaded {
			if err := s.discoverOneEvent(ctx, event); err != nil {
				s.Log.Error("event discovery failed for one event, continuing with the rest", "eventId", event.ID.Value, "error", err)
				failed++
				continue
			}
		}
		// "partial" (rather than always "success") makes a run that silently
		// dropped some events visible in metrics — without it, a dashboard/
		// alert keyed on the failure label would never fire for this job no
		// matter how many individual events failed, since the run itself
		// always returns nil to guarded().
		result := "success"
		if failed > 0 {
			result = "partial"
		}
		s.Metrics.SyncRuns.WithLabelValues("events", result).Inc()
		s.Metrics.SyncEntities.WithLabelValues("events").Observe(float64(len(loaded)))
		s.Log.Info("event catalog synchronized", "count", len(loaded), "failed", failed)
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
	// Only a tournament this bot watched while it was running can have a
	// result worth completing. One that is already over the first time it
	// is seen — the whole back catalogue a newly enabled game brings in —
	// has no chat subscribed to it and no prediction in it.
	if event.Status == competition.EventFinished && previous != nil && previous.Status != competition.EventFinished {
		if err := s.EventCompletion.Complete(ctx, event); err != nil {
			return err
		}
	}
	// A tournament that is already over when it first appears in the
	// catalog is not news: announcing it invites a chat to subscribe to a
	// finished event, and auto-subscribing one drags it into the completion
	// recap that fires the next time its matches sync. Enabling a new game
	// discovers months of that game's history at once, which is how a chat
	// ended up with a burst of recaps for tournaments it never followed.
	if previous == nil && event.Tier.IsTopTier() && event.IsSubscribable(s.Clock.Now()) {
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
		failed := 0
		for _, m := range loaded {
			if procErr := s.processMatch(ctx, m); procErr != nil {
				s.Log.Error("match processing failed for one match, continuing with the rest", "matchId", m.ID.Value, "error", procErr)
				failed++
				continue
			}
		}
		if err != nil {
			return err
		}
		// "partial" (rather than always "success") makes a run that silently
		// dropped some matches visible in metrics — see the identical
		// comment in DiscoverEvents above.
		result := "success"
		if failed > 0 {
			result = "partial"
		}
		s.Metrics.SyncRuns.WithLabelValues("matches", result).Inc()
		s.Metrics.SyncEntities.WithLabelValues("matches").Observe(float64(len(loaded)))
		s.Log.Info("global match snapshot synchronized", "events", len(activeEvents), "matches", len(loaded), "failed", failed)
		return nil
	})
}

func (s *CompetitionSynchronization) CloseDuePolls(ctx context.Context) {
	s.guarded(ctx, "close-due-polls", func(ctx context.Context) error {
		count, err := s.Predictions.CloseDue(ctx, s.Clock.Now())
		if err != nil {
			if count == 0 {
				return err
			}
			// Some polls closed even though others in this batch failed —
			// same per-item resilience as DiscoverEvents/SynchronizeMatches
			// above: a partial batch must not be reported as a failed run
			// (guarded() would log/count it as a total failure), but the
			// failures themselves must still be visible rather than
			// silently swallowed.
			s.Log.Error("closing due polls: some polls failed to close, continuing with the rest", "closed", count, "error", err)
		}
		if count > 0 {
			s.Log.Info("closed due prediction polls", "count", count)
		}
		return nil
	})
}

// reconcileTeamOrder anchors incoming's FirstTeam/SecondTeam (and, with it,
// Score) to whichever order was established the first time this match's
// participants were resolved and persisted (previous) — a real production
// incident: PandaScore's own opponents[] order for a match isn't guaranteed
// stable across two separate API calls, and prediction.Service.Create builds
// every poll option's MatchScore{First, Second} from the match snapshot at
// the moment its participants first became known (sync.go's
// MatchNotStarted branch below). If a later fetch (e.g. the one that
// reports the match as finished) returns the same two teams in the opposite
// order, mapMatch would relabel FirstTeam/SecondTeam accordingly and the
// final score would then be compared against poll options built under the
// original labeling — silently crediting the wrong side's voters. Swapping
// back here, before the match is ever saved or settled, keeps every
// downstream consumer (SaveMatch, Settle, notifications) working with one
// stable team identity → position mapping for the lifetime of the match.
func reconcileTeamOrder(previous *competition.Match, incoming competition.Match) competition.Match {
	if previous == nil || previous.FirstTeam == nil || previous.SecondTeam == nil ||
		incoming.FirstTeam == nil || incoming.SecondTeam == nil {
		return incoming
	}
	if previous.FirstTeam.ID != incoming.SecondTeam.ID || previous.SecondTeam.ID != incoming.FirstTeam.ID {
		return incoming
	}
	incoming.FirstTeam, incoming.SecondTeam = incoming.SecondTeam, incoming.FirstTeam
	if incoming.Score != nil {
		incoming.Score.First, incoming.Score.Second = incoming.Score.Second, incoming.Score.First
	}
	return incoming
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (s *CompetitionSynchronization) processMatch(ctx context.Context, incoming competition.Match) error {
	previous, err := s.Catalog.FindMatch(ctx, incoming.ID)
	if err != nil {
		return err
	}
	incoming = reconcileTeamOrder(previous, incoming)
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
		return s.fanOutNewPolls(ctx, incoming, s.ensureTeamsMatched(ctx, incoming))
	}
	return nil
}

// ensureTeamsMatched is the identity-resolution trigger point: a team only
// ever gets checked against a ranking feed once it actually shows up in a
// match about to be predicted, not for every team that feed ranks (most of
// which no subscribed chat will ever see) — see
// TeamMatchService.EnsureRequests. Called once per match, not once per
// chat, since the check is identical regardless of which chats end up
// seeing the resulting poll.
func (s *CompetitionSynchronization) ensureTeamsMatched(ctx context.Context, m competition.Match) []common.RequestID {
	if s.TeamMatch == nil {
		return nil
	}
	var pending []common.RequestID
	for _, team := range [2]*competition.Team{m.FirstTeam, m.SecondTeam} {
		if team == nil {
			continue
		}
		pending = append(pending, s.TeamMatch.EnsureRequests(ctx, *team)...)
	}
	return pending
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
		if !settings.GameEnabled(event.Game) {
			continue
		}
		// AutoSubscribeTopTier skips the offer entirely and just joins the
		// chat to the tournament — still announced, but as a fait accompli
		// ("auto-subscribed") rather than a "want to add this?" the chat
		// would otherwise have to tap through every single time a new S/A
		// tournament shows up.
		eventType, notifyType := "telegram.big-event-discovered", "big event discovered"
		if settings.AutoSubscribeTopTier {
			if _, err := s.Subscriptions.Subscribe(ctx, subscription.EventSubscription{ChatID: settings.ChatID, EventID: event.ID}); err != nil {
				s.Log.Error("auto-subscribe failed", "chatId", settings.ChatID.Value, "eventId", event.ID.Value, "error", err)
				continue
			}
			eventType, notifyType = "telegram.auto-subscribed", "auto-subscribed"
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
		if _, err := s.Outbox.Enqueue(ctx, "TELEGRAM_CHAT", aggregateID, eventType, string(payload)); err != nil {
			// One chat's enqueue failure (a transient DB blip) shouldn't
			// stop the rest of the batch from being notified — same
			// per-item resilience as fanOutNewPolls below.
			s.Log.Error(notifyType+" announcement enqueue failed", "chatId", settings.ChatID.Value, "eventId", event.ID.Value, "error", err)
			continue
		}
	}
	return nil
}

func (s *CompetitionSynchronization) fanOutNewPolls(ctx context.Context, incoming competition.Match, pendingTeamMatches []common.RequestID) error {
	chatIDs, err := s.Subscriptions.SubscribedChats(ctx, incoming.EventID)
	if err != nil {
		return err
	}
	for _, chatID := range chatIDs {
		// One chat's lookup failing (a transient DB blip) must not stop
		// every chat ordered after it in chatIDs from getting this poll —
		// same per-item resilience as announceBigEvent above. Unlike a
		// failed Predictions.Create below, there's no later retry path for
		// a chat skipped here: the trigger for this whole fan-out
		// (incoming.ParticipantsKnown() flipping from false to true) is
		// one-shot per match, so silently aborting the rest of chatIDs would
		// permanently deny them this poll rather than just delaying it.
		settings, err := s.Chats.Find(ctx, chatID)
		if err != nil {
			s.Log.Error("chat lookup failed, skipping this chat's poll", "chatId", chatID.Value, "matchId", incoming.ID.Value, "error", err)
			continue
		}
		if settings == nil {
			continue
		}
		topic, err := s.Chats.EventTopic(ctx, chatID, incoming.EventID)
		if err != nil {
			s.Log.Error("event topic lookup failed, skipping this chat's poll", "chatId", chatID.Value, "matchId", incoming.ID.Value, "error", err)
			continue
		}
		if topic == nil {
			topic = settings.DefaultTopicID
		}
		if _, err := s.Predictions.Create(ctx, incoming, chatID, topic); err != nil {
			s.Log.Error("poll creation failed", "chatId", chatID.Value, "matchId", incoming.ID.Value, "error", err)
			continue
		}
		s.Metrics.PredictionPolls.WithLabelValues("created").Inc()

		// This chat's own voters are a natural, contextual crowd to ask
		// about a team whose Valve VRS identity is still unresolved — they
		// are about to see (or already play in) matches for exactly this
		// team. Best effort: a failure here must not affect the poll that
		// was already successfully created above.
		for _, reqID := range pendingTeamMatches {
			if err := s.TeamMatch.AskChatHelpers(ctx, reqID, chatID, settings.Locale); err != nil {
				s.Log.Error("team match ask helpers failed", "chatId", chatID.Value, "requestId", reqID.String(), "error", err)
			}
		}
	}
	return nil
}
