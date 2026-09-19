// Package apifyhltv implements enrichment.RankingProvider against
// hltv.org/ranking/teams, fetched via the public Apify actor
// paco_nassa/hltv-org-team-ranking — for either of the two rankings that
// actor can scrape from that one page: HLTV's own ranking (rankingType
// "hltv") or Valve's official ranking as mirrored on hltv.org (rankingType
// "valve"). Both are exposed by the same Provider type, parameterized by
// Config.RankingType/Config.Source — literally the same request/response
// handling for both, since the actor's output shape is identical either
// way.
//
// The actor's input fields (rankingType/maxTeams) match its published
// documentation. The response *shape* was corrected against a real sample
// run: the dataset holds one item per run (not one item per team) —
// {scrapedAt, rankingType, ..., rankings: [...]} — and each entry of that
// inner "rankings" array carries a top-level "players" roster (not nested
// under "team" as first assumed), which this adapter feeds into
// Identity.Roster for the same roster-overlap matching fallback
// internal/adapter/valvevrs already supports (see enrichment.MatchTeam).
//
// # Why the run is asynchronous
//
// This used to POST run-sync-get-dataset-items, which holds the connection
// open until the actor finishes. A real scrape takes minutes (an observed
// run started at 23:00:00 UTC and finished at 23:02:19), while the app's
// shared HTTP client gives up after 20 seconds — so every single fetch
// failed with a client timeout while the run itself went on to succeed and
// write its result into Apify's storage, which nothing then read. The
// ranking was never updated and the run was billed anyway.
//
// So a fetch here is now a small state machine spread over several ticks of
// the scheduled job, with the in-flight run recorded in
// enrichment.ProviderRun so it survives both a timeout and a restart:
//
//  1. a run we started earlier is still going    -> enrichment.ErrFetchPending
//  2. it finished                                -> read its dataset, done
//  3. no run of ours, but Apify already holds a successful one from this
//     period (the timed-out case, or a redeploy that lost our state)
//     -> adopt its result, for free
//  4. otherwise                                  -> start one, record it,
//     and return ErrFetchPending for a later tick to collect
//
// Nothing here ever starts a run while one is in flight, while this
// period's result already exists, or more than Config.MaxRunsPerPeriod
// times in a period — the actor bills per run, so "how often can this
// possibly spend money" has to be answerable by reading this file.
package apifyhltv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

const (
	defaultBaseURL = "https://api.apify.com"
	// defaultActorID is paco_nassa/hltv-org-team-ranking in Apify's
	// tilde-separated "owner~actor" id form, used directly in the API path.
	defaultActorID = "paco_nassa~hltv-org-team-ranking"
	// defaultMaxTeams bounds both relevance (a poll only ever needs a
	// team's own rank, never the full list) and cost: the actor is billed
	// per result returned ($3/1000 as of writing). At the weekly cadence
	// this is fetched (see app.ApifyRankingGate), 100 teams costs a small,
	// predictable fraction of a cent either way.
	defaultMaxTeams = 100
	// defaultMaxRunsPerPeriod caps how many runs a single weekly period can
	// ever pay for. Above one only so a run that genuinely fails on Apify's
	// side (FAILED/ABORTED) still gets a couple of chances that week
	// instead of writing the week off.
	defaultMaxRunsPerPeriod = 3
	// cachedRunLookback bounds how far back FetchLatestCached will look. A
	// ranking older than this is not worth adopting: the weekly job will
	// have a fresher one shortly, and a month-old table is worse than none.
	cachedRunLookback = 10 * 24 * time.Hour
	// adoptScanLimit bounds how many of the period's successful runs are
	// examined when looking for one to adopt — two rankings a week means a
	// handful at most, even counting retries.
	adoptScanLimit = 10
)

// Apify run statuses this adapter reasons about. READY/RUNNING mean "come
// back later"; anything else is terminal.
const (
	statusReady     = "READY"
	statusRunning   = "RUNNING"
	statusSucceeded = "SUCCEEDED"
)

// Config points at the Apify REST API and the actor to run, and which of
// the actor's two rankings this Provider fetches.
type Config struct {
	BaseURL     string
	ActorID     string
	Token       string
	MaxTeams    int
	RankingType string
	// MaxRunsPerPeriod caps paid runs per weekly period; zero means
	// defaultMaxRunsPerPeriod.
	MaxRunsPerPeriod int
	// Source is the enrichment.Source every RankedTeam this Provider
	// returns is tagged with — SourceHLTV for RankingType "hltv",
	// SourceValveVRS for RankingType "valve" (the two constructors below
	// pair these correctly; this field exists so FetchRankings never has
	// to infer one string from the other).
	Source enrichment.Source
}

// DefaultConfig returns the real Apify API endpoint, the public HLTV
// ranking actor, and its "hltv" (HLTV's own world ranking) mode.
func DefaultConfig(token string) Config {
	return Config{BaseURL: defaultBaseURL, ActorID: defaultActorID, Token: token, MaxTeams: defaultMaxTeams,
		RankingType: "hltv", Source: enrichment.SourceHLTV, MaxRunsPerPeriod: defaultMaxRunsPerPeriod}
}

// DefaultValveConfig is DefaultConfig's counterpart for the same actor's
// "valve" mode — Valve's own official ranking, mirrored on hltv.org. Tagged
// enrichment.SourceValveVRS: this feeds the very same VALVE_VRS ranking the
// free GitHub-based internal/adapter/valvevrs provider does, just fetched
// less often and from a different feed — the two are combined via a
// weekly-gated Apify job running alongside valvevrs's own frequent one (see
// cmd/bot/main.go), each independently free to update the shared cache.
func DefaultValveConfig(token string) Config {
	return Config{BaseURL: defaultBaseURL, ActorID: defaultActorID, Token: token, MaxTeams: defaultMaxTeams,
		RankingType: "valve", Source: enrichment.SourceValveVRS, MaxRunsPerPeriod: defaultMaxRunsPerPeriod}
}

// Provider implements enrichment.RankingProvider as the multi-tick run
// lifecycle described in the package comment.
type Provider struct {
	config Config
	client *http.Client
	runs   enrichment.ProviderRunRepository
	clock  common.Clock
}

// NewProvider wires the provider. runs persists the in-flight run across
// ticks and restarts — passing nil keeps everything working but only within
// one process lifetime, which is enough for a test and not enough for
// production. clock nil means the system UTC clock.
func NewProvider(config Config, client *http.Client, runs enrichment.ProviderRunRepository, clock common.Clock) *Provider {
	if client == nil {
		client = http.DefaultClient
	}
	if runs == nil {
		runs = newMemoryRunStore()
	}
	if clock == nil {
		clock = common.SystemUTCClock()
	}
	return &Provider{config: config, client: client, runs: runs, clock: clock}
}

var _ enrichment.RankingProvider = (*Provider)(nil)

// actorInput is paco_nassa/hltv-org-team-ranking's documented input shape.
// MaxTeams deliberately has no `omitempty`: a caller that explicitly wants
// 0 (say, to make the cap failure-obvious in a misconfiguration rather than
// silently falling back to the actor's own default) must get exactly that
// sent, not have the field vanish and the actor apply its own default
// instead.
type actorInput struct {
	RankingType string `json:"rankingType"`
	MaxTeams    int    `json:"maxTeams"`
}

// actorOutput is the run's OUTPUT record: the whole scrape result, with the
// teams under "rankings". ScrapedAt is the run's own timestamp, used as
// every one of its teams' RankedTeam.PublishedAt (that field's contract is
// "the ranking snapshot's own date, not fetch time" — see
// enrichment.RankedTeam's doc comment). RankingType is what makes a result
// attributable: one actor serves both of our rankings, so the output has to
// say which one it holds before we believe it.
type actorOutput struct {
	ScrapedAt   time.Time     `json:"scrapedAt"`
	RankingType string        `json:"rankingType"`
	Rankings    []rankingItem `json:"rankings"`
}

// rankingItem is one row of actorRun.Rankings.
type rankingItem struct {
	Place int `json:"place"`
	Team  struct {
		Name string `json:"name"`
	} `json:"team"`
	Points int `json:"points"`
	// Players is HLTV's reported roster for this team at scrape time — a
	// sibling of "team", not nested under it.
	Players []string `json:"players"`
}

// runInfo is the subset of Apify's run object this adapter needs.
type runInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// DefaultKeyValueStoreID holds the run's OUTPUT record, which is where
	// the full result envelope lives. The dataset (DefaultDatasetID) holds
	// only the bare "rankings" array — no scrapedAt, no rankingType — so it
	// can neither date a snapshot nor say which ranking it is.
	DefaultKeyValueStoreID string     `json:"defaultKeyValueStoreId"`
	DefaultDatasetID       string     `json:"defaultDatasetId"`
	FinishedAt             *time.Time `json:"finishedAt"`
}

func (r runInfo) inFlight() bool { return r.Status == statusReady || r.Status == statusRunning }

// FetchRankings implements enrichment.RankingProvider. Neither of the
// actor's two ranking modes carries a regional breakdown, so
// RegionalRank/Region stay zero — same as any RankedTeam field a source
// simply doesn't report — but Identity.Name, Identity.Roster, GlobalRank
// and Points are all populated.
//
// Returns enrichment.ErrFetchPending when the result isn't in yet; that is
// the normal outcome of the tick that starts a run.
func (p *Provider) FetchRankings(ctx context.Context) ([]enrichment.RankedTeam, error) {
	now := p.clock.Now().UTC()
	period := common.StartOfWeekUTC(now)

	state, err := p.runs.Run(ctx, p.config.Source, p.config.RankingType)
	if err != nil {
		return nil, fmt.Errorf("read apify %s run state: %w", p.config.RankingType, err)
	}

	if state != nil && state.RunID != "" {
		teams, done, err := p.collectStartedRun(ctx, state.RunID, state, period)
		if err != nil || done {
			return teams, err
		}
		// Terminal but unsuccessful — drop it and consider starting
		// another below, still inside this period's run budget.
	}

	if teams, ok, err := p.adoptFinishedRun(ctx, period); err != nil {
		return nil, err
	} else if ok {
		return teams, nil
	}

	return nil, p.startRun(ctx, state, period, now)
}

var _ enrichment.CachedRankingProvider = (*Provider)(nil)

// FetchLatestCached returns the most recent finished run's result for this
// ranking mode, whatever period it belongs to, and never starts one.
//
// It exists for startup: a run can finish while the bot is down (or during
// the very deploy that restarts it), and without this the result would sit
// unread in Apify until the weekly gate next opens — up to a week of the
// ranking being stale even though the data was already bought and paid
// for. Reading it costs nothing.
func (p *Provider) FetchLatestCached(ctx context.Context) ([]enrichment.RankedTeam, error) {
	runs, err := p.succeededRunsSince(ctx, p.clock.Now().UTC().Add(-cachedRunLookback))
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		teams, err := p.readRunOutput(ctx, run)
		if err != nil {
			// The actor's other ranking mode, or a run whose output is
			// gone: keep looking rather than failing the startup path.
			if isRankingTypeMismatch(err) || isNotFound(err) {
				continue
			}
			return nil, err
		}
		if len(teams) > 0 {
			return teams, nil
		}
	}
	return nil, nil
}

// collectStartedRun reports on a run this provider started earlier. done is
// false only when the run reached a terminal non-success state and the
// caller should consider starting another.
func (p *Provider) collectStartedRun(ctx context.Context, runID string, state *enrichment.ProviderRun, period time.Time) (teams []enrichment.RankedTeam, done bool, err error) {
	run, err := p.runInfo(ctx, runID)
	if err != nil {
		// The run itself may well be fine — don't lose track of it over a
		// transient API error, or the next tick would start a second one.
		return nil, true, err
	}
	switch {
	case run.inFlight():
		return nil, true, enrichment.ErrFetchPending
	case run.Status == statusSucceeded:
		teams, err := p.readRunOutput(ctx, run)
		if err != nil {
			return nil, true, err
		}
		return teams, true, p.markCollected(ctx, run, period, state.Attempts)
	default:
		return nil, false, nil
	}
}

// adoptFinishedRun looks for a successful run of this actor that already
// holds this period's data — the timed-out-but-succeeded case, and the
// redeploy-lost-our-state case. Reading it costs nothing; starting another
// run would cost money for data Apify already has.
//
// It scans the period's successful runs rather than just the latest one
// because a single actor serves both of our rankings: our own earlier run
// is regularly not the most recent, and mistaking "the last run isn't mine"
// for "there is nothing to adopt" would pay for a second copy of data we
// already have.
func (p *Provider) adoptFinishedRun(ctx context.Context, period time.Time) ([]enrichment.RankedTeam, bool, error) {
	runs, err := p.succeededRunsSince(ctx, period)
	if err != nil {
		return nil, false, err
	}
	for _, run := range runs {
		if run.FinishedAt == nil || run.FinishedAt.UTC().Before(period) {
			continue // last period's numbers; this one still needs its own run
		}
		teams, err := p.readRunOutput(ctx, run)
		if err != nil {
			// A dataset that isn't ours to use (the actor's other ranking
			// mode) is not an error — just keep looking.
			if isRankingTypeMismatch(err) {
				continue
			}
			return nil, false, err
		}
		return teams, true, p.markCollected(ctx, run, period, 0)
	}
	return nil, false, nil
}

// startRun starts a paid run, unless this period has already had its
// budget. The ErrFetchPending it returns on success says "come back next
// tick", not "something went wrong".
func (p *Provider) startRun(ctx context.Context, state *enrichment.ProviderRun, period, now time.Time) error {
	attempts := 0
	if state != nil && !state.PeriodStart.Before(period) {
		attempts = state.Attempts
	}
	if attempts >= p.maxRunsPerPeriod() {
		return fmt.Errorf("apify %s ranking: %d runs already started this period without a usable result, not starting another",
			p.config.RankingType, attempts)
	}

	body, err := json.Marshal(actorInput{RankingType: p.config.RankingType, MaxTeams: p.config.MaxTeams})
	if err != nil {
		return fmt.Errorf("encode apify hltv actor input: %w", err)
	}
	var started runInfo
	if err := p.call(ctx, http.MethodPost, fmt.Sprintf("/v2/acts/%s/runs", p.config.ActorID), body, &started); err != nil {
		return err
	}
	if started.ID == "" {
		return fmt.Errorf("apify %s ranking: run started without an id", p.config.RankingType)
	}
	if err := p.runs.SaveRun(ctx, enrichment.ProviderRun{
		Provider: p.config.Source, Key: p.config.RankingType, RunID: started.ID,
		Status: statusRunning, Attempts: attempts + 1, PeriodStart: period, StartedAt: now,
	}); err != nil {
		// Recording it is what stops the next tick paying for the same work
		// again, so a failure here must be loud rather than swallowed.
		return fmt.Errorf("record started apify run %s: %w", started.ID, err)
	}
	return enrichment.ErrFetchPending
}

func (p *Provider) maxRunsPerPeriod() int {
	if p.config.MaxRunsPerPeriod > 0 {
		return p.config.MaxRunsPerPeriod
	}
	return defaultMaxRunsPerPeriod
}

// markCollected keeps the run on record as this period's finished one.
// Kept rather than cleared because it is what tells the weekly gate the
// period is done — see enrichment.RunStatusCollected.
func (p *Provider) markCollected(ctx context.Context, run runInfo, period time.Time, attempts int) error {
	return p.runs.SaveRun(ctx, enrichment.ProviderRun{
		Provider: p.config.Source, Key: p.config.RankingType, RunID: run.ID,
		Status: enrichment.RunStatusCollected, Attempts: attempts,
		PeriodStart: period, StartedAt: p.clock.Now().UTC(),
	})
}

func (p *Provider) runInfo(ctx context.Context, runID string) (runInfo, error) {
	var run runInfo
	err := p.call(ctx, http.MethodGet, "/v2/actor-runs/"+url.PathEscape(runID), nil, &run)
	return run, err
}

// succeededRunsSince returns this account's successful runs of the actor
// started at or after since, most recent first.
func (p *Provider) succeededRunsSince(ctx context.Context, since time.Time) ([]runInfo, error) {
	var page struct {
		Items []runInfo `json:"items"`
	}
	path := fmt.Sprintf("/v2/acts/%s/runs?status=%s&desc=1&limit=%d&startedAfter=%s",
		p.config.ActorID, statusSucceeded, adoptScanLimit, url.QueryEscape(since.UTC().Format(time.RFC3339)))
	if err := p.call(ctx, http.MethodGet, path, nil, &page); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return page.Items, nil
}

// readRunOutput reads a finished run's OUTPUT record and maps it. It
// refuses an output produced by the actor's other ranking mode: one actor
// serves both our HLTV and our Valve ranking, and silently filing one as
// the other would corrupt the cache in a way nothing downstream could
// detect.
func (p *Provider) readRunOutput(ctx context.Context, run runInfo) ([]enrichment.RankedTeam, error) {
	if run.DefaultKeyValueStoreID == "" {
		return nil, fmt.Errorf("apify %s ranking: finished run has no key-value store", p.config.RankingType)
	}
	var output actorOutput
	path := fmt.Sprintf("/v2/key-value-stores/%s/records/OUTPUT", url.PathEscape(run.DefaultKeyValueStoreID))
	if err := p.call(ctx, http.MethodGet, path, nil, &output); err != nil {
		return nil, err
	}
	if output.RankingType != p.config.RankingType {
		return nil, &rankingTypeMismatchError{want: p.config.RankingType, got: output.RankingType}
	}

	// ScrapedAt is the run's own timestamp, not this call's — falls back to
	// the run's finish time, and only then to now, so a missing field
	// degrades gracefully instead of stamping every team with the Unix
	// epoch.
	publishedAt := output.ScrapedAt.UTC()
	if publishedAt.IsZero() && run.FinishedAt != nil {
		publishedAt = run.FinishedAt.UTC()
	}
	if publishedAt.IsZero() {
		publishedAt = p.clock.Now().UTC()
	}

	var out []enrichment.RankedTeam
	for _, item := range output.Rankings {
		if item.Team.Name == "" {
			continue
		}
		rank, points := item.Place, item.Points
		out = append(out, enrichment.RankedTeam{
			Identity:    enrichment.TeamIdentity{Name: item.Team.Name, Roster: item.Players},
			GlobalRank:  &rank,
			Points:      &points,
			PublishedAt: publishedAt,
			Source:      p.config.Source,
		})
	}
	return out, nil
}

// call performs one Apify API request and decodes it. Apify wraps every
// object response in {"data": ...} but returns dataset items as a bare
// array, so both shapes are accepted.
func (p *Provider) call(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.config.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Bearer header, not a ?token= query parameter — Apify's own docs
	// recommend this precisely because a URL (unlike a header) tends to
	// end up in logs and history; this codebase never puts credentials in
	// a URL for the same reason (see internal/adapter/telegram/client.go's
	// APIError doc comment).
	req.Header.Set("Authorization", "Bearer "+p.config.Token)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("apify hltv ranking request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{status: resp.StatusCode, body: common.TruncateForLog(respBody)}
	}
	if out == nil {
		return nil
	}
	if err := decodeAPIResponse(respBody, out); err != nil {
		return fmt.Errorf("decode apify hltv ranking response: %w", err)
	}
	return nil
}

// decodeAPIResponse unwraps Apify's {"data": ...} envelope when there is
// one, and otherwise decodes the payload as-is.
func decodeAPIResponse(body []byte, out any) error {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 {
		return json.Unmarshal(envelope.Data, out)
	}
	return json.Unmarshal(body, out)
}

// apiError carries the status code so a 404 from "last run" can be told
// apart from a real failure. The token never appears in it — only the
// status and the (truncated) response body.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("apify hltv ranking actor returned HTTP %d: %s", e.status, e.body)
}

func isNotFound(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.status == http.StatusNotFound
}

type rankingTypeMismatchError struct{ want, got string }

func (e *rankingTypeMismatchError) Error() string {
	return fmt.Sprintf("apify dataset holds the %q ranking, not %q", e.got, e.want)
}

func isRankingTypeMismatch(err error) bool {
	var mismatch *rankingTypeMismatchError
	return errors.As(err, &mismatch)
}

// memoryRunStore is the nil-repository fallback: enough to keep a single
// process's ticks coherent, deliberately not enough to survive a restart.
type memoryRunStore struct {
	mu   sync.Mutex
	runs map[string]enrichment.ProviderRun
}

func newMemoryRunStore() *memoryRunStore {
	return &memoryRunStore{runs: map[string]enrichment.ProviderRun{}}
}

func (m *memoryRunStore) key(provider enrichment.Source, key string) string {
	return string(provider) + "\x00" + key
}

func (m *memoryRunStore) SaveRun(_ context.Context, run enrichment.ProviderRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[m.key(run.Provider, run.Key)] = run
	return nil
}

func (m *memoryRunStore) Run(_ context.Context, provider enrichment.Source, key string) (*enrichment.ProviderRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[m.key(provider, key)]
	if !ok {
		return nil, nil
	}
	return &run, nil
}

func (m *memoryRunStore) ClearRun(_ context.Context, provider enrichment.Source, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.runs, m.key(provider, key))
	return nil
}
