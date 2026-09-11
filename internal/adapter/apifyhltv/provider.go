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
// The endpoint (POST /v2/acts/{actorId}/run-sync-get-dataset-items) and the
// actor's input fields (rankingType/maxTeams) match the actor's published
// documentation. The response *shape* was corrected against a real sample
// run: run-sync-get-dataset-items returns a dataset containing one item per
// run (not one item per team) — {scrapedAt, rankingType, ..., rankings:
// [...]} — and each entry of that inner "rankings" array carries a
// top-level "players" roster (not nested under "team" as first assumed),
// which this adapter now feeds into Identity.Roster for the same
// roster-overlap matching fallback internal/adapter/valvevrs already
// supports (see enrichment.MatchTeam).
package apifyhltv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
)

// Config points at the Apify REST API and the actor to run, and which of
// the actor's two rankings this Provider fetches.
type Config struct {
	BaseURL     string
	ActorID     string
	Token       string
	MaxTeams    int
	RankingType string
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
		RankingType: "hltv", Source: enrichment.SourceHLTV}
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
		RankingType: "valve", Source: enrichment.SourceValveVRS}
}

// Provider implements enrichment.RankingProvider by running the actor
// synchronously and reading its dataset items back in the same call —
// run-sync-get-dataset-items, rather than the async run+poll+fetch dance,
// since a scheduled sync job has no reason to return before the run
// actually finishes (capped at 300s server-side; well within this job's
// own budget for an occasional, low-volume fetch).
type Provider struct {
	config Config
	client *http.Client
}

func NewProvider(config Config, client *http.Client) *Provider {
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{config: config, client: client}
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

// actorRun is one dataset item as actually returned by
// run-sync-get-dataset-items: the whole scrape result for a single run, not
// a single team — see the package doc comment. ScrapedAt is the run's own
// timestamp, used as every one of its teams' RankedTeam.PublishedAt (that
// field's contract is "the ranking snapshot's own date, not fetch time" —
// see enrichment.RankedTeam's doc comment).
type actorRun struct {
	ScrapedAt time.Time     `json:"scrapedAt"`
	Rankings  []rankingItem `json:"rankings"`
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

// FetchRankings implements enrichment.RankingProvider. Neither of the
// actor's two ranking modes carries a regional breakdown, so
// RegionalRank/Region stay zero — same as any RankedTeam field a source
// simply doesn't report — but Identity.Name, Identity.Roster, GlobalRank
// and Points are all populated.
func (p *Provider) FetchRankings(ctx context.Context) ([]enrichment.RankedTeam, error) {
	body, err := json.Marshal(actorInput{RankingType: p.config.RankingType, MaxTeams: p.config.MaxTeams})
	if err != nil {
		return nil, fmt.Errorf("encode apify hltv actor input: %w", err)
	}

	url := fmt.Sprintf("%s/v2/acts/%s/run-sync-get-dataset-items", p.config.BaseURL, p.config.ActorID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Bearer header, not a ?token= query parameter — Apify's own docs
	// recommend this precisely because a URL (unlike a header) tends to
	// end up in logs and history; this codebase never puts credentials in
	// a URL for the same reason (see internal/adapter/telegram/client.go's
	// APIError doc comment).
	req.Header.Set("Authorization", "Bearer "+p.config.Token)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apify hltv ranking request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("apify hltv ranking actor returned HTTP %d: %s", resp.StatusCode, common.TruncateForLog(respBody))
	}

	var runs []actorRun
	if err := json.Unmarshal(respBody, &runs); err != nil {
		return nil, fmt.Errorf("decode apify hltv ranking response: %w", err)
	}

	var out []enrichment.RankedTeam
	for _, run := range runs {
		// ScrapedAt is the run's own timestamp, not this call's — falls
		// back to fetch time only if the actor ever omits it, so a bad or
		// missing field degrades to the old behavior instead of stamping
		// every team with the Unix epoch.
		publishedAt := run.ScrapedAt.UTC()
		if publishedAt.IsZero() {
			publishedAt = time.Now().UTC()
		}
		for _, item := range run.Rankings {
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
	}
	return out, nil
}
