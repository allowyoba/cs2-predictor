package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// The Mini App's page ships inside the binary. Shipping it as files next
// to the reverse proxy failed twice for the same reason — a release that
// did not carry the directory deployed green and served 404s — so this
// pins the property that fixes it: if the bot is running, the page is
// there.
func TestMiniappPage_IsServedByTheBinaryItself(t *testing.T) {
	router := NewRouter(RouterDeps{Registry: prometheus.NewRegistry(), Webhook: noopHandler()})

	for _, path := range []string{"/app/", "/app/app.js", "/app/styles.css"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 — the page travels with the image", path, recorder.Code)
		}
		if recorder.Body.Len() == 0 {
			t.Fatalf("%s: served an empty body", path)
		}
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "<!doctype html>") {
		t.Fatalf("expected the Mini App's own page, got %q", body[:min(120, len(body))])
	}
	// Short-lived caching: Telegram reloads the app on every open, and
	// markup that outlives the API it calls is the confusing pair.
	if cache := recorder.Header().Get("Cache-Control"); !strings.Contains(cache, "max-age=60") {
		t.Fatalf("Cache-Control = %q, want a short max-age", cache)
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected nosniff on a page that serves somebody else's WebView")
	}
}

// A path outside the app is not the app's to answer.
func TestMiniappPage_DoesNotEscapeItsPrefix(t *testing.T) {
	router := NewRouter(RouterDeps{Registry: prometheus.NewRegistry(), Webhook: noopHandler()})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/../version", nil))
	if recorder.Code == http.StatusOK && strings.Contains(recorder.Body.String(), "buildTime") {
		t.Fatal("the file server must not serve its way out of /app/")
	}
}

// noopHandler stands in for the webhook the router always registers.
func noopHandler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

// The page ships inside the binary, which makes it easy to forget it is
// still a client: these two properties are what keep it honest.
func TestMiniappPage_CarriesNoFabricatedDataOrForeignAssets(t *testing.T) {
	router := NewRouter(RouterDeps{Registry: prometheus.NewRegistry(), Webhook: noopHandler()})

	read := func(path string) string {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, recorder.Code)
		}
		return recorder.Body.String()
	}

	script, markup := read("/app/app.js"), read("/app/")

	// Demo figures were how the prototype started; a screen that keeps
	// them shows somebody else's numbers as if they were theirs.
	for _, forbidden := range []string{"GAME_DATA", "const DEMO"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("the app still carries its demo dataset (%s): every figure must come from the API", forbidden)
		}
	}
	// Crests come from this bot's own API, so the page needs no third-party
	// host at load time — the one exception being Telegram's own SDK.
	for _, host := range []string{"img-cdn.hltv.org", "cdn-api.pandascore.co"} {
		if strings.Contains(markup, host) {
			t.Fatalf("markup hard-codes %s; crests are served through the API instead", host)
		}
	}
}
