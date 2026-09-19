package pandascore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
)

// A long tournament accumulates hundreds of finished matches, and asking
// for a series without bounds re-reads every one of them on every run —
// pages, which is to say provider requests, spent on rows that have been
// final for a week.
func TestMatches_AsksOnlyForTheMatchesThatCanStillMatter(t *testing.T) {
	var queries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/matches") {
			queries = append(queries, r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	config := DefaultConfig()
	config.BaseURL, config.Token = server.URL, "panda-token"
	provider := NewProvider(config, server.Client())
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	provider.now = func() time.Time { return now }

	if _, err := provider.Matches(context.Background(), []competition.Event{
		{ExternalID: "12", Game: competition.GameCS2},
	}); err != nil {
		t.Fatal(err)
	}

	if len(queries) != 1 {
		t.Fatalf("expected one matches request, got %d", len(queries))
	}
	window := queries[0].Get("range[begin_at]")
	from, to, ok := strings.Cut(window, ",")
	if !ok {
		t.Fatalf("expected a begin_at range, got %q", window)
	}
	fromAt, err := time.Parse(time.RFC3339, from)
	if err != nil {
		t.Fatalf("range start %q: %v", from, err)
	}
	toAt, err := time.Parse(time.RFC3339, to)
	if err != nil {
		t.Fatalf("range end %q: %v", to, err)
	}
	// The past bound has to stay generous: a match played while the bot
	// was down still owes its voters a settlement.
	if !fromAt.Equal(now.Add(-config.MatchWindowPast)) || !toAt.Equal(now.Add(config.MatchWindowFuture)) {
		t.Fatalf("window = %s..%s, want it centred on now by the configured bounds", fromAt, toAt)
	}
	if got := queries[0].Get("filter[serie_id]"); got != "12" {
		t.Fatalf("the series filter must survive alongside the window, got %q", got)
	}
}

// Unset bounds keep the old fetch-everything behaviour rather than
// silently truncating what a deployment asked for.
func TestMatches_OmitsTheWindowWhenItIsNotConfigured(t *testing.T) {
	var queries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/matches") {
			queries = append(queries, r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	config := DefaultConfig()
	config.BaseURL, config.Token = server.URL, "panda-token"
	config.MatchWindowPast, config.MatchWindowFuture = 0, 0
	provider := NewProvider(config, server.Client())

	if _, err := provider.Matches(context.Background(), []competition.Event{
		{ExternalID: "12", Game: competition.GameCS2},
	}); err != nil {
		t.Fatal(err)
	}

	if len(queries) != 1 || queries[0].Get("range[begin_at]") != "" {
		t.Fatalf("expected no window, got %v", queries)
	}
}
