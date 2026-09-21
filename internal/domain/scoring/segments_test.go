package scoring_test

import (
	"testing"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
)

func rank(v int) *int { return &v }

func fact(correct bool, opts ...func(*scoring.PredictionFact)) scoring.PredictionFact {
	f := scoring.PredictionFact{Correct: correct, Game: competition.GameCS2}
	for _, opt := range opts {
		opt(&f)
	}
	return f
}

func withStage(name string) func(*scoring.PredictionFact) {
	return func(f *scoring.PredictionFact) { f.Stage = name }
}

func withRanks(picked, other int) func(*scoring.PredictionFact) {
	return func(f *scoring.PredictionFact) { f.PickedRank, f.OpponentRank = rank(picked), rank(other) }
}

// Stage names are whatever the provider writes. They are grouped, and
// anything unrecognised stays unrecognised rather than being forced into a
// bucket it might not belong in — a wrong bucket is a wrong percentage
// with a confident label on it.
func TestClassifyStage(t *testing.T) {
	cases := map[string]scoring.StageBucket{
		"Playoffs":              scoring.StagePlayoff,
		"Elimination Round":     scoring.StagePlayoff,
		"Grand Final":           scoring.StagePlayoff,
		"Group A":               scoring.StageGroup,
		"Group Stage":           scoring.StageGroup,
		"Swiss Stage":           scoring.StageGroup,
		"Last Chance Qualifier": scoring.StageQualifier,
		"Play-In":               scoring.StageQualifier,
		"Survival":              scoring.StageQualifier,
		"":                      scoring.StageUnknown,
		"Showmatch":             scoring.StageUnknown,
	}
	for name, want := range cases {
		if got := scoring.ClassifyStage(name); got != want {
			t.Errorf("ClassifyStage(%q) = %q, want %q", name, got, want)
		}
	}
}

// The sign is the whole point: backing the better-ranked side is a
// favourite, backing the worse-ranked one is an upset call, and getting
// that backwards would label every brave call as a safe one.
func TestRankGapReadsFromThePickedSide(t *testing.T) {
	// Picked 3rd against 40th: a heavy favourite.
	if got := scoring.ClassifyRankGap(fact(true, withRanks(3, 40)).RankGap()); got != scoring.RankHeavyFavourite {
		t.Errorf("picking 3rd over 40th = %q, want a heavy favourite", got)
	}
	// Picked 40th against 3rd: a heavy underdog call.
	if got := scoring.ClassifyRankGap(fact(true, withRanks(40, 3)).RankGap()); got != scoring.RankHeavyUnderdog {
		t.Errorf("picking 40th over 3rd = %q, want a heavy underdog", got)
	}
	// Two places apart is an even match either way round.
	for _, f := range []scoring.PredictionFact{fact(true, withRanks(8, 10)), fact(true, withRanks(10, 8))} {
		if got := scoring.ClassifyRankGap(f.RankGap()); got != scoring.RankEven {
			t.Errorf("a two-place gap = %q, want even", got)
		}
	}
	// A match with one side unranked cannot be read this way at all.
	unranked := fact(true)
	if unranked.Ranked() || unranked.RankGap() != 0 {
		t.Error("an unranked match must not pretend to have a gap")
	}
}

// A percentage on three predictions is noise with a label. Segments below
// the threshold are dropped rather than shown small.
func TestSegmentsByDropsSamplesTooSmallToQuote(t *testing.T) {
	facts := []scoring.PredictionFact{
		fact(true, withStage("Playoffs")), fact(true, withStage("Playoffs")),
		fact(false, withStage("Playoffs")), fact(true, withStage("Playoffs")),
		// One prediction in a group stage: not enough to say anything.
		fact(false, withStage("Group A")),
	}
	segments := scoring.SegmentsBy(facts, func(f scoring.PredictionFact) (string, bool) {
		return string(scoring.ClassifyStage(f.Stage)), f.Stage != ""
	})
	if len(segments) != 1 || segments[0].Key != string(scoring.StagePlayoff) {
		t.Fatalf("segments = %+v, want only the playoff one", segments)
	}
	if segments[0].Predictions != 4 || segments[0].AccuracyPercent() != 75 {
		t.Fatalf("segment = %+v, want 3 of 4", segments[0])
	}
	// Measured against the overall record, not against nothing: 75% here
	// versus 60% overall.
	if segments[0].DeltaPP != 15 {
		t.Fatalf("delta = %d п.п., want the distance from the overall 60%%", segments[0].DeltaPP)
	}
}

// A wrong call can be several things at once, and the tags say all of them
// rather than choosing a cause the data does not contain.
func TestTagMistakeNamesEverythingThatWasTrue(t *testing.T) {
	bo1, _ := competition.NewSeriesFormat(competition.BestOf, 1)
	wrong := scoring.PredictionFact{
		Correct: false, Format: bo1, Stage: "Playoffs", EventTier: "s",
		PickedRank: rank(30), OpponentRank: rank(4),
	}
	tags := scoring.TagMistake(wrong, 1)
	want := map[scoring.MistakeTag]bool{
		scoring.MistakeBO1: true, scoring.MistakeUpsetPick: true,
		scoring.MistakePlayoff: true, scoring.MistakeTopTier: true,
		scoring.MistakeUnfamiliar: true,
	}
	if len(tags) != len(want) {
		t.Fatalf("tags = %v, want %d of them", tags, len(want))
	}
	for _, tag := range tags {
		if !want[tag] {
			t.Errorf("unexpected tag %q", tag)
		}
	}

	// A correct call is not a mistake, whatever else was true about it.
	right := wrong
	right.Correct = true
	if tags := scoring.TagMistake(right, 1); tags != nil {
		t.Errorf("a correct prediction was tagged: %v", tags)
	}

	// A team somebody has predicted often is not unfamiliar.
	for _, tag := range scoring.TagMistake(wrong, 20) {
		if tag == scoring.MistakeUnfamiliar {
			t.Error("a team seen twenty times was called unfamiliar")
		}
	}
}
