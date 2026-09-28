package competition

import "strings"

// Finding a team or a player by what somebody actually types.
//
// The catalogue spells things one way and people type them another: the
// roster says "r0pz" and the query says "ропз"; the catalogue says "Team
// Spirit" and the query says "спирит". A plain substring match over the
// stored name finds neither, which is why searching for a team or a player
// mostly returned nothing.
//
// So both sides are folded to the same shape before they are compared. The
// fold is deliberately lossy — it throws away case, punctuation, alphabet,
// the digits people substitute for letters, and the handful of consonants
// Russian and English spell differently — because every one of those is a
// difference between how a name is written and how it is said, and a search
// box is asking about the latter.
//
// This never decides that two things ARE the same, only that one is worth
// offering as an answer to the other; the person picks from the list. That
// is what makes an aggressive fold safe here and unsafe in identity
// resolution (see enrichment.MatchTeam, which folds far less for exactly
// that reason).

// cyrillicToLatin maps each Cyrillic letter to the Latin letters a Russian
// speaker reaches for when typing the same sound. Multi-letter values are
// intentional: "щ" is "sch", and "ю" is "yu".
var cyrillicToLatin = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "j", 'з': "z", 'и': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// leetToLetter folds the digit-for-letter substitutions esports names are
// full of — "r0pz", "s1mple", "B1T", "m0NESY" — back to the letters they
// stand in for. Without this, typing a nickname the way it is pronounced
// never finds the way it is written.
var leetToLetter = map[rune]rune{
	'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't', '8': 'b',
	'@': 'a', '$': 's', '!': 'i', '|': 'i',
}

// orgWords are the words an organisation's name carries and nobody says out
// loud. Dropped only when something else is left: "Team" alone is a real
// answer to "team", and "Gaming" is somebody's whole name somewhere.
var orgWords = map[string]bool{
	"team": true, "esports": true, "esport": true, "e-sports": true,
	"gaming": true, "club": true, "org": true, "academy": false,
}

// FoldForSearch reduces a name to the shape a query is compared against:
// lower case, Cyrillic transliterated, digits-for-letters undone, everything
// that is not a letter or digit dropped, and the consonants that differ only
// between spellings collapsed.
//
// "r0pz", "ropz", "rops", "ропз" and "ропс" all fold to "rops". "Team
// Spirit" and "спирит" both contain "spirit".
func FoldForSearch(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if latin, ok := cyrillicToLatin[r]; ok {
			b.WriteString(latin)
			continue
		}
		if letter, ok := leetToLetter[r]; ok {
			b.WriteRune(letter)
			continue
		}
		if isSearchRune(r) {
			b.WriteRune(r)
		}
	}
	return collapseSounds(b.String())
}

func isSearchRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

// collapseSounds folds the spelling differences that survive
// transliteration. Each one is a pair people genuinely swap when typing a
// name they have only ever heard:
//
//   - z/s — "ропз" and "ропс" are the same guess at "r0pz"
//   - k/c — "niko"/"nico", "kaspa"/"caspa"
//   - ph/f — "phantom"/"fantom"
//   - doubled letters — "falllen"/"fallen"
//   - a trailing "y" said as "i" — "monesy"/"monesi"
func collapseSounds(s string) string {
	s = strings.ReplaceAll(s, "ph", "f")
	var b strings.Builder
	b.Grow(len(s))
	var previous rune
	for _, r := range s {
		switch r {
		case 'z':
			r = 's'
		case 'k':
			r = 'c'
		case 'y':
			r = 'i'
		}
		if r == previous {
			continue // "falllen" and "fallen" are the same attempt
		}
		previous = r
		b.WriteRune(r)
	}
	return b.String()
}

// SearchKeys are the folded forms a name should be findable by: the whole
// name, and — for a name made of several words — the same without the
// organisational ones, so "spirit" finds "Team Spirit".
//
// Returned as a slice rather than one string because both are legitimate
// targets and neither is a substring of the other once the org words are
// gone from the middle rather than the edge ("Natus Vincere Junior").
func SearchKeys(name string) []string {
	full := FoldForSearch(name)
	if full == "" {
		return nil
	}
	keys := []string{full}
	if stripped := FoldForSearch(withoutOrgWords(name)); stripped != "" && stripped != full {
		keys = append(keys, stripped)
	}
	return keys
}

// withoutOrgWords drops the organisational words, unless they are all there
// is — a team actually called "Team" must still be findable.
func withoutOrgWords(name string) string {
	fields := strings.Fields(strings.ToLower(name))
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if !orgWords[strings.Trim(f, ".,")] {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		return name
	}
	return strings.Join(kept, " ")
}

// SearchKeyBlob is what a row stores so a database can find it with one
// substring test: every key joined by a separator no fold can produce, so a
// match can never straddle two of them.
func SearchKeyBlob(name string) string {
	keys := SearchKeys(name)
	if len(keys) == 0 {
		return ""
	}
	return " " + strings.Join(keys, " ") + " "
}

// MatchesSearch reports whether a folded query is worth offering name for.
// Used to rank and to filter in memory; the database does the same test in
// SQL against the stored blob.
func MatchesSearch(name, foldedQuery string) bool {
	if foldedQuery == "" {
		return true
	}
	for _, key := range SearchKeys(name) {
		if strings.Contains(key, foldedQuery) {
			return true
		}
	}
	return false
}

// SearchRank orders results for a folded query, smaller being better: an
// exact hit first, then names that start with it, then names that merely
// contain it, with shorter names ahead of longer ones inside each band —
// "NAVI" should outrank "NAVI Junior" for "navi".
func SearchRank(name, foldedQuery string) int {
	best := 3
	for _, key := range SearchKeys(name) {
		switch {
		case key == foldedQuery:
			return 0
		case strings.HasPrefix(key, foldedQuery):
			best = min(best, 1)
		case strings.Contains(key, foldedQuery):
			best = min(best, 2)
		}
	}
	return best
}
