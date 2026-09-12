// Command backfill-team-order is a one-off remediation for a real production
// incident (2026-09-12): internal/app.reconcileTeamOrder fixed
// PandaScore's opponents[] order being unstable across two fetches for the
// same match going forward, but couldn't retroactively correct matches
// already settled under a swapped team order before the fix existed — by
// the time the fix runs, the match's own persisted "previous" order is
// already the wrong one, so there is nothing left locally to anchor to.
//
// The matches below were identified by comparing, for every FINISHED match
// with at least one vote, the points actually awarded against the points
// that would have been awarded had the match's own FirstTeam/SecondTeam
// (and therefore its score) been swapped — 16 matches award strictly more
// points under the swapped interpretation, a signal far too strong (up to
// 8 votes agreeing) to be chance. See the incident's PR description for the
// full query and reasoning.
//
// This program is intentionally a fixed, reviewed, hardcoded list rather
// than a general "swap any match" tool: swapping a match that was never
// actually affected would introduce the exact bug this exists to fix.
// Idempotent and safe to re-run — reconcileTeamOrder-style swap is
// self-inverse, so it checks each match's current state against what was
// recorded during the investigation before touching anything, and skips
// (rather than double-swapping) a match that no longer matches.
//
// Usage: DATABASE_URL/DATABASE_USER/DATABASE_PASSWORD as the bot itself
// uses them (see internal/app.LoadConfig), then:
//
//	go run ./cmd/backfill-team-order            # dry run, no writes
//	go run ./cmd/backfill-team-order -apply     # applies the corrections
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// affectedMatch pins down exactly the state observed during investigation
// (team names, provider external ids are not available here, so the
// database uuid plus the recorded first/second score is the identity
// check) — see the package doc for how this list was derived.
type affectedMatch struct {
	id                     string // esport_match.id (common.MatchID)
	firstTeamName          string
	firstScore             int
	secondTeamName         string
	secondScore            int
	currentPoints, swapped int // for the operator's own review only, not used by the program
	description            string
}

var affectedMatches = []affectedMatch{
	{id: "0ce74440-d44e-32fc-91a5-39622d25fb43", firstTeamName: "Legacy", firstScore: 2, secondTeamName: "MIBR", secondScore: 1, currentPoints: 4, swapped: 7, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "1820a092-253f-3d50-a513-8d0155d89ce7", firstTeamName: "Esport Academy Copenhagen", firstScore: 2, secondTeamName: "UNiTY esports", secondScore: 0, currentPoints: 0, swapped: 2},
	{id: "3295f945-b473-39b8-84c4-8212b1381ad3", firstTeamName: "BIG", firstScore: 0, secondTeamName: "BetBoom Team", secondScore: 2, currentPoints: 6, swapped: 8, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "334e405c-49de-32b7-8334-3dd7ff11fc55", firstTeamName: "magic", firstScore: 2, secondTeamName: "FaZe", secondScore: 1, currentPoints: 7, swapped: 10, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "42dbebd1-c9fe-371b-8bea-ec5a24178acd", firstTeamName: "Nuclear TigeRES", firstScore: 2, secondTeamName: "BET-M 33", secondScore: 1, currentPoints: 0, swapped: 2},
	{id: "47a574e0-44e4-3d74-925c-1060fa2a5853", firstTeamName: "Alliance", firstScore: 2, secondTeamName: "FaZe", secondScore: 0, currentPoints: 1, swapped: 13, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "4ad904f6-90ec-3c18-80e4-3c6a76ad27f8", firstTeamName: "Inner Circle Esports", firstScore: 0, secondTeamName: "Nemiga", secondScore: 2, currentPoints: 0, swapped: 1},
	{id: "52dd9f2a-1d48-3a87-83eb-0b0380107941", firstTeamName: "MIBR", firstScore: 2, secondTeamName: "Alliance", secondScore: 0, currentPoints: 1, swapped: 9, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "5992eb2d-8691-3ea5-9785-af2871df30a8", firstTeamName: "9z", firstScore: 0, secondTeamName: "MIBR", secondScore: 2, currentPoints: 3, swapped: 4, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "5a3f8e79-b5a4-3f56-9f88-fcbdd7c0a2ed", firstTeamName: "PARIVISION", firstScore: 0, secondTeamName: "magic", secondScore: 2, currentPoints: 3, swapped: 5, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "7ed355ee-28cd-3dfe-8091-3b8c536525e4", firstTeamName: "MIBR", firstScore: 2, secondTeamName: "TheMongolz", secondScore: 1, currentPoints: 5, swapped: 11, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "96bc8aab-f4bd-3bb8-b1ec-15acb249288e", firstTeamName: "G2", firstScore: 1, secondTeamName: "Astralis", secondScore: 2, currentPoints: 2, swapped: 14, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "b078eb6e-ab7d-33da-b8d3-9b85ad77a6ab", firstTeamName: "TYLOO", firstScore: 2, secondTeamName: "GamerLegion", secondScore: 0, currentPoints: 1, swapped: 11, description: "FISSURE PLAYGROUND Season 3 2026"},
	{id: "b40eac57-ea18-377b-a80a-90f1deea5f57", firstTeamName: "NIP", firstScore: 1, secondTeamName: "Sinners", secondScore: 2, currentPoints: 0, swapped: 2},
	{id: "d74ce506-1bf9-33b3-9f92-34da8f549227", firstTeamName: "Strael-Bora", firstScore: 0, secondTeamName: "Fire Flux Esports", secondScore: 2, currentPoints: 0, swapped: 2},
	{id: "db54c648-efb8-35bc-b29d-3b1b30295a88", firstTeamName: "5star", firstScore: 2, secondTeamName: "TheMongolz", secondScore: 0, currentPoints: 2, swapped: 12, description: "FISSURE PLAYGROUND Season 3 2026"},
	// Found via direct confirmation from a chat operator ("G2 was first in
	// the poll"), not the currentPoints/swapped heuristic above — that
	// heuristic missed this one because, by chance, the small vote samples
	// in both affected chats scored acceptably under the (buggy) unswapped
	// interpretation (currentPoints 10 and 0 vs swapped 4 and 4). The score
	// value itself (BetBoom 1, G2 2) was independently confirmed correct
	// against PandaScore's live API; only the first/second team labeling
	// was swapped relative to what the poll actually asked.
	{id: "dcd6452e-d41a-30e3-9e97-d9aa9abfdfb5", firstTeamName: "BetBoom Team", firstScore: 1, secondTeamName: "G2", secondScore: 2, currentPoints: 10, swapped: 8, description: "FISSURE PLAYGROUND Season 3 2026"},
}

func main() {
	apply := flag.Bool("apply", false, "actually write the corrections and re-settle (default: dry run, no writes)")
	flag.Parse()

	ctx := context.Background()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := app.LoadConfig()
	if err != nil {
		slog.Error("backfill failed", "error", fmt.Errorf("load config: %w", err))
		os.Exit(1)
	}
	dsn, err := databaseDSN(cfg.DatabaseURL, cfg.DatabaseUser, cfg.DatabasePassword)
	if err != nil {
		slog.Error("backfill failed", "error", fmt.Errorf("database configuration: %w", err))
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("backfill failed", "error", fmt.Errorf("connect database: %w", err))
		os.Exit(1)
	}
	defer pool.Close()

	if err := run(ctx, pool, *apply, log); err != nil {
		slog.Error("backfill failed", "error", err)
		os.Exit(1)
	}
}

// run holds all the actual logic against an already-connected pool, so
// main_test.go can drive it against a real (testcontainers) database
// without needing DATABASE_URL/etc. in the test's environment.
func run(ctx context.Context, pool *pgxpool.Pool, apply bool, log *slog.Logger) error {
	clock := common.SystemUTCClock()
	catalog := pg.NewCompetitionRepository(pool)
	predictionsRepo := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)
	settlementRepo := pg.NewSettlementRepository(pool)
	subscriptions := pg.NewSubscriptionRepository(pool)
	chats := pg.NewChatRepository(pool)
	outbox := pg.NewOutbox(pool)
	runTx := app.TxRunner(func(ctx context.Context, fn func(context.Context) error) error {
		return pg.RunInTx(ctx, pool, fn)
	})
	scoringService := scoring.NewService(predictionsRepo, scoringRepo, clock)
	settlement := app.NewResultSettlementService(predictionsRepo, scoringRepo, settlementRepo, scoringService, outbox, clock, runTx)
	completion := app.NewEventCompletionService(catalog, subscriptions, chats, scoringRepo, outbox, clock, runTx, log)

	touchedEvents := map[common.EventID]competition.Event{}

	for _, am := range affectedMatches {
		matchID := common.MatchID{Value: uuid.MustParse(am.id)}
		match, err := catalog.FindMatch(ctx, matchID)
		if err != nil {
			return fmt.Errorf("find match %s: %w", am.id, err)
		}
		if match == nil {
			log.Warn("match no longer exists, skipping", "matchId", am.id)
			continue
		}
		if !matchesRecordedState(*match, am) {
			log.Warn("match state no longer matches the investigation snapshot, skipping to avoid a double-swap",
				"matchId", am.id, "current", describeMatch(*match))
			continue
		}

		swapped := *match
		swapped.FirstTeam, swapped.SecondTeam = match.SecondTeam, match.FirstTeam
		if swapped.Score != nil {
			swapped.Score = &competition.MatchScore{First: match.Score.Second, Second: match.Score.First}
		}

		log.Info("swapping team order", "matchId", am.id,
			"before", describeMatch(*match), "after", describeMatch(swapped), "dryRun", !apply)
		if !apply {
			continue
		}

		if _, err := catalog.SaveMatch(ctx, swapped); err != nil {
			return fmt.Errorf("save corrected match %s: %w", am.id, err)
		}
		event, err := catalog.FindEvent(ctx, swapped.EventID)
		if err != nil {
			return fmt.Errorf("find event for match %s: %w", am.id, err)
		}
		if event == nil {
			log.Warn("match's event no longer exists, score corrected but nothing to re-settle", "matchId", am.id)
			continue
		}
		settledCount, err := settlement.Settle(ctx, *event, swapped)
		if err != nil {
			return fmt.Errorf("settle corrected match %s: %w", am.id, err)
		}
		log.Info("resettled", "matchId", am.id, "polls", settledCount)
		touchedEvents[event.ID] = *event
	}

	for _, event := range touchedEvents {
		if err := completion.Complete(ctx, event); err != nil {
			log.Error("event completion recompute failed", "eventId", event.ID.Value, "error", err)
		}
	}
	return nil
}

func matchesRecordedState(match competition.Match, am affectedMatch) bool {
	if match.FirstTeam == nil || match.SecondTeam == nil || match.Score == nil {
		return false
	}
	return match.FirstTeam.Name == am.firstTeamName && match.SecondTeam.Name == am.secondTeamName &&
		match.Score.First == am.firstScore && match.Score.Second == am.secondScore
}

func describeMatch(m competition.Match) string {
	if m.FirstTeam == nil || m.SecondTeam == nil || m.Score == nil {
		return "<incomplete>"
	}
	return fmt.Sprintf("%s %d:%d %s", m.FirstTeam.Name, m.Score.First, m.Score.Second, m.SecondTeam.Name)
}

func databaseDSN(base, user, password string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return "", errors.New("DATABASE_URL must be a valid postgres:// or postgresql:// URL")
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}
