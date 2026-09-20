package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/miniapp"
	"cs2predictor/internal/platform/common"
)

// Package httpapi is the bot's HTTP surface: the Telegram webhook route
// and the health, metrics and version endpoints the container, compose
// healthcheck and reverse proxy expect. It is separate from the app
// package — which orchestrates the background jobs — so that neither
// concern has to be read to change the other; the dependency only runs
// one way, from here into app.

// RouterDeps are the dependencies NewRouter wires into the HTTP mux.
type RouterDeps struct {
	Webhook  http.Handler
	Gateway  *app.CompetitionProviderGateway
	Registry *prometheus.Registry
	Pool     DBPinger // nil is allowed; readiness then skips the DB check

	// EnrichmentState/EnrichmentSources report enabled enrichment providers'
	// sync status on /healthz/ready for observability only — nil/empty
	// skips that section entirely (used by tests and when no enrichment
	// provider is configured).
	EnrichmentState   enrichment.SyncStateRepository
	EnrichmentSources []enrichment.Source

	// Metrics/Log, when both set, wrap the webhook route with panic
	// recovery and request metrics (see withObservability). Either left
	// nil skips that wrapping — used by tests that don't wire a full
	// Metrics/logger.
	Metrics *app.Metrics
	Log     *slog.Logger
	// WebhookRateLimit bounds accepted webhook requests; its zero value
	// disables the limiter (see withRateLimit).
	WebhookRateLimit app.WebhookRateLimitConfig

	// MiniApp wires the authenticated half of the Mini App API — the
	// endpoints about one person. Left zero, those routes answer 503
	// rather than disappearing: a client that cannot tell "not deployed"
	// from "wrong URL" retries the wrong thing.
	MiniApp MiniAppDeps

	// MiniAppHistory backs the history screen. Separate from MiniApp
	// because it is a different repository in the composition root, and
	// bundling them would make one nil turn both off.
	MiniAppHistory MiniAppHistory
	// MiniAppActive backs the two screens that are not about the past:
	// predictions still running, and medals per chat.
	MiniAppActive MiniAppActive

	// Teams backs the Mini App's team/crest endpoint. Nil leaves the route
	// registered but answering 503, which is a clearer signal to a client
	// than a 404 on a path that does exist in other deployments.
	Teams TeamCatalog
	// Logos serves the mirrored crests; nil leaves the app drawing
	// initials, which is its fallback for a team with no picture anyway.
	Logos MiniAppLogos
	// MiniAppSettings, Switches and MiniAppAuthz back the settings screens:
	// the same settings the bot offers in a private chat, offered in the
	// app as well. Any of them missing turns those endpoints off.
	MiniAppSettings MiniAppSettings
	Switches        common.NotifySwitchboard
	MiniAppAuthz    MiniAppAuthorizer

	// Version/Commit/BuildTime are reported by GET /version — set via
	// -ldflags at build time (see Makefile and docker/Dockerfile),
	// "dev"/"unknown" for
	// a plain `go build`/`go run`.
	Version   string
	Commit    string
	BuildTime string
}

// NewRouter builds the HTTP mux: the Telegram webhook plus health, metrics,
// and version endpoints, matched to what the image HEALTHCHECK, the
// Compose healthchecks and the Caddy path allow-list in docker/ expect.
func NewRouter(deps RouterDeps) http.Handler {
	webhook := deps.Webhook
	if deps.Metrics != nil && deps.Log != nil {
		webhook = withObservability("telegram_webhook", webhook, deps.Metrics, deps.Log)
	}
	webhook = withRateLimit(webhook, deps.WebhookRateLimit)

	mux := http.NewServeMux()
	mux.Handle("POST /telegram/webhook", webhook)
	mux.Handle("GET /healthz/live", livenessHandler())
	mux.Handle("GET /healthz/ready", readinessHandler(deps.Gateway, deps.Pool, deps.EnrichmentState, deps.EnrichmentSources))
	mux.Handle("GET /metrics", promhttp.HandlerFor(deps.Registry, promhttp.HandlerOpts{}))
	mux.Handle("GET /version", versionHandler(deps.Version, deps.Commit, deps.BuildTime))
	// The Mini App's own surface. Versioned in the path from its first
	// endpoint: a Mini App is a shipped client that keeps running against
	// whatever it was built for, so breaking changes need somewhere to go.
	instrument := func(route string, handler http.Handler) http.Handler {
		if deps.Metrics == nil || deps.Log == nil {
			return handler
		}
		return withObservability(route, handler, deps.Metrics, deps.Log)
	}
	miniappTeams := instrument("miniapp_teams", teamsHandler(deps.Teams, deps.Logos))
	mux.Handle("GET /api/miniapp/v1/teams", miniappTeams)
	mux.Handle("GET /api/miniapp/v1/teams/{id}/logo", instrument("miniapp_logo", logoHandler(deps.Logos)))
	mux.Handle("GET /api/miniapp/v1/me/dashboard", instrument("miniapp_dashboard", dashboardHandler(deps.MiniApp, deps.MiniAppActive)))
	mux.Handle("GET /api/miniapp/v1/me/active", instrument("miniapp_active", activeHandler(deps.MiniApp, deps.MiniAppActive)))
	mux.Handle("GET /api/miniapp/v1/me/chats", instrument("miniapp_chats", chatsHandler(deps.MiniApp, deps.MiniAppActive)))
	mux.Handle("GET /api/miniapp/v1/me/history", instrument("miniapp_history", historyHandler(deps.MiniApp, deps.MiniAppHistory)))
	mux.Handle("GET /api/miniapp/v1/me/settings", instrument("miniapp_settings", settingsHandler(deps.MiniApp, deps.MiniAppSettings, deps.Switches)))
	mux.Handle("PATCH /api/miniapp/v1/me/settings", instrument("miniapp_settings_patch", patchSettingsHandler(deps.MiniApp, deps.MiniAppSettings, deps.Switches)))
	mux.Handle("PATCH /api/miniapp/v1/chats/{id}/settings", instrument("miniapp_chat_settings_patch", patchChatSettingsHandler(deps.MiniApp, deps.MiniAppSettings, deps.Switches, deps.MiniAppAuthz)))
	mux.Handle("GET /api/miniapp/v1/me/access", instrument("miniapp_access", accessHandler(deps.MiniApp)))
	mux.Handle("POST /api/miniapp/v1/me/access", instrument("miniapp_access_request", accessHandler(deps.MiniApp)))
	// The Mini App's own page, served from the binary. See
	// internal/miniapp for why it is embedded rather than deployed as
	// files next to the reverse proxy.
	if page, err := miniapp.Handler("/app/"); err == nil {
		mux.Handle("GET /app/", page)
	} else if deps.Log != nil {
		deps.Log.Error("mini app assets unavailable, its page will 404", "error", err)
	}
	return mux
}
