package httpapi

import (
	"cs2predictor/internal/app"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

// statusRecorder captures the status code an inner handler wrote (defaulting
// to 200, matching http.ResponseWriter's own behavior when WriteHeader is
// never called explicitly) so middleware can label metrics/logs by outcome.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// withObservability wraps next with panic recovery — a single malformed
// Telegram update or a bug in a handler must not crash the whole process —
// plus Prometheus request-count/duration metrics labeled by route.
func withObservability(route string, next http.Handler, metrics *app.Metrics, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic recovered in http handler", "route", route, "panic", p)
				metrics.HTTPPanics.WithLabelValues(route).Inc()
				if !rec.wroteHeader {
					rec.WriteHeader(http.StatusInternalServerError)
				}
			}
			metrics.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
			metrics.HTTPDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		}()
		next.ServeHTTP(rec, r)
	})
}

// withRateLimit wraps next with a global (not per-client — Telegram's own
// servers call this webhook, so there's no meaningful per-caller identity to
// key a limiter on) token-bucket rate limiter: defense in depth beyond the
// webhook secret check, in case that secret ever leaks. A non-positive
// RequestsPerSecond disables the limiter entirely.
func withRateLimit(next http.Handler, cfg app.WebhookRateLimitConfig) http.Handler {
	if cfg.RequestsPerSecond <= 0 {
		return next
	}
	burst := cfg.Burst
	if burst < 1 {
		// rate.NewLimiter with burst 0 rejects every single request,
		// forever (the bucket never holds a token to spend) — a
		// WEBHOOK_RATE_LIMIT_BURST=0 misconfiguration would otherwise take
		// the whole webhook down silently, with no error at startup.
		burst = 1
	}
	limiter := rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), burst)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
