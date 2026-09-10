package enrichment

import (
	"strings"

	"cs2predictor/internal/platform/common"
)

// TeamCandidate is one local catalog team, with everything MatchTeam needs
// to compare it against an externally-reported identity: its own name,
// every alias on record for it, and the most recently observed roster (may
// be empty if none is known yet).
type TeamCandidate struct {
	TeamID  common.TeamID
	Name    string
	Aliases []string
	Roster  []string
}

// minRosterOverlap is how many players must match before a roster-based
// identification is trusted — low enough to survive one or two roster
// changes, high enough that two unrelated teams sharing a stand-in can't
// collide.
const minRosterOverlap = 3

// NormalizeTeamName lowercases and collapses whitespace so "Team Spirit" /
// "team   spirit" / "TEAM SPIRIT" compare equal. It deliberately does NOT
// try to fuzzy-match distinct spellings like "NAVI" vs "Natus Vincere" —
// that's what the alias table is for; silently treating those as "the same
// normalized string" would be exactly the kind of guess this package must
// never make.
func NormalizeTeamName(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

func normalizePlayerName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// MatchTeam resolves an externally-reported team identity against the local
// catalog, trying each step in priority order and returning the first
// confident result. It does NOT check external-ID mappings — the caller is
// expected to have already tried IdentityRepository.FindTeamByExternalID
// (the cheapest, most confident step) before ever calling MatchTeam, which
// only covers the name/alias/roster steps for a team with no existing
// mapping yet.
//
// Returns ok=false when nothing clears the bar — callers MUST skip
// enrichment entirely in that case rather than guess: showing a chat the
// wrong team's ranking is worse than showing none.
//
//nolint:gocyclo // pre-existing complexity, predates gocyclo being enabled; tracked for a future dedicated refactor rather than fixed as a side effect of adding this linter
func MatchTeam(candidates []TeamCandidate, external TeamIdentity) (teamID common.TeamID, confidence MatchConfidence, ok bool) {
	normExternal := NormalizeTeamName(external.Name)
	if normExternal == "" {
		return common.TeamID{}, "", false
	}

	for _, c := range candidates {
		if NormalizeTeamName(c.Name) == normExternal {
			return c.TeamID, ConfidenceExactName, true
		}
	}

	for _, c := range candidates {
		for _, alias := range c.Aliases {
			if NormalizeTeamName(alias) == normExternal {
				return c.TeamID, ConfidenceAlias, true
			}
		}
	}

	if len(external.Roster) == 0 {
		return common.TeamID{}, "", false
	}
	externalPlayers := make(map[string]bool, len(external.Roster))
	for _, p := range external.Roster {
		externalPlayers[normalizePlayerName(p)] = true
	}

	var best common.TeamID
	bestOverlap := 0
	ambiguous := false
	for _, c := range candidates {
		overlap := 0
		for _, p := range c.Roster {
			if externalPlayers[normalizePlayerName(p)] {
				overlap++
			}
		}
		if overlap < minRosterOverlap {
			continue
		}
		switch {
		case overlap > bestOverlap:
			best, bestOverlap, ambiguous = c.TeamID, overlap, false
		case overlap == bestOverlap:
			ambiguous = true
		}
	}
	if bestOverlap >= minRosterOverlap && !ambiguous {
		return best, ConfidenceRoster, true
	}
	return common.TeamID{}, "", false
}
