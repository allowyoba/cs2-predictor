package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// ProviderFailureThreshold is how many consecutive failures turn a
// provider from flaky into broken for alerting purposes. Every data source
// here is polled on a schedule and retried, so a single failure is noise —
// what an administrator needs to know is that the retries are not working.
const ProviderFailureThreshold = 3

// AdminAlerter sends operational notices to the configured administrator
// chats: a new release is live, a provider stopped working, a provider
// started working again. Delivery goes through the outbox like every other
// message, so an alert about the bot being unwell is not itself lost to a
// Telegram hiccup.
//
// ChatIDs is DEPLOY_NOTIFY_CHAT_IDS (Config.TeamMatchOperatorChatIDs) —
// reused rather than introducing a second admin-contact list. With none
// configured every method is a no-op, which is the whole opt-out.
type AdminAlerter struct {
	Outbox   common.Outbox
	Releases common.ReleaseAnnouncementStore
	ChatIDs  []int64
	// Switches decides which of these an operator chat actually wants.
	// Each kind is off until somebody turns it on from /alerts — an alert
	// nobody chose is the same unread noise as any other notification, and
	// an operator who mutes the ones they do not act on is likelier to
	// read the ones they kept.
	Switches common.NotifySwitchboard
	Log      *slog.Logger
}

var _ common.ProviderHealthObserver = (*AdminAlerter)(nil)

// AnnounceRelease tells the administrators which build is now serving,
// exactly once per version+commit. Called at startup rather than by the
// deploy pipeline: the bot then reports what is actually running, whatever
// put it there — a pipeline deploy, a manual rollback, or a restart onto an
// image somebody changed by hand.
func (a *AdminAlerter) AnnounceRelease(ctx context.Context, version, commit string) {
	if len(a.ChatIDs) == 0 || a.Releases == nil || version == "" {
		return
	}
	claimed, err := a.Releases.Claim(ctx, version, commit)
	if err != nil {
		a.Log.Error("release announcement claim failed", "version", version, "error", err)
		return
	}
	if !claimed {
		return // already announced, by an earlier start or another instance
	}
	a.fanOut(ctx, "RELEASE", version+"-"+commit, func(chatID int64) common.AdminAlertNotification {
		return common.AdminAlertNotification{
			ChatID: chatID, Kind: common.AdminAlertRelease, Version: version, Commit: commit,
		}
	})
}

func (a *AdminAlerter) ProviderDown(ctx context.Context, provider string, failures int, lastError string) {
	a.fanOut(ctx, "PROVIDER_HEALTH", provider+":down", func(chatID int64) common.AdminAlertNotification {
		return common.AdminAlertNotification{
			ChatID: chatID, Kind: common.AdminAlertProviderDown, Provider: provider,
			Failures: failures, Detail: common.TruncateForLog([]byte(lastError)),
		}
	})
}

func (a *AdminAlerter) ProviderRecovered(ctx context.Context, provider string) {
	a.fanOut(ctx, "PROVIDER_HEALTH", provider+":up", func(chatID int64) common.AdminAlertNotification {
		return common.AdminAlertNotification{
			ChatID: chatID, Kind: common.AdminAlertProviderRecovered, Provider: provider,
		}
	})
}

// DeadLetters reports undelivered messages that will stay undelivered
// until somebody replays them. aggregateID carries the count, so a pile
// that has grown since the last alert is a new message rather than a
// duplicate of one already sent.
func (a *AdminAlerter) DeadLetters(ctx context.Context, count int, detail string) {
	a.fanOut(ctx, "OUTBOX", "dead-letters:"+itoa(count), func(chatID int64) common.AdminAlertNotification {
		return common.AdminAlertNotification{
			ChatID: chatID, Kind: common.AdminAlertDeadLetters, Count: count, Detail: detail,
		}
	})
}

// WebhookBroken/WebhookRecovered report Telegram failing, and then
// managing, to deliver updates to this bot.
func (a *AdminAlerter) WebhookBroken(ctx context.Context, pending int, lastError string) {
	a.fanOut(ctx, "WEBHOOK", "broken", func(chatID int64) common.AdminAlertNotification {
		return common.AdminAlertNotification{
			ChatID: chatID, Kind: common.AdminAlertWebhookBroken, Count: pending,
			Detail: common.TruncateForLog([]byte(lastError)),
		}
	})
}

func (a *AdminAlerter) WebhookRecovered(ctx context.Context) {
	a.fanOut(ctx, "WEBHOOK", "recovered", func(chatID int64) common.AdminAlertNotification {
		return common.AdminAlertNotification{ChatID: chatID, Kind: common.AdminAlertWebhookRecovered}
	})
}

// HostPressure and HostRecovered report the machine running out of
// something, and getting it back. Separate from the provider alerts
// because the cause is different in kind: a provider being down is
// somebody else's outage, a disk filling up is ours, and only one of them
// is fixed by waiting.
func (a *AdminAlerter) HostPressure(ctx context.Context, resource, value, limit string) {
	a.fanOut(ctx, "HOST", resource+":pressure", func(chatID int64) common.AdminAlertNotification {
		return common.AdminAlertNotification{
			ChatID: chatID, Kind: common.AdminAlertHostPressure,
			Provider: resource, Detail: value + " / " + limit,
		}
	})
}

func (a *AdminAlerter) HostRecovered(ctx context.Context, resource, value string) {
	a.fanOut(ctx, "HOST", resource+":recovered", func(chatID int64) common.AdminAlertNotification {
		return common.AdminAlertNotification{
			ChatID: chatID, Kind: common.AdminAlertHostRecovered,
			Provider: resource, Detail: value,
		}
	})
}

// fanOut enqueues one message per administrator chat. Best effort by
// design: an alert that cannot be enqueued is logged and dropped rather
// than propagated, since every caller is either a background job or a
// startup path that must not fail over a notification.
func (a *AdminAlerter) fanOut(ctx context.Context, aggregateType, aggregateID string, build func(chatID int64) common.AdminAlertNotification) {
	gate := NotifyGate{Switches: a.Switches}
	for _, chatID := range a.ChatIDs {
		notification := build(chatID)
		wanted, err := gate.OperatorWants(ctx, chatID, notification.Kind)
		if err != nil {
			a.Log.Error("admin alert preference lookup failed", "chatId", chatID, "error", err)
			continue
		}
		if !wanted {
			continue
		}
		payload, err := json.Marshal(notification)
		if err != nil {
			a.Log.Error("admin alert marshal failed", "error", err)
			continue
		}
		if _, err := a.Outbox.Enqueue(ctx, aggregateType, aggregateID, "telegram.admin-alert", string(payload)); err != nil {
			a.Log.Error("admin alert enqueue failed", "chatId", chatID, "error", err)
		}
	}
}

// ObservedSyncState wraps an enrichment.SyncStateRepository and reports the
// two transitions an administrator cares about — a provider crossing into
// broken, and the first success after that. Implemented as a decorator so
// no sync job has to know an observer exists: every one of them already
// funnels its outcome through this port.
type ObservedSyncState struct {
	enrichment.SyncStateRepository
	Observer common.ProviderHealthObserver
	// Threshold is the consecutive-failure count that counts as broken;
	// zero means ProviderFailureThreshold.
	Threshold int
}

func (o *ObservedSyncState) threshold() int {
	if o.Threshold > 0 {
		return o.Threshold
	}
	return ProviderFailureThreshold
}

func (o *ObservedSyncState) RecordFailure(ctx context.Context, provider enrichment.Source, errText string) error {
	// Read before writing: the stored counter is what says whether this
	// failure is the one that crosses the line, and only the crossing is
	// worth a message.
	before, stateErr := o.State(ctx, provider)
	err := o.SyncStateRepository.RecordFailure(ctx, provider, errText)
	if err != nil || stateErr != nil || before == nil || o.Observer == nil {
		return err
	}
	if before.ConsecutiveFailures+1 == o.threshold() {
		o.Observer.ProviderDown(ctx, string(provider), before.ConsecutiveFailures+1, errText)
	}
	return nil
}

func (o *ObservedSyncState) RecordSuccess(ctx context.Context, provider enrichment.Source) error {
	before, stateErr := o.State(ctx, provider)
	err := o.SyncStateRepository.RecordSuccess(ctx, provider)
	if err != nil || stateErr != nil || before == nil || o.Observer == nil {
		return err
	}
	if before.ConsecutiveFailures >= o.threshold() {
		o.Observer.ProviderRecovered(ctx, string(provider))
	}
	return nil
}
