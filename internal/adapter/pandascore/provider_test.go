package pandascore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cs2predictor/internal/domain/competition"
)

// Enqueues one non-empty page for CS2's /upcoming and empty pages
// everywhere else (including all of Dota2's endpoints), asserts the single
// mapped event and the exact Authorization header sent.
func TestUpcomingEvents_ContractShape(t *testing.T) {
	var gotAuth string
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/csgo/") && strings.Contains(r.URL.Path, "/upcoming") {
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
	if events[0].Game != competition.GameCS2 {
		t.Fatalf("event game = %q, want CS2", events[0].Game)
	}
	if events[0].Tier != competition.TierS {
		t.Fatalf("event tier = %q, want S", events[0].Tier)
	}
	if gotAuth != "Bearer panda-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer panda-token")
	}
	if callCount != 6 { // (upcoming + running + past) x (CS2 + Dota2)
		t.Fatalf("expected 6 requests (upcoming/running/past for each of 2 games), got %d", callCount)
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

// TestUpcomingEvents_FetchesBothGamesAndTagsThemCorrectly covers the
// multi-game fan-out itself: CS2 hits /csgo/... with the cs-2 filter,
// Dota2 hits /dota2/... with no filter at all, and each returned event
// carries its own Game.
func TestUpcomingEvents_FetchesBothGamesAndTagsThemCorrectly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, "/upcoming") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/csgo/"):
			if !strings.Contains(r.URL.RawQuery, "filter[videogame_title]=cs-2") {
				t.Errorf("CS2 request missing videogame_title filter: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"id":1,"serie_id":1,"serie":{"id":1,"name":"CS2 Major"}}]`))
		case strings.Contains(r.URL.Path, "/dota2/"):
			if strings.Contains(r.URL.RawQuery, "videogame_title") {
				t.Errorf("Dota2 request must not carry a videogame_title filter: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"id":2,"serie_id":2,"serie":{"id":2,"name":"The International"}}]`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	config := DefaultConfig()
	config.BaseURL = server.URL
	config.Token = "t"
	provider := NewProvider(config, server.Client())

	events, err := provider.UpcomingEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want exactly 2 (one per game)", events)
	}
	byGame := map[competition.GameCode]competition.Event{}
	for _, e := range events {
		byGame[e.Game] = e
	}
	if byGame[competition.GameCS2].Name != "CS2 Major" {
		t.Fatalf("CS2 event = %+v, want name 'CS2 Major'", byGame[competition.GameCS2])
	}
	if byGame[competition.GameDota2].Name != "The International" {
		t.Fatalf("Dota2 event = %+v, want name 'The International'", byGame[competition.GameDota2])
	}
	// The two games' UUIDs must never collide even if PandaScore reused
	// the same numeric serie id across games.
	if byGame[competition.GameCS2].ID == byGame[competition.GameDota2].ID {
		t.Fatal("CS2 and Dota2 events must not share an ID")
	}
}
