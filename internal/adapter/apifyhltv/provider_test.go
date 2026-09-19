package apifyhltv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// A Monday 23:00 UTC — the instant the weekly gate opens, and the one the
// real incident happened at.
var testNow = time.Date(2026, 9, 14, 23, 0, 0, 0, time.UTC)

// fakeApify stands in for the Apify REST API across the whole run
// lifecycle. Every handler is optional; a request to an unstubbed path
// fails the test rather than silently 404ing, since "which endpoint did it
// call" is exactly what these tests are about.
type fakeApify struct {
	t *testing.T

	// runs started via POST /v2/acts/{id}/runs, in order.
	started []actorInput
	// nextRunID is handed out for each started run.
	nextRunID string
	// runsByID answers GET /v2/actor-runs/{id}.
	runsByID map[string]runInfo
	// succeeded answers GET /v2/acts/{id}/runs?status=SUCCEEDED.
	succeeded []runInfo
	// outputs answers GET /v2/key-value-stores/{id}/records/OUTPUT.
	outputs map[string]string

	mu       sync.Mutex
	requests []string
}

func newFakeApify(t *testing.T) *fakeApify {
	t.Helper()
	return &fakeApify{t: t, nextRunID: "run-new", runsByID: map[string]runInfo{}, outputs: map[string]string{}}
}

func (f *fakeApify) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			f.t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/runs"):
			var input actorInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			f.mu.Lock()
			f.started = append(f.started, input)
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"data":{"id":%q,"status":"RUNNING"}}`, f.nextRunID)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/actor-runs/"):
			run, ok := f.runsByID[strings.TrimPrefix(r.URL.Path, "/v2/actor-runs/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"record-not-found"}}`))
				return
			}
			body, _ := json.Marshal(map[string]any{"data": run})
			_, _ = w.Write(body)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/runs"):
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"items": f.succeeded}})
			_, _ = w.Write(body)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/key-value-stores/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/key-value-stores/"), "/records/OUTPUT")
			output, ok := f.outputs[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"record-not-found"}}`))
				return
			}
			_, _ = w.Write([]byte(output))
		default:
			f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func (f *fakeApify) startedRuns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

func newTestProvider(t *testing.T, server *httptest.Server, maxTeams int) *Provider {
	t.Helper()
	config := Config{
		BaseURL: server.URL, ActorID: "paco_nassa~hltv-org-team-ranking", Token: "test-token", MaxTeams: maxTeams,
		RankingType: "hltv", Source: enrichment.SourceHLTV,
	}
	return NewProvider(config, server.Client(), newMemoryRunStore(), common.FixedClock(testNow))
}

// sampleOutput is a trimmed, real OUTPUT record (captured against the live
// actor): the whole scrape result, with the teams nested under "rankings"
// and each team's roster as a top-level "players" array rather than nested
// under "team". The run's dataset deliberately isn't used — it holds only
// the bare "rankings" array, with neither scrapedAt nor rankingType.
const sampleOutput = `{
		"scrapedAt": "2026-09-14T23:02:19.554Z",
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
}`

func finishedAt(offset time.Duration) *time.Time {
	at := testNow.Add(offset)
	return &at
}

// The first tick of a period starts a run and reports it as pending — not
// as data, and not as a failure.
func TestFetchRankings_FirstTickStartsARunAndReportsPending(t *testing.T) {
	fake := newFakeApify(t)
	server := fake.serve()
	defer server.Close()

	provider := newTestProvider(t, server, 30)
	_, err := provider.FetchRankings(context.Background())
	if !errors.Is(err, enrichment.ErrFetchPending) {
		t.Fatalf("err = %v, want ErrFetchPending", err)
	}
	if fake.startedRuns() != 1 {
		t.Fatalf("started %d runs, want 1", fake.startedRuns())
	}
	if fake.started[0].RankingType != "hltv" || fake.started[0].MaxTeams != 30 {
		t.Fatalf("actor input = %+v, want hltv/30", fake.started[0])
	}
	run, err := provider.runs.Run(context.Background(), enrichment.SourceHLTV, "hltv")
	if err != nil || run == nil {
		t.Fatalf("expected the started run to be recorded, got %+v, %v", run, err)
	}
	if run.RunID != "run-new" || run.Attempts != 1 || !run.PeriodStart.Equal(common.StartOfWeekUTC(testNow)) {
		t.Fatalf("recorded run = %+v", *run)
	}
}

// The tick after that collects the finished run's result — the whole point
// of recording the run id.
func TestFetchRankings_LaterTickCollectsTheStartedRun(t *testing.T) {
	fake := newFakeApify(t)
	fake.outputs["ds-1"] = sampleOutput
	server := fake.serve()
	defer server.Close()
	provider := newTestProvider(t, server, 30)

	if _, err := provider.FetchRankings(context.Background()); !errors.Is(err, enrichment.ErrFetchPending) {
		t.Fatalf("first tick: err = %v, want ErrFetchPending", err)
	}
	// Still running on the next tick: still pending, still no second run.
	fake.runsByID["run-new"] = runInfo{ID: "run-new", Status: statusRunning}
	if _, err := provider.FetchRankings(context.Background()); !errors.Is(err, enrichment.ErrFetchPending) {
		t.Fatalf("second tick: err = %v, want ErrFetchPending", err)
	}
	// Finished: the data arrives, and the run is marked as this period's.
	fake.runsByID["run-new"] = runInfo{ID: "run-new", Status: statusSucceeded, DefaultKeyValueStoreID: "ds-1", FinishedAt: finishedAt(2 * time.Minute)}
	rankings, err := provider.FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 2 || rankings[0].Identity.Name != "Spirit" || *rankings[0].GlobalRank != 1 || *rankings[0].Points != 1000 {
		t.Fatalf("unexpected rankings: %+v", rankings)
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
	if fake.startedRuns() != 1 {
		t.Fatalf("started %d runs across three ticks, want exactly 1", fake.startedRuns())
	}
	// Kept, not cleared: this record is what tells the weekly gate the
	// period is done (see enrichment.RunStatusCollected).
	run, _ := provider.runs.Run(context.Background(), enrichment.SourceHLTV, "hltv")
	if run == nil || run.Status != enrichment.RunStatusCollected || run.PeriodStart.Before(common.StartOfWeekUTC(testNow)) {
		t.Fatalf("expected the run marked collected for this period, got %+v", run)
	}
}

// The incident this whole lifecycle exists for: the run succeeded on
// Apify's side but we lost track of it (an HTTP timeout back then, a
// redeploy in general). Its result must be adopted for free rather than
// paid for a second time.
func TestFetchRankings_AdoptsAFinishedRunInsteadOfPayingAgain(t *testing.T) {
	fake := newFakeApify(t)
	fake.succeeded = []runInfo{{ID: "run-lost", Status: statusSucceeded, DefaultKeyValueStoreID: "ds-1", FinishedAt: finishedAt(2 * time.Minute)}}
	fake.outputs["ds-1"] = sampleOutput
	server := fake.serve()
	defer server.Close()

	rankings, err := newTestProvider(t, server, 30).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 2 {
		t.Fatalf("got %d rankings, want the adopted run's 2", len(rankings))
	}
	if fake.startedRuns() != 0 {
		t.Fatal("a run whose result already exists must never be started again")
	}
}

// A successful run from before this period is last week's ranking — it must
// not be mistaken for this week's, or the ranking would freeze forever.
func TestFetchRankings_IgnoresAFinishedRunFromAnEarlierPeriod(t *testing.T) {
	fake := newFakeApify(t)
	fake.succeeded = []runInfo{{ID: "run-old", Status: statusSucceeded, DefaultKeyValueStoreID: "ds-old", FinishedAt: finishedAt(-7 * 24 * time.Hour)}}
	fake.outputs["ds-old"] = sampleOutput
	server := fake.serve()
	defer server.Close()

	if _, err := newFakeProviderTick(t, server); !errors.Is(err, enrichment.ErrFetchPending) {
		t.Fatalf("err = %v, want a fresh run to be started", err)
	}
	if fake.startedRuns() != 1 {
		t.Fatalf("started %d runs, want 1", fake.startedRuns())
	}
}

func newFakeProviderTick(t *testing.T, server *httptest.Server) ([]enrichment.RankedTeam, error) {
	t.Helper()
	return newTestProvider(t, server, 30).FetchRankings(context.Background())
}

// One actor serves both of our rankings, so "the last successful run" is
// regularly the other ranking's. Its dataset must be skipped — never filed
// as ours — and our own run from the same period adopted instead.
func TestFetchRankings_SkipsTheOtherRankingsDatasetAndAdoptsItsOwn(t *testing.T) {
	fake := newFakeApify(t)
	fake.succeeded = []runInfo{
		{ID: "run-valve", Status: statusSucceeded, DefaultKeyValueStoreID: "ds-valve", FinishedAt: finishedAt(10 * time.Minute)},
		{ID: "run-hltv", Status: statusSucceeded, DefaultKeyValueStoreID: "ds-1", FinishedAt: finishedAt(2 * time.Minute)},
	}
	fake.outputs["ds-valve"] = strings.Replace(sampleOutput, `"rankingType": "hltv"`, `"rankingType": "valve"`, 1)
	fake.outputs["ds-1"] = sampleOutput
	server := fake.serve()
	defer server.Close()

	rankings, err := newTestProvider(t, server, 30).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 2 {
		t.Fatalf("got %d rankings, want the HLTV run's 2", len(rankings))
	}
	if fake.startedRuns() != 0 {
		t.Fatal("our own finished run was there to adopt; nothing should have been started")
	}
}

// The cost guarantee: however often the job ticks, a period pays for at
// most MaxRunsPerPeriod runs. Every tick here sees its run end as FAILED,
// which is the only way another one is ever started within a period.
func TestFetchRankings_NeverStartsMoreThanMaxRunsPerPeriod(t *testing.T) {
	fake := newFakeApify(t)
	server := fake.serve()
	defer server.Close()
	provider := newTestProvider(t, server, 30)
	provider.config.MaxRunsPerPeriod = 2

	var lastErr error
	for tick := 0; tick < 10; tick++ {
		_, lastErr = provider.FetchRankings(context.Background())
		fake.runsByID["run-new"] = runInfo{ID: "run-new", Status: "FAILED"}
	}
	if fake.startedRuns() != 2 {
		t.Fatalf("started %d runs over 10 ticks, want the cap of 2", fake.startedRuns())
	}
	if lastErr == nil || errors.Is(lastErr, enrichment.ErrFetchPending) {
		t.Fatalf("err = %v, want a real error once the period's budget is spent", lastErr)
	}
}

// A transient API error while checking on a run must not orphan it: the
// next tick has to still know about the run, or it would pay for a second.
func TestFetchRankings_KeepsTheRunRecordedWhenTheStatusCheckFails(t *testing.T) {
	fake := newFakeApify(t)
	server := fake.serve()
	defer server.Close()
	provider := newTestProvider(t, server, 30)

	if _, err := provider.FetchRankings(context.Background()); !errors.Is(err, enrichment.ErrFetchPending) {
		t.Fatal(err)
	}
	// runsByID has no entry for run-new, so the status check 404s.
	if _, err := provider.FetchRankings(context.Background()); err == nil {
		t.Fatal("expected the failed status check to surface as an error")
	}
	run, _ := provider.runs.Run(context.Background(), enrichment.SourceHLTV, "hltv")
	if run == nil || run.RunID != "run-new" {
		t.Fatalf("expected run-new to still be recorded, got %+v", run)
	}
	if fake.startedRuns() != 1 {
		t.Fatalf("started %d runs, want 1", fake.startedRuns())
	}
}

// The run is started through the async endpoint (not the old blocking
// run-sync one), authenticated by header, and never carries the token in
// the body.
func TestFetchRankings_StartsRunsViaTheAsyncEndpoint(t *testing.T) {
	fake := newFakeApify(t)
	server := fake.serve()
	defer server.Close()

	if _, err := newTestProvider(t, server, 30).FetchRankings(context.Background()); !errors.Is(err, enrichment.ErrFetchPending) {
		t.Fatal(err)
	}
	for _, req := range fake.requests {
		if strings.Contains(req, "run-sync") {
			t.Fatalf("the blocking run-sync endpoint must not be used any more: %s", req)
		}
	}
	if want := "POST /v2/acts/paco_nassa~hltv-org-team-ranking/runs"; !slices.Contains(fake.requests, want) {
		t.Fatalf("requests = %v, want %q", fake.requests, want)
	}
}

func TestFetchRankings_SkipsItemsWithNoTeamName(t *testing.T) {
	fake := newFakeApify(t)
	fake.succeeded = []runInfo{{ID: "r", Status: statusSucceeded, DefaultKeyValueStoreID: "ds", FinishedAt: finishedAt(time.Minute)}}
	fake.outputs["ds"] = `{"rankingType":"hltv","rankings":[{"place":1,"team":{"name":""},"points":500}]}`
	server := fake.serve()
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

// The valve mode must send its own rankingType and tag results with its own
// Source — the actor's two modes are otherwise byte-identical in shape.
func TestFetchRankings_ValveModeSendsValveRankingTypeAndTagsSourceValveVRS(t *testing.T) {
	fake := newFakeApify(t)
	fake.outputs["ds-valve"] = strings.Replace(sampleOutput, `"rankingType": "hltv"`, `"rankingType": "valve"`, 1)
	server := fake.serve()
	defer server.Close()

	config := DefaultValveConfig("test-token")
	config.BaseURL = server.URL
	config.ActorID = "paco_nassa~hltv-org-team-ranking"
	provider := NewProvider(config, server.Client(), newMemoryRunStore(), common.FixedClock(testNow))

	if _, err := provider.FetchRankings(context.Background()); !errors.Is(err, enrichment.ErrFetchPending) {
		t.Fatal(err)
	}
	if fake.started[0].RankingType != "valve" {
		t.Fatalf("rankingType = %v, want valve", fake.started[0].RankingType)
	}
	fake.runsByID["run-new"] = runInfo{ID: "run-new", Status: statusSucceeded, DefaultKeyValueStoreID: "ds-valve", FinishedAt: finishedAt(time.Minute)}
	rankings, err := provider.FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) == 0 {
		t.Fatal("expected the valve dataset to be accepted by the valve provider")
	}
	for _, r := range rankings {
		if r.Source != enrichment.SourceValveVRS {
			t.Fatalf("expected Source=VALVE_VRS, got %q", r.Source)
		}
	}
}

// PublishedAt must reflect the actor's own scrapedAt, not whenever the
// dataset happened to be read.
func TestFetchRankings_PublishedAtUsesScrapedAtNotFetchTime(t *testing.T) {
	fake := newFakeApify(t)
	fake.succeeded = []runInfo{{ID: "r", Status: statusSucceeded, DefaultKeyValueStoreID: "ds", FinishedAt: finishedAt(3 * time.Minute)}}
	fake.outputs["ds"] = sampleOutput
	server := fake.serve()
	defer server.Close()

	rankings, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 14, 23, 2, 19, 554000000, time.UTC)
	for _, r := range rankings {
		if !r.PublishedAt.Equal(want) {
			t.Fatalf("PublishedAt = %v, want the run's own scrapedAt %v", r.PublishedAt, want)
		}
	}
}

// A missing/zero scrapedAt must not silently produce the Unix epoch as
// PublishedAt — it falls back to when the run actually finished.
func TestFetchRankings_FallsBackToFetchTimeWhenScrapedAtIsMissing(t *testing.T) {
	fake := newFakeApify(t)
	fake.succeeded = []runInfo{{ID: "r", Status: statusSucceeded, DefaultKeyValueStoreID: "ds", FinishedAt: finishedAt(time.Minute)}}
	fake.outputs["ds"] = `{"rankingType":"hltv","rankings":[{"place":1,"team":{"name":"Spirit"},"points":1000}]}`
	server := fake.serve()
	defer server.Close()

	rankings, err := newTestProvider(t, server, 50).FetchRankings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rankings) != 1 {
		t.Fatalf("got %d rankings, want 1", len(rankings))
	}
	if want := testNow.Add(time.Minute); !rankings[0].PublishedAt.Equal(want) {
		t.Fatalf("PublishedAt = %v, want the run's finish time %v", rankings[0].PublishedAt, want)
	}
}

// APIFY_MAX_TEAMS=0 must be sent as a literal 0, not silently omitted
// (which would let the actor fall back to its own default team count
// instead of honoring an explicit operator choice).
func TestFetchRankings_SendsMaxTeamsZeroExplicitly(t *testing.T) {
	fake := newFakeApify(t)
	server := fake.serve()
	defer server.Close()

	if _, err := newTestProvider(t, server, 0).FetchRankings(context.Background()); !errors.Is(err, enrichment.ErrFetchPending) {
		t.Fatal(err)
	}
	body, err := json.Marshal(fake.started[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"maxTeams":0`) {
		t.Fatalf("actor input = %s, want an explicit maxTeams:0", body)
	}
}
