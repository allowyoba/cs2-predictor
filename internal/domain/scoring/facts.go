package scoring

import (
	"context"
	"time"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

// One settled prediction with everything known about the match behind it.
//
// Four different screens ask about the same predictions from different
// angles — by format and stage, by what went wrong, by the shape of
// somebody's reading, by how strong the opponent was — and each of them
// would otherwise be its own query over the same rows. This is that query,
// once; the segments are arithmetic on top.
type PredictionFact struct {
	PlayedAt time.Time
	Game     competition.GameCode
	ChatID   common.ChatID
	// EventTier is "s", "a", … as the provider reports it; empty when it
	// does not say.
	EventTier string
	// Stage is the provider's own stage name ("Playoffs", "Group A"), free
	// text and sometimes absent. Classified rather than trusted verbatim.
	Stage string
	// Format is the series shape: BO1, BO3 and so on.
	Format competition.SeriesFormat
	// PickedTeam is the side this person called to win; Opponent is the
	// other one.
	PickedTeam   string
	OpponentTeam string
	// PickedRank and OpponentRank are the teams' global ranking positions
	// when both are known. Nil is the common case rather than an error:
	// rankings cover the teams a feed lists, and most matches have at
	// least one side outside it.
	PickedRank   *int
	OpponentRank *int
	Correct      bool
}

// Ranked reports whether this match can be read as favourite against
// underdog at all.
func (f PredictionFact) Ranked() bool { return f.PickedRank != nil && f.OpponentRank != nil }

// RankGap is how far ahead the picked team stood, in ranking places:
// positive means the person backed the better-ranked side. Only meaningful
// when Ranked.
func (f PredictionFact) RankGap() int {
	if !f.Ranked() {
		return 0
	}
	// Ranks count upwards from first place, so the better team has the
	// smaller number and the sign has to be flipped to read as "ahead".
	return *f.OpponentRank - *f.PickedRank
}

// PredictionFactsMax bounds one read. Deep enough for every segment on the
// analytics screen, shallow enough that it stays one page of rows.
const PredictionFactsMax = 400

// FactsRepository reads them, newest first.
type FactsRepository interface {
	UserPredictionFacts(ctx context.Context, userID common.UserID, limit int) ([]PredictionFact, error)
}
