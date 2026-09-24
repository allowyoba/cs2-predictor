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
	Outbox  common.Outbox
	Gate    NotifyGate
	log     *slog.Logger
}

func NewTargetCrossSellService(catalog competition.Catalog, subs subscription.Repository, targets subscription.TargetRepository,
	offers subscription.CrossSellRepository, outbox common.Outbox, switches common.NotifySwitchboard, log *slog.Logger) *TargetCrossSellService {
	return &TargetCrossSellService{
		Catalog: catalog, Subs: subs, Targets: targets, Offers: offers, Outbox: outbox,
		Gate: NotifyGate{Switches: switches}, log: log,
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
		chats, err := s.Targets.ChatsForTarget(ctx, subscription.TargetTeam, teamID.Value.String())
		if err != nil {
			return err
		}
		for _, chatID := range chats {
			if skip[chatID] {
				continue
			}
			if err := s.offerOne(ctx, chatID, event, subscription.TargetTeam, teamID.Value.String()); err != nil && s.log != nil {
				s.log.Error("target cross-sell offer failed for one chat, continuing with the rest",
					"chatId", chatID.Value, "eventId", event.ID.Value, "error", err)
			}
		}
	}
	return nil
}

func (s *TargetCrossSellService) offerOne(ctx context.Context, chatID common.ChatID, event competition.Event, kind subscription.TargetKind, targetID string) error {
	wanted, err := s.Gate.ChatWants(ctx, chatID, common.ChatNotifyTargetCrossSell)
	if err != nil || !wanted {
		return err
	}
	isNew, err := s.Offers.RecordOffer(ctx, subscription.CrossSellOffer{
		ChatID: chatID, EventID: event.ID, Kind: kind, TargetID: targetID,
	})
	if err != nil || !isNew {
		return err
	}
	payload, err := json.Marshal(TargetCrossSellNotification{
		ChatID: chatID.Value, EventID: event.ID.Value.String(), EventName: event.Name,
		TargetKind: string(kind), TargetName: targetID,
	})
	if err != nil {
		return err
	}
	aggregateID := chatID.String() + ":" + event.ID.Value.String()
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
