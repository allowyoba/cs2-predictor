package grid

import (
	"context"
	"net/http"
	"strings"

	"cs2predictor/internal/domain/enrichment"
)

// Provider implements enrichment.TeamStatsProvider and
// enrichment.MatchStatsProvider against GRID's Central Data (allSeries) and
// Series State (seriesState) GraphQL APIs — see the package doc for what is
// and isn't confirmed against the live schema.
type Provider struct {
	config Config
	client *http.Client
	// sampleSize bounds how many recent, decided (finished) series are
	// counted for a form/H2H result — each counted series costs one extra
	// seriesState request, so this trades sample size against request
	// volume against a rate-limited API.
	sampleSize int
	// seriesSearchLimit bounds how many of the most recent series (across
	// every team/title GRID has) are scanned client-side for a name match,
	// since the exact server-side filter argument names could not be
	// confirmed (see package doc). allSeries is assumed to return
	// most-recent-first, matching every public GRID example seen; this is
	// itself unverified and should be checked once real access exists.
	seriesSearchLimit int
}

func NewProvider(config Config, client *http.Client) *Provider {
	return &Provider{config: config, client: client, sampleSize: 10, seriesSearchLimit: 500}
}

var (
	_ enrichment.TeamStatsProvider  = (*Provider)(nil)
	_ enrichment.MatchStatsProvider = (*Provider)(nil)
)

type seriesTeamRef struct {
	BaseInfo struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"baseInfo"`
}

type seriesEdge struct {
	Node struct {
		ID    string          `json:"id"`
		Teams []seriesTeamRef `json:"teams"`
	} `json:"node"`
}

type allSeriesResponse struct {
	AllSeries struct {
		Edges []seriesEdge `json:"edges"`
	} `json:"allSeries"`
}

const allSeriesQuery = `
query GetSeries($limit: Int!) {
	allSeries(first: $limit) {
		edges {
			node {
				id
				teams {
					baseInfo {
						id
						name
					}
				}
			}
		}
	}
}`

// findSeries returns the ids of recent series whose participants include
// every name in names (case-insensitive exact match against GRID's own
// reported team name) — one name for a single team's recent series, two
// for a head-to-head search.
func (p *Provider) findSeries(ctx context.Context, names ...string) ([]string, error) {
	var resp allSeriesResponse
	if err := execute(ctx, p.client, p.config.CentralDataURL, p.config.APIKey, allSeriesQuery,
		map[string]any{"limit": p.seriesSearchLimit}, &resp); err != nil {
		return nil, err
	}

	var ids []string
	for _, edge := range resp.AllSeries.Edges {
		if seriesHasAllTeams(edge.Node.Teams, names) {
			ids = append(ids, edge.Node.ID)
		}
	}
	return ids, nil
}

func seriesHasAllTeams(teams []seriesTeamRef, names []string) bool {
	for _, name := range names {
		found := false
		for _, t := range teams {
			if strings.EqualFold(t.BaseInfo.Name, name) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type seriesStateTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Won  bool   `json:"won"`
}

type seriesStateResponse struct {
	SeriesState struct {
		ID       string            `json:"id"`
		Finished bool              `json:"finished"`
		Teams    []seriesStateTeam `json:"teams"`
	} `json:"seriesState"`
}

const seriesStateQuery = `
query SeriesState($seriesId: ID!) {
	seriesState(id: $seriesId) {
		id
		finished
		teams {
			id
			name
			won
		}
	}
}`

// seriesResult reports whether teamName won the given (finished) series.
// ok is false when the series isn't finished yet or teamName isn't one of
// its participants — both cases the caller must skip rather than count.
func (p *Provider) seriesResult(ctx context.Context, seriesID, teamName string) (won bool, ok bool, err error) {
	var resp seriesStateResponse
	if err := execute(ctx, p.client, p.config.SeriesStateURL, p.config.APIKey, seriesStateQuery,
		map[string]any{"seriesId": seriesID}, &resp); err != nil {
		return false, false, err
	}
	if !resp.SeriesState.Finished {
		return false, false, nil
	}
	for _, t := range resp.SeriesState.Teams {
		if strings.EqualFold(t.Name, teamName) {
			return t.Won, true, nil
		}
	}
	return false, false, nil
}

// GetTeamStats implements enrichment.TeamStatsProvider: recent win/loss
// record over the last sampleSize finished series GRID has for this team.
func (p *Provider) GetTeamStats(ctx context.Context, team enrichment.TeamIdentity) (*enrichment.RecentForm, error) {
	seriesIDs, err := p.findSeries(ctx, team.Name)
	if err != nil {
		return nil, err
	}

	wins, losses, sample := 0, 0, 0
	for _, id := range seriesIDs {
		if sample >= p.sampleSize {
			break
		}
		won, ok, err := p.seriesResult(ctx, id, team.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		sample++
		if won {
			wins++
		} else {
			losses++
		}
	}
	if sample == 0 {
		return nil, nil
	}
	return &enrichment.RecentForm{Wins: wins, Losses: losses, Sample: sample, Source: enrichment.SourceGRID}, nil
}

// GetHeadToHead implements enrichment.MatchStatsProvider: the two teams'
// record against each other over the last sampleSize finished series GRID
// has where both were participants.
func (p *Provider) GetHeadToHead(ctx context.Context, teamA, teamB enrichment.TeamIdentity) (*enrichment.HeadToHead, error) {
	seriesIDs, err := p.findSeries(ctx, teamA.Name, teamB.Name)
	if err != nil {
		return nil, err
	}

	aWins, bWins, sample := 0, 0, 0
	for _, id := range seriesIDs {
		if sample >= p.sampleSize {
			break
		}
		aWon, ok, err := p.seriesResult(ctx, id, teamA.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		sample++
		if aWon {
			aWins++
		} else {
			bWins++
		}
	}
	if sample == 0 {
		return nil, nil
	}
	return &enrichment.HeadToHead{TeamAWins: aWins, TeamBWins: bWins, Sample: sample, Source: enrichment.SourceGRID}, nil
}
