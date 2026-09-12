// Command bot is the CS2 Predictor Telegram bot — composition root.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"cs2predictor/internal/adapter/apifyhltv"
	"cs2predictor/internal/adapter/grid"
	"cs2predictor/internal/adapter/liquipedia"
	"cs2predictor/internal/adapter/pandascore"
	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/adapter/telegram"
	"cs2predictor/internal/adapter/valvevrs"
	"cs2predictor/internal/app"
	"cs2predictor/internal/app/httpapi"
	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/domain/prediction"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// version/commit/buildTime are set via -ldflags at build time (see
// Makefile's LDFLAGS and docker/Dockerfile's build args); "dev" means the
// binary was built without them, e.g. a plain `go build`/`go run`.
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal startup error", "error", err)
		os.Exit(1)
	}
}

//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func run() error {
	cfg, err := app.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn, err := databaseDSN(cfg.DatabaseURL, cfg.DatabaseUser, cfg.DatabasePassword)
	if err != nil {
		return fmt.Errorf("database configuration: %w", err)
	}

	if err := pg.Migrate(ctx, dsn); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	pool, err := connectDatabase(ctx, dsn, cfg.DatabasePoolSize)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	clock := common.SystemUTCClock()
	httpClient := newHTTPClient(cfg.HTTP)

	// --- persistence adapters ---
	chats := pg.NewChatRepository(pool)
	catalog := pg.NewCompetitionRepository(pool)
	subscriptions := pg.NewSubscriptionRepository(pool)
	predictionsRepo := pg.NewPredictionRepository(pool)
	scoringRepo := pg.NewScoringRepository(pool)
	settlementRepo := pg.NewSettlementRepository(pool)
	reportStore := pg.NewScheduledReportRepository(pool)
	outbox := pg.NewOutbox(pool)
	clusterLock := pg.NewClusterLock(pool)
	dedup := pg.NewUpdateDeduplicator(pool)
	pendingUnsubscribes := pg.NewPendingUnsubscribeRepository(pool)
	retentionStore := pg.NewRetentionRepository(pool)
	adminActions := pg.NewAdminActionRepository(pool)
	invitations := pg.NewInvitationRepository(pool)
	runTx := app.TxRunner(func(ctx context.Context, fn func(context.Context) error) error {
		return pg.RunInTx(ctx, pool, fn)
	})

	// --- external providers ---
	pandaConfig := pandascore.Config{
		BaseURL: cfg.PandaScore.BaseURL, Token: cfg.PandaScore.Token,
		PageSize: cfg.PandaScore.PageSize, EventBatchSize: cfg.PandaScore.EventBatchSize,
		MaxConcurrency: cfg.PandaScore.MaxConcurrency,
	}
	providers := []competition.DataProvider{pandascore.NewProvider(pandaConfig, httpClient)}

	registry := prometheus.NewRegistry()
	metrics := app.NewMetrics(registry)

	gateway, err := app.NewCompetitionProviderGateway(providers, cfg.Providers, metrics, clock)
	if err != nil {
		return fmt.Errorf("build competition provider gateway: %w", err)
	}

	// --- enrichment (optional; PandaScore remains the sole source of truth
	// for events/matches/schedule/results — this only ever adds cached,
	// best-effort context like a Valve VRS rank line to an outgoing poll) ---
	enrichmentRepo := pg.NewEnrichmentRepository(pool)
	var enrichmentSources []enrichment.Source
	// teamMatchSources feeds TeamMatchService: every ranking source whose
	// RankingSync is actually running, so an unmatched team only ever gets
	// checked against feeds this deployment has real data for.
	var teamMatchSources []enrichment.Source
	var valveVRSSync *app.RankingSync
	if cfg.Enrichment.ValveVRSEnabled {
		valveVRSSync = &app.RankingSync{
			Source:   enrichment.SourceValveVRS,
			Provider: valvevrs.NewProvider(valvevrs.DefaultConfig(), httpClient),
			Teams:    enrichmentRepo, Rankings: enrichmentRepo, Identity: enrichmentRepo, State: enrichmentRepo,
			Snapshots: enrichmentRepo,
			Lock:      clusterLock, Log: log,
		}
	}
	// hltvRankingSync and vrsApifyRankingSync are a paired weekly fetch —
	// both hit the same Apify actor, just in its two different ranking
	// modes (see apifyhltv.DefaultConfig/DefaultValveConfig) — and share
	// one ApifyRankingGate so both fire (or both stay quiet) together, at
	// most once a calendar week, on HLTV's own Monday update day. VRS's job
	// here writes to the exact same enrichment.SourceValveVRS as
	// valveVRSSync above; that free GitHub-based job keeps running on its
	// own frequent schedule as the fallback for the rest of the week. It
	// gets its own LockKey (see RankingSync.LockKey's doc comment) rather
	// than sharing valveVRSSync's Source-derived one: with no jitter and a
	// 6h interval that's an exact multiple of this job's 1h check interval,
	// a shared lock name would let the two silently and repeatedly starve
	// each other.
	var hltvRankingSync, vrsApifyRankingSync *app.RankingSync
	if cfg.Enrichment.HLTVEnabled {
		apifyGate := &app.ApifyRankingGate{State: enrichmentRepo, Clock: clock}
		newApifyRankingSync := func(source enrichment.Source, providerConfig apifyhltv.Config, lockKey string) *app.RankingSync {
			providerConfig.MaxTeams = cfg.Enrichment.ApifyMaxTeams
			return &app.RankingSync{
				Source:   source,
				Provider: apifyhltv.NewProvider(providerConfig, httpClient),
				Teams:    enrichmentRepo, Rankings: enrichmentRepo, Identity: enrichmentRepo, State: enrichmentRepo,
				Snapshots: enrichmentRepo,
				Gate:      apifyGate,
				LockKey:   lockKey,
				Lock:      clusterLock, Log: log,
			}
		}
		hltvRankingSync = newApifyRankingSync(enrichment.SourceHLTV, apifyhltv.DefaultConfig(cfg.Enrichment.HLTVAPIToken), "")
		vrsApifyRankingSync = newApifyRankingSync(enrichment.SourceValveVRS, apifyhltv.DefaultValveConfig(cfg.Enrichment.HLTVAPIToken),
			"cs2predictor:ranking-sync:VALVE_VRS_APIFY")
	}
	// SourceValveVRS is registered once here, covering either or both of
	// valveVRSSync (free, frequent) and vrsApifyRankingSync (paid, weekly)
	// being the one(s) actually running — team-matching against VALVE_VRS
	// only needs at least one of them producing data, not both.
	if cfg.Enrichment.ValveVRSEnabled || cfg.Enrichment.HLTVEnabled {
		enrichmentSources = append(enrichmentSources, enrichment.SourceValveVRS)
		teamMatchSources = append(teamMatchSources, enrichment.SourceValveVRS)
	}
	if cfg.Enrichment.HLTVEnabled {
		enrichmentSources = append(enrichmentSources, enrichment.SourceHLTV)
		teamMatchSources = append(teamMatchSources, enrichment.SourceHLTV)
	}
	var teamStatsSync *app.TeamStatsSync
	if cfg.Enrichment.GRIDEnabled {
		gridProvider := grid.NewProvider(grid.DefaultConfig(cfg.Enrichment.GRIDAPIKey), httpClient)
		teamStatsSync = &app.TeamStatsSync{
			TeamStats: gridProvider, MatchStats: gridProvider,
			Catalog: catalog, Subscriptions: subscriptions,
			Form: enrichmentRepo, H2H: enrichmentRepo, State: enrichmentRepo,
			Lock: clusterLock, Log: log,
		}
		enrichmentSources = append(enrichmentSources, enrichment.SourceGRID)
	}
	var tournamentMetadataSync *app.TournamentMetadataSync
	if cfg.Enrichment.LiquipediaEnabled {
		tournamentMetadataSync = &app.TournamentMetadataSync{
			Provider:      liquipedia.NewProvider(liquipedia.DefaultConfig(cfg.Enrichment.LiquipediaAPIKey), httpClient),
			Catalog:       catalog,
			Subscriptions: subscriptions,
			Metadata:      enrichmentRepo, State: enrichmentRepo,
			Lock: clusterLock, Log: log,
		}
		enrichmentSources = append(enrichmentSources, enrichment.SourceLiquipedia)
	}

	// --- telegram adapter ---
	texts, err := telegram.LoadTexts()
	if err != nil {
		return fmt.Errorf("load i18n texts: %w", err)
	}
	telegramConfig := telegram.Config{
		BaseURL: cfg.Telegram.BaseURL, Token: cfg.Telegram.Token, WebhookSecret: cfg.Telegram.WebhookSecret,
		GlobalMessagesPerSecond:  telegram.DefaultGlobalMessagesPerSecond,
		PerChatMessagesPerSecond: telegram.DefaultPerChatMessagesPerSecond,
	}
	telegramClient := telegram.NewClient(telegramConfig, httpClient)
	// Resolved once at startup (rather than per-render) for the t.me/<username>
	// deep links that hand a group's admin panel off to a DM — fails fast like
	// the other required startup checks, since the whole bot depends on
	// Telegram connectivity anyway.
	botUser, err := telegramClient.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("get bot identity: %w", err)
	}
	if botUser.Username == nil || *botUser.Username == "" {
		return fmt.Errorf("bot account has no username set")
	}
	// Best-effort: the "/" autocomplete hints are a convenience, not a
	// dependency — a failure here must never block startup.
	if err := telegram.RegisterCommands(ctx, telegramClient); err != nil {
		log.Warn("registering telegram command hints failed", "error", err)
	}
	membership := telegram.NewMembershipAdapter(telegramClient)
	authorization := chat.NewAuthorizationService(chats, membership)
	var pollEnrichment telegram.PollEnrichmentSources
	if cfg.Enrichment.ValveVRSEnabled {
		pollEnrichment.Rankings = enrichmentRepo
	}
	if cfg.Enrichment.GRIDEnabled {
		pollEnrichment.Form = enrichmentRepo
		pollEnrichment.H2H = enrichmentRepo
	}
	pollGateway := telegram.NewPollGateway(telegramClient, catalog, chats, texts, log, pollEnrichment)

	// --- domain services ---
	predictionService := prediction.NewService(predictionsRepo, pollGateway, clock)
	scoringService := scoring.NewService(predictionsRepo, scoringRepo, clock)

	// chatTitle names a chat in a message that lands outside it — a private
	// recap or reminder, where the tournament name alone doesn't say which
	// of the reader's groups it came from. Best effort: an unnamed chat
	// just omits the line.
	chatTitle := func(ctx context.Context, chatID common.ChatID) string {
		settings, err := chats.Find(ctx, chatID)
		if err != nil || settings == nil {
			return ""
		}
		return settings.Title
	}
	settlement := app.NewResultSettlementService(predictionsRepo, scoringRepo, settlementRepo, scoringService, outbox, clock, runTx).
		WithRecaps(chats, chatTitle, log)
	completion := app.NewEventCompletionService(catalog, subscriptions, chats, scoringRepo, outbox, clock, runTx, log)

	// teamMatch resolves a team with no cached ranking against whichever
	// ranking feeds (teamMatchSources) are actually enabled — pointless
	// with none of them on, so it shares that gate rather than adding a
	// separate on/off flag of its own.
	var teamMatch *app.TeamMatchService
	if len(teamMatchSources) > 0 {
		teamMatch = &app.TeamMatchService{
			Requests: enrichmentRepo, Helpers: enrichmentRepo, Snapshots: enrichmentRepo,
			Rankings: enrichmentRepo, Identity: enrichmentRepo, Sources: teamMatchSources,
			Predictions: predictionsRepo, Chats: chats,
			Outbox: outbox, Clock: clock, Log: log, OperatorChatIDs: cfg.TeamMatchOperatorChatIDs,
		}
	}

	updateHandler := &telegram.UpdateHandler{
		Dedup: dedup, Predictions: predictionService, Chats: chats, Authorization: authorization,
		Catalog: catalog, Subscriptions: subscriptions, Scoring: scoringRepo, Texts: texts,
		Client: telegramClient, Clock: clock, Log: log, BotUsername: *botUser.Username,
		PendingUnsubscribes: pendingUnsubscribes, Outbox: outbox, RunTx: runTx, Metrics: metrics,
		AdminActions: adminActions, Invitations: invitations,
		InboundLimiter:           telegram.NewInboundLimiter(telegram.DefaultInboundPerSecond, telegram.DefaultInboundBurst),
		TeamMatches:              enrichmentRepo,
		TeamMatchHelpers:         enrichmentRepo,
		TeamMatchOperators:       enrichmentRepo,
		TeamRankings:             enrichmentRepo,
		TeamIdentity:             enrichmentRepo,
		TeamSnapshots:            enrichmentRepo,
		TeamMatchOperatorChatIDs: cfg.TeamMatchOperatorChatIDs,
		ProviderGateway:          gateway,
		EnrichmentState:          enrichmentRepo,
		EnrichmentSources:        enrichmentSources,
	}
	webhookHandler := telegram.NewWebhookHandler(telegramConfig, updateHandler)

	synchronizer := &app.CompetitionSynchronization{
		Gateway: gateway, Catalog: catalog, Subscriptions: subscriptions, Chats: chats, ActiveChats: chats,
		Predictions: predictionService, Settlement: settlement, EventCompletion: completion, TeamMatch: teamMatch,
		Outbox: outbox, Lock: clusterLock, Clock: clock, Metrics: metrics, Log: log,
	}
	digests := &app.DigestScheduler{
		Chats: chats, Scoring: scoringRepo, Insights: scoringRepo, Store: reportStore,
		Outbox: outbox, Lock: clusterLock, Clock: clock, RunTx: runTx, Log: log,
	}

	reminders := &app.PollReminderScheduler{
		Predictions: predictionsRepo, Catalog: catalog, Audience: chats, ChatTitles: chatTitle,
		Outbox: outbox, Lock: clusterLock, Clock: clock, RunTx: runTx, Log: log,
		Lead: cfg.PollReminderLead, ParticipantWindow: cfg.PollReminderParticipantWindow,
	}

	retention := &app.RetentionSweep{
		Store: retentionStore, Lock: clusterLock, Clock: clock, Log: log,
		ProcessedUpdatesTTL: cfg.Retention.ProcessedUpdatesTTL,
		PublishedOutboxTTL:  cfg.Retention.PublishedOutboxTTL,
		ResolvedRequestsTTL: cfg.Retention.ResolvedRequestsTTL,
		AdminActionsTTL:     cfg.Retention.AdminActionsTTL,
	}

	dispatcher := &app.OutboxDispatcher{
		Outbox: outbox, Lock: clusterLock, BatchSize: cfg.OutboxBatchSize, Metrics: metrics, Log: log,
		Publishers: []common.OutboxPublisher{
			telegram.NewMatchResultPublisher(telegramClient, chats, texts, *botUser.Username),
			telegram.NewEventFinishedPublisher(telegramClient, chats, texts),
			telegram.NewMonthlyDigestPublisher(telegramClient, chats, texts),
			telegram.NewAnnualDigestPublisher(telegramClient, chats, texts),
			telegram.NewBigEventPublisher(telegramClient, chats, texts),
			telegram.NewUnsubscribeConfirmationPublisher(telegramClient, chats, texts, metrics),
			telegram.NewResultRecapPublisher(telegramClient, chats, texts, metrics),
			telegram.NewPollReminderPublisher(telegramClient, chats, texts, metrics),
			telegram.NewTeamMatchAskPublisher(telegramClient, chats, texts, metrics),
			telegram.NewTeamMatchOperatorPingPublisher(telegramClient, chats, texts, metrics),
		},
	}

	// backgroundJobs tracks every scheduler goroutine so shutdown can wait
	// for them to actually stop (they each respect ctx via RunFixedDelay's
	// select) before the deferred pool.Close() runs — otherwise a job still
	// mid-query when the process exits could hit a closed pool.
	var backgroundJobs sync.WaitGroup
	runBackground := func(delay time.Duration, job func(ctx context.Context)) {
		backgroundJobs.Add(1)
		go func() {
			defer backgroundJobs.Done()
			// Each run gets its own bounded deadline (cfg.JobTimeout)
			// derived from the long-lived process ctx, rather than ctx
			// itself: without it, a single stuck run (a hung query, an
			// unresponsive provider) could hold its goroutine — and
			// whatever pool connection it's using — indefinitely, since
			// RunFixedDelay would otherwise just wait for it to return.
			app.RunFixedDelay(ctx, delay, func(ctx context.Context) {
				jobCtx, cancel := context.WithTimeout(ctx, cfg.JobTimeout)
				defer cancel()
				job(jobCtx)
			})
		}()
	}
	if cfg.SchedulingEnabled {
		runBackground(cfg.SyncEventsDelay, synchronizer.DiscoverEvents)
		runBackground(cfg.SyncMatchesDelay, synchronizer.SynchronizeMatches)
		runBackground(cfg.SyncPollCloseDelay, synchronizer.CloseDuePolls)
		runBackground(cfg.DigestCheckDelay, digests.Dispatch)
		runBackground(cfg.OutboxDelay, dispatcher.Dispatch)
		if valveVRSSync != nil {
			runBackground(cfg.Enrichment.ValveVRSSyncInterval, valveVRSSync.Dispatch)
		}
		if hltvRankingSync != nil {
			runBackground(cfg.Enrichment.ApifyRankingCheckInterval, hltvRankingSync.Dispatch)
		}
		if vrsApifyRankingSync != nil {
			runBackground(cfg.Enrichment.ApifyRankingCheckInterval, vrsApifyRankingSync.Dispatch)
		}
		if teamStatsSync != nil {
			runBackground(cfg.Enrichment.GRIDSyncInterval, teamStatsSync.Dispatch)
		}
		if tournamentMetadataSync != nil {
			runBackground(cfg.Enrichment.LiquipediaSyncInterval, tournamentMetadataSync.Dispatch)
		}
		if cfg.PollReminderLead > 0 {
			runBackground(cfg.PollReminderCheckDelay, reminders.Dispatch)
		}
		if cfg.Retention.Interval > 0 {
			runBackground(cfg.Retention.Interval, retention.Dispatch)
		}
	}
	defer backgroundJobs.Wait()

	mux := httpapi.NewRouter(httpapi.RouterDeps{
		Webhook: webhookHandler, Gateway: gateway, Registry: registry, Pool: pool,
		Metrics: metrics, Log: log, WebhookRateLimit: cfg.WebhookRateLimit,
		Version: version, Commit: commit, BuildTime: buildTime,
		EnrichmentState: enrichmentRepo, EnrichmentSources: enrichmentSources,
	})
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // guards against a slow/malicious client trickling headers (Slowloris)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error("graceful shutdown failed", "error", err)
		}
	}()

	log.Info("starting cs2predictor bot", "port", cfg.Port, "version", version, "commit", commit)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

func databaseDSN(base, user, password string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return "", errors.New("DATABASE_URL must be a valid postgres:// or postgresql:// URL")
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}

// connectDatabase opens the pool used for all runtime traffic. maxConns is
// set on the parsed pgxpool.Config struct directly rather than via a
// "pool_max_conns" DSN parameter — the DSN itself (dsn) must stay a plain
// connection string, since it's shared with Migrate's stdlib/goose
// connection and Postgres rejects pool_max_conns as an unrecognized
// startup parameter.
func connectDatabase(ctx context.Context, dsn string, maxConns int) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// Clamped rather than a bare int32(maxConns): DATABASE_POOL_SIZE is
	// operator-controlled config, but a bare conversion would silently wrap
	// around for a pathological value instead of failing loudly.
	switch {
	case maxConns < 1:
		maxConns = 1
	case maxConns > 1000:
		maxConns = 1000
	}
	poolConfig.MaxConns = int32(maxConns)
	return pgxpool.NewWithConfig(ctx, poolConfig)
}

func newHTTPClient(cfg app.HTTPClientConfig) *http.Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext,
	}
	return &http.Client{Transport: transport, Timeout: cfg.ReadTimeout + cfg.ResponseTimeout}
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
