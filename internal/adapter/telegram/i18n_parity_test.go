package telegram

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// bundleKeys parses a .properties bundle the same way readBundle does, but
// keeps only the key set.
func bundleKeys(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		keys[strings.TrimSpace(key)] = true
	}
	return keys
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Both bundles must define exactly the same keys. Texts.Get falls back to
// returning the key itself when one is missing, so drift here doesn't fail
// anywhere at runtime — it just ships a raw key like "poll.vrs" into
// somebody's chat, in whichever language happens to be short an entry.
func TestI18n_BundlesDefineTheSameKeys(t *testing.T) {
	ru := bundleKeys(t, filepath.Join("i18n", "messages_ru.properties"))
	en := bundleKeys(t, filepath.Join("i18n", "messages_en.properties"))

	var missingInEN, missingInRU []string
	for _, k := range sortedKeys(ru) {
		if !en[k] {
			missingInEN = append(missingInEN, k)
		}
	}
	for _, k := range sortedKeys(en) {
		if !ru[k] {
			missingInRU = append(missingInRU, k)
		}
	}
	if len(missingInEN) > 0 {
		t.Errorf("keys present in RU but missing from EN: %v", missingInEN)
	}
	if len(missingInRU) > 0 {
		t.Errorf("keys present in EN but missing from RU: %v", missingInRU)
	}
}

// textsGetKey matches the string literal in every Texts.Get("…") call, which
// is how every message in this package is looked up.
var textsGetKey = regexp.MustCompile(`Texts\.Get\("([a-z][a-zA-Z0-9._]*)"`)

// Every key the code actually asks for must exist in both bundles — the
// other half of the same failure mode: a typo at a call site is invisible
// until a user sees the raw key.
func TestI18n_EveryReferencedKeyResolves(t *testing.T) {
	ru := bundleKeys(t, filepath.Join("i18n", "messages_ru.properties"))
	en := bundleKeys(t, filepath.Join("i18n", "messages_en.properties"))

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	referenced := map[string][]string{} // key -> files referencing it
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range textsGetKey.FindAllStringSubmatch(string(src), -1) {
			referenced[m[1]] = append(referenced[m[1]], name)
		}
	}
	if len(referenced) == 0 {
		t.Fatal("found no Texts.Get calls at all — the scanner is broken, not the bundles")
	}

	for key, files := range referenced {
		if !ru[key] {
			t.Errorf("key %q used in %v is missing from the RU bundle", key, files)
		}
		if !en[key] {
			t.Errorf("key %q used in %v is missing from the EN bundle", key, files)
		}
	}
}

// Every nomination the domain can hand out must have a title and a value
// format in both bundles. Like the change-history labels below, these keys
// are composed at runtime ("award."+kind), so the scanner above cannot see
// them — enumerating the pool here is what keeps a new nomination from
// shipping as a raw key.
func TestI18n_EveryEventAwardHasALabel(t *testing.T) {
	ru := bundleKeys(t, filepath.Join("i18n", "messages_ru.properties"))
	en := bundleKeys(t, filepath.Join("i18n", "messages_en.properties"))
	kinds := []string{
		scoring.AwardUnderdog, scoring.AwardLoneVoice, scoring.AwardStreak, scoring.AwardExact,
		scoring.AwardFlawless, scoring.AwardSniper, scoring.AwardIronman, scoring.AwardVolume,
	}
	for _, kind := range kinds {
		for _, key := range []string{"award." + kind, "award.value." + kind} {
			if !ru[key] {
				t.Errorf("award %q has no RU label (%s)", kind, key)
			}
			if !en[key] {
				t.Errorf("award %q has no EN label (%s)", kind, key)
			}
		}
		if awardIcons[kind] == "" {
			t.Errorf("award %q has no icon", kind)
		}
	}
}

// A bundle that fails to load at all would take every screen down, so the
// loader itself is worth one assertion.
func TestI18n_LoadTextsResolvesBothLocales(t *testing.T) {
	texts, err := LoadTexts()
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []common.LocaleCode{common.LocaleRU, common.LocaleEN} {
		if got := texts.Get("menu.title", locale); got == "menu.title" || got == "" {
			t.Fatalf("menu.title did not resolve for %s, got %q", locale, got)
		}
	}
}

// The change-history labels are looked up through a composed key, so the
// scanner above can't see them. Enumerated in adminActionKinds precisely so
// they can still be checked.
func TestI18n_EveryAdminActionKindHasALabel(t *testing.T) {
	ru := bundleKeys(t, filepath.Join("i18n", "messages_ru.properties"))
	en := bundleKeys(t, filepath.Join("i18n", "messages_en.properties"))
	for _, kind := range adminActionKinds {
		key := historyKindKey(kind)
		if !ru[key] {
			t.Errorf("admin action kind %q has no RU label (%s)", kind, key)
		}
		if !en[key] {
			t.Errorf("admin action kind %q has no EN label (%s)", kind, key)
		}
	}
}

// Permission labels are looked up through a composed key too (see
// permissionLabelKey), invisible to the same static scanner for the same
// reason history kinds are.
func TestI18n_EveryPermissionHasALabel(t *testing.T) {
	ru := bundleKeys(t, filepath.Join("i18n", "messages_ru.properties"))
	en := bundleKeys(t, filepath.Join("i18n", "messages_en.properties"))
	for _, p := range chat.AllPermissions() {
		key := permissionLabelKey(p)
		if !ru[key] {
			t.Errorf("permission %q has no RU label (%s)", p, key)
		}
		if !en[key] {
			t.Errorf("permission %q has no EN label (%s)", p, key)
		}
	}
}
