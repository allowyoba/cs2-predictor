package httpapi

import (
	"context"
	"net/http"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// The four readings of a record that need more than the outcome: by format
// and stage, by what the wrong calls had in common, by the shape of
// somebody's reading, and by how strong the other side was.
//
// One read of the facts, four aggregations — see scoring.PredictionFact.
// The arithmetic lives in the domain so the rules can be tested without a
// server; this file turns it into JSON and says what the numbers rest on.

// MiniAppFacts is the read behind them.
type MiniAppFacts interface {
	UserPredictionFacts(ctx context.Context, userID common.UserID, limit int) ([]scoring.PredictionFact, error)
}

type segmentDTO struct {
	Key         string `json:"key"`
	Accuracy    int    `json:"accuracy"`
	Predictions int    `json:"predictions"`
	DeltaPP     int    `json:"delta_pp"`
}

// heatCellDTO is one square of the format-by-stage grid.
type heatCellDTO struct {
	Format      string `json:"format"`
	Stage       string `json:"stage"`
	Accuracy    int    `json:"accuracy"`
	Predictions int    `json:"predictions"`
	DeltaPP     int    `json:"delta_pp"`
}

// mistakeDTO is one thing the wrong calls had in common, and how many of
// them it was true of. A single mistake appears under every tag that fits:
// they are observations about the match, not a diagnosis.
type mistakeDTO struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// axisDTO is one side of the fingerprint. Sample travels with the score
// because an axis resting on four predictions and one resting on eighty
// are not the same claim.
type axisDTO struct {
	Axis   string `json:"axis"`
	Score  int    `json:"score"`
	Sample int    `json:"sample"`
	// Meaningful is false when there is not enough behind it to draw. The
	// app shows the axis anyway, greyed, rather than hiding it: an absent
	// axis reads as a bug, a grey one reads as "not yet".
	Meaningful bool `json:"meaningful"`
}

type segmentsDTO struct {
	// Total is how many settled predictions everything here rests on.
	Total int `json:"total"`
	// Heatmap is format against stage.
	Heatmap []heatCellDTO `json:"heatmap"`
	// Mistakes is the wrong calls, grouped by what was true of them.
	Mistakes []mistakeDTO `json:"mistakes"`
	// MistakeTotal is how many wrong calls there were, which is not the
	// sum of the tags — one mistake can carry several.
	MistakeTotal int `json:"mistake_total"`
	// Fingerprint is the four axes.
	Fingerprint []axisDTO `json:"fingerprint"`
	// Opponents is accuracy by how the other side was ranked, and Ranked
	// says how many predictions could be read that way at all. Rankings
	// cover the teams a feed lists, so this is a slice of the record
	// rather than all of it, and the screen says so.
	Opponents []segmentDTO `json:"opponents"`
	Ranked    int          `json:"ranked"`
}

// segmentsHandler serves GET /api/miniapp/v1/me/segments.
func segmentsHandler(deps MiniAppDeps, facts MiniAppFacts) http.Handler {
	return withMiniAppAuth(deps, func(w http.ResponseWriter, r *http.Request, user authenticatedUser) {
		if facts == nil {
			http.Error(w, "segments are not configured", http.StatusServiceUnavailable)
			return
		}
		selected, err := scopeFrom(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		all, err := facts.UserPredictionFacts(r.Context(), user.ID, scoring.PredictionFactsMax)
		if err != nil {
			miniAppError(w, deps.Log, "segments unavailable", err)
			return
		}

		scoped := make([]scoring.PredictionFact, 0, len(all))
		for _, fact := range all {
			if selected.keepsGame(fact.Game) && selected.keepsChat(fact.ChatID) {
				scoped = append(scoped, fact)
			}
		}
		writeJSON(w, buildSegments(scoped))
	})
}

func buildSegments(facts []scoring.PredictionFact) segmentsDTO {
	body := segmentsDTO{
		Total:       len(facts),
		Heatmap:     buildHeatmap(facts),
		Mistakes:    buildMistakes(facts),
		Fingerprint: buildFingerprint(facts),
	}
	for _, fact := range facts {
		if !fact.Correct {
			body.MistakeTotal++
		}
		if fact.Ranked() {
			body.Ranked++
		}
	}
	for _, segment := range scoring.SegmentsBy(facts, func(f scoring.PredictionFact) (string, bool) {
		if !f.Ranked() {
			return "", false
		}
		return string(scoring.ClassifyRankGap(f.RankGap())), true
	}) {
		body.Opponents = append(body.Opponents, segmentDTO{
			Key: segment.Key, Accuracy: segment.AccuracyPercent(),
			Predictions: segment.Predictions, DeltaPP: segment.DeltaPP,
		})
	}
	return body
}

// buildHeatmap crosses series format with stage. A pair with too little
// behind it is left out entirely rather than drawn faintly: an empty
// square says "not enough" more clearly than a pale one.
func buildHeatmap(facts []scoring.PredictionFact) []heatCellDTO {
	segments := scoring.SegmentsBy(facts, func(f scoring.PredictionFact) (string, bool) {
		stage := scoring.ClassifyStage(f.Stage)
		if stage == scoring.StageUnknown || f.Format.Size == 0 {
			return "", false
		}
		return f.Format.Label() + "|" + string(stage), true
	})
	out := make([]heatCellDTO, 0, len(segments))
	for _, segment := range segments {
		format, stage, ok := splitKey(segment.Key)
		if !ok {
			continue
		}
		out = append(out, heatCellDTO{
			Format: format, Stage: stage, Accuracy: segment.AccuracyPercent(),
			Predictions: segment.Predictions, DeltaPP: segment.DeltaPP,
		})
	}
	return out
}

func splitKey(key string) (string, string, bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}

// buildMistakes counts what the wrong calls had in common.
func buildMistakes(facts []scoring.PredictionFact) []mistakeDTO {
	// How often each team was predicted at all, so "barely knew them" is a
	// fact about this person's history rather than about one match.
	seen := map[string]int{}
	for _, fact := range facts {
		seen[fact.PickedTeam]++
	}
	counts := map[scoring.MistakeTag]int{}
	for _, fact := range facts {
		for _, tag := range scoring.TagMistake(fact, seen[fact.PickedTeam]) {
			counts[tag]++
		}
	}
	// A fixed order, so the bars do not reshuffle between two views of the
	// same record.
	order := []scoring.MistakeTag{
		scoring.MistakeEvenMatch, scoring.MistakeUpsetPick, scoring.MistakeBO1,
		scoring.MistakePlayoff, scoring.MistakeTopTier, scoring.MistakeUnfamiliar,
	}
	out := make([]mistakeDTO, 0, len(order))
	for _, tag := range order {
		if counts[tag] == 0 {
			continue
		}
		out = append(out, mistakeDTO{Tag: string(tag), Count: counts[tag]})
	}
	return out
}

func buildFingerprint(facts []scoring.PredictionFact) []axisDTO {
	print := scoring.BuildFingerprint(facts)
	axes := []struct {
		name string
		axis scoring.Axis
	}{
		{"consistency", print.Consistency},
		{"upset_reading", print.UpsetReading},
		{"big_matches", print.BigMatches},
		{"multi_game", print.MultiGame},
	}
	out := make([]axisDTO, 0, len(axes))
	for _, entry := range axes {
		out = append(out, axisDTO{
			Axis: entry.name, Score: entry.axis.Score,
			Sample: entry.axis.Sample, Meaningful: entry.axis.Meaningful(),
		})
	}
	return out
}
