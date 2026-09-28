package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

// TargetCrossSellNotification is the payload behind
// "telegram.target-cross-sell-offer": a one-tap tournament-subscribe
// prompt for a chat that follows a team/player now playing in event but
// isn't subscribed to it. Rendering this into an actual Telegram message
// with a subscribe/dismiss button is the remaining piece of the Telegram
// adapter — this is the notification the outbox already carries once that
// consumer exists.
type TargetCrossSellNotification struct {
	ChatID     int64  `json:"chatId"`
	EventID    string `json:"eventId"`
	EventName  string `json:"eventName"`
	TargetKind string `json:"targetKind"`
	TargetName string `json:"targetName"`
}

// TargetCrossSellService implements requirement (b): when a team/player a
// chat follows turns up in a tournament that chat has not subscribed to,
// offer a one-tap subscribe — once per (chat, tournament), gated by the
// same notification switchboard every other proactive message goes
// through (ChatNotifyTargetCrossSell), never a parallel opt-in path.
type TargetCrossSellService struct {
	Catalog competition.Catalog
	Subs    subscription.Repository
	Targets subscription.TargetRepository
	Offers  subscription.CrossSellRepository
	// Rosters answers "who plays for this team right now", so a chat
	// following a player is offered the tournament that player turns up in —
	// nil leaves the player half off, the team half untouched.
	Rosters competition.RosterRepository
	Outbox  common.Outbox
	Gate    NotifyGate
	Clock   common.Clock
	log     *slog.Logger
}

func NewTargetCrossSellService(catalog competition.Catalog, subs subscription.Repository, targets subscription.TargetRepository,
	offers subscription.CrossSellRepository, rosters competition.RosterRepository, outbox common.Outbox,
	switches common.NotifySwitchboard, clock common.Clock, log *slog.Logger) *TargetCrossSellService {
	return &TargetCrossSellService{
		Catalog: catalog, Subs: subs, Targets: targets, Offers: offers, Rosters: rosters, Outbox: outbox,
		Gate: NotifyGate{Switches: switches}, Clock: clock, log: log,
	}
}

// DiscoverAndOffer scans event's actual field (from its real matches, not
// from a subscription's say-so) and, for every team playing in it, offers
// every chat following that team a one-tap subscribe to event — unless
// that chat is already tournament-subscribed to it, or has already been
// offered this exact (chat, event) pair before. One chat followed by two
// different teams in the same event still gets at most one offer, since
// RecordOffer dedupes on (chat, event) alone.
//
// Best-effort per chat, same as EventCompletionService.Complete: one
// chat's failure must not stop the rest of the fan-out.
func (s *TargetCrossSellService) DiscoverAndOffer(ctx context.Context, event competition.Event) error {
	matches, err := s.Catalog.FindMatches(ctx, event.ID)
	if err != nil {
		return err
	}
	teamIDs := teamsOf(matches)
	if len(teamIDs) == 0 {
		return nil
	}

	alreadySubscribed, err := s.Subs.SubscribedChats(ctx, event.ID)
	if err != nil {
		return err
	}
	skip := make(map[common.ChatID]bool, len(alreadySubscribed))
	for _, id := range alreadySubscribed {
		skip[id] = true
	}

	for _, teamID := range teamIDs {
		if err := s.offerToFollowersOf(ctx, subscription.TargetTeam, teamID.Value.String(), event, skip); err != nil {
			return err
		}
		// The people on that team are playing here too, and somebody
		// following a player rather than an organisation is asking about
		// exactly that — a roster change is precisely when the two answers
		// differ, which is why this reads the roster instead of assuming the
		// team stands in for its players.
		for _, playerID := range s.playersOf(ctx, teamID) {
			if err := s.offerToFollowersOf(ctx, subscription.TargetPlayer, playerID.Value.String(), event, skip); err != nil {
				return err
			}
		}
	}
	return nil
}

// playersOf is the team's current roster, or nothing at all when rosters are
// not wired — a missing roster reader means no player offers, never a failed
// team offer.
func (s *TargetCrossSellService) playersOf(ctx context.Context, teamID common.TeamID) []common.PlayerID {
	if s.Rosters == nil {
		return nil
	}
	members, err := s.Rosters.Roster(ctx, teamID)
	if err != nil {
		if s.log != nil {
			s.log.Error("roster lookup failed, skipping this team's player offers", "teamId", teamID.Value, "error", err)
		}
		return nil
	}
	out := make([]common.PlayerID, 0, len(members))
	for _, member := range members {
		out = append(out, member.Player.ID)
	}
	return out
}

func (s *TargetCrossSellService) offerToFollowersOf(ctx context.Context, kind subscription.TargetKind, targetID string,
	event competition.Event, skip map[common.ChatID]bool) error {
	subs, err := s.Targets.SubscribersOf(ctx, kind, targetID)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if skip[sub.ChatID] {
			continue
		}
		if err := s.offerOne(ctx, sub, event); err != nil && s.log != nil {
			s.log.Error("target cross-sell offer failed for one chat, continuing with the rest",
				"chatId", sub.ChatID.Value, "eventId", event.ID.Value, "error", err)
		}
	}
	return nil
}

func (s *TargetCrossSellService) offerOne(ctx context.Context, sub subscription.TargetSubscription, event competition.Event) error {
	wanted, err := s.Gate.ChatWants(ctx, sub.ChatID, common.ChatNotifyTargetCrossSell)
	if err != nil || !wanted {
		return err
	}
	isNew, err := s.Offers.RecordOffer(ctx, subscription.CrossSellOffer{
		ChatID: sub.ChatID, EventID: event.ID, Kind: sub.Kind, TargetID: sub.TargetID, OfferedAt: s.Clock.Now(),
	})
	if err != nil || !isNew {
		return err
	}
	// The name the chat actually followed, not sub.TargetID — that is a
	// UUID for a team, and it was being rendered straight into the message.
	name := sub.TargetName
	if name == "" {
		name = sub.TargetID
	}
	payload, err := json.Marshal(TargetCrossSellNotification{
		ChatID: sub.ChatID.Value, EventID: event.ID.Value.String(), EventName: event.Name,
		TargetKind: string(sub.Kind), TargetName: name,
	})
	if err != nil {
		return err
	}
	aggregateID := sub.ChatID.String() + ":" + event.ID.Value.String()
	_, err = s.Outbox.Enqueue(ctx, "CHAT", aggregateID, "telegram.target-cross-sell-offer", string(payload))
	return err
}

// teamsOf lists every distinct team actually playing across matches.
func teamsOf(matches []competition.Match) []common.TeamID {
	seen := map[common.TeamID]bool{}
	var out []common.TeamID
	for _, m := range matches {
		for _, team := range []*competition.Team{m.FirstTeam, m.SecondTeam} {
			if team == nil || seen[team.ID] {
				continue
			}
			seen[team.ID] = true
			out = append(out, team.ID)
		}
	}
	return out
}
