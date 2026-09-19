package app

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds every Prometheus collector the app emits: sync-job
// outcomes and entity counts, provider call results/latency, poll
// lifecycle events, and outbox delivery results.
type Metrics struct {
	SyncRuns        *prometheus.CounterVec
	SyncEntities    *prometheus.HistogramVec
	ProviderCalls   *prometheus.CounterVec
	ProviderLatency *prometheus.HistogramVec
	PredictionPolls *prometheus.CounterVec
	OutboxEvents    *prometheus.CounterVec
	HTTPRequests    *prometheus.CounterVec
	HTTPDuration    *prometheus.HistogramVec
	HTTPPanics      *prometheus.CounterVec
	// AdminActions/DMDeliveries cover the DM admin surface: what managers
	// do, and whether the confirmation requests aimed at them actually
	// arrive. Without these, "the bot never told me" has no answer.
	AdminActions *prometheus.CounterVec
	DMDeliveries *prometheus.CounterVec
	// The three below answer "is this thing still working" rather than
	// "what did it do": how long ago each scheduled job last finished a
	// run, how far behind Telegram's own delivery queue is, and how many
	// messages have been given up on. Each covers a failure that is
	// otherwise completely silent — a job that stopped being scheduled, a
	// webhook Telegram can no longer reach, a message nobody will ever
	// receive.
	JobLastRun   *prometheus.GaugeVec
	WebhookState *prometheus.GaugeVec
	DeadLetters  *prometheus.GaugeVec
}

// RecordAdminAction and RecordDMDelivery implement telegram.AdminMetrics.
func (m *Metrics) RecordAdminAction(action, result string) {
	m.AdminActions.WithLabelValues(action, result).Inc()
}

func (m *Metrics) RecordDMDelivery(result string) {
	m.DMDeliveries.WithLabelValues(result).Inc()
}

func NewMetrics(registry *prometheus.Registry) *Metrics {
	m := &Metrics{
		SyncRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "competition_sync_runs_total", Help: "Scheduled sync job runs.",
		}, []string{"operation", "result"}),
		// Histogram, not Summary: a Summary's quantiles are computed
		// per-process and can't be aggregated across instances in
		// Prometheus, whereas a histogram's bucket counts can (and
		// ProviderLatency right below already uses one — this keeps both
		// consistent).
		SyncEntities: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "competition_sync_entities", Help: "Entities synchronized per run.",
			Buckets: []float64{0, 1, 2, 5, 10, 25, 50, 100, 250, 500},
		}, []string{"type"}),
		ProviderCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "competition_provider_calls_total", Help: "Competition provider calls.",
		}, []string{"provider", "operation", "result"}),
		ProviderLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "competition_provider_latency_seconds", Help: "Competition provider call latency.",
		}, []string{"provider", "operation", "result"}),
		PredictionPolls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "prediction_polls_total", Help: "Prediction poll lifecycle events.",
		}, []string{"action"}),
		OutboxEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_events_total", Help: "Outbox events by result.",
		}, []string{"result", "type"}),
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "HTTP requests handled, by route/method/status.",
		}, []string{"route", "method", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency, by route/method.",
		}, []string{"route", "method"}),
		HTTPPanics: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_panics_recovered_total", Help: "Panics recovered from an HTTP handler, by route.",
		}, []string{"route"}),
		AdminActions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "admin_actions_total", Help: "Admin actions attempted, by action and outcome.",
		}, []string{"action", "result"}),
		DMDeliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dm_deliveries_total", Help: "Private messages the bot tried to deliver, by outcome.",
		}, []string{"result"}),
	}
	m.JobLastRun = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "scheduled_job_last_run_timestamp_seconds", Help: "Unix time each scheduled job last finished a run.",
	}, []string{"job"})
	m.WebhookState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "telegram_webhook_state", Help: "Telegram webhook health: pending updates, and 1/0 for whether Telegram reports an error.",
	}, []string{"metric"})
	m.DeadLetters = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "outbox_dead_letters", Help: "Undelivered outbox messages that have exhausted their retries, by event type.",
	}, []string{"event_type"})
	registry.MustRegister(m.JobLastRun, m.WebhookState, m.DeadLetters)
	registry.MustRegister(m.SyncRuns, m.SyncEntities, m.ProviderCalls, m.ProviderLatency, m.PredictionPolls, m.OutboxEvents,
		m.HTTPRequests, m.HTTPDuration, m.HTTPPanics, m.AdminActions, m.DMDeliveries)
	return m
}

// RecordJobRun timestamps a scheduled job's completed run. An alert on
// "this gauge stopped moving" catches a job that is no longer being
// scheduled at all, which no counter can express: a counter that stops
// increasing looks exactly like a counter with nothing to count.
func (m *Metrics) RecordJobRun(job string, at time.Time) {
	m.JobLastRun.WithLabelValues(job).Set(float64(at.Unix()))
}

// RecordCall implements ProviderMetrics for CompetitionProviderGateway.
func (m *Metrics) RecordCall(provider, operation, result string, duration time.Duration) {
	m.ProviderCalls.WithLabelValues(provider, operation, result).Inc()
	m.ProviderLatency.WithLabelValues(provider, operation, result).Observe(duration.Seconds())
}
