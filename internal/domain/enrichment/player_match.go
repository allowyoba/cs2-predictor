package enrichment

import (
	"cs2predictor/internal/platform/common"
)

// Matching one team's roster across the two feeds that report it. PandaScore
// gives players an id and a nickname; HLTV gives a nickname and nothing else,
// which is why the nickname has to carry the match.
//
// Doing it per team is what makes that safe. Nicknames collide across the
// scene — there is more than one "Twista", and a global nickname match would
// eventually pick the wrong one with no way to notice — but two people on the
// same team do not share a nickname, so inside one roster a nickname is a key.
// This is the one advantage the player pipeline has over the team pipeline
// (see team_match.go), and it is why players need no operator review queue:
// within a roster the answer is either unambiguous or absent.

// PlayerNicknameAutoAcceptThreshold is how similar two nicknames must be,
// inside the same roster, to be accepted without a human. Set high because
// the only differences worth absorbing here are punctuation and casing
// ("s1mple" / "S1mple", "Boombl4" / "boombl4"); two genuinely different
// people on one roster never come close.
const PlayerNicknameAutoAcceptThreshold = 88

// RosterCandidate is one player already on record for a team, as the local
// side of the comparison.
type RosterCandidate struct {
	PlayerID common.PlayerID
	Nickname string
}

// PlayerMatch is one resolved pairing: the local player, the nickname the
// other feed calls them, and which rule fired.
type PlayerMatch struct {
	PlayerID     common.PlayerID
	ExternalName string
	Confidence   MatchConfidence
}

// NormalizeNickname lowercases and trims a nickname for comparison. Unlike
// NormalizeTeamName it does not collapse internal whitespace differently or
// strip organisational words — a nickname is one token and every character in
// it is meaningful ("dev1ce" is not "device").
func NormalizeNickname(nickname string) string {
	return normalizePlayerName(nickname)
}

// FuzzyNicknameScore is FuzzyNameScore's counterpart for nicknames: the same
// edit-distance ratio, without the organisational-word stripping that only
// makes sense for team names.
func FuzzyNicknameScore(a, b string) int {
	na, nb := NormalizeNickname(a), NormalizeNickname(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 100
	}
	dist := levenshtein(na, nb)
	maxLen := max(len(na), len(nb))
	if maxLen == 0 {
		return 0
	}
	if score := 100 - (dist*100)/maxLen; score > 0 {
		return score
	}
	return 0
}

// MatchRoster pairs one team's known players against the nicknames another
// feed reports for that same team. Exact normalized matches are taken first,
// across the whole roster, before any fuzzy one is considered — otherwise a
// close-but-wrong pair could claim a player whose exact match was still to
// come. Each side is used at most once.
//
// Whatever is left over is returned as unmatched rather than forced onto the
// nearest candidate: an unmatched nickname is usually a stand-in or a
// transfer one feed has and the other has not caught up with, and inventing a
// mapping for it would attach somebody's subscription to the wrong person.
func MatchRoster(candidates []RosterCandidate, externalNicknames []string) (matched []PlayerMatch, unmatched []string) {
	takenCandidate := make([]bool, len(candidates))
	takenExternal := make([]bool, len(externalNicknames))

	for i, external := range externalNicknames {
		norm := NormalizeNickname(external)
		if norm == "" {
			takenExternal[i] = true // nothing to match, and not worth reporting
			continue
		}
		for j, candidate := range candidates {
			if takenCandidate[j] || NormalizeNickname(candidate.Nickname) != norm {
				continue
			}
			takenCandidate[j], takenExternal[i] = true, true
			matched = append(matched, PlayerMatch{
				PlayerID: candidate.PlayerID, ExternalName: external, Confidence: ConfidenceExactName,
			})
			break
		}
	}

	for i, external := range externalNicknames {
		if takenExternal[i] {
			continue
		}
		bestScore, best := 0, -1
		for j, candidate := range candidates {
			if takenCandidate[j] {
				continue
			}
			if score := FuzzyNicknameScore(external, candidate.Nickname); score > bestScore {
				bestScore, best = score, j
			}
		}
		if best < 0 || bestScore < PlayerNicknameAutoAcceptThreshold {
			unmatched = append(unmatched, external)
			continue
		}
		takenCandidate[best], takenExternal[i] = true, true
		matched = append(matched, PlayerMatch{
			PlayerID: candidates[best].PlayerID, ExternalName: external, Confidence: ConfidenceFuzzyName,
		})
	}
	return matched, unmatched
}
