package telegram

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A callback edits the message it was tapped on. A screen rendered without
// a keyboard therefore replaces whatever the person was looking at with
// something they cannot leave — no buttons, nothing to go back to, and the
// only way out a command typed from memory.
//
// Refusals were the worst of it: eleven screens said "you may not do that"
// and stranded the person for saying so. This fails on any new one.
func TestNoScreenRefusesWithoutAWayBack(t *testing.T) {
	refusal := regexp.MustCompile(`h\.respond\([^)]*error\.forbidden[^)]*,\s*nil\)`)
	for _, path := range packageSources(t) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if match := refusal.Find(body); match != nil {
			t.Errorf("%s refuses with no keyboard: %s — use h.refuse, which carries a way back",
				filepath.Base(path), match)
		}
	}
}

// packageSources lists this package's non-test Go files.
func packageSources(t *testing.T) []string {
	t.Helper()
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("no sources found; the glob is wrong")
	}
	return out
}
