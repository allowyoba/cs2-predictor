package scoring

import "sort"

// Four readings of the same settled predictions.
//
// All of them are arithmetic over PredictionFact, kept here rather than in
// the API so the rules — what counts as a playoff, what counts as an upset,
// how small a sample is too small to quote — are stated once and can be
// tested without a web server.

// Segment is one cell, bar or row: a label, a record, and how far that
// record sits from the person's own average.
type Segment struct {
	Key         string
	Correct     int
	Predictions int
	// DeltaPP is the distance from the overall accuracy, in percentage
	// points. A percentage means little on its own; the distance from
	// somebody's own average is the finding.
	DeltaPP int
}

func (s Segment) AccuracyPercent() int { return accuracyPercent(s.Correct, s.Predictions) }

// SegmentMinSample is the fewest predictions worth showing a percentage
// for. Below it a cell is left empty rather than filled with noise.
const SegmentMinSample = 3

// StageBucket classifies a provider's free-text stage name.
//
// The names are whatever the feed says — "Group A", "Playoffs", "Last
// Chance Qualifier", "Survival" — so they are grouped rather than trusted
// one by one, and anything unrecognised stays unrecognised instead of
// being forced into a bucket it might not belong to.
type StageBucket string

const (
	StageGroup     StageBucket = "group"
	StagePlayoff   StageBucket = "playoff"
	StageQualifier StageBucket = "qualifier"
	StageUnknown   StageBucket = "other"
)

// ClassifyStage maps a stage name to its bucket.
func ClassifyStage(name string) StageBucket {
	lower := lowerASCII(name)
	if lower == "" {
		return StageUnknown
	}
	// Checked in order: a "Playoff Qualifier" is a playoff, and reversing
	// these would file it as a qualifier on the strength of the second
	// word.
	for _, group := range stageWords {
		for _, word := range group.words {
			if contains(lower, word) {
				return group.bucket
			}
		}
	}
	return StageUnknown
}

// stageWords is what the providers actually write, in the order the words
// have to be tested.
var stageWords = []struct {
	bucket StageBucket
	words  []string
}{
	{StagePlayoff, []string{"playoff", "elimination", "final", "semi", "quarter", "bracket"}},
	{StageQualifier, []string{"qualifier", "play-in", "play in", "survival", "open"}},
	{StageGroup, []string{"group", "swiss", "league", "round robin"}},
}

// RankBucket is how the picked side stood against the other one.
type RankBucket string

const (
	RankHeavyFavourite RankBucket = "heavy_favourite"
	RankFavourite      RankBucket = "favourite"
	RankEven           RankBucket = "even"
	RankUnderdog       RankBucket = "underdog"
	RankHeavyUnderdog  RankBucket = "heavy_underdog"
)

// Ranking gaps, in places. Chosen against how esports rankings actually
// spread rather than as round numbers: the distance between first and
// fifth is a different kind of gap from the distance between fortieth and
// forty-fifth, and beyond about twenty places the favourite is simply the
// favourite.
const (
	heavyGap = 20
	closeGap = 5
)

// ClassifyRankGap buckets a gap in ranking places.
func ClassifyRankGap(gap int) RankBucket {
	switch {
	case gap >= heavyGap:
		return RankHeavyFavourite
	case gap > closeGap:
		return RankFavourite
	case gap >= -closeGap:
		return RankEven
	case gap > -heavyGap:
		return RankUnderdog
	default:
		return RankHeavyUnderdog
	}
}

// SegmentsBy groups facts by a key and measures each group against the
// overall accuracy. Groups below SegmentMinSample are dropped: a cell that
// says 100% on one prediction is worse than an empty one.
func SegmentsBy(facts []PredictionFact, key func(PredictionFact) (string, bool)) []Segment {
	overall := accuracyPercent(countCorrect(facts), len(facts))
	grouped := map[string]*Segment{}
	for _, fact := range facts {
		name, ok := key(fact)
		if !ok {
			continue
		}
		segment, exists := grouped[name]
		if !exists {
			segment = &Segment{Key: name}
			grouped[name] = segment
		}
		segment.Predictions++
		if fact.Correct {
			segment.Correct++
		}
	}
	out := make([]Segment, 0, len(grouped))
	for _, segment := range grouped {
		if segment.Predictions < SegmentMinSample {
			continue
		}
		segment.DeltaPP = segment.AccuracyPercent() - overall
		out = append(out, *segment)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Predictions != out[b].Predictions {
			return out[a].Predictions > out[b].Predictions
		}
		return out[a].Key < out[b].Key
	})
	return out
}

// MistakeTag names one thing that was true about a prediction that went
// wrong. A mistake can carry several: they are observations about the
// match, not a single cause, and pretending to pick one would be inventing
// an explanation the data does not contain.
type MistakeTag string

const (
	MistakeBO1        MistakeTag = "bo1"
	MistakeEvenMatch  MistakeTag = "even"
	MistakeUpsetPick  MistakeTag = "upset_pick"
	MistakePlayoff    MistakeTag = "playoff"
	MistakeTopTier    MistakeTag = "top_tier"
	MistakeUnfamiliar MistakeTag = "unfamiliar"
)

// MistakeMinSeen is how few predictions on a team still counts as barely
// knowing it.
const MistakeMinSeen = 3

// TagMistake lists everything true about one wrong call.
//
// seen is how many predictions this person has made on the picked team
// overall, which is what makes "barely knew them" a fact rather than a
// guess.
func TagMistake(fact PredictionFact, seen int) []MistakeTag {
	if fact.Correct {
		return nil
	}
	var tags []MistakeTag
	if fact.Format.Kind == "BEST_OF" && fact.Format.Size == 1 {
		tags = append(tags, MistakeBO1)
	}
	if fact.Ranked() {
		switch ClassifyRankGap(fact.RankGap()) {
		case RankEven:
			tags = append(tags, MistakeEvenMatch)
		case RankUnderdog, RankHeavyUnderdog:
			tags = append(tags, MistakeUpsetPick)
		}
	}
	if ClassifyStage(fact.Stage) == StagePlayoff {
		tags = append(tags, MistakePlayoff)
	}
	if fact.EventTier == "s" || fact.EventTier == "a" {
		tags = append(tags, MistakeTopTier)
	}
	if seen < MistakeMinSeen {
		tags = append(tags, MistakeUnfamiliar)
	}
	return tags
}

func countCorrect(facts []PredictionFact) int {
	var n int
	for _, fact := range facts {
		if fact.Correct {
			n++
		}
	}
	return n
}

func contains(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// lowerASCII lowercases the Latin letters a stage name is written with.
// The provider writes these in English; anything else is left alone rather
// than put through a full Unicode fold for no gain.
func lowerASCII(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}
