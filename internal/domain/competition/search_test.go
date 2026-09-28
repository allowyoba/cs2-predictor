package competition

import "testing"

// The two cases this exists for, stated the way they were reported: a
// nickname typed the way it sounds must find the way it is written, in
// either alphabet.
func TestFoldForSearch_FindsANicknameHoweverItIsTyped(t *testing.T) {
	want := FoldForSearch("r0pz")
	for _, query := range []string{"ropz", "rops", "ропз", "ропс", "R0PZ", "Ropz"} {
		if got := FoldForSearch(query); got != want {
			t.Fatalf("FoldForSearch(%q) = %q, want %q (same as r0pz)", query, got, want)
		}
	}
}

func TestMatchesSearch_FindsATeamByTheWordPeopleActuallySay(t *testing.T) {
	for _, query := range []string{"spirit", "спирит", "Spirit", "team spirit", "спирит "} {
		if !MatchesSearch("Team Spirit", FoldForSearch(query)) {
			t.Fatalf("%q did not find Team Spirit", query)
		}
	}
}

func TestMatchesSearch_CoversTheNicknamesTheSceneWritesWithDigits(t *testing.T) {
	cases := []struct{ name, query string }{
		{"s1mple", "simple"},
		{"s1mple", "симпл"},
		{"B1T", "bit"},
		{"m0NESY", "monesi"},
		{"m0NESY", "монеси"},
		{"NiKo", "nico"},
		{"FalleN", "fallen"},
		{"dev1ce", "device"},
		{"ZywOo", "zywoo"},
		{"Natus Vincere", "натус винцере"},
	}
	for _, c := range cases {
		if !MatchesSearch(c.name, FoldForSearch(c.query)) {
			t.Fatalf("%q did not find %q (folded: %q vs %q)",
				c.query, c.name, FoldForSearch(c.query), FoldForSearch(c.name))
		}
	}
}

// The fold is lossy on purpose, but it must not become a fold that matches
// everything — a search that offers the whole catalogue is the same as one
// that offers nothing.
func TestMatchesSearch_StillTellsDifferentNamesApart(t *testing.T) {
	cases := []struct{ name, query string }{
		{"donk", "s1mple"},
		{"Team Spirit", "vitality"},
		{"NAVI", "mouz"},
		{"ZywOo", "apex"},
	}
	for _, c := range cases {
		if MatchesSearch(c.name, FoldForSearch(c.query)) {
			t.Fatalf("%q must not find %q", c.query, c.name)
		}
	}
}

func TestSearchKeys_OffersTheNameWithAndWithoutItsOrganisationalWords(t *testing.T) {
	keys := SearchKeys("Team Spirit")
	if len(keys) != 2 {
		t.Fatalf("expected the full name and the stripped one, got %v", keys)
	}
	if keys[0] != "teamspirit" || keys[1] != "spirit" {
		t.Fatalf("unexpected keys: %v", keys)
	}
	// A team actually called "Team" must survive the stripping.
	if got := SearchKeys("Team"); len(got) != 1 || got[0] != "team" {
		t.Fatalf("stripping must not empty a name: %v", got)
	}
	if got := SearchKeys("   "); got != nil {
		t.Fatalf("a blank name has no keys, got %v", got)
	}
}

// The blob a row stores has to keep its keys apart, or a query could match
// across the join and find a name nobody typed.
func TestSearchKeyBlob_KeepsKeysApart(t *testing.T) {
	blob := SearchKeyBlob("Team Spirit")
	if blob != " teamspirit spirit " {
		t.Fatalf("unexpected blob: %q", blob)
	}
	if SearchKeyBlob("") != "" {
		t.Fatal("a blank name has no blob")
	}
	// "tspirit" straddles the two keys and must not be findable.
	if got := FoldForSearch("tspirit"); len(got) > 0 && MatchesSearch("Team Spirit", got) {
		t.Fatal("a query straddling two keys must not match")
	}
}

func TestSearchRank_PutsTheClosestAnswerFirst(t *testing.T) {
	query := FoldForSearch("navi")
	exact := SearchRank("NAVI", query)
	prefix := SearchRank("NAVI Junior", query)
	if exact >= prefix {
		t.Fatalf("an exact name must outrank a longer one: %d vs %d", exact, prefix)
	}
	if contains := SearchRank("Old NAVI Squad", query); prefix >= contains {
		t.Fatalf("a prefix must outrank a mere containment: %d vs %d", prefix, contains)
	}
	if miss := SearchRank("Vitality", query); miss != 3 {
		t.Fatalf("a non-match ranks last, got %d", miss)
	}
}

// An empty query is "show me everything", which is what the browse list is.
func TestMatchesSearch_EmptyQueryMatchesEverything(t *testing.T) {
	if !MatchesSearch("Anything At All", "") {
		t.Fatal("an empty query must match everything")
	}
}
