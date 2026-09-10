package scoring

import (
	"context"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// UserBet is one settled bet: full match context — both team names and the
// chat it was placed in — plus what was predicted against what actually
// happened. Unlike UserPrediction (which only tracks who-won, the raw
// material for the derived insights), this carries the exact scoreline on
// both sides and the points it earned, so a single row can stand on its own
// on a "my bets" screen.
type UserBet struct {
	PlayedAt       time.Time
	ChatID         common.ChatID
	ChatTitle      string
	FirstTeamName  string
	SecondTeamName string
	PredictedScore competition.MatchScore
	ActualScore    competition.MatchScore
	Correct        bool
	Points         int
}

// UserBetsMaxRows bounds how much history a "my bets" screen reads — enough
// for the deepest pagination anyone will actually click through, without
// pulling a heavy user's entire career into memory on every page turn.
const UserBetsMaxRows = 300

// PersonalBetsRepository reads one person's own settled bets, across every
// chat or narrowed to one, newest first. Separate from
// PersonalInsightsRepository because it returns the full per-bet scoreline
// and chat context that the insights screen (which only needs winner/loser
// and a team name) has no use for.
type PersonalBetsRepository interface {
	// UserBets returns the person's most recent settled bets, at most limit
	// of them, newest first. chatID narrows the result to one chat when
	// non-nil, or every chat they've played in when nil.
	UserBets(ctx context.Context, userID common.UserID, chatID *common.ChatID, limit int) ([]UserBet, error)
}
