package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/subscription"
	"cs2predictor/internal/platform/common"
)

const (
	// eventOfferScanLimit bounds the catalogue read per chat. Top-tier and
	// still-subscribable is already a small set — a few dozen at most across
	// both games — so this only ever guards against a pathological catalogue.
	eventOfferScanLimit = 200
	// defaultEventOffersPerChatPerRun is how many tournaments one chat can be
	// told about in a single pass. The reconciler's first run after this
	// feature lands faces every top-tier tournament the old one-shot path
	// missed, and a room that gets fifteen messages at once has been spammed
	// rather than informed. The backlog drains over the following runs, in
	// "playing now, then starting soonest" order.
	defaultEventOffersPerChatPerRun = 3
)

// EventOfferReconciler asks, on a schedule, the question
// CompetitionSynchronization.announceBigEvent only ever asked at the instant
// an event row was first inserted: for each active chat, is there a top-tier
// tournament it could still join that it has not been told about?
//
// The one-shot version lost the offer whenever anything arrived in the wrong
// order — the tier resolved on a later sync (the normal case: PandaScore
// often reports none at first, which is why the adapter merges a better tier
// in later), the chat turned the switch on afterwards, the chat enabled the
// game afterwards, or the chat was created afterwards. None of those were
// recoverable, because nothing recorded that the chat had never been asked.
// Now the asking is idempotent per (chat, tournament) instead of per insert,
// so ordering stops mattering.
type EventOfferReconciler struct {
	Chats         chat.ActiveChatLister
	Catalog       competition.Catalog
	Subscriptions subscription.Repository
	Offers        subscription.EventOfferRepository
	Outbox        common.Outbox
	// Switches gates the message only. A chat that auto-subscribes still
	// joins the tournament with the switch off, it just is not told it did —
	// the same split announceBigEvent draws.
	Switches common.NotifySwitchboard
	Lock     common.ClusterLock
	Clock    common.Clock
	Log      *slog.Logger
	// PerChatPerRun overrides defaultEventOffersPerChatPerRun; zero uses it.
	PerChatPerRun int
}

func (s *EventOfferReconciler) perRun() int {
	if s.PerChatPerRun <= 0 {
		return defaultEventOffersPerChatPerRun
	}
	return s.PerChatPerRun
}

func (s *EventOfferReconciler) Dispatch(ctx context.Context) {
	_, err := s.Lock.Execute(ctx, "cs2predictor:event-offers", func(ctx context.Context) error {
		return s.reconcile(ctx)
	})
	if err != nil {
		s.Log.Error("event offer reconcile failed", "error", err)
	}
}

func (s *EventOfferReconciler) reconcile(ctx context.Context) error {
	chats, err := s.Chats.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, settings := range chats {
		if err := s.reconcileChat(ctx, settings); err != nil {
			// One chat's failure must not cost every chat ordered after it
			// its offer — the same per-item resilience the rest of the
			// chat-wide fan-outs use.
			s.Log.Error("event offer reconcile failed for chat", "chatId", settings.ChatID.Value, "error", err)
		}
	}
	return nil
}

func (s *EventOfferReconciler) reconcileChat(ctx context.Context, settings chat.Settings) error {
	if len(settings.EnabledGames) == 0 {
		return nil
	}
	// SearchEvents with an empty query is already "top-tier, still joinable,
	// in this chat's games, playing now first then starting soonest" — the
	// exact candidate set, ordered the way the per-run cap wants to spend
	// itself. No new catalogue read had to be invented for this.
	candidates, err := s.Catalog.SearchEvents(ctx, "", eventOfferScanLimit, true, settings.EnabledGames)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}

	byID, ids, err := s.notFollowing(ctx, settings.ChatID, candidates)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	// Asked once for this chat rather than once per candidate tournament: a
	// silent chat has nothing to decide for any of them, and the answer is
	// the same for all of them either way.
	wanted, err := (NotifyGate{Switches: s.Switches}).ChatWants(ctx, settings.ChatID, common.ChatNotifyNewEvents)
	if err != nil {
		return err
	}
	if !wanted && !anyAutoSubscribed(settings, candidates) {
		return nil
	}

	open, err := s.Offers.UndecidedEvents(ctx, settings.ChatID, ids)
	if err != nil {
		return err
	}

	budget := s.perRun()
	for _, id := range open {
		if budget <= 0 {
			return nil
		}
		decided, err := s.decide(ctx, settings, byID[id], wanted)
		if err != nil {
			return err
		}
		if decided {
			budget--
		}
	}
	return nil
}

// notFollowing drops the tournaments this chat is already in, returning the
// rest both keyed for lookup and in the order the caller's cap should spend
// itself on.
func (s *EventOfferReconciler) notFollowing(ctx context.Context, chatID common.ChatID,
	candidates []competition.Event) (map[common.EventID]competition.Event, []common.EventID, error) {
	subscribed, err := s.Subscriptions.Subscriptions(ctx, chatID)
	if err != nil {
		return nil, nil, err
	}
	following := make(map[common.EventID]bool, len(subscribed))
	for _, sub := range subscribed {
		following[sub.EventID] = true
	}
	byID := make(map[common.EventID]competition.Event, len(candidates))
	ids := make([]common.EventID, 0, len(candidates))
	for _, event := range candidates {
		if following[event.ID] {
			continue
		}
		byID[event.ID] = event
		ids = append(ids, event.ID)
	}
	return byID, ids, nil
}

// anyAutoSubscribed reports whether any candidate is for a game this chat
// joins automatically — the other half of "is there anything to do here at
// all", since auto-subscription happens with the announcement switched off.
func anyAutoSubscribed(settings chat.Settings, candidates []competition.Event) bool {
	for _, event := range candidates {
		if settings.AutoSubscribesTo(event.Game) {
			return true
		}
	}
	return false
}

// Decide settles one (chat, tournament) pair and reports whether anything
// actually happened. It is the single place both the discovery-time path and
// the scheduled sweep go through, so a tournament can never be both
// announced on insert and announced again on the next reconcile.
//
// Whether the chat is already following event is the caller's question, not
// this one's: the discovery-time fan-out answers it once per tournament for
// every chat at once, which is a query this would otherwise repeat per chat.
func (s *EventOfferReconciler) Decide(ctx context.Context, settings chat.Settings, event competition.Event) (bool, error) {
	wanted, err := (NotifyGate{Switches: s.Switches}).ChatWants(ctx, settings.ChatID, common.ChatNotifyNewEvents)
	if err != nil {
		return false, err
	}
	return s.decide(ctx, settings, event, wanted)
}

// decide is Decide with the chat's switch already resolved, so a sweep over
// many candidate tournaments asks about it once instead of once each.
//
// A chat that neither auto-subscribes to this game nor wants to hear about
// new tournaments is deliberately left undecided: nothing was sent, so
// nothing is recorded, and turning the switch on later still works. That is
// the whole failure this type exists to fix, and recording a silent skip
// would reintroduce it.
func (s *EventOfferReconciler) decide(ctx context.Context, settings chat.Settings, event competition.Event, wanted bool) (bool, error) {
	if event.ID == (common.EventID{}) {
		return false, nil
	}
	auto := settings.AutoSubscribesTo(event.Game)
	if !auto && !wanted {
		return false, nil
	}

	kind := subscription.EventOffered
	eventType := "telegram.big-event-discovered"
	if auto {
		kind, eventType = subscription.EventAutoSubscribed, "telegram.auto-subscribed"
	}
	// Claimed before the subscription is written so two instances cannot
	// both join the chat and both announce it; the subscribe itself is an
	// upsert, so the loser simply does nothing.
	isNew, err := s.Offers.RecordEventOffer(ctx, settings.ChatID, event.ID, kind, s.Clock.Now())
	if err != nil || !isNew {
		return false, err
	}
	if auto {
		sub := subscription.EventSubscription{ChatID: settings.ChatID, EventID: event.ID, SubscribedAt: s.Clock.Now(), Active: true}
		if _, err := s.Subscriptions.Subscribe(ctx, sub); err != nil {
			return false, err
		}
	}
	if !wanted {
		// Joined, but this room asked not to be told about it.
		return true, nil
	}

	n := common.BigEventDiscoveredNotification{
		ChatID: settings.ChatID.Value, TopicID: settings.DefaultTopicID,
		EventID: event.ID.Value.String(), EventName: event.Name, Tier: string(event.Tier),
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return false, err
	}
	aggregateID := fmt.Sprintf("%d:big-event:%s", settings.ChatID.Value, event.ID.Value)
	if _, err := s.Outbox.Enqueue(ctx, "TELEGRAM_CHAT", aggregateID, eventType, string(payload)); err != nil {
		return false, err
	}
	return true, nil
}
