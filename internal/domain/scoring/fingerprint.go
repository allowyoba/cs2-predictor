package scoring

// A short description of how somebody predicts, rather than how much.
//
// Four axes, not five: the obvious fifth — how well somebody judges their
// own confidence — needs them to state a confidence before the match, and
// this bot asks for a scoreline. An axis drawn from data that does not
// exist is the one thing a profile like this must not do, so it is absent
// and the screen says why.

// Fingerprint scores each axis out of 100, with the sample behind it.
type Fingerprint struct {
	// Consistency is how little the record swings: the share of results
	// that match the one before them. A person who alternates right and
	// wrong every time scores zero however good their average is.
	Consistency Axis
	// UpsetReading is accuracy on the matches where the lower-ranked side
	// was backed — the calls that are worth something precisely because
	// the ranking disagreed.
	UpsetReading Axis
	// BigMatches is accuracy in S and A tier tournaments.
	BigMatches Axis
	// MultiGame is how evenly a record holds across disciplines: the
	// weakest game's accuracy against the strongest. One game scores zero
	// sample rather than a perfect hundred, because a single discipline
	// says nothing about breadth.
	MultiGame Axis
}

// Axis is one score and the evidence under it. Sample matters as much as
// the score: an axis resting on four predictions is shown differently from
// one resting on eighty.
type Axis struct {
	Score  int
	Sample int
}

// FingerprintMinSample is the fewest predictions an axis needs before its
// score is worth drawing at all.
const FingerprintMinSample = 5

// Meaningful reports whether an axis rests on enough to be shown.
func (a Axis) Meaningful() bool { return a.Sample >= FingerprintMinSample }

// BuildFingerprint measures the four axes.
func BuildFingerprint(facts []PredictionFact) Fingerprint {
	return Fingerprint{
		Consistency:  consistencyAxis(facts),
		UpsetReading: upsetAxis(facts),
		BigMatches:   bigMatchAxis(facts),
		MultiGame:    multiGameAxis(facts),
	}
}

// consistencyAxis measures streakiness rather than accuracy: how often a
// result is the same as the one before it. Facts arrive newest first,
// which does not matter here — agreement between neighbours reads the same
// in either direction.
func consistencyAxis(facts []PredictionFact) Axis {
	if len(facts) < 2 {
		return Axis{}
	}
	var same int
	for i := 1; i < len(facts); i++ {
		if facts[i].Correct == facts[i-1].Correct {
			same++
		}
	}
	return Axis{Score: accuracyPercent(same, len(facts)-1), Sample: len(facts)}
}

func upsetAxis(facts []PredictionFact) Axis {
	var axis Axis
	var correct int
	for _, fact := range facts {
		if !fact.Ranked() {
			continue
		}
		switch ClassifyRankGap(fact.RankGap()) {
		case RankUnderdog, RankHeavyUnderdog:
			axis.Sample++
			if fact.Correct {
				correct++
			}
		}
	}
	axis.Score = accuracyPercent(correct, axis.Sample)
	return axis
}

func bigMatchAxis(facts []PredictionFact) Axis {
	var axis Axis
	var correct int
	for _, fact := range facts {
		if fact.EventTier != "s" && fact.EventTier != "a" {
			continue
		}
		axis.Sample++
		if fact.Correct {
			correct++
		}
	}
	axis.Score = accuracyPercent(correct, axis.Sample)
	return axis
}

// multiGameAxis scores the weakest discipline against the strongest, so a
// person who is strong everywhere scores high and one who carries a single
// game scores low. Only disciplines with enough history count.
func multiGameAxis(facts []PredictionFact) Axis {
	perGame := SegmentsBy(facts, func(f PredictionFact) (string, bool) {
		return string(f.Game), f.Game != ""
	})
	if len(perGame) < 2 {
		return Axis{}
	}
	best, worst, sample := 0, 100, 0
	for _, segment := range perGame {
		accuracy := segment.AccuracyPercent()
		if accuracy > best {
			best = accuracy
		}
		if accuracy < worst {
			worst = accuracy
		}
		sample += segment.Predictions
	}
	if best == 0 {
		return Axis{Sample: sample}
	}
	return Axis{Score: accuracyPercent(worst, best), Sample: sample}
}
