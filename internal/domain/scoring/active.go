package scoring

import (
	"context"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// Predictions that have not been settled yet.
//
// Everything else in this package is about what already happened. This is
// the other half of somebody's record: the polls they have answered whose
// matches have not been played, which is what a person actually wants to
// see when they open the app on a match day.

// ActivePrediction is one vote on a match still to come.
type ActivePrediction struct {
	ChatID    common.ChatID
	ChatTitle string
	Game      competition.GameCode
	EventName string
	// ScheduledAt is nil for a match whose time the provider has not
	// published yet — a real state, not a missing value, and one the
	// screen says out loud rather than guessing at.
	ScheduledAt    *time.Time
	FirstTeamName  string
	SecondTeamName string
	PredictedScore competition.MatchScore
	// StreamURL is the broadcast to watch it on, empty when none is
	// published. Taken in the chat's own preferred language.
	StreamURL string
	// ClosesAt is when the poll stops accepting answers.
	ClosesAt time.Time
}

// ActivePredictionsMax bounds the read: a person with predictions on more
// matches than this is looking at a schedule, not a list.
const ActivePredictionsMax = 50

// EventMedal is one placing the bot awarded: which tournament, in which
// chat, and when.
//
// A bare count answers "how many medals do I have" and nothing else. The
// question somebody actually has in front of a trophy shelf is which
// tournament each one is for — a gold in a four-person chat's minor and a
// gold in a Major are not the same object, and a number that adds them
// together describes neither.
type EventMedal struct {
	ChatID    common.ChatID
	ChatTitle string
	EventName string
	Game      competition.GameCode
	// Place is 1, 2 or 3.
	Place     int
	AwardedAt time.Time
}

// ActiveRepository reads the not-yet-settled half of somebody's record,
// and the medals they have won per chat.
type ActiveRepository interface {
	// ActivePredictions lists this person's votes on matches that have not
	// been played yet, soonest first.
	ActivePredictions(ctx context.Context, userID common.UserID, limit int) ([]ActivePrediction, error)
	// UserMedals lists this person's tournament placings, newest first —
	// the medals already awarded by the bot, not a score recomputed here.
	UserMedals(ctx context.Context, userID common.UserID) ([]EventMedal, error)
}
