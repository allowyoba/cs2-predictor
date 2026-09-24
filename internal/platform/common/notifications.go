package common

import "context"

// Every message the bot sends on its own initiative is off until somebody
// turns it on. Not "off for new chats" — off, for everyone, including the
// chats and people that already exist: a notification nobody asked for is
// spam however long the bot has been sending it.
//
// What is NOT covered here, deliberately, is the bot answering something
// somebody just did: a poll's own result, the confirmation of an
// unsubscribe, the verdict on a Mini App request. Those are replies, and a
// reply nobody receives is a broken command rather than a quiet one.

// NotifyScope says whose switch it is. The values reach SQL as a checked
// column, so they must stay stable.
type NotifyScope string

const (
	// ScopeChat is a switch a chat's managers set, covering what the bot
	// posts into that room.
	ScopeChat NotifyScope = "chat"
	// ScopeUser is a switch a person sets for their own DM.
	ScopeUser NotifyScope = "user"
	// ScopeOperator is a switch for one of the bot's own administrator
	// chats, covering the operational alerts sent there.
	ScopeOperator NotifyScope = "operator"
)

// ChatNotificationKind names something the bot posts into a group.
type ChatNotificationKind string

const (
	// ChatNotifyDigests covers the monthly and annual leaderboard posts.
	ChatNotifyDigests ChatNotificationKind = "digests"
	// ChatNotifyEventFinished is the tournament's closing recap with its
	// final standings and nominations.
	ChatNotifyEventFinished ChatNotificationKind = "event_finished"
	// ChatNotifyEventEve is the "this tournament starts tomorrow" note.
	ChatNotifyEventEve ChatNotificationKind = "event_eve"
	// ChatNotifyNewEvents covers both halves of tournament discovery: the
	// offer to join a newly found top-tier event, and the note that the
	// chat was joined to one automatically.
	ChatNotifyNewEvents ChatNotificationKind = "new_events"
	// ChatNotifyStreams is the broadcast link a closing poll can post.
	ChatNotifyStreams ChatNotificationKind = "streams"
	// ChatNotifyTargetCrossSell is the one-tap "this team/player you
	// follow is playing in a tournament you're not subscribed to yet"
	// offer. Reuses this same switchboard rather than a parallel
	// notification path, same as every other proactive message here.
	ChatNotifyTargetCrossSell ChatNotificationKind = "target_cross_sell"
)

// ChatNotificationKinds is the catalogue, in the order the settings screen
// shows it. A slice rather than a map so the switches never swap places.
var ChatNotificationKinds = []ChatNotificationKind{
	ChatNotifyNewEvents,
	ChatNotifyEventEve,
	ChatNotifyEventFinished,
	ChatNotifyDigests,
	ChatNotifyStreams,
	ChatNotifyTargetCrossSell,
}

// KnownChatNotification reports whether kind is one of the catalogue's,
// which is what keeps a callback payload from reaching the store.
func KnownChatNotification(kind ChatNotificationKind) bool {
	for _, known := range ChatNotificationKinds {
		if known == kind {
			return true
		}
	}
	return false
}

// AdminAlertKinds is the operator catalogue. The recovery notices are not
// separately switchable: an alert you can turn on without its "it is over
// again" counterpart is a worse alert, not a more configurable one — so
// each pair travels under the kind that opens it.
var AdminAlertKinds = []AdminAlertKind{
	AdminAlertRelease,
	AdminAlertProviderDown,
	AdminAlertWebhookBroken,
	AdminAlertDeadLetters,
	AdminAlertHostPressure,
}

// AlertSwitch is the kind an alert is filed under: the closing half of a
// pair asks about the kind that opened it.
func AlertSwitch(kind AdminAlertKind) AdminAlertKind {
	switch kind {
	case AdminAlertProviderRecovered:
		return AdminAlertProviderDown
	case AdminAlertWebhookRecovered:
		return AdminAlertWebhookBroken
	case AdminAlertHostRecovered:
		return AdminAlertHostPressure
	default:
		return kind
	}
}

// KnownAdminAlert reports whether kind has a switch of its own.
func KnownAdminAlert(kind AdminAlertKind) bool {
	for _, known := range AdminAlertKinds {
		if known == kind {
			return true
		}
	}
	return false
}

// NotifySwitchboard is the one store behind every switch. Absence is off,
// so nothing here has to be seeded for a new chat or a new person.
type NotifySwitchboard interface {
	// NotifyEnabled answers for one subject and kind. An unknown subject
	// is off rather than an error: the bot asks this about chats and
	// people it may never have stored a preference for.
	NotifyEnabled(ctx context.Context, scope NotifyScope, subject int64, kind string) (bool, error)
	// NotifySettings returns every switch this subject has turned on.
	NotifySettings(ctx context.Context, scope NotifyScope, subject int64) (map[string]bool, error)
	// SetNotifyEnabled writes one switch.
	SetNotifyEnabled(ctx context.Context, scope NotifyScope, subject int64, kind string, on bool) error
	// NotifySubjects narrows candidates to those with kind on — the fan-out
	// question, asked once per batch instead of once per recipient.
	NotifySubjects(ctx context.Context, scope NotifyScope, kind string, candidates []int64) ([]int64, error)
}
