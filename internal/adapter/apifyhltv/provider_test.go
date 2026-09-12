package apifyhltv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/enrichment"
)

func newTestProvider(t *testing.T, server *httptest.Server, maxTeams int) *Provider {
	t.Helper()
	config := Config{
		BaseURL: server.URL, ActorID: "paco_nassa~hltv-org-team-ranking", Token: "test-token", MaxTeams: maxTeams,
		RankingType: "hltv", Source: enrichment.SourceHLTV,
	}
	return NewProvider(config, server.Client())
}

// sampleActorRunResponse is a trimmed, real run-sync-get-dataset-items
// response (captured against the live actor) — the dataset holds one item
// per RUN, not per team: the whole scrape result, with the actual teams
// nested under "rankings", and each team's roster reported as a top-level
// "players" array rather than nested under "team".
const sampleActorRunResponse = `[
	{
		"scrapedAt": "2026-09-11T21:57:15.554Z",
		"rankingType": "hltv",
		"totalTeams": 2,
		"parameters": {"rankingType": "hltv", "maxTeams": 2},
		"source": "hltv.org",
		"url": "https://www.hltv.org/ranking/teams",
		"rankings": [
			{
				"place": 1,
				"team": {"name": "Spirit", "id": 7020, "logo": "https://img-cdn.hltv.org/teamlogo/x.png", "country": "Russia"},
				"points": 1000,
				"change": 0,
				"isNew": false,
				"players": ["sh1ro", "magixx", "tN1R", "zont1x", "donk"]
			},
			{
				"place": 2,
				"team": {"name": "Falcons", "id": 11283, "logo": "https://img-cdn.hltv.org/teamlogo/y.png", "country": "Denmark"},
				"points": 606,
				"change": 0,
				"isNew": false,
				"players": ["karrigan", "NiKo", "TeSeS", "m0NESY", "kyousuke"]
			}
		]
	}
]`

func TestFetchRankings_ParsesRealActorResponseShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleActorRunResponse))
	}))
	defer server.Close()

	rankings, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 2 {
		t.Fatalf("got %d rankings, want 2: %+v", len(rankings), rankings)
	}
	if rankings[0].Identity.Name != "Spirit" || *rankings[0].GlobalRank != 1 || *rankings[0].Points != 1000 {
		t.Fatalf("unexpected first ranking: %+v", rankings[0])
	}
	wantRoster := []string{"sh1ro", "magixx", "tN1R", "zont1x", "donk"}
	if !slices.Equal(rankings[0].Identity.Roster, wantRoster) {
		t.Fatalf("roster = %v, want %v", rankings[0].Identity.Roster, wantRoster)
	}
	for _, r := range rankings {
		if r.Source != enrichment.SourceHLTV {
			t.Fatalf("expected Source=HLTV, got %q", r.Source)
		}
	}
}

func TestFetchRankings_EmptyDatasetProducesNoRankings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	rankings, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 0 {
		t.Fatalf("expected no rankings for an empty dataset, got %+v", rankings)
	}
}

// The request must send the actor's documented input shape and
// authenticate via the Authorization header, never a ?token= query
// parameter (see the package doc comment for why).
func TestFetchRankings_SendsExpectedRequestShape(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	if _, err := newTestProvider(t, server, 30).FetchRankings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v2/acts/paco_nassa~hltv-org-team-ranking/run-sync-get-dataset-items" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want Bearer test-token", gotAuth)
	}
	if gotBody["rankingType"] != "hltv" {
		t.Fatalf("rankingType = %v, want hltv", gotBody["rankingType"])
	}
	if gotBody["maxTeams"] != float64(30) {
		t.Fatalf("maxTeams = %v, want 30", gotBody["maxTeams"])
	}
	if _, tokenLeaked := gotBody["token"]; tokenLeaked {
		t.Fatal("token must never be sent in the request body")
	}
}

func TestFetchRankings_SkipsItemsWithNoTeamName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"rankings": [{"place": 1, "team": {"name": ""}, "points": 500}]}]`))
	}))
	defer server.Close()

	rankings, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 0 {
		t.Fatalf("expected a nameless item to be skipped, got %+v", rankings)
	}
}

func TestFetchRankings_HTTPErrorNeverExposesTheTokenInTheMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer server.Close()

	_, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if got := err.Error(); strings.Contains(got, "test-token") {
		t.Fatalf("error message leaks the token: %q", got)
	}
}

// DefaultConfig and DefaultValveConfig share every field except
// RankingType/Source — this is what lets a single Provider implementation
// serve both of the actor's ranking modes.
func TestDefaultValveConfig_DiffersFromDefaultConfigOnlyByRankingTypeAndSource(t *testing.T) {
	hltv := DefaultConfig("tok")
	valve := DefaultValveConfig("tok")
	if valve.RankingType != "valve" || valve.Source != enrichment.SourceValveVRS {
		t.Fatalf("unexpected valve config: %+v", valve)
	}
	if hltv.RankingType != "hltv" || hltv.Source != enrichment.SourceHLTV {
		t.Fatalf("unexpected hltv config: %+v", hltv)
	}
	valve.RankingType, hltv.RankingType = "", ""
	valve.Source, hltv.Source = "", ""
	if valve != hltv {
		t.Fatalf("expected every other field to match: valve=%+v hltv=%+v", valve, hltv)
	}
}

// FetchRankings must send whichever rankingType Config carries and tag
// results with whichever Source Config carries — the actor's "valve" mode
// output is otherwise byte-identical in shape to its "hltv" mode.
func TestFetchRankings_ValveModeSendsValveRankingTypeAndTagsSourceValveVRS(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleActorRunResponse))
	}))
	defer server.Close()

	provider := NewProvider(DefaultValveConfig("test-token"), server.Client())
	provider.config.BaseURL = server.URL
	rankings, err := provider.FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["rankingType"] != "valve" {
		t.Fatalf("rankingType = %v, want valve", gotBody["rankingType"])
	}
	for _, r := range rankings {
		if r.Source != enrichment.SourceValveVRS {
			t.Fatalf("expected Source=VALVE_VRS, got %q", r.Source)
		}
	}
}

// PublishedAt must reflect the actor's own scrapedAt, not whenever
// FetchRankings happened to be called — the sample response's scrapedAt is
// 2026-09-11T21:57:15.554Z, deliberately far from "now" so a regression to
// time.Now() would fail this immediately rather than by coincidence.
func TestFetchRankings_PublishedAtUsesScrapedAtNotFetchTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleActorRunResponse))
	}))
	defer server.Close()

	rankings, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 11, 21, 57, 15, 554000000, time.UTC)
	for _, r := range rankings {
		if !r.PublishedAt.Equal(want) {
			t.Fatalf("PublishedAt = %v, want the run's own scrapedAt %v", r.PublishedAt, want)
		}
	}
}

// A missing/zero scrapedAt must not silently produce the Unix epoch as
// PublishedAt — it should fall back to the old fetch-time behavior instead.
func TestFetchRankings_FallsBackToFetchTimeWhenScrapedAtIsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"rankings": [{"place": 1, "team": {"name": "Spirit"}, "points": 1000}]}]`))
	}))
	defer server.Close()

	before := time.Now().Add(-time.Second)
	rankings, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 1 {
		t.Fatalf("got %d rankings, want 1", len(rankings))
	}
	if rankings[0].PublishedAt.Before(before) {
		t.Fatalf("PublishedAt = %v, want it no earlier than the fallback fetch time", rankings[0].PublishedAt)
	}
}

// APIFY_MAX_TEAMS=0 must be sent as a literal 0, not silently omitted
// (which would let the actor fall back to its own default team count
// instead of honoring an explicit operator choice).
func TestFetchRankings_SendsMaxTeamsZeroExplicitly(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	if _, err := newTestProvider(t, server, 0).FetchRankings(context.Background()); err != nil {
		t.Fatal(err)
	}
	maxTeams, present := gotBody["maxTeams"]
	if !present {
		t.Fatal("expected maxTeams to be present in the request body even when 0")
	}
	if maxTeams != float64(0) {
		t.Fatalf("maxTeams = %v, want 0", maxTeams)
	}
}
