//go:build live

package liquipedia

import (
	"fmt"
	"net/http"
	"os"
	"testing"
)

// TestZZZLiveCheck_RealLiquipedia hits the real LPDB v3 API. The transport
// (URL, auth header, envelope) is confirmed against real client source
// code, but exact-name lookups against a real PandaScore event name may
// simply miss (Liquipedia's own tournament page naming rarely matches
// PandaScore's verbatim) — this is expected and not itself a bug; run this
// once you have a real LIQUIPEDIA_API_KEY to sanity-check the transport
// still works and see how often a real event name actually resolves.
// Skipped entirely otherwise, including in normal `go test ./...` (this
// file is excluded by the "live" build tag).
func TestZZZLiveCheck_RealLiquipedia(t *testing.T) {
	apiKey := os.Getenv("LIQUIPEDIA_API_KEY")
	if apiKey == "" {
		t.Skip("LIQUIPEDIA_API_KEY not set — skipping live Liquipedia check")
	}
	p := NewProvider(DefaultConfig(apiKey), http.DefaultClient)

	meta, err := p.EnrichTournament(t.Context(), "IEM Katowice 2026")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("IEM Katowice 2026: %+v\n", meta)
}
