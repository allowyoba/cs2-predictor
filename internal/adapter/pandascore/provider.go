package pandascore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"cs2predictor/internal/domain/competition"
)

// Provider implements competition.DataProvider against the PandaScore API.
type Provider struct {
	config Config
	client *http.Client
	now    func() time.Time
	log    *slog.Logger
}

func NewProvider(config Config, client *http.Client) *Provider {
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{config: config, client: client, now: time.Now, log: slog.Default().With("provider", "pandascore")}
}

func (p *Provider) ProviderName() string { return "PANDASCORE" }

// UpcomingEvents refreshes the local event catalog from PandaScore's
// tournament endpoints. We still expose a PandaScore *series* as our domain
// Event (so one subscription covers all child stages), but enrich it with
// tournament-level tier information and the series' full/league name.
//
// This matches PandaScore's current hierarchy (League -> Series -> Tournament
// -> Match) and avoids an N+1 "fetch tournaments for each series" pattern:
// three paginated tournament lists are enough for the whole catalog.
func (p *Provider) UpcomingEvents(ctx context.Context) ([]competition.Event, error) {
	const cs2Filter = "filter[videogame_title]=cs-2"
	upcoming, err := fetchPages[tournamentDTO](ctx, p, "/csgo/tournaments/upcoming?"+cs2Filter, -1)
	if err != nil {
		return nil, err
	}
	running, err := fetchPages[tournamentDTO](ctx, p, "/csgo/tournaments/running?"+cs2Filter, -1)
	if err != nil {
		return nil, err
	}
	past, err := fetchPages[tournamentDTO](ctx, p, "/csgo/tournaments/past?"+cs2Filter+"&sort=-end_at", 1)
	if err != nil {
		return nil, err
	}

	// add() is only ever called for past, then upcoming, then running, each
	// loop run to completion before the next starts — so simply
	// overwriting Status on every call already prefers the most-live status
	// for a series appearing in more than one list, with no extra ordering
	// state to track.
	bySeries := map[int64]competition.Event{}
	now := p.now()
	add := func(dto tournamentDTO, status competition.EventStatus) {
		serie := dto.Serie
		if serie.ID == 0 {
			serie.ID = dto.SerieID
		}
		if serie.ID == 0 {
			return
		}
		if serie.League.ID == 0 && serie.League.Name == "" {
			serie.League = dto.League
		}
		e := mapEvent(serie, now)
		e.Status = status
		if e.StartsAt == nil {
			e.StartsAt = dto.BeginAt
		}
		if e.EndsAt == nil {
			e.EndsAt = dto.EndAt
		}
		e.Tier = betterTier(e.Tier, mapTier(dto.Tier))

		merged, ok := bySeries[serie.ID]
		if !ok {
			bySeries[serie.ID] = e
			return
		}
		if len(e.Name) > len(merged.Name) {
			merged.Name = e.Name
		}
		merged.StartsAt = earlierTime(merged.StartsAt, e.StartsAt)
		merged.EndsAt = laterTime(merged.EndsAt, e.EndsAt)
		merged.Tier = betterTier(merged.Tier, e.Tier)
		merged.Status = status
		bySeries[serie.ID] = merged
	}

	for _, dto := range past {
		add(dto, competition.EventFinished)
	}
	for _, dto := range upcoming {
		add(dto, competition.EventUpcoming)
	}
	for _, dto := range running {
		add(dto, competition.EventRunning)
	}

	events := make([]competition.Event, 0, len(bySeries))
	for _, e := range bySeries {
		events = append(events, e)
	}
	p.log.Info("upcoming events synchronized", "count", len(events))
	return events, nil
}

// Matches resolves the supplied canonical Events back to PandaScore's own
// external ids, batches them in groups of config.EventBatchSize, and calls
// GET /csgo/matches?filter[serie_id]=<comma-joined ids> per batch. Batches
// are independent HTTP calls, so up to config.MaxConcurrency of them run in
// parallel to cut wall-clock sync time; a single batch's failure cancels the
// rest (errgroup) and the first error is returned.
func (p *Provider) Matches(ctx context.Context, events []competition.Event) ([]competition.Match, error) {
	batchSize := p.config.EventBatchSize
	if batchSize < 1 {
		batchSize = 1
	}
	concurrency := p.config.MaxConcurrency
	if concurrency < 1 {
		concurrency = 1
	}

	var batches [][]competition.Event
	for start := 0; start < len(events); start += batchSize {
		end := start + batchSize
		if end > len(events) {
			end = len(events)
		}
		batches = append(batches, events[start:end])
	}

	results := make([][]competition.Match, len(batches))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for i, batch := range batches {
		g.Go(func() error {
			ids := make([]string, 0, len(batch))
			for _, e := range batch {
				ids = append(ids, e.ExternalID)
			}
			path := "/csgo/matches?filter[serie_id]=" + strings.Join(ids, ",") + "&filter[videogame_title]=cs-2"
			dtos, err := fetchPages[matchDTO](ctx, p, path, -1)
			if err != nil {
				return err
			}
			batchMatches := make([]competition.Match, 0, len(dtos))
			for _, dto := range dtos {
				batchMatches = append(batchMatches, mapMatch(dto))
			}
			results[i] = batchMatches
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	var matches []competition.Match
	for _, batchMatches := range results {
		matches = append(matches, batchMatches...)
	}
	p.log.Info("matches synchronized", "events", len(events), "matches", len(matches))
	return matches, nil
}

// fetchPages replicates PandaScoreProvider.get(): appends
// per_page=<pageSize>&page=<n> (using '&' if the path already has a '?'),
// loops while page<=maxPages (maxPages<0 means unlimited) AND the page
// returned exactly pageSize rows AND (X-Total is unknown OR fewer rows than
// X-Total have been collected so far).
func fetchPages[T any](ctx context.Context, p *Provider, path string, maxPages int) ([]T, error) {
	if strings.TrimSpace(p.config.Token) == "" {
		return nil, fmt.Errorf("pandascore token is not configured")
	}

	var result []T
	page := 1
	var total *int
	for maxPages <= 0 || page <= maxPages {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		pageURL := fmt.Sprintf("%s%s%sper_page=%d&page=%d", p.config.BaseURL, path, sep, p.config.PageSize, page)
		p.log.Debug("requesting pandascore resource", "path", strings.SplitN(path, "?", 2)[0], "page", page)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+p.config.Token)
		req.Header.Set("Accept", "application/json")

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("pandascore returned HTTP %d for %s: %s", resp.StatusCode, pageURL, truncateForError(body))
		}
		if len(body) == 0 {
			body = []byte("[]")
		}

		var batch []T
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("decode pandascore response: %w", err)
		}
		result = append(result, batch...)

		if totalHeader := resp.Header.Get("X-Total"); totalHeader != "" {
			if v, err := strconv.Atoi(totalHeader); err == nil {
				total = &v
			}
		}

		page++
		if len(batch) != p.config.PageSize {
			break
		}
		if total != nil && len(result) >= *total {
			break
		}
	}
	return result, nil
}

// truncateForError keeps an error-message body preview short and safe to
// log (PandaScore error bodies are small JSON objects, but this guards
// against an unexpectedly large or non-UTF8 response).
func truncateForError(body []byte) string {
	const max = 300
	if len(body) > max {
		body = body[:max]
	}
	return strings.ToValidUTF8(string(body), "�")
}
