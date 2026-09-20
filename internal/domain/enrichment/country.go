package enrichment

import "strings"

// Ranking feeds name a team's country in words. Flags are built from ISO
// 3166-1 alpha-2 codes, so something has to bridge the two — and it has to
// be a bridge that fails quietly: an unmapped country means no flag, which
// is what the screens already do for a team whose country nobody published.
//
// Only countries that actually appear in esports rosters are listed. The
// list is short on purpose: every entry is one somebody has seen in a
// ranking, not a transcription of ISO 3166 that would have to be trusted
// without ever being exercised.
var countryCodes = map[string]string{
	"argentina": "AR", "australia": "AU", "austria": "AT", "belgium": "BE",
	"bosnia and herzegovina": "BA", "brazil": "BR", "bulgaria": "BG", "canada": "CA",
	"chile": "CL", "china": "CN", "croatia": "HR", "czech republic": "CZ", "czechia": "CZ",
	"denmark": "DK", "estonia": "EE", "finland": "FI", "france": "FR", "georgia": "GE",
	"germany": "DE", "greece": "GR", "hungary": "HU", "india": "IN", "indonesia": "ID",
	"israel": "IL", "italy": "IT", "japan": "JP", "kazakhstan": "KZ", "kosovo": "XK",
	"latvia": "LV", "lithuania": "LT", "malaysia": "MY", "mexico": "MX", "moldova": "MD",
	"mongolia": "MN", "montenegro": "ME", "netherlands": "NL", "new zealand": "NZ",
	"north macedonia": "MK", "norway": "NO", "peru": "PE", "philippines": "PH",
	"poland": "PL", "portugal": "PT", "romania": "RO", "russia": "RU",
	"saudi arabia": "SA", "serbia": "RS", "singapore": "SG", "slovakia": "SK",
	"slovenia": "SI", "south africa": "ZA", "south korea": "KR", "korea": "KR",
	"spain": "ES", "sweden": "SE", "switzerland": "CH", "thailand": "TH",
	"turkey": "TR", "türkiye": "TR", "ukraine": "UA",
	"united arab emirates": "AE", "united kingdom": "GB", "great britain": "GB",
	"united states": "US", "united states of america": "US", "usa": "US",
	"uruguay": "UY", "uzbekistan": "UZ", "vietnam": "VN",
	// Rosters drawn from several countries are ranked under a region, and a
	// region has no flag. Mapped explicitly so they read as "known, and
	// deliberately without a flag" rather than as a gap in the list.
	"europe": "", "cis": "", "international": "", "world": "",
}

// CountryCode resolves a feed's country name to its ISO 3166-1 alpha-2
// code, or "" when the name is one nothing here knows. A two-letter input
// is passed through: some feeds publish the code already.
func CountryCode(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) == 2 {
		return strings.ToUpper(trimmed)
	}
	return countryCodes[strings.ToLower(trimmed)]
}
