package pandascore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cs2predictor/internal/domain/competition"
)

// TestMatches_BatchesAndMergesAcrossConcurrentRequests exercises the
// bounded-concurrency batching in Provider.Matches: with 5 events and an
// EventBatchSize of 2, that's 3 independent /csgo/matches?filter[serie_id]=
// requests, each returning one match for its own serie id — every match
// must come back in the merged result regardless of which goroutine fetched
// it or the order batches complete in.
func TestMatches_BatchesAndMergesAcrossConcurrentRequests(t *testing.T) {
	var maxConcurrent, current int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&current, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if n <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
				break
			}
		}
		defer atomic.AddInt32(&current, -1)
		// Held open briefly so concurrent requests have a wide enough
		// window to measurably overlap regardless of goroutine scheduling
		// variance — without this, a fast/trivial handler can let requests
		// complete before the next one even starts under CPU contention
		// (e.g. a busy CI runner), making the concurrency assertion below
		// flaky even though Matches really did dispatch them in parallel.
		time.Sleep(20 * time.Millisecond)

		q := r.URL.Query().Get("filter[serie_id]")
		ids := strings.Split(q, ",")
		w.Header().Set("Content-Type", "application/json")
		var body strings.Builder
		body.WriteByte('[')
		for i, id := range ids {
			serieID, _ := strconv.ParseInt(id, 10, 64)
			if i > 0 {
				body.WriteByte(',')
			}
			fmt.Fprintf(&body, `{"id":%d,"status":"not_started","serie":{"id":%d,"name":"S"}}`, serieID*100, serieID)
		}
		body.WriteByte(']')
		_, _ = w.Write([]byte(body.String()))
	}))
	defer server.Close()

	config := DefaultConfig()
	config.BaseURL = server.URL
	config.Token = "t"
	config.EventBatchSize = 2
	config.MaxConcurrency = 3
	provider := NewProvider(config, server.Client())

	var events []competition.Event
	for i := int64(1); i <= 5; i++ {
		events = append(events, competition.Event{ExternalID: strconv.FormatInt(i, 10), Game: competition.GameCS2})
	}

	matches, err := provider.Matches(context.Background(), events)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 5 {
		t.Fatalf("expected 5 matches (one per event across 3 batches), got %d: %+v", len(matches), matches)
	}
	gotExternalIDs := map[string]bool{}
	for _, m := range matches {
		gotExternalIDs[m.ExternalID] = true
	}
	for i := int64(1); i <= 5; i++ {
		want := strconv.FormatInt(i*100, 10)
		if !gotExternalIDs[want] {
			t.Errorf("missing match with external id %s in result: %+v", want, matches)
		}
	}
	if atomic.LoadInt32(&maxConcurrent) < 2 {
		t.Errorf("max observed concurrent requests = %d, want at least 2 (batches should overlap)", maxConcurrent)
	}
}

// TestMatches_OneFailingBatchFailsTheWholeCall verifies a batch's HTTP error
// surfaces as the overall Matches error (errgroup semantics), rather than
// being silently dropped from the merged result.
func TestMatches_OneFailingBatchFailsTheWholeCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	config := DefaultConfig()
	config.BaseURL = server.URL
	config.Token = "t"
	config.EventBatchSize = 1
	config.MaxConcurrency = 4
	provider := NewProvider(config, server.Client())

	events := []competition.Event{
		{ExternalID: "1", Game: competition.GameCS2},
		{ExternalID: "2", Game: competition.GameCS2},
		{ExternalID: "3", Game: competition.GameCS2},
	}
	if _, err := provider.Matches(context.Background(), events); err == nil {
		t.Fatal("expected an error when one batch's request fails")
	}
}
