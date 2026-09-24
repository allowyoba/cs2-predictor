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
	// Game and EventName say what this bet was about. The bot's own bets
	// screen is already scoped to one chat, but a cross-chat list — the
	// Mini App's history — is unreadable without them: two rows of team
	// names with no tournament and no discipline look like duplicates.
	Game      competition.GameCode
	EventName string
	// EventTier is the tournament's own tier ("s", "a", …). A Major and a
	// qualifier read as the same line without it, and "your accuracy by
	// tournament" is mostly a question about which tournaments.
	EventTier string
}

// BetResultKind classifies how a settled bet turned out, for filtering the
// "my bets" screen: exact-score hit, winner-only hit, or miss. Both hit kinds
// count as Correct, but calling the winner is worth a point while calling the
// exact scoreline is worth several — the thing people actually boast about —
// so they read as different outcomes even though the underlying poll only
// ever tracked a single win/loss bit.
type BetResultKind string

const (
	BetResultExact  BetResultKind = "exact"
	BetResultWinner BetResultKind = "winner"
	BetResultMiss   BetResultKind = "miss"
)

// ResultKind classifies this bet into one of the three BetResultKind values.
func (b UserBet) ResultKind() BetResultKind {
	switch {
	case b.Correct && b.PredictedScore == b.ActualScore:
		return BetResultExact
	case b.Correct:
		return BetResultWinner
	default:
		return BetResultMiss
	}
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
	// UserBetsForEvent returns userID's settled bets within one chat and one
	// tournament only, oldest first (unlike UserBets' newest-first) so a
	// reader follows the tournament chronologically match by match. Backs
	// both "my results in this tournament" and, for an arbitrary userID
	// picked off the leaderboard, "this participant's results" — same data,
	// same rendering, just a different subject.
	UserBetsForEvent(ctx context.Context, userID common.UserID, chatID common.ChatID, eventID common.EventID, limit int) ([]UserBet, error)
}
