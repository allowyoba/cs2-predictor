package grid

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"cs2predictor/internal/domain/enrichment"
)

// fakeGRID serves both the Central Data (allSeries) and Series State
// (seriesState) endpoints from fixtures, and records every request's raw
// GraphQL body for assertions on what was actually sent (auth header,
// variables). GetTeamStats/GetHeadToHead issue seriesState lookups
// concurrently (see collectSeriesResults), so every field the handler
// mutates is guarded by mu rather than assumed single-goroutine.
type fakeGRID struct {
	t          *testing.T
	wantAPIKey string

	mu           sync.Mutex
	allSeries    map[string]any // {"data": {"allSeries": {...}}}
	seriesStates map[string]map[string]any
	requests     []*http.Request
}

func newFakeGRID(t *testing.T) *fakeGRID {
	t.Helper()
	return &fakeGRID{t: t, seriesStates: map[string]map[string]any{}, wantAPIKey: "test-api-key"}
}

func (f *fakeGRID) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.mu.Unlock()
		if got := r.Header.Get("x-api-key"); got != f.wantAPIKey {
			f.t.Errorf("x-api-key header = %q, want %q", got, f.wantAPIKey)
		}

		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Fatalf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/central-data":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"allSeries": f.allSeries}})
		case "/series-state":
			seriesID, _ := body.Variables["seriesId"].(string)
			state, ok := f.seriesStates[seriesID]
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"seriesState": nil}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"seriesState": state}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func seriesEdgeFixture(id string, teamNames ...string) map[string]any {
	teams := make([]map[string]any, len(teamNames))
	for i, name := range teamNames {
		teams[i] = map[string]any{"baseInfo": map[string]any{"id": name, "name": name}}
	}
	return map[string]any{"node": map[string]any{"id": id, "teams": teams}}
}

func seriesStateFixture(finished bool, teamWins map[string]bool) map[string]any {
	teams := make([]map[string]any, 0, len(teamWins))
	for name, won := range teamWins {
		teams = append(teams, map[string]any{"id": name, "name": name, "won": won})
	}
	return map[string]any{"finished": finished, "teams": teams}
}

func newTestProvider(server *httptest.Server) *Provider {
	config := Config{CentralDataURL: server.URL + "/central-data", SeriesStateURL: server.URL + "/series-state", APIKey: "test-api-key"}
	return NewProvider(config, server.Client())
}

func TestGetTeamStats_TalliesWinsAndLossesFromFinishedSeries(t *testing.T) {
	fake := newFakeGRID(t)
	fake.allSeries = map[string]any{"edges": []any{
		seriesEdgeFixture("s1", "Spirit", "NAVI"),
		seriesEdgeFixture("s2", "Spirit", "Falcons"),
		seriesEdgeFixture("s3", "FaZe", "G2"), // Spirit not involved — must be skipped
	}}
	fake.seriesStates["s1"] = seriesStateFixture(true, map[string]bool{"Spirit": true, "NAVI": false})
	fake.seriesStates["s2"] = seriesStateFixture(true, map[string]bool{"Spirit": false, "Falcons": true})
	server := fake.server()
	defer server.Close()

	form, err := newTestProvider(server).GetTeamStats(t.Context(), enrichment.TeamIdentity{Name: "Spirit"})
	if err != nil {
		t.Fatal(err)
	}
	if form == nil || form.Wins != 1 || form.Losses != 1 || form.Sample != 2 || form.Source != enrichment.SourceGRID {
		t.Fatalf("GetTeamStats = %+v, want Wins=1 Losses=1 Sample=2 Source=GRID", form)
	}
}

func TestGetTeamStats_SkipsUnfinishedSeries(t *testing.T) {
	fake := newFakeGRID(t)
	fake.allSeries = map[string]any{"edges": []any{
		seriesEdgeFixture("s1", "Spirit", "NAVI"),
	}}
	fake.seriesStates["s1"] = seriesStateFixture(false, map[string]bool{"Spirit": true, "NAVI": false})
	server := fake.server()
	defer server.Close()

	form, err := newTestProvider(server).GetTeamStats(t.Context(), enrichment.TeamIdentity{Name: "Spirit"})
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		t.Fatalf("GetTeamStats = %+v, want nil (no finished series)", form)
	}
}

func TestGetTeamStats_NoSeriesReturnsNil(t *testing.T) {
	fake := newFakeGRID(t)
	fake.allSeries = map[string]any{"edges": []any{}}
	server := fake.server()
	defer server.Close()

	form, err := newTestProvider(server).GetTeamStats(t.Context(), enrichment.TeamIdentity{Name: "Unknown Team"})
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		t.Fatalf("GetTeamStats = %+v, want nil", form)
	}
}

func TestGetTeamStats_NameMatchIsCaseInsensitiveButExact(t *testing.T) {
	fake := newFakeGRID(t)
	fake.allSeries = map[string]any{"edges": []any{
		seriesEdgeFixture("s1", "spirit", "NAVI"),       // case-insensitive match
		seriesEdgeFixture("s2", "Spirit Academy", "G2"), // must NOT match "Spirit" (substring, not exact)
	}}
	fake.seriesStates["s1"] = seriesStateFixture(true, map[string]bool{"spirit": true, "NAVI": false})
	server := fake.server()
	defer server.Close()

	form, err := newTestProvider(server).GetTeamStats(t.Context(), enrichment.TeamIdentity{Name: "Spirit"})
	if err != nil {
		t.Fatal(err)
	}
	if form == nil || form.Sample != 1 {
		t.Fatalf("GetTeamStats = %+v, want exactly 1 sample (Spirit Academy must not match)", form)
	}
}

// GetTeamStats fetches seriesState lookups concurrently in windows of
// seriesResultBatch (see collectSeriesResults), but must still behave
// exactly as if it had walked seriesIDs one at a time in order: stop once
// sampleSize results are collected, using the FIRST sampleSize
// finished/matching series (most-recent-first) rather than an arbitrary
// subset, and never call the endpoint at all for series beyond that point.
func TestGetTeamStats_StopsAfterSampleSizeAndNeverQueriesLaterSeries(t *testing.T) {
	fake := newFakeGRID(t)
	var edges []any
	for i := 1; i <= 15; i++ {
		id := fmt.Sprintf("s%d", i)
		edges = append(edges, seriesEdgeFixture(id, "Spirit", fmt.Sprintf("Opponent%d", i)))
		// First 10 (within the first seriesResultBatch window) alternate
		// win/loss; the rest are all wins, so if they were ever counted the
		// tally would no longer be the expected 5/5.
		won := i <= 10 && i%2 == 1
		fake.seriesStates[id] = seriesStateFixture(true, map[string]bool{"Spirit": won, fmt.Sprintf("Opponent%d", i): !won})
	}
	fake.allSeries = map[string]any{"edges": edges}
	server := fake.server()
	defer server.Close()

	form, err := newTestProvider(server).GetTeamStats(t.Context(), enrichment.TeamIdentity{Name: "Spirit"})
	if err != nil {
		t.Fatal(err)
	}
	if form == nil || form.Sample != 10 || form.Wins != 5 || form.Losses != 5 {
		t.Fatalf("GetTeamStats = %+v, want Sample=10 Wins=5 Losses=5 (exactly the first 10 series)", form)
	}

	fake.mu.Lock()
	requestCount := len(fake.requests)
	fake.mu.Unlock()
	// 1 allSeries call + exactly 10 seriesState calls (s1..s10) — s11..s15
	// must never be queried once the sample is full.
	if requestCount != 11 {
		t.Fatalf("recorded %d requests, want 11 (1 allSeries + 10 seriesState, stopping before s11..s15)", requestCount)
	}
}

func TestGetHeadToHead_TalliesBothTeamsRecordAgainstEachOther(t *testing.T) {
	fake := newFakeGRID(t)
	fake.allSeries = map[string]any{"edges": []any{
		seriesEdgeFixture("s1", "Spirit", "NAVI"),
		seriesEdgeFixture("s2", "Spirit", "NAVI"),
		seriesEdgeFixture("s3", "Spirit", "Falcons"), // NAVI not involved — must be skipped
	}}
	fake.seriesStates["s1"] = seriesStateFixture(true, map[string]bool{"Spirit": true, "NAVI": false})
	fake.seriesStates["s2"] = seriesStateFixture(true, map[string]bool{"Spirit": false, "NAVI": true})
	server := fake.server()
	defer server.Close()

	h2h, err := newTestProvider(server).GetHeadToHead(t.Context(), enrichment.TeamIdentity{Name: "Spirit"}, enrichment.TeamIdentity{Name: "NAVI"})
	if err != nil {
		t.Fatal(err)
	}
	if h2h == nil || h2h.TeamAWins != 1 || h2h.TeamBWins != 1 || h2h.Sample != 2 {
		t.Fatalf("GetHeadToHead = %+v, want TeamAWins=1 TeamBWins=1 Sample=2", h2h)
	}
}

func TestGetHeadToHead_NoSharedSeriesReturnsNil(t *testing.T) {
	fake := newFakeGRID(t)
	fake.allSeries = map[string]any{"edges": []any{
		seriesEdgeFixture("s1", "Spirit", "Falcons"),
	}}
	server := fake.server()
	defer server.Close()

	h2h, err := newTestProvider(server).GetHeadToHead(t.Context(), enrichment.TeamIdentity{Name: "Spirit"}, enrichment.TeamIdentity{Name: "NAVI"})
	if err != nil {
		t.Fatal(err)
	}
	if h2h != nil {
		t.Fatalf("GetHeadToHead = %+v, want nil", h2h)
	}
}

func TestGetTeamStats_GraphQLErrorPropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": "invalid api key"}}})
	}))
	defer server.Close()

	_, err := newTestProvider(server).GetTeamStats(t.Context(), enrichment.TeamIdentity{Name: "Spirit"})
	if err == nil {
		t.Fatal("expected an error when the GraphQL response carries an errors array")
	}
}
