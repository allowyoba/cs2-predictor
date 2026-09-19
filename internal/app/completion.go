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
	// specials backs the nominations attached to the recap. Optional: a
	// deployment without it still gets standings, just no awards.
	specials scoring.EventSpecialsRepository
	outbox   common.Outbox
	clock    common.Clock
	runTx    TxRunner
	log      *slog.Logger
}

func NewEventCompletionService(catalog competition.Catalog, subscriptions subscription.Repository, chats chat.Repository,
	scoringRepo scoring.Repository, specials scoring.EventSpecialsRepository, outbox common.Outbox,
	clock common.Clock, runTx TxRunner, log *slog.Logger) *EventCompletionService {
	return &EventCompletionService{
		catalog: catalog, subscriptions: subscriptions, chats: chats,
		scoringRepo: scoringRepo, specials: specials, outbox: outbox, clock: clock, runTx: runTx, log: log,
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

// completeForChat's entire body runs inside one transaction, starting with
// LockEventCompletion as its very first statement: DiscoverEvents and
// SynchronizeMatches are two different scheduled jobs, guarded by two
// different cluster locks, that can both reach event completion for the
// same event around the same time. Without a lock scoped to this exact
// (chat, event) pair, both could read EventCompletionHash before either
// had written it, both see "not completed yet", and both award medals and
// enqueue an "event finished" notification. The lock forces the second
// caller to wait for the first transaction to commit (or roll back) before
// it re-reads the hash itself — by then it observes the first caller's
// write and takes the idempotency no-op path below instead.
func (s *EventCompletionService) completeForChat(ctx context.Context, event competition.Event, chatID common.ChatID) error {
	return s.runTx(ctx, func(txCtx context.Context) error {
		if err := s.scoringRepo.LockEventCompletion(txCtx, chatID, event.ID); err != nil {
			return err
		}

		standings, err := s.scoringRepo.Leaderboard(txCtx, chatID, scoring.ForEvent(event.ID))
		if err != nil {
			return err
		}
		hash := completionHash(standings)

		existing, ok, err := s.scoringRepo.EventCompletionHash(txCtx, chatID, event.ID)
		if err != nil {
			return err
		}
		if ok && existing == hash {
			return nil // idempotency guard: nothing changed since last completion pass (also the path a racer takes after waiting out the lock above)
		}

		// A chat that never voted on this tournament has no result to be
		// told about: the recap would be a leaderboard with nobody in it.
		// The completion is still recorded, so this stays a one-time
		// decision rather than a check re-run on every sync.
		if !anyPredictions(standings) {
			return s.scoringRepo.MarkEventCompleted(txCtx, chatID, event.ID, hash, s.clock.Now())
		}

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
				ExactPredictions: st.ExactPredictions, CorrectPredictions: st.CorrectPredictions, Predictions: st.Predictions,
			}
		}
		notification := common.EventFinishedNotification{
			ChatID: chatID.Value, TopicID: topicID, EventName: event.Name,
			Standings: notificationStandings, Awards: s.awards(txCtx, chatID, event.ID, standings),
		}
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

// awards picks this tournament's nominations. Best effort: a recap without
// its nominations is still a recap, whereas failing the whole completion —
// and with it the medals — over a decorative section would not be.
func (s *EventCompletionService) awards(ctx context.Context, chatID common.ChatID, eventID common.EventID,
	standings []scoring.UserStanding) []common.EventAwardNotification {
	if s.specials == nil {
		return nil
	}
	specials, err := s.specials.EventSpecials(ctx, chatID, eventID)
	if err != nil {
		s.log.Error("event awards lookup failed", "chatId", chatID.Value, "eventId", eventID.Value, "error", err)
		return nil
	}
	picked := scoring.PickEventAwards(standings, specials, scoring.DefaultAwardsShown)
	out := make([]common.EventAwardNotification, 0, len(picked))
	for _, a := range picked {
		out = append(out, common.EventAwardNotification{
			Kind: a.Kind, DisplayName: a.DisplayName, Value: a.Value, Detail: a.Detail,
		})
	}
	return out
}

// anyPredictions reports whether anybody in the chat actually predicted
// anything in this tournament. Standings can be non-empty without it — a
// member can appear with a zero count — so the votes are counted rather
// than the rows.
func anyPredictions(standings []scoring.UserStanding) bool {
	for _, s := range standings {
		if s.Predictions > 0 {
			return true
		}
	}
	return false
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
