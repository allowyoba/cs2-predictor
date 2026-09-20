package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cs2predictor/internal/domain/enrichment"
	"cs2predictor/internal/platform/common"
)

type fakeLogoCache struct {
	pending  []enrichment.TeamLogoNeed
	stale    []enrichment.TeamLogoNeed
	saved    []enrichment.TeamLogo
	touched  []common.TeamID
	askedFor time.Duration
}

func (f *fakeLogoCache) PendingLogos(context.Context, int) ([]enrichment.TeamLogoNeed, error) {
	return f.pending, nil
}

func (f *fakeLogoCache) StaleLogos(_ context.Context, _ time.Time, within time.Duration, _ int) ([]enrichment.TeamLogoNeed, error) {
	f.askedFor = within
	return f.stale, nil
}

func (f *fakeLogoCache) SaveLogo(_ context.Context, logo enrichment.TeamLogo) error {
	f.saved = append(f.saved, logo)
	return nil
}

func (f *fakeLogoCache) TouchLogo(_ context.Context, teamID common.TeamID, _ enrichment.Source, _ time.Time) error {
	f.touched = append(f.touched, teamID)
	return nil
}

func (f *fakeLogoCache) FindLogo(context.Context, common.TeamID, enrichment.Source) (*enrichment.TeamLogo, error) {
	return nil, nil
}

func (f *fakeLogoCache) LogoDigests(context.Context) (map[common.TeamID]map[enrichment.Source]string, error) {
	return nil, nil
}

func (f *fakeLogoCache) LogoChips(context.Context) (map[common.TeamID]map[enrichment.Source]bool, error) {
	return nil, nil
}

func newMirror(cache *fakeLogoCache) *LogoMirror {
	return &LogoMirror{
		Cache: cache, Client: http.DefaultClient, Clock: common.SystemUTCClock(),
		Lock: fakeClusterLock{}, Log: slog.Default(), Pause: time.Millisecond,
	}
}

// The crest is copied here once, and everything about the request says
// "this is a small, identified, occasional client" rather than a scraper.
func TestLogoMirror_FetchesOnceAndIdentifiesItself(t *testing.T) {
	var requests int
	var agent, accept string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		agent, accept = r.Header.Get("User-Agent"), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nfake"))
	}))
	defer server.Close()

	cache := &fakeLogoCache{pending: []enrichment.TeamLogoNeed{
		{TeamID: common.NewTeamID(), Source: enrichment.SourceHLTV, SourceURL: server.URL + "/crest.png"},
	}}
	mirror := newMirror(cache)
	mirror.Client = server.Client()

	mirror.Dispatch(context.Background())

	if requests != 1 {
		t.Fatalf("made %d requests for one crest, want exactly 1", requests)
	}
	if agent != LogoMirrorUserAgent {
		t.Fatalf("User-Agent = %q, want the bot to say who it is", agent)
	}
	if accept == "" {
		t.Fatal("the request must say it wants an image")
	}
	if len(cache.saved) != 1 || cache.saved[0].ETag != `"v1"` {
		t.Fatalf("saved = %+v, want the bytes and the validator kept for next time", cache.saved)
	}
	if cache.saved[0].Digest == "" {
		t.Fatal("a crest without a digest cannot be served immutable")
	}
}

// Re-checking is conditional: the far end answers "not modified" and sends
// no image. This is the difference between asking politely once a month
// and re-downloading the whole catalogue on a timer.
func TestLogoMirror_RevalidatesWithoutDownloadingAgain(t *testing.T) {
	var sentBody int
	var ifNoneMatch string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ifNoneMatch = r.Header.Get("If-None-Match")
		if ifNoneMatch == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		sentBody++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("fake"))
	}))
	defer server.Close()

	teamID := common.NewTeamID()
	cache := &fakeLogoCache{stale: []enrichment.TeamLogoNeed{
		{TeamID: teamID, Source: enrichment.SourceHLTV, SourceURL: server.URL + "/crest.png", ETag: `"v1"`},
	}}
	mirror := newMirror(cache)
	mirror.Client = server.Client()

	mirror.Dispatch(context.Background())

	if ifNoneMatch != `"v1"` {
		t.Fatalf("If-None-Match = %q, want the stored validator sent", ifNoneMatch)
	}
	if sentBody != 0 {
		t.Fatal("an unchanged crest was downloaded again")
	}
	if len(cache.saved) != 0 {
		t.Fatalf("nothing changed, so nothing should have been written: %+v", cache.saved)
	}
	if len(cache.touched) != 1 || cache.touched[0] != teamID {
		t.Fatalf("touched = %v, want the check recorded so it is not repeated tomorrow", cache.touched)
	}
	// Only teams with a match close at hand are re-checked at all.
	if cache.askedFor != LogoMirrorMatchWindow {
		t.Fatalf("asked for a %v window, want %v", cache.askedFor, LogoMirrorMatchWindow)
	}
}

// Somebody else's bytes are about to be served from our own origin, so
// anything that is not plainly a small image is refused rather than stored.
func TestLogoMirror_RefusesAnythingThatIsNotASmallImage(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"html pretending to be a crest": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<script>alert(1)</script>"))
		},
		"far too large": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(make([]byte, LogoMirrorMaxBytes+10))
		},
		"empty": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
		},
		"an error page": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			cache := &fakeLogoCache{pending: []enrichment.TeamLogoNeed{
				{TeamID: common.NewTeamID(), Source: enrichment.SourceHLTV, SourceURL: server.URL + "/x.png"},
			}}
			mirror := newMirror(cache)
			mirror.Client = server.Client()

			mirror.Dispatch(context.Background())

			if len(cache.saved) != 0 {
				t.Fatalf("stored %+v, want it refused", cache.saved)
			}
		})
	}
}

// Only https, and only a URL a provider published for that team.
func TestLogoMirror_RefusesPlainHTTP(t *testing.T) {
	cache := &fakeLogoCache{pending: []enrichment.TeamLogoNeed{
		{TeamID: common.NewTeamID(), Source: enrichment.SourceHLTV, SourceURL: "http://example.invalid/x.png"},
	}}
	mirror := newMirror(cache)

	mirror.Dispatch(context.Background())

	if len(cache.saved) != 0 {
		t.Fatalf("stored %+v over plain http", cache.saved)
	}
}
