package httpapi

import (
	"cs2predictor/internal/app"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestWithObservability_RecoversPanicAndReturns500(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := app.NewMetrics(registry)
	log := slog.New(slog.NewTextHandler(httptest.NewRecorder(), nil))

	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	handler := withObservability("test_route", panicking, metrics, log)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/test", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 after a recovered panic", rec.Code)
	}
	if got := testutilCounterTotal(t, metrics.HTTPPanics); got != 1 {
		t.Fatalf("HTTPPanics total = %v, want 1", got)
	}
}

func TestWithObservability_RecordsSuccessMetricsWithoutAlteringResponse(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := app.NewMetrics(registry)
	log := slog.New(slog.NewTextHandler(httptest.NewRecorder(), nil))

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hi"))
	})
	handler := withObservability("test_route", ok, metrics, log)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/test", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 (untouched)", rec.Code)
	}
	if rec.Body.String() != "hi" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "hi")
	}
	if got := testutilCounterTotal(t, metrics.HTTPPanics); got != 0 {
		t.Fatalf("HTTPPanics total = %v, want 0 for a non-panicking handler", got)
	}
}

func TestWithRateLimit_RejectsOnceBurstExhausted(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := withRateLimit(ok, app.WebhookRateLimitConfig{RequestsPerSecond: 1, Burst: 2})

	var codes []int
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/telegram/webhook", nil))
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("first 2 requests (within burst) = %v, want [200 200 ...]", codes)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("3rd request (burst exhausted) = %d, want 429", codes[2])
	}
}

// TestWithRateLimit_ZeroBurstDoesNotBlackholeEveryRequest guards against a
// real footgun: rate.NewLimiter(rps, 0) rejects every single call forever
// (the bucket never holds a token), so a WEBHOOK_RATE_LIMIT_BURST=0
// misconfiguration would otherwise silently take the whole webhook down
// with no startup error. withRateLimit must clamp burst to at least 1.
func TestWithRateLimit_ZeroBurstDoesNotBlackholeEveryRequest(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := withRateLimit(ok, app.WebhookRateLimitConfig{RequestsPerSecond: 20, Burst: 0})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/telegram/webhook", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (burst=0 must be clamped to 1, not block everything)", rec.Code)
	}
}

func TestWithRateLimit_DisabledWhenRequestsPerSecondIsZero(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := withRateLimit(ok, app.WebhookRateLimitConfig{})

	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/telegram/webhook", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 (limiter disabled)", i, rec.Code)
		}
	}
}

// testutilCounterTotal sums a CounterVec's values across all label
// combinations, without pulling in the promtest helper package just for
// this.
func testutilCounterTotal(t *testing.T, vec *prometheus.CounterVec) float64 {
	t.Helper()
	metricCh := make(chan prometheus.Metric, 16)
	vec.Collect(metricCh)
	close(metricCh)
	var total float64
	for m := range metricCh {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatal(err)
		}
		total += d.GetCounter().GetValue()
	}
	return total
}
