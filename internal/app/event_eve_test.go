package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

type eveOutbox struct {
	fakeOutbox
	sent []common.EventEveNotification
}

func (o *eveOutbox) Enqueue(_ context.Context, _, _, eventType, payload string) (uuid.UUID, error) {
	if eventType == "telegram.event-eve" {
		var n common.EventEveNotification
		if err := json.Unmarshal([]byte(payload), &n); err != nil {
			return uuid.Nil, err
		}
		o.sent = append(o.sent, n)
	}
	return uuid.New(), nil
}

// eveStore is an in-memory ScheduledReportStore.
type eveStore struct{ claims map[string]bool }

func newEveStore() *eveStore { return &eveStore{claims: map[string]bool{}} }

func (s *eveStore) key(chatID common.ChatID, reportType, periodKey string) string {
	return reportType + "/" + periodKey + "/" + chatID.String()
}

func (s *eveStore) Claimed(_ context.Context, chatID common.ChatID, reportType, periodKey string) (bool, error) {
	return s.claims[s.key(chatID, reportType, periodKey)], nil
}

func (s *eveStore) Claim(_ context.Context, chatID common.ChatID, reportType, periodKey string) (bool, error) {
	k := s.key(chatID, reportType, periodKey)
	if s.claims[k] {
		return false, nil
	}
	s.claims[k] = true
	return true, nil
}

type eveChats struct {
	settings []chat.Settings
	topics   map[common.EventID]int64
}

func (c *eveChats) ListActive(context.Context) ([]chat.Settings, error) { return c.settings, nil }

func (c *eveChats) EventTopic(_ context.Context, _ common.ChatID, eventID common.EventID) (*int64, error) {
	if topic, ok := c.topics[eventID]; ok {
		return &topic, nil
	}
	return nil, nil
}

func (c *eveChats) Find(_ context.Context, chatID common.ChatID) (*chat.Settings, error) {
	for i := range c.settings {
		if c.settings[i].ChatID == chatID {
			return &c.settings[i], nil
		}
	}
	return nil, nil
}

type eveSubs struct {
	byChat map[common.ChatID][]common.EventID
}

func (s *eveSubs) Subscribe(context.Context, subscription.EventSubscription) (subscription.EventSubscription, error) {
	return subscription.EventSubscription{}, nil
}
func (s *eveSubs) Unsubscribe(context.Context, common.ChatID, common.EventID) error { return nil }
func (s *eveSubs) SubscribedChats(context.Context, common.EventID) ([]common.ChatID, error) {
	return nil, nil
}
func (s *eveSubs) ActiveEventIDs(context.Context) ([]common.EventID, error) { return nil, nil }
func (s *eveSubs) Subscriptions(_ context.Context, chatID common.ChatID) ([]subscription.EventSubscription, error) {
	var out []subscription.EventSubscription
	for _, id := range s.byChat[chatID] {
		out = append(out, subscription.EventSubscription{ChatID: chatID, EventID: id})
	}
	return out, nil
}

type eveCatalog struct {
	competition.Catalog
	events  map[common.EventID]competition.Event
	matches map[common.EventID][]competition.Match
}

func (c *eveCatalog) FindEvent(_ context.Context, id common.EventID) (*competition.Event, error) {
	if e, ok := c.events[id]; ok {
		return &e, nil
	}
	return nil, nil
}

func (c *eveCatalog) FindUnstartedMatchesForEvents(_ context.Context, ids []common.EventID) ([]competition.Match, error) {
	var out []competition.Match
	for _, id := range ids {
		out = append(out, c.matches[id]...)
	}
	return out, nil
}

type eveScoring struct {
	scoring.Repository
	eventIDs  []common.EventID
	standings map[common.EventID][]scoring.UserStanding
}

func (s *eveScoring) AvailableEventIDs(context.Context, common.ChatID) ([]common.EventID, error) {
	return s.eventIDs, nil
}

func (s *eveScoring) Leaderboard(_ context.Context, _ common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	return s.standings[period.EventID], nil
}

func match(eventID common.EventID, at time.Time, first, second string) competition.Match {
	scheduled := at
	return competition.Match{
		ID: common.NewMatchID(), EventID: eventID, ScheduledAt: &scheduled,
		FirstTeam: &competition.Team{Name: first}, SecondTeam: &competition.Team{Name: second},
	}
}

type eveFixture struct {
	scheduler *EventEveScheduler
	outbox    *eveOutbox
	store     *eveStore
	eventID   common.EventID
	chatID    common.ChatID
}

// newEveFixture builds a chat subscribed to one tournament whose first
// match is `startsIn` from now, in a UTC chat so local time is easy to
// assert on.
func newEveFixture(t *testing.T, now time.Time, startsIn time.Duration, matches ...competition.Match) *eveFixture {
	t.Helper()
	chatID := common.ChatID{Value: -1}
	eventID := common.EventID{Value: uuid.New()}
	if len(matches) == 0 {
		matches = []competition.Match{match(eventID, now.Add(startsIn), "Spirit", "Falcons")}
	} else {
		// Caller-supplied matches carry their own event id; the fixture
		// follows them rather than the other way round.
		eventID = matches[0].EventID
	}
	outbox := &eveOutbox{}
	store := newEveStore()
	chats := &eveChats{
		settings: []chat.Settings{{ChatID: chatID, Locale: common.LocaleRU, Timezone: "UTC", Active: true}},
		topics:   map[common.EventID]int64{},
	}
	return &eveFixture{
		outbox: outbox, store: store, eventID: eventID, chatID: chatID,
		scheduler: &EventEveScheduler{
			Chats: chats, ChatSettings: chats,
			Subscriptions: &eveSubs{byChat: map[common.ChatID][]common.EventID{chatID: {eventID}}},
			Catalog: &eveCatalog{
				events:  map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major", Status: competition.EventUpcoming}},
				matches: map[common.EventID][]competition.Match{eventID: matches},
			},
			Scoring: &eveScoring{standings: map[common.EventID][]scoring.UserStanding{}},
			Store:   store, Outbox: outbox, Switches: allNotificationsOn{}, Lock: fakeClusterLock{},
			Clock: fixedClock{now: now}, Log: slog.Default(),
		},
	}
}

func TestEventEve_SendsOnceInsideTheLeadWindow(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	f := newEveFixture(t, now, 20*time.Hour)

	f.scheduler.Dispatch(context.Background())
	f.scheduler.Dispatch(context.Background())

	if len(f.outbox.sent) != 1 {
		t.Fatalf("expected exactly one nudge across two passes, got %d", len(f.outbox.sent))
	}
	sent := f.outbox.sent[0]
	if sent.EventName != "Major" || sent.ChatID != f.chatID.Value {
		t.Fatalf("notification = %+v", sent)
	}
	if sent.StartsAt != "21.09 08:00" {
		t.Fatalf("StartsAt = %q, want the first match in the chat's own timezone", sent.StartsAt)
	}
	if sent.FirstDayMatches != 1 || len(sent.Matches) != 1 || sent.Matches[0].FirstTeam != "Spirit" {
		t.Fatalf("expected the opening match to be described, got %+v", sent)
	}
}

// A tournament further out than the lead is not announced yet — the message
// is worth reading precisely because it means "tomorrow".
func TestEventEve_StaysQuietBeyondTheLead(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	f := newEveFixture(t, now, 50*time.Hour)

	f.scheduler.Dispatch(context.Background())

	if len(f.outbox.sent) != 0 {
		t.Fatalf("expected silence three days out, got %+v", f.outbox.sent)
	}
	// ...and nothing is claimed either, or the nudge would be lost when it
	// does come due.
	if claimed, _ := f.store.Claimed(context.Background(), f.chatID, eventEveReportType, f.eventID.Value.String()); claimed {
		t.Fatal("a tournament that was not announced must not be marked as announced")
	}
}

// "Matches on the opening day" is the chat's own calendar day: a tournament
// starting at 22:00 local has a two-match evening, and counting the next
// day's matches into that number would be a different day's schedule.
func TestEventEve_CountsOnlyTheOpeningLocalDay(t *testing.T) {
	now := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	opening := time.Date(2026, 9, 21, 20, 0, 0, 0, time.UTC)
	eventID := common.EventID{Value: uuid.New()}
	f := newEveFixture(t, now, 0,
		match(eventID, opening, "Spirit", "Falcons"),
		match(eventID, opening.Add(90*time.Minute), "MOUZ", "FaZe"),
		match(eventID, opening.Add(14*time.Hour), "NAVI", "G2"), // already the next local day
	)

	f.scheduler.Dispatch(context.Background())

	if len(f.outbox.sent) != 1 {
		t.Fatalf("expected one nudge, got %d", len(f.outbox.sent))
	}
	if got := f.outbox.sent[0].FirstDayMatches; got != 2 {
		t.Fatalf("FirstDayMatches = %d, want 2 (the third match falls on the next local day)", got)
	}
	if len(f.outbox.sent[0].Matches) != 2 {
		t.Fatalf("expected only the opening day's matches listed, got %+v", f.outbox.sent[0].Matches)
	}
}

// The reigning champion is the social hook; a chat that has never finished
// a tournament simply does not get that line, rather than getting an empty
// one or no message at all.
func TestEventEve_CarriesThePreviousChampionWhenThereIsOne(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	f := newEveFixture(t, now, 10*time.Hour)
	previous := common.EventID{Value: uuid.New()}
	f.scheduler.Scoring = &eveScoring{
		eventIDs: []common.EventID{previous},
		standings: map[common.EventID][]scoring.UserStanding{
			previous: {{DisplayName: "sh1ro", Rank: 1, Points: 42}},
		},
	}

	f.scheduler.Dispatch(context.Background())

	if len(f.outbox.sent) != 1 || f.outbox.sent[0].Champion != "sh1ro" {
		t.Fatalf("expected the previous tournament's winner, got %+v", f.outbox.sent)
	}
}

func TestEventEve_NegativeLeadDisablesTheJob(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	f := newEveFixture(t, now, time.Hour)
	f.scheduler.Lead = -time.Second

	f.scheduler.Dispatch(context.Background())

	if len(f.outbox.sent) != 0 {
		t.Fatalf("expected nothing sent with the job disabled, got %+v", f.outbox.sent)
	}
}

// A tournament already under way always has an unplayed match coming up,
// so "the next match is within a day" is true for it too. Announcing that
// as "it starts tomorrow" is simply false — and on a fresh deploy it would
// be false for every running tournament at once.
func TestEventEve_SaysNothingAboutATournamentAlreadyUnderWay(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	f := newEveFixture(t, now, 10*time.Hour)
	eventID := f.eventID
	f.scheduler.Catalog = &eveCatalog{
		events:  map[common.EventID]competition.Event{eventID: {ID: eventID, Name: "Major", Status: competition.EventRunning}},
		matches: map[common.EventID][]competition.Match{eventID: {match(eventID, now.Add(10*time.Hour), "A", "B")}},
	}

	f.scheduler.Dispatch(context.Background())

	if len(f.outbox.sent) != 0 {
		t.Fatalf("expected silence for a running tournament, got %+v", f.outbox.sent)
	}
}

// Inside the last few hours "tomorrow" is untrue and the first poll is
// already on its way, so the nudge stands down rather than contradicting
// itself.
func TestEventEve_SaysNothingWhenTheStartIsHoursAway(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	f := newEveFixture(t, now, EventEveMinLead-time.Minute)

	f.scheduler.Dispatch(context.Background())

	if len(f.outbox.sent) != 0 {
		t.Fatalf("expected no 'tomorrow' message hours before the start, got %+v", f.outbox.sent)
	}
}
