package valvevrs

import "testing"

// sampleGlobalTable is a trimmed excerpt of a real
// standings_global_YYYY_MM_DD.md file's structure (fetched live from
// github.com/ValveSoftware/counter-strike_regional_standings while building
// this parser), including the title line, header, alignment separator, and
// a details-link trailing column — all of which parseTable must tolerate.
const sampleGlobalTable = `### Standings as of 2026_08_03<br />
<br />

| Standing | Points | Team Name            | Roster                                                |                                                                                                                |
| :- | -: | :- | :- | :- |
| 1        |   2011 | Spirit               | donk, magixx, sh1ro, tN1R, zont1x                     | [details](details/2026_08_03/0001--spirit.md)                   |
| 2        |   1950 | Falcons              | karrigan, kyousuke, m0NESY, NiKo, TeSeS               | [details](details/2026_08_03/0002--falcons.md)                            |
| 3        |   1873 | MOUZ                 | PR, Spinx, torzsi, xelex, xertioN                     | [details](details/2026_08_03/0003--mouz.md)                     |
`

func TestParseTable_ParsesRealGlobalStandingsFormat(t *testing.T) {
	rows, err := parseTable(sampleGlobalTable)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].standing != 1 || rows[0].points != 2011 || rows[0].name != "Spirit" {
		t.Fatalf("row 0 = %+v, want standing=1 points=2011 name=Spirit", rows[0])
	}
	wantRoster := []string{"donk", "magixx", "sh1ro", "tN1R", "zont1x"}
	if len(rows[0].roster) != len(wantRoster) {
		t.Fatalf("roster = %v, want %v", rows[0].roster, wantRoster)
	}
	for i, p := range wantRoster {
		if rows[0].roster[i] != p {
			t.Errorf("roster[%d] = %q, want %q", i, rows[0].roster[i], p)
		}
	}
}

func TestParseTable_SkipsHeaderAndSeparatorRows(t *testing.T) {
	rows, err := parseTable(sampleGlobalTable)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.name == "Team Name" {
			t.Fatalf("header row leaked into results: %+v", r)
		}
	}
}

func TestParseTable_ToleratesMissingRosterColumn(t *testing.T) {
	content := "| Standing | Points | Team Name |\n| :- | -: | :- |\n| 1 | 100 | NoRosterTeam |\n"
	rows, err := parseTable(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].name != "NoRosterTeam" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].roster != nil {
		t.Fatalf("expected nil roster when the column is absent, got %v", rows[0].roster)
	}
}

func TestParseTable_EmptyOrMalformedContentReturnsError(t *testing.T) {
	for _, content := range []string{"", "no table here at all", "### Just a title\n\nSome prose."} {
		if _, err := parseTable(content); err == nil {
			t.Errorf("parseTable(%q) = nil error, want an error for a table with no data rows", content)
		}
	}
}

func TestParseTable_RosterNamesAreTrimmed(t *testing.T) {
	content := "| Standing | Points | Team Name | Roster |\n| :- | -: | :- | :- |\n| 1 | 100 | Team | alice ,  bob,carol  |\n"
	rows, err := parseTable(content)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alice", "bob", "carol"}
	if len(rows[0].roster) != len(want) {
		t.Fatalf("roster = %v, want %v", rows[0].roster, want)
	}
	for i, p := range want {
		if rows[0].roster[i] != p {
			t.Errorf("roster[%d] = %q, want %q", i, rows[0].roster[i], p)
		}
	}
}
