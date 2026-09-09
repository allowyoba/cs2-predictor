package valvevrs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/enrichment"
)

const globalTable2026 = `### Standings as of 2026_08_03<br />

| Standing | Points | Team Name | Roster |  |
| :- | -: | :- | :- |  |
| 1 | 2011 | Spirit | donk, magixx, sh1ro, tN1R, zont1x |  |
| 2 | 1950 | Falcons | karrigan, kyousuke, m0NESY, NiKo, TeSeS |  |
`

const europeTable2026 = `### Regional Standings for Europe as of 2026_08_03<br />

| Standing | Points | Team Name | Roster |  |
| :- | -: | :- | :- |  |
| 1 | 2011 | Spirit | donk, magixx, sh1ro, tN1R, zont1x |  |
`

// newFakeGitHub serves both the Contents API directory listing and raw file
// content this package depends on, entirely offline — no test in this repo
// may require reaching the real github.com.
func newFakeGitHub(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var names []string
	for name := range files {
		names = append(names, name)
	}
	mux.HandleFunc("/repos/ValveSoftware/counter-strike_regional_standings/contents/live/", func(w http.ResponseWriter, r *http.Request) {
		entries := make([]contentEntry, 0, len(names))
		for _, name := range names {
			entries = append(entries, contentEntry{Name: name, Type: "file"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	})
	mux.HandleFunc("/ValveSoftware/counter-strike_regional_standings/main/live/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		content, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func newTestProvider(t *testing.T, server *httptest.Server, fixedNow time.Time) *Provider {
	t.Helper()
	p := NewProvider(Config{APIBaseURL: server.URL, RawBaseURL: server.URL}, server.Client())
	p.now = func() time.Time { return fixedNow }
	return p
}

func TestFetchRankings_MergesGlobalAndRegionalRank(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	americasTable := "### Regional Standings for Americas as of 2026_08_03<br />\n\n| Standing | Points | Team Name | Roster |  |\n| :- | -: | :- | :- |  |\n| 1 | 1847 | 9z | dgt, HUASOPEEK, luchov, max, meyern |  |\n"
	asiaTable := "### Regional Standings for Asia as of 2026_08_03<br />\n\n| Standing | Points | Team Name | Roster |  |\n| :- | -: | :- | :- |  |\n| 1 | 1690 | Aurora | Jimpphat, kyxsan, Wicadia, woxic, XANTARES |  |\n"
	server := newFakeGitHub(t, map[string]string{
		"standings_global_2026_08_03.md":   globalTable2026,
		"standings_europe_2026_08_03.md":   europeTable2026,
		"standings_americas_2026_08_03.md": americasTable,
		"standings_asia_2026_08_03.md":     asiaTable,
	})
	p := newTestProvider(t, server, now)

	ranked, err := p.FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 2 global-listed teams (Spirit, Falcons) + 2 regional-only teams (9z in
	// americas, Aurora in asia — not in the trimmed global fixture, kept as
	// regional-only entries per FetchRankings' documented behavior).
	if len(ranked) != 4 {
		t.Fatalf("expected 4 teams, got %d: %+v", len(ranked), ranked)
	}

	var spirit, falcons *enrichment.RankedTeam
	for i := range ranked {
		switch ranked[i].Identity.Name {
		case "Spirit":
			spirit = &ranked[i]
		case "Falcons":
			falcons = &ranked[i]
		}
	}
	if spirit == nil || falcons == nil {
		t.Fatalf("missing expected teams in %+v", ranked)
	}

	if spirit.GlobalRank == nil || *spirit.GlobalRank != 1 {
		t.Errorf("Spirit.GlobalRank = %v, want 1", spirit.GlobalRank)
	}
	if spirit.RegionalRank == nil || *spirit.RegionalRank != 1 || spirit.Region != "europe" {
		t.Errorf("Spirit.RegionalRank/Region = %v/%q, want 1/europe", spirit.RegionalRank, spirit.Region)
	}
	if spirit.Points == nil || *spirit.Points != 2011 {
		t.Errorf("Spirit.Points = %v, want 2011", spirit.Points)
	}
	if !spirit.PublishedAt.Equal(time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Spirit.PublishedAt = %v, want 2026-08-03", spirit.PublishedAt)
	}

	// Falcons is in the global table but absent from every regional table
	// in this fixture — must still come back, just with no regional rank.
	if falcons.GlobalRank == nil || *falcons.GlobalRank != 2 {
		t.Errorf("Falcons.GlobalRank = %v, want 2", falcons.GlobalRank)
	}
	if falcons.RegionalRank != nil {
		t.Errorf("Falcons.RegionalRank = %v, want nil (not present in any regional fixture)", falcons.RegionalRank)
	}
}

func TestFetchRankings_PicksTheMostRecentlyDatedFile(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	older := "### Standings as of 2026_07_06<br />\n\n| Standing | Points | Team Name | Roster |  |\n| :- | -: | :- | :- |  |\n| 5 | 1000 | Old Leader | a, b |  |\n"
	server := newFakeGitHub(t, map[string]string{
		"standings_global_2026_07_06.md": older,
		"standings_global_2026_08_03.md": globalTable2026,
	})
	p := newTestProvider(t, server, now)

	ranked, err := p.FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, team := range ranked {
		if team.Identity.Name == "Old Leader" {
			t.Fatalf("picked the older 07_06 snapshot instead of the newer 08_03 one: %+v", ranked)
		}
	}
	if !ranked[0].PublishedAt.Equal(time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("PublishedAt = %v, want the newer snapshot's date", ranked[0].PublishedAt)
	}
}

func TestFetchRankings_NoGlobalFileReturnsError(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	server := newFakeGitHub(t, map[string]string{
		"standings_europe_2026_08_03.md": europeTable2026,
	})
	p := newTestProvider(t, server, now)

	if _, err := p.FetchRankings(context.Background()); err == nil {
		t.Fatal("expected an error when no global standings file is found")
	}
}

func TestParseSnapshotDate_ParsesTheTrailingDate(t *testing.T) {
	got, err := parseSnapshotDate("standings_global_2026_08_03.md")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v, want 2026-08-03", got)
	}
	if _, err := parseSnapshotDate("not_a_dated_file.md"); err == nil {
		t.Fatal("expected an error for a filename with no embedded date")
	}
}
