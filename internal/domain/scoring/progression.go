package scoring

import (
	"context"
	"time"

	"cs2predictor/internal/platform/common"
)

// ProgressionPoint is one settled prediction's contribution to a rating
// chart: who it belongs to, when the match it was for was played, and how
// many points it earned (0 for a wrong guess — still a point in the series,
// since a flat stretch is as meaningful as a climb). The caller turns a
// time-ordered slice of these per user into a cumulative-points line.
type ProgressionPoint struct {
	UserID      common.UserID
	DisplayName string
	PlayedAt    time.Time
	Points      int
}

// ProgressionRepository reads the raw match-by-match point events a rating
// chart is built from, for every participant in chatID within period,
// oldest first. Separate from the core Repository (like
// PersonalBetsRepository) because rendering a chart needs a fundamentally
// different shape — per-event points, not a pre-aggregated standing.
type ProgressionRepository interface {
	PointsProgression(ctx context.Context, chatID common.ChatID, period StatsPeriod) ([]ProgressionPoint, error)
}
