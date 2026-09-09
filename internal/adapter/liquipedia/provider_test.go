package liquipedia

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func newTestProvider(server *httptest.Server, apiKey string) *Provider {
	config := Config{BaseURL: server.URL + "/", Wiki: "counterstrike", APIKey: apiKey}
	return NewProvider(config, server.Client())
}

func TestEnrichTournament_ParsesNameAndShortname(t *testing.T) {
	var gotQuery url.Values
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": []map[string]any{
				{"name": "CCT Europe Series #8 2026", "shortname": "CCT Europe Series", "tickername": "CCT EU S8"},
			},
			"warning": []string{},
		})
	}))
	defer server.Close()

	meta, err := newTestProvider(server, "test-key").EnrichTournament(t.Context(), "CCT Europe Series #8 2026")
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.FullName != "CCT Europe Series #8 2026" || meta.Series != "CCT Europe Series" {
		t.Fatalf("EnrichTournament = %+v, want FullName/Series set", meta)
	}
	if gotAuth != "Apikey test-key" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Apikey test-key")
	}
	if got := gotQuery.Get("wiki"); got != "counterstrike" {
		t.Fatalf("wiki param = %q, want counterstrike", got)
	}
	if got := gotQuery.Get("conditions"); got != "[[name::CCT Europe Series #8 2026]]" {
		t.Fatalf("conditions param = %q", got)
	}
}

func TestEnrichTournament_FallsBackToTickerNameWhenNoShortname(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": []map[string]any{{"name": "Some Major", "shortname": "", "tickername": "SM26"}},
		})
	}))
	defer server.Close()

	meta, err := newTestProvider(server, "test-key").EnrichTournament(t.Context(), "Some Major")
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.Series != "SM26" {
		t.Fatalf("EnrichTournament = %+v, want Series=SM26 (fallback to tickername)", meta)
	}
}

func TestEnrichTournament_NoResultReturnsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []map[string]any{}})
	}))
	defer server.Close()

	meta, err := newTestProvider(server, "test-key").EnrichTournament(t.Context(), "Unknown Tournament")
	if err != nil {
		t.Fatal(err)
	}
	if meta != nil {
		t.Fatalf("EnrichTournament = %+v, want nil", meta)
	}
}

func TestEnrichTournament_InvalidAPIKeyReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	_, err := newTestProvider(server, "bad-key").EnrichTournament(t.Context(), "Some Major")
	if err == nil {
		t.Fatal("expected an error on HTTP 403")
	}
}

func TestEnrichTournament_APIErrorFieldPropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid conditions syntax"})
	}))
	defer server.Close()

	_, err := newTestProvider(server, "test-key").EnrichTournament(t.Context(), "Some Major")
	if err == nil {
		t.Fatal("expected an error when the response carries a non-empty error field")
	}
}
