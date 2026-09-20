package enrichment_test

import (
	"testing"

	"cs2predictor/internal/domain/enrichment"
)

// Ranking feeds name countries in words; flags are built from codes. An
// unmapped name must come back empty rather than as a guess — a wrong flag
// beside a team name is worse than no flag, because nobody checks it.
func TestCountryCode(t *testing.T) {
	cases := map[string]string{
		"Russia":         "RU",
		"denmark":        "DK",
		"United States":  "US",
		"Czech Republic": "CZ",
		"Türkiye":        "TR",
		" Poland ":       "PL",
		// Already a code: some feeds publish one, and re-mapping it would
		// only be a chance to get it wrong.
		"BR": "BR",
		"br": "BR",
		// A region is not a country and has no flag. Known, and
		// deliberately blank.
		"Europe": "",
		// Nothing recognises this, so nothing is claimed about it.
		"Westeros": "",
		"":         "",
	}
	for name, want := range cases {
		if got := enrichment.CountryCode(name); got != want {
			t.Errorf("CountryCode(%q) = %q, want %q", name, got, want)
		}
	}
}
