package scoring

import (
	"context"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// Who somebody backs, against who actually wins.
//
// Accuracy per team answers "how well do you read this team". It does not
// answer the more interesting question: do you back them more often than
// they deserve. Those come apart — a person can read a team well and
// rarely pick them, or pick them constantly and be wrong about it — and
// the gap between the two is the closest thing here to a blind spot.

// TeamBias is one team's side of that comparison, over the matches this
// person actually voted on.
type TeamBias struct {
	TeamID   common.TeamID
	TeamName string
	Game     competition.GameCode
	// Matches is how many of this team's settled matches the person voted
	// on at all — the denominator for everything else here.
	Matches int
	// Picks is how many of those they called this team to win.
	Picks int
	// Wins is how many of those this team actually won. The comparison
	// with Picks is the whole point: it is measured over the same matches,
	// not against the team's overall record, because a record built from
	// matches somebody never saw says nothing about their reading of it.
	Wins int
	// Correct is how many of those matches the person called right,
	// whichever side they picked.
	Correct int
}

// PickRatePercent is how often this person backs the team.
func (b TeamBias) PickRatePercent() int { return accuracyPercent(b.Picks, b.Matches) }

// WinRatePercent is how often the team actually won those same matches.
func (b TeamBias) WinRatePercent() int { return accuracyPercent(b.Wins, b.Matches) }

// AccuracyPercent is how often this person called those matches right.
func (b TeamBias) AccuracyPercent() int { return accuracyPercent(b.Correct, b.Matches) }

// BiasPP is the gap, in percentage points: positive means backed more
// often than the team won, negative means backed less often than it did.
//
// Neither direction is a mistake on its own — the number is an
// observation, not a verdict — which is why nothing here calls it an
// error. A team backed less often than it wins is just as interesting as
// the reverse, and rather more actionable.
func (b TeamBias) BiasPP() int { return b.PickRatePercent() - b.WinRatePercent() }

// TeamBiasMinMatches is the fewest matches worth quoting a bias for.
// Below it the gap is arithmetic on a handful of games.
const TeamBiasMinMatches = 4

// BiasRepository reads it.
type BiasRepository interface {
	// UserTeamBias returns one row per team the person has voted on,
	// busiest first, at most limit of them.
	UserTeamBias(ctx context.Context, userID common.UserID, limit int) ([]TeamBias, error)
}
