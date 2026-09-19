package common

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ClusterLock guards a scheduled job so only one instance in a multi-instance
// deployment runs it at a time. Execute returns (result, false) if the lock
// could not be acquired — the caller must treat that as "skipped, another
// instance is running it", not an error. Backed by Postgres
// pg_try_advisory_lock in the postgres adapter.
type ClusterLock interface {
	Execute(ctx context.Context, name string, action func(ctx context.Context) error) (acquired bool, err error)
}

// UpdateDeduplicator makes Telegram webhook update processing idempotent.
// Claim must atomically record updateID and report whether this call was the
// first to claim it; Release un-claims it (e.g. after a failed handler) so a
// Telegram webhook retry can reprocess the same update.
type UpdateDeduplicator interface {
	Claim(ctx context.Context, updateID int64) (bool, error)
	Release(ctx context.Context, updateID int64) error
}

// OutboxMessage is a row in the transactional outbox.
type OutboxMessage struct {
	ID         uuid.UUID
	Type       string
	Payload    string
	OccurredAt time.Time
	Attempts   int
}

// OutboxMaxAttempts bounds how many times Failed can back off a message
// before Pending stops returning it — a message that hits this cap goes
// permanently quiet rather than erroring loudly, so callers that dispatch
// (see app.OutboxDispatcher) must treat crossing it as its own observable
// event (metric/log), not assume "no longer pending" means "resolved".
const OutboxMaxAttempts = 20

// Outbox is the transactional-outbox port. Enqueue must be called in the
// same DB transaction as the domain write it announces. Published/Failed
// take occurredAt (from Pending) so the adapter's UPDATE can prune to
// outbox_event's partition (migration 0031) instead of scanning all of them.
type Outbox interface {
	Enqueue(ctx context.Context, aggregateType, aggregateID, eventType, payload string) (uuid.UUID, error)
	Pending(ctx context.Context, limit int) ([]OutboxMessage, error)
	Published(ctx context.Context, id uuid.UUID, occurredAt time.Time) error
	Failed(ctx context.Context, id uuid.UUID, occurredAt time.Time, errText string) error
}

// WebhookInfo is Telegram's own account of how delivery to this bot is
// going, as reported by getWebhookInfo.
type WebhookInfo struct {
	URL string
	// PendingUpdateCount is how many updates Telegram is holding because
	// it could not hand them over.
	PendingUpdateCount int
	// LastErrorAt/LastErrorMessage describe the most recent delivery
	// failure, zero when Telegram has never failed to reach this bot.
	LastErrorAt      time.Time
	LastErrorMessage string
	IPAddress        string
}

// WebhookInspector reads that report. Implemented by the Telegram client,
// consumed by app.WebhookWatchdog.
type WebhookInspector interface {
	WebhookInfo(ctx context.Context) (WebhookInfo, error)
}

// DeadLetterStore is the operator's view of messages that ran out of
// retries. Separate from Outbox because nothing on the delivery path needs
// it: a dead letter is only ever looked at, or deliberately replayed, by a
// human — see app.DeadLetterWatch and the /outbox command.
type DeadLetterStore interface {
	// DeadLetters counts undelivered messages that have exhausted their
	// retry budget, grouped by event type, worst first.
	DeadLetters(ctx context.Context) ([]DeadLetterGroup, error)
	// ReplayDeadLetters puts them back in the delivery queue by clearing
	// their attempt count, and returns how many were released. Delivery
	// itself is unchanged: a message that fails again simply exhausts its
	// budget again rather than looping forever.
	ReplayDeadLetters(ctx context.Context) (int, error)
}

// DeadLetterGroup is one event type's dead letters: how many, when the
// oldest and newest arrived, and the error the last one recorded.
type DeadLetterGroup struct {
	EventType string
	Count     int
	Oldest    time.Time
	Newest    time.Time
	LastError string
}

// OutboxPublisher fans an outbox message out to its destination (Telegram)
// by event type.
type OutboxPublisher interface {
	Supports(eventType string) bool
	Publish(ctx context.Context, message OutboxMessage) error
}

// StandingNotification is a single leaderboard row as sent in both
// match-result and event-finished outbox payloads.
type StandingNotification struct {
	UserID       int64  `json:"userId"`
	DisplayName  string `json:"displayName"`
	Rank         int    `json:"rank"`
	PreviousRank *int   `json:"previousRank"`
	Points       int    `json:"points"`
	PointsDelta  int    `json:"pointsDelta"`
	// ExactPredictions/CorrectPredictions/Predictions mirror
	// scoring.UserStanding's own fields — carried through the outbox so a
	// published leaderboard can render the same "(exact-outcome-wrong)"
	// breakdown the live /stats view does, without a second query.
	ExactPredictions   int `json:"exactPredictions"`
	CorrectPredictions int `json:"correctPredictions"`
	Predictions        int `json:"predictions"`
}

// MatchResultNotification is the payload for the "telegram.match-result"
// outbox event type.
type MatchResultNotification struct {
	ChatID           int64                  `json:"chatId"`
	TopicID          *int64                 `json:"topicId"`
	ReplyToMessageID *int64                 `json:"replyToMessageId"`
	EventID          string                 `json:"eventId"`
	EventName        string                 `json:"eventName"`
	Stage            *string                `json:"stage"`
	Format           string                 `json:"format"`
	FirstTeam        string                 `json:"firstTeam"`
	SecondTeam       string                 `json:"secondTeam"`
	Score            string                 `json:"score"`
	Standings        []StandingNotification `json:"standings"`
}

// EventFinishedNotification is the payload for the "telegram.event-finished"
// outbox event type.
type EventFinishedNotification struct {
	ChatID    int64                  `json:"chatId"`
	TopicID   *int64                 `json:"topicId"`
	EventName string                 `json:"eventName"`
	Standings []StandingNotification `json:"standings"`
	// Awards are the nominations picked for this tournament — at most a
	// few, chosen from a larger pool so two tournaments in a row rarely
	// read the same (see scoring.PickEventAwards).
	Awards []EventAwardNotification `json:"awards,omitempty"`
}

// DigestStanding is a compact leaderboard row for scheduled monthly/yearly
// summaries. Keeping this separate from StandingNotification avoids changing
// match-result/event-finished payload semantics just to carry activity stats.
type DigestStanding struct {
	DisplayName string `json:"displayName"`
	Rank        int    `json:"rank"`
	Points      int    `json:"points"`
	Predictions int    `json:"predictions"`
	Accuracy    int    `json:"accuracy"`
}

type MonthlyDigestNotification struct {
	ChatID         int64            `json:"chatId"`
	TopicID        *int64           `json:"topicId"`
	Year           int              `json:"year"`
	Month          int              `json:"month"`
	MonthStandings []DigestStanding `json:"monthStandings"`
	YearStandings  []DigestStanding `json:"yearStandings"`
}

type AnnualComebackNotification struct {
	DisplayName string `json:"displayName"`
	StartRank   int    `json:"startRank"`
	FinalRank   int    `json:"finalRank"`
}

type AnnualUserMetricNotification struct {
	DisplayName string `json:"displayName"`
	Value       int    `json:"value"`
	Predictions int    `json:"predictions,omitempty"`
	Accuracy    int    `json:"accuracy,omitempty"`
}

type AnnualTeamSynergyNotification struct {
	DisplayName string `json:"displayName"`
	TeamName    string `json:"teamName"`
	Correct     int    `json:"correct"`
	Predictions int    `json:"predictions"`
	Accuracy    int    `json:"accuracy"`
}

type AnnualHighlightsNotification struct {
	Comeback    *AnnualComebackNotification    `json:"comeback,omitempty"`
	Sniper      *AnnualUserMetricNotification  `json:"sniper,omitempty"`
	Expert      *AnnualUserMetricNotification  `json:"expert,omitempty"`
	Exact       *AnnualUserMetricNotification  `json:"exact,omitempty"`
	Streak      *AnnualUserMetricNotification  `json:"streak,omitempty"`
	TeamSynergy *AnnualTeamSynergyNotification `json:"teamSynergy,omitempty"`
}

type AnnualDigestNotification struct {
	ChatID            int64                        `json:"chatId"`
	TopicID           *int64                       `json:"topicId"`
	Year              int                          `json:"year"`
	DecemberStandings []DigestStanding             `json:"decemberStandings"`
	YearStandings     []DigestStanding             `json:"yearStandings"`
	Participants      int                          `json:"participants"`
	Predictions       int                          `json:"predictions"`
	Highlights        AnnualHighlightsNotification `json:"highlights"`
}

// BigEventDiscoveredNotification is the payload for the
// "telegram.big-event-discovered" outbox event type: sent once to every
// active chat not already subscribed to a newly discovered S/A tier
// tournament, suggesting they add it.
type BigEventDiscoveredNotification struct {
	ChatID    int64  `json:"chatId"`
	TopicID   *int64 `json:"topicId"`
	EventID   string `json:"eventId"`
	EventName string `json:"eventName"`
	Tier      string `json:"tier"`
}

// TeamMatchAskNotification is the payload for the "telegram.team-match-ask"
// outbox event type: one crowd-review question sent to one helper's DM. See
// app.TeamMatchService for the pipeline this feeds — the helper's answer
// only ever nudges a candidate's score, never confirms a mapping by itself.
type TeamMatchAskNotification struct {
	UserID          int64  `json:"userId"`
	RequestID       string `json:"requestId"`
	ExternalName    string `json:"externalName"`
	CandidateTeamID string `json:"candidateTeamId"`
	CandidateName   string `json:"candidateName"`
	Locale          string `json:"locale"`
}

// TeamMatchOperatorPingNotification is the payload for the
// "telegram.team-match-operator-ping" outbox event type: a one-line heads
// up to a configured operator chat that a new team-match request needs
// review (Config.TeamMatchOperatorChatIDs), sent once per request created,
// not once per crowd response.
type TeamMatchOperatorPingNotification struct {
	ChatID       int64  `json:"chatId"`
	ExternalName string `json:"externalName"`
}

// AdminAlertKind names what an administrator is being told about. The
// values are persisted in outbox payloads, so they must stay stable.
type AdminAlertKind string

const (
	// AdminAlertRelease reports that a new build is now serving traffic.
	AdminAlertRelease AdminAlertKind = "release"
	// AdminAlertProviderDown reports a data provider that has failed often
	// enough in a row to be considered broken rather than flaky.
	AdminAlertProviderDown AdminAlertKind = "provider_down"
	// AdminAlertProviderRecovered closes the loop on an AdminAlertProviderDown:
	// without it, an administrator has no way to tell a still-broken
	// provider from one that quietly healed.
	AdminAlertProviderRecovered AdminAlertKind = "provider_recovered"
	// AdminAlertWebhookBroken reports that Telegram is failing to deliver
	// updates to this bot. Nothing else notices: the process is healthy,
	// the database answers, and the chats simply go quiet.
	AdminAlertWebhookBroken AdminAlertKind = "webhook_broken"
	// AdminAlertWebhookRecovered closes that loop.
	AdminAlertWebhookRecovered AdminAlertKind = "webhook_recovered"
	// AdminAlertDeadLetters reports messages that have run out of retries
	// and will never be delivered without a deliberate replay.
	AdminAlertDeadLetters AdminAlertKind = "dead_letters"
)

// AdminAlertNotification is the payload for the "telegram.admin-alert"
// outbox event type: an operational heads-up to each configured
// administrator chat (DEPLOY_NOTIFY_CHAT_IDS). Deliberately one payload
// type for every kind — these are rare, low-volume messages, and one
// publisher that switches on Kind beats three near-identical ones.
type AdminAlertNotification struct {
	ChatID int64          `json:"chatId"`
	Kind   AdminAlertKind `json:"kind"`
	// Version/Commit are set for AdminAlertRelease.
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
	// Provider/Failures/Detail are set for the provider kinds. Detail is
	// the provider's own last error, truncated for a chat message.
	Provider string `json:"provider,omitempty"`
	Failures int    `json:"failures,omitempty"`
	Detail   string `json:"detail,omitempty"`
	// Count carries the "how many" of a kind that is about a quantity:
	// undelivered updates for the webhook kinds, dead letters for
	// AdminAlertDeadLetters.
	Count int `json:"count,omitempty"`
}

// SuggestionNotification is the payload for the "telegram.suggestion"
// outbox event type: one accepted idea, delivered to an administrator
// chat. The sender is named so a reply is possible at all — an idea worth
// acting on is usually worth asking a question about first.
type SuggestionNotification struct {
	ChatID       int64  `json:"chatId"`
	SuggestionID string `json:"suggestionId"`
	UserID       int64  `json:"userId"`
	DisplayName  string `json:"displayName"`
	Username     string `json:"username,omitempty"`
	Text         string `json:"text"`
}

// ReleaseAnnouncementStore records which releases have already been
// announced, so a restart, a second instance or a rollback-and-forward
// cannot repeat the same announcement. Claim returns true only for the
// caller that recorded it first.
type ReleaseAnnouncementStore interface {
	Claim(ctx context.Context, version, commit string) (bool, error)
}

// ProviderHealthObserver is notified when a data provider crosses between
// working and broken. Implemented by the admin alerter; the gateway and
// the enrichment sync-state both call it, which is why it lives here
// rather than in either of their packages.
type ProviderHealthObserver interface {
	// ProviderDown fires once per transition into a broken state, never
	// per failed call — an outage otherwise produces one message per retry.
	ProviderDown(ctx context.Context, provider string, failures int, lastError string)
	// ProviderRecovered fires on the first success after ProviderDown.
	ProviderRecovered(ctx context.Context, provider string)
}

// RetentionRepository prunes the tables that grow with traffic rather than
// with the domain: the webhook dedup ledger, published outbox rows,
// resolved confirmation requests, and the admin change history. Each method
// deletes rows strictly older than cutoff and reports how many went, so the
// job can log a real number instead of "done".
type RetentionRepository interface {
	DeleteProcessedUpdatesBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeletePublishedOutboxBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteResolvedUnsubscribesBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteAdminActionsBefore(ctx context.Context, cutoff time.Time) (int64, error)

	// DeleteProcessedUpdatesExceeding/DeletePublishedOutboxExceeding are the
	// row-count backstop under the TTL-based deletes above: keep at most
	// maxRows of the most recent rows, regardless of how young the rest are.
	// A time window alone caps how long rows live, not how big the table
	// gets in between sweeps at high traffic volume — this caps that
	// directly. maxRows <= 0 disables it, same "keep everything" escape
	// hatch the TTL fields use.
	DeleteProcessedUpdatesExceeding(ctx context.Context, maxRows int) (int64, error)
	DeletePublishedOutboxExceeding(ctx context.Context, maxRows int) (int64, error)

	// Maintain outbox_event's daily RANGE partitions (migration 0031). day
	// is a partition's UTC calendar date; Drop is a no-op if the partition
	// is missing or still holds rows.
	EnsureOutboxPartition(ctx context.Context, day time.Time) error
	DropOutboxPartitionIfEmpty(ctx context.Context, day time.Time) (bool, error)
}

// NotificationKind names one of the private nudges a person can opt into.
// The string values are the wire form used in callback data and stored
// preference columns alike, so they must stay stable.
type NotificationKind string

const (
	// NotifyResultRecaps DMs the person their own result when a match they
	// predicted is settled.
	NotifyResultRecaps NotificationKind = "recaps"
	// NotifyPollReminders DMs the person shortly before a poll closes in a
	// chat they play in, if they haven't voted yet.
	NotifyPollReminders NotificationKind = "reminders"
)

// NotificationAudience narrows a candidate list to the people who actually
// asked to hear about something privately. Both nudges are opt-in and both
// need a DM-reachable user, so this is the single gate they share — a bot
// that starts messaging people because they once voted in a group is spam.
//
// Declared here rather than on chat.Repository because the jobs that use
// it (settlement, the reminder scheduler) have no other reason to depend
// on the chat domain.
type NotificationAudience interface {
	// Recipients returns the subset of candidates who opted into kind and
	// can be reached by DM. Order is not significant.
	Recipients(ctx context.Context, kind NotificationKind, candidates []UserID) ([]UserID, error)
}

// ResultRecapNotification is the payload for the "telegram.result-recap"
// outbox event type: one per voter who opted into recaps, sent to their own
// DM after a match they predicted is settled.
type ResultRecapNotification struct {
	// UserID doubles as the private chat id.
	UserID     int64  `json:"userId"`
	ChatTitle  string `json:"chatTitle"`
	EventName  string `json:"eventName"`
	FirstTeam  string `json:"firstTeam"`
	SecondTeam string `json:"secondTeam"`
	// Score is the actual result; Predicted is what this person picked.
	Score     string `json:"score"`
	Predicted string `json:"predicted"`
	Points    int    `json:"points"`
	Locale    string `json:"locale"`
}

// PollReminderNotification is the payload for the "telegram.poll-reminder"
// outbox event type: one per opted-in participant who hasn't voted yet on a
// poll that is about to close.
type PollReminderNotification struct {
	UserID     int64  `json:"userId"`
	ChatTitle  string `json:"chatTitle"`
	FirstTeam  string `json:"firstTeam"`
	SecondTeam string `json:"secondTeam"`
	// MinutesLeft is rounded to whole minutes at enqueue time; the message
	// says "about N minutes" rather than a deadline it cannot guarantee.
	MinutesLeft int    `json:"minutesLeft"`
	Locale      string `json:"locale"`
}

// EventEveMatchNotification is one of the first matches a tournament opens
// with, as shown in the eve nudge.
type EventEveMatchNotification struct {
	// LocalTime is already rendered in the chat's own timezone at enqueue
	// time: the publisher has no business re-deriving a chat's clock.
	LocalTime  string `json:"localTime"`
	FirstTeam  string `json:"firstTeam"`
	SecondTeam string `json:"secondTeam"`
}

// EventEveNotification is the payload for the "telegram.event-eve" outbox
// event type: one message to a subscribed chat the day before a tournament
// it follows starts. It leads with what is actually happening (when, how
// many matches, who opens) rather than a bare "get ready", and carries the
// defending champion when the chat has one — the social hook that makes a
// prediction game worth showing up for.
type EventEveNotification struct {
	ChatID    int64  `json:"chatId"`
	TopicID   *int64 `json:"topicId"`
	EventName string `json:"eventName"`
	// StartsAt is the first match's local start time, pre-rendered.
	StartsAt string `json:"startsAt"`
	// FirstDayMatches counts every match scheduled on the opening day.
	FirstDayMatches int                         `json:"firstDayMatches"`
	Matches         []EventEveMatchNotification `json:"matches"`
	// Champion is whoever won this chat's previous finished tournament, if
	// there was one.
	Champion string `json:"champion,omitempty"`
}

// EventAwardNotification is one nomination in the post-tournament recap:
// a title, who won it, and the number that earned it.
type EventAwardNotification struct {
	// Kind is the nomination's stable key; the publisher maps it to a
	// localized title and a value format.
	Kind        string `json:"kind"`
	DisplayName string `json:"displayName"`
	Value       int    `json:"value"`
	// Detail carries a second number a nomination needs (a sample size, a
	// share) — zero when it needs none.
	Detail int `json:"detail,omitempty"`
}

// UnsubscribeConfirmationNotification is the payload for the
// "telegram.unsubscribe-confirmation" outbox event type: one per manager
// who is being asked to approve someone else's unsubscribe request. It goes
// through the outbox rather than being sent inline so the webhook that
// started it returns immediately, and so delivery gets the dispatcher's
// retries and pacing instead of N sequential sends inside a request.
type UnsubscribeConfirmationNotification struct {
	// UserID doubles as the private chat id: in Telegram a user's DM has
	// the same numeric id as the user.
	UserID    int64  `json:"userId"`
	RequestID string `json:"requestId"`
	ChatTitle string `json:"chatTitle"`
	// EventName is what the approval is about, already human-readable: a
	// tournament name, or a game's name for a game being switched off.
	EventName string `json:"eventName"`
	// Kind mirrors chat.ApprovalKind so the DM can say what is actually
	// being asked. Empty means the original unsubscribe, which is what
	// every message enqueued before this field existed carries.
	Kind      string `json:"kind,omitempty"`
	Requester string `json:"requester"`
	Locale    string `json:"locale"`
}
