package pandascore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
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

// gameSpec is one game's slice of PandaScore's API: its own path prefix and
// (CS2 only) an extra filter — PandaScore's /csgo path still serves the
// pre-rebrand CS:GO game too, so cs-2 narrows it; /dota2 has no such split
// and needs no filter at all (confirmed against PandaScore's own API
// reference: no videogame_title filter exists for Dota 2 endpoints).
type gameSpec struct {
	code       competition.GameCode
	pathPrefix string
	filter     string
}

var supportedGames = []gameSpec{
	{code: competition.GameCS2, pathPrefix: "csgo", filter: "filter[videogame_title]=cs-2"},
	{code: competition.GameDota2, pathPrefix: "dota2"},
}

// endpoint builds this game's URL for path, appending the game filter (when
// it has one) plus any extra query parameters. Every parameter goes through
// here rather than being concatenated at the call site: the first one needs
// "?" and the rest need "&", and getting that wrong silently produced
// /dota2/tournaments/past&sort=-end_at — a path PandaScore answers with 404
// "Route not found", which is exactly how Dota 2's past tournaments went
// unsynced from the day the game was added. CS2 hid the bug, because its
// own filter had already opened the query string.
func (g gameSpec) endpoint(path string, params ...string) string {
	if g.filter != "" {
		params = append([]string{g.filter}, params...)
	}
	for _, param := range params {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + param
	}
	return path
}

// UpcomingEvents refreshes the local event catalog from PandaScore's
// tournament endpoints, once per supported game (see supportedGames). We
// still expose a PandaScore *series* as our domain Event (so one
// subscription covers all child stages), but enrich it with tournament-level
// tier information and the series' full/league name.
//
// This matches PandaScore's current hierarchy (League -> Series -> Tournament
// -> Match) and avoids an N+1 "fetch tournaments for each series" pattern:
// three paginated tournament lists per game are enough for the whole catalog.
func (p *Provider) UpcomingEvents(ctx context.Context) ([]competition.Event, error) {
	var events []competition.Event
	var errs []error
	for _, game := range supportedGames {
		gameEvents, err := p.upcomingEventsForGame(ctx, game)
		if err != nil {
			// One game's endpoint having a bad day must not also take down
			// every other game's discovery. Only escalated to a real error
			// below if every game failed — CompetitionProviderGateway.execute
			// discards the whole result on any error (it's a failover
			// chain, not a partial-success API), so a per-game error here
			// would silently throw away a healthy game's events too.
			errs = append(errs, fmt.Errorf("%s: %w", game.code, err))
			p.log.Error("upcoming events failed for one game, continuing with the rest", "game", game.code, "error", err)
			continue
		}
		events = append(events, gameEvents...)
	}
	p.log.Info("upcoming events synchronized", "count", len(events), "failedGames", len(errs))
	if len(errs) == len(supportedGames) {
		return nil, errors.Join(errs...)
	}
	return events, nil
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func (p *Provider) upcomingEventsForGame(ctx context.Context, game gameSpec) ([]competition.Event, error) {
	upcoming, err := fetchPages[tournamentDTO](ctx, p, game.endpoint("/"+game.pathPrefix+"/tournaments/upcoming"), -1)
	if err != nil {
		return nil, err
	}
	running, err := fetchPages[tournamentDTO](ctx, p, game.endpoint("/"+game.pathPrefix+"/tournaments/running"), -1)
	if err != nil {
		return nil, err
	}
	past, err := fetchPages[tournamentDTO](ctx, p, game.endpoint("/"+game.pathPrefix+"/tournaments/past", "sort=-end_at"), 1)
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
		e := mapEvent(serie, game.code, now)
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
	return events, nil
}

// Matches resolves the supplied canonical Events back to PandaScore's own
// external ids, batches them in groups of config.EventBatchSize, and calls
// GET /csgo/matches?filter[serie_id]=<comma-joined ids> per batch. Batches
// are independent HTTP calls, so up to config.MaxConcurrency of them run in
// parallel to cut wall-clock sync time; a single batch's failure cancels the
// rest (errgroup) and the first error is returned.
// specFor looks up a GameCode's gameSpec — the events passed to Matches
// come back from UpcomingEvents, so a code that isn't in supportedGames
// would mean a caller round-tripped an Event this provider never produced.
func specFor(code competition.GameCode) (gameSpec, bool) {
	for _, g := range supportedGames {
		if g.code == code {
			return g, true
		}
	}
	return gameSpec{}, false
}

// gameBatch pairs a game's endpoint spec with one batch of that game's own
// events — events must never mix across games in a single filter[serie_id]
// request, since the path prefix and filter differ per game.
type gameBatch struct {
	game  gameSpec
	batch []competition.Event
}

func (p *Provider) Matches(ctx context.Context, events []competition.Event) ([]competition.Match, error) {
	batchSize := p.config.EventBatchSize
	if batchSize < 1 {
		batchSize = 1
	}
	concurrency := p.config.MaxConcurrency
	if concurrency < 1 {
		concurrency = 1
	}

	byGame := map[competition.GameCode][]competition.Event{}
	var order []competition.GameCode
	for _, e := range events {
		if _, seen := byGame[e.Game]; !seen {
			order = append(order, e.Game)
		}
		byGame[e.Game] = append(byGame[e.Game], e)
	}

	var batches []gameBatch
	for _, code := range order {
		game, ok := specFor(code)
		if !ok {
			p.log.Error("skipping matches for an unsupported game code", "game", code)
			continue
		}
		group := byGame[code]
		for start := 0; start < len(group); start += batchSize {
			end := start + batchSize
			if end > len(group) {
				end = len(group)
			}
			batches = append(batches, gameBatch{game: game, batch: group[start:end]})
		}
	}

	results := make([][]competition.Match, len(batches))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for i, gb := range batches {
		g.Go(func() error {
			ids := make([]string, 0, len(gb.batch))
			for _, e := range gb.batch {
				ids = append(ids, e.ExternalID)
			}
			path := p.matchesPath(gb.game, ids)
			dtos, err := fetchPages[matchDTO](ctx, p, path, -1)
			if err != nil {
				return err
			}
			batchMatches := make([]competition.Match, 0, len(dtos))
			for _, dto := range dtos {
				batchMatches = append(batchMatches, mapMatch(dto, gb.game.code))
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
//
// matchWindow renders PandaScore's range filter for begin_at, bounding a
// request to the matches that can still change or still need settling.
// Without it every request re-reads a long tournament's entire history:
// a Major's group stage is several hundred finished matches, which is
// several extra pages — several extra requests — on every single run, for
// data that has been final for a week.
//
// Empty when either bound is unset, which keeps the old fetch-everything
// behaviour available rather than silently truncating what a deployment
// asks for.
// matchesPath builds one batch's request: the series filter every batch
// carries, plus the time window when one is configured.
func (p *Provider) matchesPath(game gameSpec, serieIDs []string) string {
	params := []string{"filter[serie_id]=" + strings.Join(serieIDs, ",")}
	if window := p.matchWindow(); window != "" {
		params = append(params, window)
	}
	return game.endpoint("/"+game.pathPrefix+"/matches", params...)
}

func (p *Provider) matchWindow() string {
	if p.config.MatchWindowPast <= 0 || p.config.MatchWindowFuture <= 0 {
		return ""
	}
	now := p.now()
	return "range[begin_at]=" + now.Add(-p.config.MatchWindowPast).UTC().Format(time.RFC3339) +
		"," + now.Add(p.config.MatchWindowFuture).UTC().Format(time.RFC3339)
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
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
			return nil, fmt.Errorf("pandascore returned HTTP %d for %s: %s", resp.StatusCode, pageURL, common.TruncateForLog(body))
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
