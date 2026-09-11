package valvevrs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

// Provider implements enrichment.RankingProvider against Valve's public
// GitHub repository of CS2 Regional Standings snapshots.
type Provider struct {
	config Config
	client *http.Client
	now    func() time.Time
}

func NewProvider(config Config, client *http.Client) *Provider {
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{config: config, client: client, now: time.Now}
}

var _ enrichment.RankingProvider = (*Provider)(nil)

var regions = []string{"europe", "americas", "asia"}

func prefixFor(region string) string { return "standings_" + region + "_" }

const globalPrefix = "standings_global_"

// FetchRankings finds the latest global + regional snapshot files, parses
// them, and merges them into one RankedTeam per team (global rank always
// present when the team is in the global top list; regional rank/region set
// whichever regional file the team appears in, if any).
func (p *Provider) FetchRankings(ctx context.Context) ([]enrichment.RankedTeam, error) {
	files, err := p.latestFiles(ctx)
	if err != nil {
		return nil, err
	}
	global, ok := files[globalPrefix]
	if !ok {
		return nil, fmt.Errorf("valve vrs: no global standings file found in live/%d or live/%d", p.now().UTC().Year(), p.now().UTC().Year()-1)
	}
	publishedAt, err := parseSnapshotDate(global.name)
	if err != nil {
		return nil, err
	}

	globalRows, err := p.fetchTable(ctx, global)
	if err != nil {
		return nil, err
	}

	out := make([]enrichment.RankedTeam, 0, len(globalRows))
	indexByName := make(map[string]int, len(globalRows))
	for _, r := range globalRows {
		standing, pts := r.standing, r.points
		out = append(out, enrichment.RankedTeam{
			Identity:    enrichment.TeamIdentity{Name: r.name, Roster: r.roster},
			GlobalRank:  &standing,
			Points:      &pts,
			PublishedAt: publishedAt,
			Source:      enrichment.SourceValveVRS,
		})
		indexByName[normalizeKey(r.name)] = len(out) - 1
	}

	for _, region := range regions {
		file, ok := files[prefixFor(region)]
		if !ok {
			continue
		}
		// A region's failure aborts the whole fetch rather than silently
		// returning a partial ranking set: this runs from a background sync
		// job (never the poll-send path), so failing loudly and retrying
		// next tick is safer than caching an incomplete snapshot.
		regionRows, err := p.fetchTable(ctx, file)
		if err != nil {
			return nil, fmt.Errorf("valve vrs: fetch %s standings: %w", region, err)
		}
		for _, r := range regionRows {
			standing := r.standing
			if i, ok := indexByName[normalizeKey(r.name)]; ok {
				out[i].RegionalRank = &standing
				out[i].Region = region
				continue
			}
			// Present regionally but not in the global list at all —
			// unusual, but keep it as a regional-only ranking rather than
			// drop it.
			pts := r.points
			out = append(out, enrichment.RankedTeam{
				Identity:     enrichment.TeamIdentity{Name: r.name, Roster: r.roster},
				RegionalRank: &standing,
				Region:       region,
				Points:       &pts,
				PublishedAt:  publishedAt,
				Source:       enrichment.SourceValveVRS,
			})
		}
	}
	return out, nil
}

func normalizeKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

type dated struct {
	year int
	name string
}

var snapshotDateRe = regexp.MustCompile(`_(\d{4})_(\d{2})_(\d{2})\.md$`)

func parseSnapshotDate(filename string) (time.Time, error) {
	m := snapshotDateRe.FindStringSubmatch(filename)
	if m == nil {
		return time.Time{}, fmt.Errorf("valve vrs: cannot parse snapshot date from filename %q", filename)
	}
	return time.Parse("2006-01-02", fmt.Sprintf("%s-%s-%s", m[1], m[2], m[3]))
}

// latestFiles finds, for the global standings and each region, the most
// recently dated file across the current and previous year's live/
// directory (a fresh year's directory may not exist yet in early January,
// and Valve's own cadence is roughly monthly — see this package's tests for
// the exact published format this was built against).
func (p *Provider) latestFiles(ctx context.Context) (map[string]dated, error) {
	year := p.now().UTC().Year()
	entries, err := p.listDir(ctx, year)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		year--
		entries, err = p.listDir(ctx, year)
		if err != nil {
			return nil, err
		}
	}

	prefixes := append([]string{globalPrefix}, func() []string {
		out := make([]string, len(regions))
		for i, r := range regions {
			out[i] = prefixFor(r)
		}
		return out
	}()...)

	latest := map[string]dated{}
	latestDateStr := map[string]string{}
	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		for _, prefix := range prefixes {
			if !strings.HasPrefix(e.Name, prefix) {
				continue
			}
			m := snapshotDateRe.FindStringSubmatch(e.Name)
			if m == nil {
				continue
			}
			dateStr := m[1] + m[2] + m[3] // YYYYMMDD sorts lexically = chronologically
			if cur, ok := latestDateStr[prefix]; !ok || dateStr > cur {
				latest[prefix] = dated{year: year, name: e.Name}
				latestDateStr[prefix] = dateStr
			}
		}
	}
	return latest, nil
}

type contentEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (p *Provider) listDir(ctx context.Context, year int) ([]contentEntry, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/live/%d", p.config.APIBaseURL, repoOwner, repoName, year)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // that year's directory doesn't exist (yet, or no longer listed) — not an error
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("valve vrs github api returned HTTP %d for %s: %s", resp.StatusCode, url, common.TruncateForLog(body))
	}
	var entries []contentEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("decode valve vrs directory listing: %w", err)
	}
	return entries, nil
}

func (p *Provider) fetchTable(ctx context.Context, file dated) ([]row, error) {
	url := fmt.Sprintf("%s/%s/%s/main/live/%d/%s", p.config.RawBaseURL, repoOwner, repoName, file.year, file.name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("valve vrs raw content returned HTTP %d for %s: %s", resp.StatusCode, url, common.TruncateForLog(body))
	}
	return parseTable(string(body))
}
