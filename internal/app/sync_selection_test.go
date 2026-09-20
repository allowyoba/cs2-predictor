package app

import (
	"context"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// Every subscribed tournament used to be asked about on every run, which
// meant a provider request budget spent mostly on matches nobody could be
// watching — a Saturday final re-fetched every three minutes since
// Wednesday. These cover which tournaments a run actually asks about, and
// the far more important question of which ones it must never skip.

// liveCatalog is a fakeSyncCatalog that can also answer "which of these
// have a match in play", like the real repository does.
type liveCatalog struct {
	*fakeSyncCatalog
	unstarted []competition.Match
	live      []common.EventID
}

func (c *liveCatalog) FindUnstartedMatchesForEvents(context.Context, []common.EventID) ([]competition.Match, error) {
	return c.unstarted, nil
}
func (c *liveCatalog) EventsWithLiveMatches(context.Context, []common.EventID) ([]common.EventID, error) {
	return c.live, nil
}

func syncWithCatalog(t *testing.T, catalog competition.Catalog, cold time.Duration) *CompetitionSynchronization {
	t.Helper()
	sync := newTestSync(t, &fixedProvider{name: "PANDASCORE"}, newFakeSyncCatalog(), &fakeSyncSubs{}, &fakeSyncChats{}, &fakeSyncOutbox{})
	sync.Catalog = catalog
	sync.MatchSyncColdInterval = cold
	return sync
}

func scheduledMatch(eventID common.EventID, at time.Time) competition.Match {
	return competition.Match{ID: common.NewMatchID(), EventID: eventID, Status: competition.MatchNotStarted, ScheduledAt: &at}
}

// selectedIDs names which events a run would ask the provider about.
func selectedIDs(events []competition.Event) map[common.EventID]bool {
	out := map[common.EventID]bool{}
	for _, e := range events {
		out[e.ID] = true
	}
	return out
}

func TestSelectEventsToSync_AsksOnlyAboutTournamentsWithSomethingHappening(t *testing.T) {
	now := time.Now()
	soon := topTierEvent("Starting soon")
	soon.Status = competition.EventRunning
	later := topTierEvent("Next weekend")
	later.Status = competition.EventUpcoming
	playing := topTierEvent("Being played")
	playing.Status = competition.EventRunning

	catalog := &liveCatalog{
		fakeSyncCatalog: newFakeSyncCatalog(),
		unstarted: []competition.Match{
			scheduledMatch(soon.ID, now.Add(20*time.Minute)),
			scheduledMatch(later.ID, now.Add(72*time.Hour)),
			scheduledMatch(playing.ID, now.Add(48*time.Hour)),
		},
		live: []common.EventID{playing.ID},
	}
	sync := syncWithCatalog(t, catalog, time.Hour)
	// Claim the cold sweep so this run is an ordinary one.
	sync.selectEventsToSync(context.Background(), nil)

	selected := selectedIDs(sync.selectEventsToSync(context.Background(), []competition.Event{soon, later, playing}))

	if !selected[soon.ID] {
		t.Fatal("a match starting within the hour must be polled at the full rate")
	}
	if !selected[playing.ID] {
		t.Fatal("a tournament with a match in play must be polled at the full rate")
	}
	if selected[later.ID] {
		t.Fatal("a match three days out cannot change in a way anybody notices this minute")
	}
}

// A tournament nothing is known about yet is what creates the polls, so it
// is never the thing that gets skipped.
func TestSelectEventsToSync_AlwaysAsksAboutATournamentItHasNoMatchesFor(t *testing.T) {
	fresh := topTierEvent("Just subscribed")
	catalog := &liveCatalog{fakeSyncCatalog: newFakeSyncCatalog()}
	sync := syncWithCatalog(t, catalog, time.Hour)
	sync.selectEventsToSync(context.Background(), nil)

	selected := selectedIDs(sync.selectEventsToSync(context.Background(), []competition.Event{fresh}))

	if !selected[fresh.ID] {
		t.Fatal("with no local matches there is nothing to decide from — it has to be fetched")
	}
}

// The slow lane still sweeps everything, or a schedule that moved days
// ahead would never be noticed.
func TestSelectEventsToSync_SweepsEverythingOnTheColdTimer(t *testing.T) {
	now := time.Now()
	later := topTierEvent("Next weekend")
	catalog := &liveCatalog{
		fakeSyncCatalog: newFakeSyncCatalog(),
		unstarted:       []competition.Match{scheduledMatch(later.ID, now.Add(72*time.Hour))},
	}
	sync := syncWithCatalog(t, catalog, time.Hour)

	// The very first run of a process is a full sweep...
	if len(sync.selectEventsToSync(context.Background(), []competition.Event{later})) != 1 {
		t.Fatal("the first run must fetch everything")
	}
	// ...the next one is not...
	if len(sync.selectEventsToSync(context.Background(), []competition.Event{later})) != 0 {
		t.Fatal("expected the quiet tournament to be skipped between sweeps")
	}
	// ...and once the timer comes round, everything is swept again.
	sync.lastFullMatchSync = now.Add(-2 * time.Hour)
	if len(sync.selectEventsToSync(context.Background(), []competition.Event{later})) != 1 {
		t.Fatal("expected the cold sweep to fetch everything again")
	}
}

// Turning the split off has to restore exactly the old behaviour, which is
// what a deployment falls back to if any of this misbehaves.
func TestSelectEventsToSync_FetchesEverythingWhenTheSplitIsDisabled(t *testing.T) {
	quiet := topTierEvent("Next weekend")
	catalog := &liveCatalog{
		fakeSyncCatalog: newFakeSyncCatalog(),
		unstarted:       []competition.Match{scheduledMatch(quiet.ID, time.Now().Add(72*time.Hour))},
	}
	sync := syncWithCatalog(t, catalog, 0)

	for i := 0; i < 3; i++ {
		if len(sync.selectEventsToSync(context.Background(), []competition.Event{quiet})) != 1 {
			t.Fatal("with the split disabled every run fetches every tournament")
		}
	}
}

// FindPlayableMatchesForEvents widens the same fixture by one status; the
// tests here only ever seed unstarted matches, so it answers the same way.
func (c *liveCatalog) FindPlayableMatchesForEvents(ctx context.Context, ids []common.EventID) ([]competition.Match, error) {
	return c.FindUnstartedMatchesForEvents(ctx, ids)
}
