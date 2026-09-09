package pandascore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cs2predictor/internal/domain/competition"
)

// Enqueues one non-empty page for /upcoming and empty pages for /running
// and /past, asserts the single mapped event and the exact Authorization
// header sent.
func TestUpcomingEvents_ContractShape(t *testing.T) {
	var gotAuth string
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/upcoming") {
			_, _ = w.Write([]byte(`[{"id":1201,"name":"Playoffs","begin_at":"2027-01-01T10:00:00Z","end_at":"2027-01-10T10:00:00Z","tier":"s","serie_id":12,"serie":{"id":12,"name":"Cologne","full_name":"Cologne","year":2027,"begin_at":"2027-01-01T10:00:00Z","end_at":"2027-01-10T10:00:00Z"},"league":{"id":77,"name":"IEM"}}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	config := DefaultConfig()
	config.BaseURL = server.URL
	config.Token = "panda-token"
	provider := NewProvider(config, server.Client())

	events, err := provider.UpcomingEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Name != "IEM 2027 Cologne" {
		t.Fatalf("events = %+v, want single 'IEM 2027 Cologne'", events)
	}
	if events[0].Tier != competition.TierS {
		t.Fatalf("event tier = %q, want S", events[0].Tier)
	}
	if gotAuth != "Bearer panda-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer panda-token")
	}
	if callCount != 3 { // upcoming + running + past(maxPages=1)
		t.Fatalf("expected 3 requests (upcoming/running/past), got %d", callCount)
	}
}

func TestUpcomingEvents_RequiresToken(t *testing.T) {
	config := DefaultConfig()
	config.Token = ""
	provider := NewProvider(config, http.DefaultClient)
	if _, err := provider.UpcomingEvents(context.Background()); err == nil {
		t.Fatal("expected error when token is not configured")
	}
}

// Pagination stops once a short page is returned and X-Total is respected
// when present.
func TestFetchPages_StopsOnShortPage(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		if pages == 1 {
			_, _ = w.Write([]byte(`[{"id":1,"name":"A"},{"id":2,"name":"B"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	config := DefaultConfig()
	config.BaseURL = server.URL
	config.Token = "t"
	config.PageSize = 2
	provider := NewProvider(config, server.Client())

	dtos, err := fetchPages[seriesDTO](context.Background(), provider, "/csgo/series/upcoming", -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(dtos) != 2 {
		t.Fatalf("expected 2 items from a single full page then stop, got %d (pages=%d)", len(dtos), pages)
	}
	if pages != 2 { // one full page (loop continues since batch==pageSize), then one short/empty page ends it
		t.Fatalf("expected 2 requests, got %d", pages)
	}
}

// A non-2xx response (e.g. 401 with a JSON error object, not an array) must
// surface a clear HTTP-status error instead of a confusing JSON-decode
// error from trying to unmarshal an object into a []T.
func TestFetchPages_NonOKStatusReturnsClearError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer server.Close()

	config := DefaultConfig()
	config.BaseURL = server.URL
	config.Token = "bad-token"
	provider := NewProvider(config, server.Client())

	_, err := fetchPages[seriesDTO](context.Background(), provider, "/csgo/series/upcoming", -1)
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %q, want it to mention the HTTP status", err.Error())
	}
	if strings.Contains(err.Error(), "cannot unmarshal") {
		t.Fatalf("error = %q, should not be a raw JSON-decode error", err.Error())
	}
}
