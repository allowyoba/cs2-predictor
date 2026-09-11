package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// EventCompletionService awards medals and publishes a "the event is over"
// notification once every match in an event has reached a terminal state,
// guarded per-chat by a content hash of the final standings so re-running
// it after nothing has changed is a no-op.
type EventCompletionService struct {
	catalog       competition.Catalog
	subscriptions subscription.Repository
	chats         chat.Repository
	scoringRepo   scoring.Repository
	outbox        common.Outbox
	clock         common.Clock
	runTx         TxRunner
	log           *slog.Logger
}

func NewEventCompletionService(catalog competition.Catalog, subscriptions subscription.Repository, chats chat.Repository,
	scoringRepo scoring.Repository, outbox common.Outbox, clock common.Clock, runTx TxRunner, log *slog.Logger) *EventCompletionService {
	return &EventCompletionService{
		catalog: catalog, subscriptions: subscriptions, chats: chats,
		scoringRepo: scoringRepo, outbox: outbox, clock: clock, runTx: runTx, log: log,
	}
}

var terminalMatchStatuses = map[competition.MatchStatus]bool{
	competition.MatchFinished:  true,
	competition.MatchCancelled: true,
	competition.MatchForfeit:   true,
}

// Complete defers (does nothing) until every match in event has reached a
// terminal state (FINISHED/CANCELLED/FORFEIT) — event completion only fires
// once, not per-match.
func (s *EventCompletionService) Complete(ctx context.Context, event competition.Event) error {
	matches, err := s.catalog.FindMatches(ctx, event.ID)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}
	for _, m := range matches {
		if !terminalMatchStatuses[m.Status] {
			return nil
		}
	}

	chatIDs, err := s.subscriptions.SubscribedChats(ctx, event.ID)
	if err != nil {
		return err
	}
	// One chat's completion failing (a transient DB blip, or a chat whose
	// data is in a bad state) must not stop every other subscribed chat's
	// completion from running — same per-item resilience as
	// CompetitionSynchronization's announceBigEvent/fanOutNewPolls. Each
	// chat is independent and individually idempotency-guarded (the
	// EventCompletionHash check in completeForChat), so a retried next run
	// safely picks up wherever this one left off.
	for _, chatID := range chatIDs {
		if err := s.completeForChat(ctx, event, chatID); err != nil {
			if s.log != nil {
				s.log.Error("event completion failed for one chat, continuing with the rest",
					"chatId", chatID.Value, "eventId", event.ID.Value, "error", err)
			}
			continue
		}
	}
	return nil
}

func (s *EventCompletionService) completeForChat(ctx context.Context, event competition.Event, chatID common.ChatID) error {
	standings, err := s.scoringRepo.Leaderboard(ctx, chatID, scoring.ForEvent(event.ID))
	if err != nil {
		return err
	}
	hash := completionHash(standings)

	existing, ok, err := s.scoringRepo.EventCompletionHash(ctx, chatID, event.ID)
	if err != nil {
		return err
	}
	if ok && existing == hash {
		return nil // idempotency guard: nothing changed since last completion pass
	}

	return s.runTx(ctx, func(txCtx context.Context) error {
		if err := s.scoringRepo.AwardMedals(txCtx, chatID, event.ID, standings, s.clock.Now()); err != nil {
			return err
		}

		var topicID *int64
		if t, err := s.chats.EventTopic(txCtx, chatID, event.ID); err != nil {
			return err
		} else if t != nil {
			topicID = t
		} else if settings, err := s.chats.Find(txCtx, chatID); err != nil {
			return err
		} else if settings != nil {
			topicID = settings.DefaultTopicID
		}

		notificationStandings := make([]common.StandingNotification, len(standings))
		for i, st := range standings {
			notificationStandings[i] = common.StandingNotification{
				UserID: st.UserID.Value, DisplayName: st.DisplayName, Rank: st.Rank,
				PreviousRank: nil, Points: st.Points, PointsDelta: 0,
			}
		}
		notification := common.EventFinishedNotification{ChatID: chatID.Value, TopicID: topicID, EventName: event.Name, Standings: notificationStandings}
		payload, err := json.Marshal(notification)
		if err != nil {
			return err
		}
		if _, err := s.outbox.Enqueue(txCtx, "EVENT", event.ID.Value.String(), "telegram.event-finished", string(payload)); err != nil {
			return err
		}

		return s.scoringRepo.MarkEventCompleted(txCtx, chatID, event.ID, hash, s.clock.Now())
	})
}

// completionHash is a cheap content hash of the final standings
// ("<userId>:<rank>:<points>" rows joined by "|"), used only to detect
// whether anything changed since the last completion pass.
func completionHash(standings []scoring.UserStanding) string {
	parts := make([]string, len(standings))
	for i, s := range standings {
		parts[i] = fmt.Sprintf("%d:%d:%d", s.UserID.Value, s.Rank, s.Points)
	}
	return strings.Join(parts, "|")
}
