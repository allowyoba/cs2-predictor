package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/competition"
	"cs2predictor/internal/platform/common"
)

type fakePinger struct{ err error }

// healthyProvider is the smallest competition.DataProvider that lets the
// gateway report UP — this package tests the HTTP surface, not routing.
type healthyProvider struct{ name string }

func (p *healthyProvider) ProviderName() string { return p.name }
func (p *healthyProvider) UpcomingEvents(context.Context) ([]competition.Event, error) {
	return nil, nil
}
func (p *healthyProvider) Matches(context.Context, []competition.Event) ([]competition.Match, error) {
	return nil, nil
}

func (f fakePinger) Ping(context.Context) error { return f.err }

// newTestGateway returns a gateway with a single healthy provider that has
// already served one successful call (so Health() reports UP).
func newTestGateway(t *testing.T) *app.CompetitionProviderGateway {
	t.Helper()
	provider := &healthyProvider{name: "PRIMARY"}
	gw, err := app.NewCompetitionProviderGateway([]competition.DataProvider{provider},
		app.ProviderRoutingConfig{Order: []string{"PRIMARY"}, HealthStartupGrace: 0, HealthMaxStaleness: time.Hour}, nil, common.SystemUTCClock())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = gw.UpcomingEvents(context.Background()) // populate lastSuccess for Health()
	return gw
}

func TestLivenessHandler_AlwaysUp(t *testing.T) {
	rec := httptest.NewRecorder()
	livenessHandler()(rec, httptest.NewRequest("GET", "/healthz/live", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "UP" {
		t.Fatalf("status field = %q, want UP", body["status"])
	}
}

func TestReadinessHandler_DownWhenDatabaseUnreachable(t *testing.T) {
	gw := newTestGateway(t)
	rec := httptest.NewRecorder()
	readinessHandler(gw, fakePinger{err: errors.New("connection refused")}, nil, nil)(rec, httptest.NewRequest("GET", "/healthz/ready", nil))

	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "DOWN" {
		t.Fatalf("body status = %v, want DOWN", body["status"])
	}
}

func TestReadinessHandler_UpWhenDatabaseAndProvidersHealthy(t *testing.T) {
	gw := newTestGateway(t)
	rec := httptest.NewRecorder()
	readinessHandler(gw, fakePinger{}, nil, nil)(rec, httptest.NewRequest("GET", "/healthz/ready", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestReadinessHandler_SkipsDBCheckWhenPoolIsNil(t *testing.T) {
	gw := newTestGateway(t)
	rec := httptest.NewRecorder()
	readinessHandler(gw, nil, nil, nil)(rec, httptest.NewRequest("GET", "/healthz/ready", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (no DB check should still allow UP)", rec.Code)
	}
}

func TestVersionHandler_ReportsBuildMetadata(t *testing.T) {
	rec := httptest.NewRecorder()
	versionHandler("1.2.3", "abc123", "2026-01-01T00:00:00Z")(rec, httptest.NewRequest("GET", "/version", nil))

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "1.2.3" || body["commit"] != "abc123" {
		t.Fatalf("body = %+v", body)
	}
}

func TestNewRouter_RegistersAllExpectedRoutes(t *testing.T) {
	gw := newTestGateway(t)
	mux := NewRouter(RouterDeps{
		Webhook:  http.NotFoundHandler(), // placeholder; only route registration is checked below
		Gateway:  gw,
		Registry: prometheus.NewRegistry(),
		Pool:     nil,
		Version:  "dev",
	})

	for _, path := range []string{"/healthz/live", "/healthz/ready", "/metrics", "/version"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code == 404 {
			t.Errorf("route %s not registered (404)", path)
		}
	}
}
