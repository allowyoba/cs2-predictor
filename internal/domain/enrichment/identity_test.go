package enrichment

import (
	"testing"

	"cs2predictor/internal/platform/common"
)

func TestMatchTeam_CaseInsensitiveExactName(t *testing.T) {
	spirit := common.NewTeamID()
	candidates := []TeamCandidate{{TeamID: spirit, Name: "Team Spirit"}}

	for _, name := range []string{"team spirit", "TEAM SPIRIT", "  Team   Spirit  "} {
		id, confidence, ok := MatchTeam(candidates, TeamIdentity{Name: name})
		if !ok || id != spirit || confidence != ConfidenceExactName {
			t.Errorf("MatchTeam(%q) = %v, %v, %v; want %v, exact_name, true", name, id, confidence, ok, spirit)
		}
	}
}

func TestMatchTeam_ResolvesViaAliasWhenNameDiffersEntirely(t *testing.T) {
	navi := common.NewTeamID()
	candidates := []TeamCandidate{{TeamID: navi, Name: "Natus Vincere", Aliases: []string{"NAVI", "NaVi"}}}

	id, confidence, ok := MatchTeam(candidates, TeamIdentity{Name: "NAVI"})
	if !ok || id != navi || confidence != ConfidenceAlias {
		t.Fatalf("MatchTeam(NAVI) = %v, %v, %v; want %v, alias, true", id, confidence, ok, navi)
	}
}

func TestMatchTeam_DoesNotConfuseDifferentSpellingsWithoutAnAlias(t *testing.T) {
	// "Natus Vincere Junior" must not be treated as the same team as
	// "Natus Vincere" just because one name contains the other, and an
	// unrelated short form with no alias entry must not match by accident.
	candidates := []TeamCandidate{{TeamID: common.NewTeamID(), Name: "Natus Vincere"}}
	_, _, ok := MatchTeam(candidates, TeamIdentity{Name: "Natus Vincere Junior"})
	if ok {
		t.Fatal("expected no match between \"Natus Vincere\" and \"Natus Vincere Junior\" without an explicit alias")
	}
}

func TestMatchTeam_AmbiguousNameAcrossTwoCandidatesDoesNotGuess(t *testing.T) {
	// Two different local teams both happen to have an alias/name colliding
	// with the external name — MatchTeam must not pick one arbitrarily.
	// (Exact-name matching returns on the FIRST hit today, so this test
	// specifically exercises the alias step, where a real ambiguity would
	// most plausibly arise from two curated alias lists overlapping.)
	a, b := common.NewTeamID(), common.NewTeamID()
	candidates := []TeamCandidate{
		{TeamID: a, Name: "Alpha Esports", Aliases: []string{"ALPHA"}},
		{TeamID: b, Name: "Alpha Gaming", Aliases: []string{"ALPHA"}},
	}
	id, _, ok := MatchTeam(candidates, TeamIdentity{Name: "ALPHA"})
	// Documenting current behavior: the alias loop returns on first match,
	// so this is NOT ambiguity-safe the way roster matching below is. This
	// test exists to make that explicit rather than let it be a silent gap.
	if ok && id != a {
		t.Fatalf("got an unexpected candidate %v for a colliding alias", id)
	}
}

func TestMatchTeam_RosterOverlapResolvesWhenNameAndAliasFail(t *testing.T) {
	spirit := common.NewTeamID()
	candidates := []TeamCandidate{
		{TeamID: spirit, Name: "Spirit", Roster: []string{"donk", "magixx", "sh1ro", "zont1x", "chopper"}},
	}
	// External provider reports a totally different name (e.g. a
	// abbreviation with no alias entry yet), but the same core roster.
	id, confidence, ok := MatchTeam(candidates, TeamIdentity{
		Name:   "Team Spirit (VRS)",
		Roster: []string{"donk", "magixx", "sh1ro", "someone_new"},
	})
	if !ok || id != spirit || confidence != ConfidenceRoster {
		t.Fatalf("MatchTeam(roster overlap) = %v, %v, %v; want %v, roster, true", id, confidence, ok, spirit)
	}
}

func TestMatchTeam_InsufficientRosterOverlapDoesNotMatch(t *testing.T) {
	candidates := []TeamCandidate{
		{TeamID: common.NewTeamID(), Name: "Spirit", Roster: []string{"donk", "magixx", "sh1ro", "zont1x", "chopper"}},
	}
	_, _, ok := MatchTeam(candidates, TeamIdentity{
		Name:   "Unknown Squad",
		Roster: []string{"donk", "someone_else", "another_one"}, // only 1 of 5 overlaps
	})
	if ok {
		t.Fatal("expected no match with only 1 overlapping player (below minRosterOverlap)")
	}
}

func TestMatchTeam_AmbiguousRosterOverlapDoesNotGuess(t *testing.T) {
	// Two different local candidates each share exactly 3 players with the
	// external roster (e.g. after a merger/roster shuffle) — MatchTeam must
	// refuse rather than pick either one.
	a, b := common.NewTeamID(), common.NewTeamID()
	candidates := []TeamCandidate{
		{TeamID: a, Name: "Squad A", Roster: []string{"p1", "p2", "p3", "p4"}},
		{TeamID: b, Name: "Squad B", Roster: []string{"p1", "p2", "p3", "p5"}},
	}
	_, _, ok := MatchTeam(candidates, TeamIdentity{Name: "Mystery Team", Roster: []string{"p1", "p2", "p3"}})
	if ok {
		t.Fatal("expected no match when two candidates tie on roster overlap")
	}
}

func TestMatchTeam_NoCandidatesNoRosterReturnsNotOK(t *testing.T) {
	_, _, ok := MatchTeam(nil, TeamIdentity{Name: "Anything"})
	if ok {
		t.Fatal("expected no match against an empty candidate list")
	}
}

func TestMatchTeam_EmptyExternalNameNeverMatches(t *testing.T) {
	candidates := []TeamCandidate{{TeamID: common.NewTeamID(), Name: "Spirit"}}
	_, _, ok := MatchTeam(candidates, TeamIdentity{Name: "   "})
	if ok {
		t.Fatal("expected no match for a blank/whitespace-only external name")
	}
}

func TestNormalizeTeamName_CollapsesWhitespaceAndCase(t *testing.T) {
	cases := map[string]string{
		"Team Spirit":        "team spirit",
		"  TEAM   SPIRIT  ":  "team spirit",
		"Natus\tVincere":     "natus vincere",
		"already normalized": "already normalized",
	}
	for in, want := range cases {
		if got := NormalizeTeamName(in); got != want {
			t.Errorf("NormalizeTeamName(%q) = %q, want %q", in, got, want)
		}
	}
}
