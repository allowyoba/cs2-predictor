package app

import (
	"context"
	"log/slog"
	"testing"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// reconcileCatalog is FindEvent/FindMatches backed by fixed maps — the
// EventCompletionService.Complete path this sweep drives needs both, unlike
// fakeSyncCatalog (no matches) or fakeCatalogForCompletion (no FindEvent).
type reconcileCatalog struct {
	fakeCatalogForCompletion
	events map[common.EventID]competition.Event
}

func (c *reconcileCatalog) FindEvent(_ context.Context, id common.EventID) (*competition.Event, error) {
	if e, ok := c.events[id]; ok {
		return &e, nil
	}
	return nil, nil
}

// TestReconcileEventCompletions_CompletesAnEventStuckOnANeverUpdatedStatus
// is the StarLadder-shaped case: every match is terminal, but the event's
// own status field never flipped to Finished, so neither
// discoverOneEvent's nor processMatch's status-transition gate ever fired.
// The sweep must still send the completion recap, since Complete derives
// "is this over" from the matches themselves, not from event.Status.
func TestReconcileEventCompletions_CompletesAnEventStuckOnANeverUpdatedStatus(t *testing.T) {
	eventID := common.NewEventID()
	chatID := common.ChatID{Value: -1}
	catalog := &reconcileCatalog{
		fakeCatalogForCompletion: fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
			eventID: {newMatch(competition.MatchFinished), newMatch(competition.MatchFinished)},
		}},
		events: map[common.EventID]competition.Event{
			eventID: {ID: eventID, Name: "StarSeries Fall 2026", Status: competition.EventUpcoming},
		},
	}
	subs := &fakeSyncSubs{
		activeEvents:      []common.EventID{eventID},
		subscribedByEvent: map[common.EventID][]common.ChatID{eventID: {chatID}},
	}
	scoringRepo := &fakeScoringForCompletion{leaderboard: []scoring.UserStanding{
		{UserID: common.UserID{Value: 1}, DisplayName: "A", Points: 5, Rank: 1, Predictions: 2},
	}}
	outbox := newTestOutboxForCompletion()
	completion := NewEventCompletionService(catalog, subs, fakeChatsForCompletion{}, scoringRepo, nil, outbox,
		common.SystemUTCClock(), identityTx, slog.Default()).WithSwitches(allNotificationsOn{})

	sync := &CompetitionSynchronization{
		Catalog: catalog, Subscriptions: subs, EventCompletion: completion,
		Switches: allNotificationsOn{}, Lock: fakeClusterLock{}, Clock: common.SystemUTCClock(),
		Metrics: newTestMetrics(), Log: slog.Default(),
	}

	sync.ReconcileEventCompletions(context.Background())

	if len(outbox.enqueued) == 0 {
		t.Fatal("expected the sweep to send the completion recap for an event whose matches are all terminal, regardless of event.Status")
	}
}

// A second run must not re-notify: completeForChat's hash guard makes the
// sweep idempotent, which is what makes running it on every tick safe.
func TestReconcileEventCompletions_SecondRunIsANoOp(t *testing.T) {
	eventID := common.NewEventID()
	chatID := common.ChatID{Value: -1}
	catalog := &reconcileCatalog{
		fakeCatalogForCompletion: fakeCatalogForCompletion{matches: map[common.EventID][]competition.Match{
			eventID: {newMatch(competition.MatchFinished)},
		}},
		events: map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major"}},
	}
	subs := &fakeSyncSubs{
		activeEvents:      []common.EventID{eventID},
		subscribedByEvent: map[common.EventID][]common.ChatID{eventID: {chatID}},
	}
	scoringRepo := &fakeScoringForCompletion{leaderboard: []scoring.UserStanding{
		{UserID: common.UserID{Value: 1}, DisplayName: "A", Points: 5, Rank: 1, Predictions: 2},
	}}
	outbox := newTestOutboxForCompletion()
	completion := NewEventCompletionService(catalog, subs, fakeChatsForCompletion{}, scoringRepo, nil, outbox,
		common.SystemUTCClock(), identityTx, slog.Default()).WithSwitches(allNotificationsOn{})
	sync := &CompetitionSynchronization{
		Catalog: catalog, Subscriptions: subs, EventCompletion: completion,
		Switches: allNotificationsOn{}, Lock: fakeClusterLock{}, Clock: common.SystemUTCClock(),
		Metrics: newTestMetrics(), Log: slog.Default(),
	}

	sync.ReconcileEventCompletions(context.Background())
	first := len(outbox.enqueued)
	sync.ReconcileEventCompletions(context.Background())

	if len(outbox.enqueued) != first {
		t.Fatalf("expected the second sweep to send nothing new, went from %d to %d", first, len(outbox.enqueued))
	}
}
