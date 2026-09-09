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

// Outbox is the transactional-outbox port. Enqueue must be called in the
// same DB transaction as the domain write it announces.
type Outbox interface {
	Enqueue(ctx context.Context, aggregateType, aggregateID, eventType, payload string) (uuid.UUID, error)
	Pending(ctx context.Context, limit int) ([]OutboxMessage, error)
	Published(ctx context.Context, id uuid.UUID) error
	Failed(ctx context.Context, id uuid.UUID, errText string) error
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
	EventName string `json:"eventName"`
	Requester string `json:"requester"`
	Locale    string `json:"locale"`
}
