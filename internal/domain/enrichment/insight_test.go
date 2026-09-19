package enrichment

import "testing"

// An insight block with nothing in it must not be rendered at all — an
// empty card in a poll reads as broken data rather than as missing data.
func TestMatchInsight_IsEmpty(t *testing.T) {
	var nilInsight *MatchInsight
	if !nilInsight.IsEmpty() {
		t.Fatal("a nil insight is empty")
	}
	if !(&MatchInsight{}).IsEmpty() {
		t.Fatal("an insight with no ranking, form or head-to-head is empty")
	}
	rank := 1
	withRanking := &MatchInsight{Home: TeamInsight{Ranking: &TeamRanking{GlobalRank: &rank}}}
	if withRanking.IsEmpty() {
		t.Fatal("one side's ranking is enough to have something to show")
	}
}
