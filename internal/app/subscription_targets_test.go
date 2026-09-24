package app

import (
	"context"
	"testing"
	"time"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// memTargets is an in-memory subscription.TargetRepository.
type memTargets struct {
	byChat   map[common.ChatID][]subscription.Target
	rosters  map[string][]common.TeamID // normalized player -> teams
	followed []common.EventID
	offers   map[string]time.Time
	lastByCh map[common.ChatID]time.Time
}

func newMemTargets() *memTargets {
	return &memTargets{byChat: map[common.ChatID][]subscription.Target{}, rosters: map[string][]common.TeamID{},
		offers: map[string]time.Time{}, lastByCh: map[common.ChatID]time.Time{}}
}

func (m *memTargets) SubscribeTarget(_ context.Context, chatID common.ChatID, t subscription.Target, _ time.Time) error {
	for _, existing := range m.byChat[chatID] {
		if existing.Key() == t.Key() {
			return nil
		}
	}
	m.byChat[chatID] = append(m.byChat[chatID], t)
	return nil
}
func (m *memTargets) UnsubscribeTarget(_ context.Context, chatID common.ChatID, t subscription.Target) error {
	var kept []subscription.Target
	for _, existing := range m.byChat[chatID] {
		if existing.Key() != t.Key() {
			kept = append(kept, existing)
		}
	}
	m.byChat[chatID] = kept
	return nil
}
func (m *memTargets) Targets(_ context.Context, chatID common.ChatID) ([]subscription.Target, error) {
	return m.byChat[chatID], nil
}
func (m *memTargets) ResolveTeams(_ context.Context, targets []subscription.Target) (map[string][]common.TeamID, error) {
	out := map[string][]common.TeamID{}
	for _, t := range targets {
		switch t.Kind {
		case subscription.TargetTeam:
			out[t.Key()] = []common.TeamID{t.TeamID}
		case subscription.TargetPlayer:
			if teams := m.rosters[t.Player]; len(teams) > 0 {
				out[t.Key()] = teams
			}
		}
	}
	return out, nil
}
func (m *memTargets) ChatsFollowingTeams(ctx context.Context, teamIDs []common.TeamID) ([]common.ChatID, error) {
	want := map[common.TeamID]bool{}
	for _, id := range teamIDs {
		want[id] = true
	}
	var out []common.ChatID
	for chatID, targets := range m.byChat {
		resolved, _ := m.ResolveTeams(ctx, targets)
		for _, id := range subscription.TeamsOf(targets, resolved) {
			if want[id] {
				out = append(out, chatID)
				break
			}
		}
	}
	return out, nil
}
func (m *memTargets) FollowedEventIDs(context.Context) ([]common.EventID, error) {
	return m.followed, nil
}
func (m *memTargets) SearchTargets(context.Context, string, int) ([]subscription.Target, error) {
	return nil, nil
}
func (m *memTargets) ClaimCrossSellOffer(_ context.Context, chatID common.ChatID, eventID common.EventID, at time.Time, cooldown time.Duration) (bool, error) {
	key := chatID.String() + eventID.Value.String()
	if _, ok := m.offers[key]; ok {
		return false, nil
	}
	if last, ok := m.lastByCh[chatID]; ok && at.Sub(last) < cooldown {
		return false, nil
	}
	m.offers[key], m.lastByCh[chatID] = at, at
	return true, nil
}

// periodRecorder is a scoring.Repository that remembers the period it saw.
type periodRecorder struct {
	scoring.Repository
	seen []scoring.StatsPeriod
}

func (p *periodRecorder) Leaderboard(_ context.Context, _ common.ChatID, period scoring.StatsPeriod) ([]scoring.UserStanding, error) {
	p.seen = append(p.seen, period)
	return nil, nil
}

// The trickiest overlap: a chat following a team, a player on that same
// team and a player on another, plus a DIFFERENT tournament. Its board for
// this tournament must be sliced to exactly the two teams, each once.
func TestScopedScoring_OverlappingTargetsSliceTheTournamentOnce(t *testing.T) {
	ctx := context.Background()
	chatID := common.ChatID{Value: -42}
	event, otherEvent := common.NewEventID(), common.NewEventID()
	navi, vitality := common.NewTeamID(), common.NewTeamID()
	targets := newMemTargets()
	targets.rosters["s1mple"] = []common.TeamID{navi}
	targets.rosters["zywoo"] = []common.TeamID{vitality}
	for _, target := range []subscription.Target{
		subscription.TournamentTarget(otherEvent),
		subscription.TeamTarget(navi, "NAVI"),
		subscription.PlayerTarget("s1mple"),
		subscription.PlayerTarget("ZywOo"),
	} {
		_ = targets.SubscribeTarget(ctx, chatID, target, time.Time{})
	}
	inner := &periodRecorder{}
	scoped := ScopedScoring{Repository: inner, Scopes: SubscriptionScopes{Targets: targets}}

	if _, err := scoped.Leaderboard(ctx, chatID, scoring.ForEvent(event)); err != nil {
		t.Fatal(err)
	}
	got := inner.seen[0]
	if got.Teams == nil || len(got.Teams.IDs) != 2 {
		t.Fatalf("tournament board teams = %+v, want the NAVI/Vitality slice with no duplicates", got.Teams)
	}

	// Other periods are not tournament-level views and stay whole.
	if _, err := scoped.Leaderboard(ctx, chatID, scoring.AllTime()); err != nil {
		t.Fatal(err)
	}
	if inner.seen[1].Teams != nil {
		t.Fatal("an all-time board must not be sliced")
	}

	// Subscribing to the tournament itself widens it to the full view.
	_ = targets.SubscribeTarget(ctx, chatID, subscription.TournamentTarget(event), time.Time{})
	if _, err := scoped.Leaderboard(ctx, chatID, scoring.ForEvent(event)); err != nil {
		t.Fatal(err)
	}
	if inner.seen[2].Teams != nil {
		t.Fatal("a tournament subscriber must get the full board")
	}
}

func TestScopedScoring_ChatWithoutFollowsKeepsFullView(t *testing.T) {
	inner := &periodRecorder{}
	scoped := ScopedScoring{Repository: inner, Scopes: SubscriptionScopes{Targets: newMemTargets()}}
	if _, err := scoped.Leaderboard(context.Background(), common.ChatID{Value: 1}, scoring.ForEvent(common.NewEventID())); err != nil {
		t.Fatal(err)
	}
	if inner.seen[0].Teams != nil {
		t.Fatal("a chat with no team/player targets keeps the unscoped board")
	}
}

// A followed team's match reaches a chat not subscribed to its tournament,
// exactly once, and the chat is offered the tournament exactly once.
func TestSynchronizeMatches_FollowersGetThePollAndOneTournamentOffer(t *testing.T) {
	soon := time.Now().Add(2 * time.Hour)
	fx := newMatchSyncFixture(t)
	follower := common.ChatID{Value: -2}
	chats := fx.sync.Chats.(*fakeSyncChats)
	chats.active = append(chats.active, chat.Settings{ChatID: follower, Active: true, EnabledGames: []competition.GameCode{competition.GameCS2}})
	outbox := &fakeSyncOutbox{}
	fx.sync.Outbox = outbox

	first := syncMatch(fx.event.ID, &soon)
	second := syncMatch(fx.event.ID, &soon)
	second.ExternalID = "m-2"
	second.FirstTeam = first.FirstTeam
	targets := newMemTargets()
	targets.rosters["niko"] = []common.TeamID{first.FirstTeam.ID}
	// Team and player covering the same side: must not produce two polls.
	_ = targets.SubscribeTarget(context.Background(), follower, subscription.TeamTarget(first.FirstTeam.ID, "G2"), time.Time{})
	_ = targets.SubscribeTarget(context.Background(), follower, subscription.PlayerTarget("NiKo"), time.Time{})
	// A tournament subscriber that also follows the team: still one poll.
	_ = targets.SubscribeTarget(context.Background(), fx.chatID, subscription.TeamTarget(first.FirstTeam.ID, "G2"), time.Time{})
	fx.sync.Targets = targets
	fx.sync.Gateway = gatewayWithMatches(t, []competition.Match{first, second})

	fx.sync.SynchronizeMatches(context.Background())

	perChat := map[common.ChatID]int{}
	for _, p := range fx.polls.polls {
		perChat[p.ChatID]++
	}
	if perChat[follower] != 2 || perChat[fx.chatID] != 2 {
		t.Fatalf("polls per chat = %v, want 2 each (one per match, no duplicates)", perChat)
	}
	offers := 0
	for _, call := range outbox.enqueued {
		if call.eventType == "telegram.follow-cross-sell" {
			offers++
		}
	}
	if offers != 1 {
		t.Fatalf("tournament offers = %d, want exactly one for the follower and none for the subscriber", offers)
	}
}
