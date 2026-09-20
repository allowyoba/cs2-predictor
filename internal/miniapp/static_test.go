package miniapp

import (
	"regexp"
	"strings"
	"testing"
)

// The page shipped for months with four discipline chips and their win
// rates written straight into the markup, so someone who had only ever
// predicted CS2 was shown Dota 2, Valorant and LoL at 68%, 72% and 64%.
// Every figure on screen has to come from the API, and markup is the one
// place a figure can hide without any code reading it.
func TestPageCarriesNoFiguresOfItsOwn(t *testing.T) {
	page := readStatic(t, "static/index.html")

	if strings.Contains(page, "game-chip") {
		t.Error("index.html declares discipline chips; the rail is filled from /me/dashboard")
	}

	// Text between tags, with the placeholder dash and the ordinals that
	// belong to the layout (2xl grids and the like) left out.
	text := regexp.MustCompile(`>[^<>]+<`)
	digits := regexp.MustCompile(`[0-9]`)
	for _, match := range text.FindAllString(page, -1) {
		body := strings.TrimSpace(strings.Trim(match, "<>"))
		if body == "" || !digits.MatchString(body) {
			continue
		}
		t.Errorf("index.html shows the figure %q; it must be written by app.js from the API", body)
	}
}

// A chip, a filter or a button that does nothing is worse than a missing
// one: it promises a feature the bot does not have.
func TestPageHasNoControlsWithoutBehaviour(t *testing.T) {
	page := readStatic(t, "static/index.html")
	script := readStatic(t, "static/app.js")

	// An id the page points at itself — aria-labelledby naming a heading —
	// is doing its job without any script.
	ids := regexp.MustCompile(`id="([a-zA-Z0-9_-]+)"`)
	for _, match := range ids.FindAllStringSubmatch(page, -1) {
		if strings.Contains(page, `aria-labelledby="`+match[1]+`"`) {
			continue
		}
		if !strings.Contains(script, "#"+match[1]) {
			t.Errorf("index.html carries #%s, which app.js never touches", match[1])
		}
	}
}

func readStatic(t *testing.T, name string) string {
	t.Helper()
	data, err := files.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// The initials behind a crest are a fallback, not a backdrop. A crest is
// rarely a filled square, so letters showing around its edges — and
// through the transparent parts of a PNG — read as a rendering fault. The
// page must hide them once an image has actually painted, and bring them
// back if it fails.
func TestCrestAndInitialsAreNeverDrawnTogether(t *testing.T) {
	style := readStatic(t, "static/styles.css")
	script := readStatic(t, "static/app.js")

	if !strings.Contains(style, ".team-logo.has-crest b{display:none}") {
		t.Error("nothing hides the initials once a crest is on screen")
	}
	for _, needed := range []string{`img.addEventListener('load'`, `img.addEventListener('error'`, "img.complete"} {
		if !strings.Contains(script, needed) {
			t.Errorf("teamMark does not handle %s — the fallback is then either always or never shown", needed)
		}
	}
}
