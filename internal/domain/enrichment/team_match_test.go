package enrichment

import "testing"

func TestFuzzyNameScore_OrgWordDifferenceScoresAboveAutoAccept(t *testing.T) {
	cases := []struct{ a, b string }{
		{"Team Vitality", "Vitality"},
		{"Team Spirit", "Spirit"},
		{"OG Esports", "OG"},
	}
	for _, c := range cases {
		if got := FuzzyNameScore(c.a, c.b); got < FuzzyAutoAcceptThreshold {
			t.Fatalf("FuzzyNameScore(%q, %q) = %d, want >= %d (auto-accept)", c.a, c.b, got, FuzzyAutoAcceptThreshold)
		}
	}
}

func TestFuzzyNameScore_UnrelatedNamesScoreBelowRequestFloor(t *testing.T) {
	cases := []struct{ a, b string }{
		{"Vitality", "NAVI"},
		{"FaZe", "G2"},
		{"Spirit", "Falcons"},
	}
	for _, c := range cases {
		if got := FuzzyNameScore(c.a, c.b); got >= FuzzyRequestThreshold {
			t.Fatalf("FuzzyNameScore(%q, %q) = %d, want < %d (not even worth a request)", c.a, c.b, got, FuzzyRequestThreshold)
		}
	}
}

func TestFuzzyNameScore_ExactMatchAfterNormalizationIs100(t *testing.T) {
	if got := FuzzyNameScore("Team   SPIRIT", "spirit"); got != 100 {
		t.Fatalf("FuzzyNameScore = %d, want 100", got)
	}
}

// A name that is ENTIRELY an org word ("Team") must not collapse to an
// empty string and compare equal to every other all-org-word name.
func TestFuzzyNameScore_AllOrgWordsFallsBackToUnstrippedName(t *testing.T) {
	if got := FuzzyNameScore("Team", "Club"); got >= FuzzyRequestThreshold {
		t.Fatalf("FuzzyNameScore(%q, %q) = %d, want a low score, not a false match via empty-string collapse", "Team", "Club", got)
	}
}

func TestFuzzyNameScore_EmptyNameScoresZero(t *testing.T) {
	if got := FuzzyNameScore("", "Vitality"); got != 0 {
		t.Fatalf("FuzzyNameScore(\"\", ...) = %d, want 0", got)
	}
}

func TestCrowdAdjustedScore_NeverCrossesTheAutoAcceptBar(t *testing.T) {
	score := 80
	for i := 0; i < 10; i++ {
		score = CrowdAdjustedScore(score, TeamMatchAnswerYes)
	}
	if score > CrowdScoreCap {
		t.Fatalf("score = %d after repeated yes votes, want capped at %d", score, CrowdScoreCap)
	}
	if CrowdScoreCap >= FuzzyAutoAcceptThreshold {
		t.Fatalf("CrowdScoreCap (%d) must stay strictly below FuzzyAutoAcceptThreshold (%d)", CrowdScoreCap, FuzzyAutoAcceptThreshold)
	}
}

func TestCrowdAdjustedScore_NoPenaltyIsStrongerThanYesBoost(t *testing.T) {
	up := CrowdAdjustedScore(50, TeamMatchAnswerYes) - 50
	down := 50 - CrowdAdjustedScore(50, TeamMatchAnswerNo)
	if down <= up {
		t.Fatalf("a 'no' moved the score by %d, a 'yes' by %d — want 'no' to move it further (wrongly raising a bad candidate costs more)", down, up)
	}
}

func TestCrowdAdjustedScore_NeverGoesNegative(t *testing.T) {
	if got := CrowdAdjustedScore(5, TeamMatchAnswerNo); got != 0 {
		t.Fatalf("CrowdAdjustedScore(5, no) = %d, want floored at 0", got)
	}
}
