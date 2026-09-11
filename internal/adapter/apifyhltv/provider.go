// Package apifyhltv implements enrichment.RankingProvider against HLTV's
// own weekly world ranking, fetched via the public Apify actor
// paco_nassa/hltv-org-team-ranking, which scrapes hltv.org/ranking/teams.
//
// This adapter has NOT been exercised against a live Apify run — no token
// was available while building it. The endpoint (POST
// /v2/acts/{actorId}/run-sync-get-dataset-items), the actor's input fields
// (rankingType/maxTeams/country/year/month/day), and its output item shape
// (place/team.name/team.id/points/change/isNew) are all drawn from the
// actor's own published documentation, not confirmed against a live
// response. Once a real token is available, this should be revisited —
// same caveat internal/adapter/grid's package doc carries for its own
// unverified schema.
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
	// per result returned ($3/1000 as of writing), so this caps a single
	// sync run at a small, predictable fraction of a cent.
	defaultMaxTeams = 50
)

// Config points at the Apify REST API and the actor to run.
type Config struct {
	BaseURL  string
	ActorID  string
	Token    string
	MaxTeams int
}

// DefaultConfig returns the real Apify API endpoint and the public HLTV
// ranking actor, with the given token.
func DefaultConfig(token string) Config {
	return Config{BaseURL: defaultBaseURL, ActorID: defaultActorID, Token: token, MaxTeams: defaultMaxTeams}
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
// rankingType is always "hltv" here — Valve's own official ranking is the
// same actor's "valve" mode, but this bot already has a free, roster-aware
// source for that (internal/adapter/valvevrs); duplicating it via Apify
// would only lose the roster data without gaining anything.
type actorInput struct {
	RankingType string `json:"rankingType"`
	MaxTeams    int    `json:"maxTeams,omitempty"`
}

// rankingItem is one row of the actor's documented output.
type rankingItem struct {
	Place int `json:"place"`
	Team  struct {
		Name string `json:"name"`
	} `json:"team"`
	Points int `json:"points"`
}

// FetchRankings implements enrichment.RankingProvider. HLTV's own ranking
// carries no regional breakdown or player roster — only Identity.Name,
// GlobalRank and Points are populated; RegionalRank/Region/Roster stay
// zero, same as any RankedTeam field a source simply doesn't report.
func (p *Provider) FetchRankings(ctx context.Context) ([]enrichment.RankedTeam, error) {
	body, err := json.Marshal(actorInput{RankingType: "hltv", MaxTeams: p.config.MaxTeams})
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

	var items []rankingItem
	if err := json.Unmarshal(respBody, &items); err != nil {
		return nil, fmt.Errorf("decode apify hltv ranking response: %w", err)
	}

	publishedAt := time.Now().UTC()
	out := make([]enrichment.RankedTeam, 0, len(items))
	for _, item := range items {
		if item.Team.Name == "" {
			continue
		}
		rank, points := item.Place, item.Points
		out = append(out, enrichment.RankedTeam{
			Identity:    enrichment.TeamIdentity{Name: item.Team.Name},
			GlobalRank:  &rank,
			Points:      &points,
			PublishedAt: publishedAt,
			Source:      enrichment.SourceHLTV,
		})
	}
	return out, nil
}
