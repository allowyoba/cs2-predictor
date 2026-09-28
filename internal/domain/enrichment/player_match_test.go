package enrichment

import (
	"testing"

	"cs2predictor/internal/platform/common"
)

func candidate(nickname string) RosterCandidate {
	return RosterCandidate{PlayerID: common.NewPlayerID(), Nickname: nickname}
}

func TestMatchRoster_PairsOnNicknameIgnoringCase(t *testing.T) {
	navi := []RosterCandidate{candidate("s1mple"), candidate("b1t"), candidate("Aleksib")}
	matched, unmatched := MatchRoster(navi, []string{"S1MPLE", "b1t", "aleksib"})

	if len(matched) != 3 {
		t.Fatalf("expected the whole roster paired, got %d: %+v", len(matched), matched)
	}
	if len(unmatched) != 0 {
		t.Fatalf("expected nothing left over, got %v", unmatched)
	}
	for _, m := range matched {
		if m.Confidence != ConfidenceExactName {
			t.Fatalf("a case difference is an exact match, got %q for %q", m.Confidence, m.ExternalName)
		}
	}
}

// An exact match must win the player it belongs to even when an earlier
// nickname would have claimed them on similarity alone. A matcher that
// resolves each external nickname in one pass — exact, else nearest — gives
// "electronic" away to "electronicc" and then leaves the real "electronic"
// with nobody, which is a subscription pointed at the wrong person.
func TestMatchRoster_ExactMatchesWinBeforeFuzzyOnesAreTried(t *testing.T) {
	electronic := candidate("electronic")
	matched, unmatched := MatchRoster([]RosterCandidate{electronic}, []string{"electronicc", "electronic"})

	if len(matched) != 1 {
		t.Fatalf("expected exactly one pairing, got %+v", matched)
	}
	if matched[0].ExternalName != "electronic" || matched[0].Confidence != ConfidenceExactName {
		t.Fatalf("expected the exact nickname to win the player, got %+v", matched[0])
	}
	if len(unmatched) != 1 || unmatched[0] != "electronicc" {
		t.Fatalf("expected the near-miss left unmatched, got %v", unmatched)
	}
}

// A nickname nobody on the roster resembles is reported, not forced onto the
// closest player: that is how a subscription ends up following the wrong
// person.
func TestMatchRoster_LeavesAStrangerUnmatched(t *testing.T) {
	matched, unmatched := MatchRoster([]RosterCandidate{candidate("ZywOo"), candidate("apEX")}, []string{"donk"})

	if len(matched) != 0 {
		t.Fatalf("expected no match for an unrelated nickname, got %+v", matched)
	}
	if len(unmatched) != 1 || unmatched[0] != "donk" {
		t.Fatalf("expected donk reported as unmatched, got %v", unmatched)
	}
}

// Each side is used once: two feeds reporting the same nickname twice must
// not both land on the same player.
func TestMatchRoster_UsesEachPlayerOnce(t *testing.T) {
	only := candidate("donk")
	matched, unmatched := MatchRoster([]RosterCandidate{only}, []string{"donk", "donk"})

	if len(matched) != 1 || matched[0].PlayerID != only.PlayerID {
		t.Fatalf("expected one pairing, got %+v", matched)
	}
	if len(unmatched) != 1 {
		t.Fatalf("expected the duplicate reported as unmatched, got %v", unmatched)
	}
}

func TestMatchRoster_IgnoresBlankNicknames(t *testing.T) {
	matched, unmatched := MatchRoster([]RosterCandidate{candidate("donk")}, []string{"", "   "})

	if len(matched) != 0 {
		t.Fatalf("expected no pairing, got %+v", matched)
	}
	if len(unmatched) != 0 {
		t.Fatalf("a blank nickname is not an unmatched player, got %v", unmatched)
	}
}

// Nicknames are one token and every character counts, so the team-name
// scorer's organisational-word stripping must not be applied to them.
func TestFuzzyNicknameScore_DoesNotStripWords(t *testing.T) {
	if score := FuzzyNicknameScore("dev1ce", "device"); score >= PlayerNicknameAutoAcceptThreshold {
		t.Fatalf("dev1ce and device must not auto-accept, scored %d", score)
	}
	if score := FuzzyNicknameScore("Boombl4", "boombl4"); score != 100 {
		t.Fatalf("a case difference must score 100, got %d", score)
	}
	if score := FuzzyNicknameScore("", "donk"); score != 0 {
		t.Fatalf("an empty nickname must score 0, got %d", score)
	}
}
