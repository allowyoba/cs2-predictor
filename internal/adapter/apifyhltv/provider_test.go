package apifyhltv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cs2predictor/internal/domain/enrichment"
)

func newTestProvider(t *testing.T, server *httptest.Server, maxTeams int) *Provider {
	t.Helper()
	config := Config{BaseURL: server.URL, ActorID: "paco_nassa~hltv-org-team-ranking", Token: "test-token", MaxTeams: maxTeams}
	return NewProvider(config, server.Client())
}

func TestFetchRankings_ParsesRankingItemsAndTagsSourceHLTV(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"place": 1, "team": {"name": "Vitality"}, "points": 1000, "change": 0, "isNew": false},
			{"place": 2, "team": {"name": "Spirit"}, "points": 900, "change": -1, "isNew": false}
		]`))
	}))
	defer server.Close()

	rankings, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 2 {
		t.Fatalf("got %d rankings, want 2: %+v", len(rankings), rankings)
	}
	if rankings[0].Identity.Name != "Vitality" || *rankings[0].GlobalRank != 1 || *rankings[0].Points != 1000 {
		t.Fatalf("unexpected first ranking: %+v", rankings[0])
	}
	for _, r := range rankings {
		if r.Source != enrichment.SourceHLTV {
			t.Fatalf("expected Source=HLTV, got %q", r.Source)
		}
		if len(r.Identity.Roster) != 0 {
			t.Fatalf("HLTV ranking must not fabricate roster data, got %+v", r.Identity.Roster)
		}
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
		_, _ = w.Write([]byte(`[{"place": 1, "team": {"name": ""}, "points": 500}]`))
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
