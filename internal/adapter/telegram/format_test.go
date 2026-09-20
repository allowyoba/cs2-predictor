package telegram

import (
	"testing"

	"cs2predictor/internal/domain/competition"
)

// A team's country is already in the catalog (PandaScore fills it for about
// nine teams in ten), and a flag is the only form of "team badge" a poll can
// carry: its question is plain text and its description is HTML without
// images, so an actual logo cannot go there at all.
func TestCountryFlag(t *testing.T) {
	cases := map[string]string{
		"RU": "🇷🇺",
		"BR": "🇧🇷",
		"DE": "🇩🇪",
		// Not two uppercase letters: no flag, and no placeholder either.
		"ru":  "",
		"R":   "",
		"RUS": "",
		"":    "",
		"R1":  "",
	}
	for code, want := range cases {
		if got := countryFlag(code); got != want {
			t.Errorf("countryFlag(%q) = %q, want %q", code, got, want)
		}
	}
}

// An unknown country must leave the name exactly as it was — no box, no
// stray leading space.
func TestTeamNameWithFlag(t *testing.T) {
	known := &competition.Team{Name: "Spirit", Location: "RU"}
	if got := teamNameWithFlag(known, "Spirit", false); got != "🇷🇺 Spirit" {
		t.Errorf("teamNameWithFlag(known) = %q", got)
	}
	unknown := &competition.Team{Name: "Spirit"}
	if got := teamNameWithFlag(unknown, "Spirit", false); got != "Spirit" {
		t.Errorf("teamNameWithFlag(unknown) = %q, want the bare name", got)
	}
	if got := teamNameWithFlag(nil, "TBD", false); got != "TBD" {
		t.Errorf("teamNameWithFlag(nil) = %q, want the bare name", got)
	}
}

// The two sources are a preference, not a replacement: both are stored, and
// a chat that asked for HLTV's answer still gets the provider's for the
// teams HLTV does not rank.
func TestTeamNameWithFlag_ChoosesTheChatsSource(t *testing.T) {
	both := &competition.Team{Name: "Spirit", Location: "DE", HLTVLocation: "RU"}
	if got := teamNameWithFlag(both, "Spirit", false); got != "🇩🇪 Spirit" {
		t.Errorf("with the provider chosen = %q, want its own country", got)
	}
	if got := teamNameWithFlag(both, "Spirit", true); got != "🇷🇺 Spirit" {
		t.Errorf("with HLTV chosen = %q, want HLTV's country", got)
	}

	// HLTV ranks thirty teams; everyone else falls back rather than losing
	// the flag they had.
	providerOnly := &competition.Team{Name: "Some Qualifier Team", Location: "BR"}
	if got := teamNameWithFlag(providerOnly, "T", true); got != "🇧🇷 T" {
		t.Errorf("unranked team = %q, want the provider's flag as a fallback", got)
	}
}
