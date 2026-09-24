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

	"cs2predictor/internal/adapter/pandascore"
	pg "cs2predictor/internal/adapter/postgres"
	"cs2predictor/internal/adapter/telegram"
	"cs2predictor/internal/app"
	"cs2predictor/internal/app/httpapi"
	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/competition"
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
	pendingApprovals := pg.NewPendingApprovalRepository(pool)
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
		MaxConcurrency:  cfg.PandaScore.MaxConcurrency,
		MatchWindowPast: cfg.PandaScore.MatchWindowPast, MatchWindowFuture: cfg.PandaScore.MatchWindowFuture,
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

	// Operational notices to the administrator chats. Wired here, between
	// the gateway and the sync jobs, because both report their health
	// through it: the gateway via ObserveHealth, every enrichment sync via
	// the ObservedSyncState decorator below.
	adminAlerter := &app.AdminAlerter{
		Outbox: outbox, Releases: enrichmentRepo, ChatIDs: cfg.TeamMatchOperatorChatIDs,
		Switches: chats, Log: log,
	}
	gateway.ObserveHealth(adminAlerter)
	enrichmentState := &app.ObservedSyncState{SyncStateRepository: enrichmentRepo, Observer: adminAlerter}

	enrichmentBuilt := buildEnrichment(cfg.Enrichment, enrichmentRepo, enrichmentState, catalog, subscriptions, httpClient, clock, clusterLock, log)
	enrichmentSources := enrichmentBuilt.Sources
	teamMatchSources := enrichmentBuilt.TeamMatchSources

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
	pollGateway.StreamRecorder = predictionsRepo

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
	// Tournament boards are narrowed for team/player followers everywhere.
	scopes := app.SubscriptionScopes{Targets: subscriptions}
	scopedScoring := app.ScopedScoring{Repository: scoringRepo, Scopes: scopes}
	settlement := app.NewResultSettlementService(predictionsRepo, scopedScoring, settlementRepo, scoringService, outbox, clock, runTx).
		WithRecaps(chats, chatTitle, log)
	completion := app.NewEventCompletionService(catalog, subscriptions, chats, scopedScoring, scoringRepo, outbox, clock, runTx, log).
		WithPersonalRecaps(chats).WithSwitches(chats)

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

	// The Ideas channel. Its limits live in feedback.Policy; a deployment
	// can loosen or tighten them, but never to nothing — see LoadConfig.
	feedbackService := &app.FeedbackService{
		Repo: pg.NewFeedbackRepository(pool), Outbox: outbox, Policy: cfg.Feedback,
		Clock: clock, Metrics: metrics, Log: log, AdminChatIDs: cfg.TeamMatchOperatorChatIDs,
	}

	updateHandler := &telegram.UpdateHandler{
		Dedup: dedup, Predictions: predictionService, Chats: chats, Authorization: authorization,
		Catalog: catalog, Subscriptions: subscriptions, Scoring: scoringRepo, Texts: texts,
		Scopes: scopes,
		Follows: &app.FollowService{
			Targets: subscriptions, Catalog: catalog, Predictions: predictionService, Chats: chats, Clock: clock, Log: log,
		},
		Client: telegramClient, Clock: clock, Log: log, BotUsername: *botUser.Username,
		PendingApprovals: pendingApprovals, Outbox: outbox, RunTx: runTx, Metrics: metrics,
		AdminActions: adminActions, Invitations: invitations,
		InboundLimiter:           telegram.NewInboundLimiter(telegram.DefaultInboundPerSecond, telegram.DefaultInboundBurst),
		TeamMatches:              enrichmentRepo,
		TeamMatchHelpers:         enrichmentRepo,
		TeamMatchOperators:       enrichmentRepo,
		TeamRankings:             enrichmentRepo,
		TeamIdentity:             enrichmentRepo,
		TeamSnapshots:            enrichmentRepo,
		TournamentMetadata:       enrichmentRepo,
		TeamMatchOperatorChatIDs: cfg.TeamMatchOperatorChatIDs,
		ProviderGateway:          gateway,
		EnrichmentState:          enrichmentRepo,
		EnrichmentSources:        enrichmentSources,
		EnrichmentIntervals:      enrichmentBuilt.Intervals,
		DeadLetters:              outbox,
		Feedback:                 feedbackService,
		MiniAppURL:               cfg.MiniAppURL,
	}
	webhookHandler := telegram.NewWebhookHandler(telegramConfig, updateHandler)

	synchronizer := &app.CompetitionSynchronization{
		Gateway: gateway, Catalog: catalog, Subscriptions: subscriptions, Targets: subscriptions, Chats: chats, ActiveChats: chats,
		Predictions: predictionService, Settlement: settlement, EventCompletion: completion, TeamMatch: teamMatch,
		Outbox: outbox, Switches: chats, Lock: clusterLock, Clock: clock, Metrics: metrics, Log: log,
		MatchSyncColdInterval: cfg.SyncMatchesColdInterval,
	}
	digests := &app.DigestScheduler{
		Chats: chats, Scoring: scoringRepo, Insights: scoringRepo, Store: reportStore,
		Outbox: outbox, Switches: chats, Lock: clusterLock, Clock: clock, RunTx: runTx, Log: log,
	}

	// Runs on the digest job's own cadence: both are "is anything due for
	// this chat right now?" sweeps, and neither needs a schedule of its own.
	eve := &app.EventEveScheduler{
		Chats: chats, ChatSettings: chats, Subscriptions: subscriptions, Catalog: catalog,
		Scoring: scopedScoring, Store: reportStore, Outbox: outbox, Switches: chats, Lock: clusterLock,
		Clock: clock, Log: log, Lead: cfg.EventEveLead,
	}

	reminders := &app.PollReminderScheduler{
		Predictions: predictionsRepo, Catalog: catalog, Audience: chats, ChatTitles: chatTitle,
		Outbox: outbox, Lock: clusterLock, Clock: clock, RunTx: runTx, Log: log,
		Lead: cfg.PollReminderLead, ParticipantWindow: cfg.PollReminderParticipantWindow,
	}

	retention := &app.RetentionSweep{
		Store: retentionStore, Lock: clusterLock, Clock: clock, Log: log,
		ProcessedUpdatesTTL:     cfg.Retention.ProcessedUpdatesTTL,
		PublishedOutboxTTL:      cfg.Retention.PublishedOutboxTTL,
		ResolvedRequestsTTL:     cfg.Retention.ResolvedRequestsTTL,
		AdminActionsTTL:         cfg.Retention.AdminActionsTTL,
		ProcessedUpdatesMaxRows: cfg.Retention.ProcessedUpdatesMaxRows,
		PublishedOutboxMaxRows:  cfg.Retention.PublishedOutboxMaxRows,
	}

	dispatcher := &app.OutboxDispatcher{
		Outbox: outbox, Lock: clusterLock, BatchSize: cfg.OutboxBatchSize, Metrics: metrics, Log: log,
		Publishers: []common.OutboxPublisher{
			telegram.WithQuietHours(telegram.NewMatchResultPublisher(telegramClient, chats, texts, *botUser.Username), chats, clock),
			// Quiet hours apply to the proactive, non-urgent messages a
			// chat receives; see telegram/quiet_hours.go for what is
			// deliberately left out of that list.
			telegram.WithQuietHours(telegram.NewEventFinishedPublisher(telegramClient, chats, texts), chats, clock),
			// The personal half of that recap is a DM, and quiet hours are
			// a chat's setting — somebody's own inbox is not the room.
			telegram.NewEventRecapPublisher(telegramClient, chats, texts, metrics),
			telegram.WithQuietHours(telegram.NewMonthlyDigestPublisher(telegramClient, chats, texts), chats, clock),
			telegram.WithQuietHours(telegram.NewAnnualDigestPublisher(telegramClient, chats, texts), chats, clock),
			telegram.WithQuietHours(telegram.NewBigEventPublisher(telegramClient, chats, texts), chats, clock),
			telegram.NewUnsubscribeConfirmationPublisher(telegramClient, chats, texts, metrics),
			telegram.NewResultRecapPublisher(telegramClient, chats, texts, metrics),
			telegram.NewPollReminderPublisher(telegramClient, chats, texts, metrics),
			telegram.NewTeamMatchAskPublisher(telegramClient, chats, texts, metrics),
			telegram.NewTeamMatchOperatorPingPublisher(telegramClient, chats, texts, metrics),
			telegram.NewAdminAlertPublisher(telegramClient, chats, texts, metrics),
			telegram.WithQuietHours(telegram.NewEventEvePublisher(telegramClient, chats, texts), chats, clock),
			telegram.NewSuggestionPublisher(telegramClient, chats, texts, metrics),
			telegram.NewMiniAppAccessPublisher(telegramClient, chats, texts, metrics),
		},
	}

	// The two watchdogs. Neither can be satisfied by the readiness probe:
	// a bot whose webhook Telegram cannot reach, and a message the outbox
	// has given up on, both leave a perfectly healthy process behind.
	webhookWatchdog := &app.WebhookWatchdog{
		Inspector: telegramClient, Alerter: adminAlerter, Metrics: metrics, Clock: clock, Log: log,
		PendingThreshold: cfg.DeliveryBacklogAlert,
	}
	// The machine itself: the only failure that takes every job down at
	// once, and the only one nothing else here would notice.
	hostMonitor := &app.HostMonitor{
		Metrics: metrics, Alerter: adminAlerter, Limits: cfg.HostLimits, Log: log, Root: "/",
	}

	deadLetters := &app.DeadLetterWatch{Store: outbox, Alerter: adminAlerter, Metrics: metrics, Log: log}

	logoMirror := &app.LogoMirror{
		Cache: enrichmentRepo, Games: enrichmentRepo, Client: httpClient, Clock: clock, Lock: clusterLock, Log: log,
	}

	// backgroundJobs tracks every scheduler goroutine so shutdown can wait
	// for them to actually stop (they each respect ctx via RunFixedDelay's
	// select) before the deferred pool.Close() runs — otherwise a job still
	// mid-query when the process exits could hit a closed pool.
	var backgroundJobs sync.WaitGroup
	// name is what the job's heartbeat gauge is labelled with: a job that
	// silently stops being scheduled produces no error and no missing
	// counter — only a timestamp that stops moving.
	runBackground := func(name string, delay time.Duration, job func(ctx context.Context)) {
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
				metrics.RecordJobRun(name, time.Now())
			})
		}()
	}
	if cfg.SchedulingEnabled {
		runBackground("discover-events", cfg.SyncEventsDelay, synchronizer.DiscoverEvents)
		runBackground("synchronize-matches", cfg.SyncMatchesDelay, synchronizer.SynchronizeMatches)
		// Reuses the event-discovery cadence: this is a cheap DB-only sweep,
		// not another provider call, so it needs no config of its own.
		runBackground("reconcile-event-completions", cfg.SyncEventsDelay, synchronizer.ReconcileEventCompletions)
		runBackground("close-due-polls", cfg.SyncPollCloseDelay, synchronizer.CloseDuePolls)
		runBackground("digests", cfg.DigestCheckDelay, digests.Dispatch)
		if cfg.EventEveLead >= 0 {
			runBackground("event-eve", cfg.DigestCheckDelay, eve.Dispatch)
		}
		runBackground("outbox", cfg.OutboxDelay, dispatcher.Dispatch)
		for _, job := range enrichmentBuilt.Jobs {
			runBackground("enrichment-"+job.name, job.interval, job.dispatch)
		}
		if cfg.PollReminderLead > 0 {
			runBackground("poll-reminders", cfg.PollReminderCheckDelay, reminders.Dispatch)
		}
		if cfg.Retention.Interval > 0 {
			runBackground("retention", cfg.Retention.Interval, retention.Dispatch)
		}
		// The two watchdogs: one asks Telegram whether it can still reach
		// this bot, the other reports messages the outbox has given up on.
		// Both exist because their failures are otherwise completely
		// silent — see app.WebhookWatchdog and app.DeadLetterWatch.
		runBackground("webhook-watchdog", cfg.WatchdogInterval, webhookWatchdog.Check)
		runBackground("dead-letter-watch", cfg.WatchdogInterval, deadLetters.Check)
		runBackground("host-monitor", cfg.WatchdogInterval, hostMonitor.Check)
		// Copies team crests here so no viewer's browser ever fetches one
		// from HLTV or PandaScore. Runs on the slow watchdog cadence and
		// does nothing at all once it has them — see app.LogoMirror.
		runBackground("logo-mirror", cfg.WatchdogInterval, logoMirror.Dispatch)
	}
	defer backgroundJobs.Wait()

	mux := httpapi.NewRouter(httpapi.RouterDeps{
		Webhook: webhookHandler, Gateway: gateway, Registry: registry, Pool: pool,
		Metrics: metrics, Log: log, WebhookRateLimit: cfg.WebhookRateLimit, Teams: catalog,
		Logos:            enrichmentRepo,
		MiniAppHistory:   scoringRepo,
		MiniAppActive:    scoringRepo,
		MiniAppChatFacts: scoringRepo,
		MiniAppTargets:   subscriptions,
		MiniApp: httpapi.MiniAppDeps{
			BotToken: cfg.Telegram.Token, Stats: scoringRepo, Access: chats, Names: chats, Prefs: chats,
			Logos:     enrichmentRepo,
			Operators: cfg.TeamMatchOperatorChatIDs, Clock: clock, Log: log,
		},
		MiniAppFacts:    scoringRepo,
		MiniAppSettings: chats, Switches: chats, MiniAppAuthz: authorization,
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

	// Opportunistic catch-up: adopt anything a provider already finished
	// while this process was down — a weekly feed would otherwise sit
	// unread until its next window, even though the result exists (and was
	// paid for) already.
	//
	// Off the boot path and time-bounded. Run inline, it delayed serving by
	// as long as the provider took to answer, and the scheduled jobs that
	// had already started burned their own timeouts waiting behind it.
	for _, task := range enrichmentBuilt.StartupTasks {
		backgroundJobs.Add(1)
		go func(task func(ctx context.Context)) {
			defer backgroundJobs.Done()
			taskCtx, cancel := context.WithTimeout(ctx, cfg.JobTimeout)
			defer cancel()
			task(taskCtx)
		}(task)
	}

	// Announced from here rather than by the deploy pipeline: this reports
	// the build that is actually serving, however it got here — a pipeline
	// deploy, a manual rollback, or a restart onto a hand-changed image.
	adminAlerter.AnnounceRelease(ctx, version, commit)

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
