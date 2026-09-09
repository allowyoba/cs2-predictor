//go:build live

package grid

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"cs2predictor/internal/domain/enrichment"
)

// TestZZZLiveCheck_RealGRID hits the real GRID Open Access API — unlike
// valvevrs's live check, this one is NOT expected to just work: the
// allSeries/seriesState query shape here follows a publicly seen working
// example, but the exact filter arguments for "series involving team X"
// were never confirmed against the live schema (see package doc). Run this
// once you have a real GRID_API_KEY to find out whether it needs
// adjustment — skipped entirely otherwise, including in normal `go test
// ./...` (this file is excluded by the "live" build tag).
func TestZZZLiveCheck_RealGRID(t *testing.T) {
	apiKey := os.Getenv("GRID_API_KEY")
	if apiKey == "" {
		t.Skip("GRID_API_KEY not set — skipping live GRID check")
	}
	p := NewProvider(DefaultConfig(apiKey), http.DefaultClient)

	form, err := p.GetTeamStats(t.Context(), enrichment.TeamIdentity{Name: "Team Spirit"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("Team Spirit recent form: %+v\n", form)

	h2h, err := p.GetHeadToHead(t.Context(), enrichment.TeamIdentity{Name: "Team Spirit"}, enrichment.TeamIdentity{Name: "Natus Vincere"})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("Spirit vs NAVI head-to-head: %+v\n", h2h)
}
