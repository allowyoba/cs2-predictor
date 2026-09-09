// Package prediction holds the poll domain: creating a poll from a match,
// recording/withdrawing votes, and closing/cancelling/rescheduling polls.
package prediction

import (
	"context"
	"errors"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// ErrPollMissingTelegramMessage marks an attempt to close/cancel a poll that
// was never actually sent to Telegram (no TelegramMessageID recorded) —
// distinguishable via errors.Is from any other Gateway failure.
var ErrPollMissingTelegramMessage = errors.New("poll has no telegram message id")

type PollStatus string

const (
	PollOpen      PollStatus = "OPEN"
	PollClosed    PollStatus = "CLOSED"
	PollCancelled PollStatus = "CANCELLED"
)

type Option struct {
	Index int
	Score competition.MatchScore
}

type Poll struct {
	ID                common.PollID
	ChatID            common.ChatID
	MatchID           common.MatchID
	TopicID           *int64
	TelegramPollID    *string
	TelegramMessageID *int64
	Options           []Option
	Status            PollStatus
	ClosesAt          time.Time
}

type Vote struct {
	PollID      common.PollID
	UserID      common.UserID
	OptionIndex int
	Username    *string
	DisplayName string
	VotedAt     time.Time
}

// Repository is the persistence port. Votes are keyed by (pollID, userID) —
// one vote per user per poll, upserted on repeat SaveVote — and polls are
// keyed uniquely by (matchID, chatID) for FindByMatchAndChat-driven
// idempotent poll creation.
type Repository interface {
	FindPoll(ctx context.Context, id common.PollID) (*Poll, error)
	FindByTelegramPollID(ctx context.Context, telegramPollID string) (*Poll, error)
	FindByMatchAndChat(ctx context.Context, matchID common.MatchID, chatID common.ChatID) (*Poll, error)
	OpenPollsForMatch(ctx context.Context, matchID common.MatchID) ([]Poll, error)
	PollsForMatch(ctx context.Context, matchID common.MatchID) ([]Poll, error)
	SavePoll(ctx context.Context, poll Poll) (Poll, error)
	OpenPollsDue(ctx context.Context, at time.Time) ([]Poll, error)
	Votes(ctx context.Context, pollID common.PollID) ([]Vote, error)
	// PollsAwaitingReminder returns open polls closing before the given
	// time that have not been reminded about yet, oldest deadline first.
	// Marking is a separate step (MarkReminded) so a crash between the two
	// costs at most a duplicate reminder rather than a silently skipped one.
	PollsAwaitingReminder(ctx context.Context, closingBefore time.Time, limit int) ([]Poll, error)
	// MarkReminded records that this poll's pre-close reminder went out.
	// Idempotent: marking an already-marked poll is not an error.
	MarkReminded(ctx context.Context, pollID common.PollID, at time.Time) error
	// ChatParticipants lists everyone who has voted in this chat since the
	// given time — the people a reminder could plausibly be for. It is
	// deliberately activity-based: Telegram gives a bot no member list.
	ChatParticipants(ctx context.Context, chatID common.ChatID, since time.Time) ([]common.UserID, error)
	SaveVote(ctx context.Context, vote Vote) error
	RemoveVote(ctx context.Context, pollID common.PollID, userID common.UserID) error
}

type SentPoll struct {
	PollID    string
	MessageID int64
	TopicID   *int64
}

// Gateway is the Telegram side-effect port for sending/closing/cancelling a
// poll message.
type Gateway interface {
	Send(ctx context.Context, poll Poll) (SentPoll, error)
	Close(ctx context.Context, poll Poll) error
	Cancel(ctx context.Context, poll Poll) error
}
