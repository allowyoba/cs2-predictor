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

// A screen whose data did not arrive must offer a way to try again.
//
// A dropped connection, a restart mid-deploy, a phone that loses signal
// for a second: all of them fix themselves on a retry, and every one of
// these screens used to answer them with a flat "could not load" and no
// way forward. They also said the same thing when the real answer was "you
// do not have access yet", which retrying cannot fix.
func TestFailedScreensOfferARetry(t *testing.T) {
	script := readStatic(t, "static/app.js")

	if strings.Contains(script, "emptyLine('Не удалось загрузить") {
		t.Error("a load failure is still rendered as a dead end; use failedLine, which carries a retry")
	}
	for _, needed := range []string{"function failedLine(", "function describeFailure(", "Попробовать снова"} {
		if !strings.Contains(script, needed) {
			t.Errorf("missing %q — a failed screen has no way out without it", needed)
		}
	}
	// The three causes have to read differently: only one of them is
	// worth retrying.
	for _, loader := range []string{"loadHistory", "loadActive", "loadChats", "loadSettings"} {
		if !strings.Contains(script, "failedLine(describeFailure(error, ") {
			t.Fatal("failures are not described by cause")
		}
		if !strings.Contains(script, "), "+loader+"))") {
			t.Errorf("%s does not pass itself as the retry", loader)
		}
	}
}

// Shared state the page keeps in a Map or a Set must actually be declared,
// and nothing deeper may shadow its name.
//
// `chips` was used in two functions and declared in none: an edit meant to
// add the declaration silently matched nothing. Nothing caught it —
// `node --check` parses, it does not resolve names — so the page shipped
// and threw ReferenceError the moment it drew a team, which the crest
// loader swallowed into "falling back to initials" and the history screen
// turned into "could not load".
//
// The shadowing half matters as much as the declaration half: a local
// variable of the same name inside one function is what made the missing
// declaration hard to see in the first place, and would make any check
// like this one answer "declared" about the wrong thing.
func TestSharedCollectionsAreDeclared(t *testing.T) {
	script := readStatic(t, "static/app.js")

	// Module level is two spaces in: this file is one IIFE.
	moduleLevel := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^  (?:const|let|var)\s+([A-Za-z_$][\w$]*)`).FindAllStringSubmatch(script, -1) {
		moduleLevel[match[1]] = true
	}
	collections := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^  const\s+([A-Za-z_$][\w$]*)\s*=\s*new (?:Map|Set)\(`).FindAllStringSubmatch(script, -1) {
		collections[match[1]] = true
	}

	for _, match := range regexp.MustCompile(`(?m)^\s{4,}(?:const|let|var)\s+([A-Za-z_$][\w$]*)`).FindAllStringSubmatch(script, -1) {
		if collections[match[1]] {
			t.Errorf("a local %q shadows the shared collection of that name; rename the local", match[1])
		}
	}

	// The leading class excludes a dot, so `wrap.classList.add(` is read as
	// a property chain rather than as a bare name called `classList`.
	used := regexp.MustCompile(`(^|[^.\w$])([a-z][\w$]*)\.(?:get|set|has|clear|delete|add)\(`)
	declared := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?:const|let|var|function)\s+([A-Za-z_$][\w$]*)`).FindAllStringSubmatch(script, -1) {
		declared[match[1]] = true
	}
	for _, match := range regexp.MustCompile(`[(,]\s*([a-z][\w$]*)\s*[,)]`).FindAllStringSubmatch(script, -1) {
		declared[match[1]] = true
	}
	seen := map[string]bool{}
	for _, match := range used.FindAllStringSubmatch(script, -1) {
		name := match[2]
		if seen[name] || declared[name] {
			continue
		}
		seen[name] = true
		switch name {
		case "window", "document", "localStorage", "sessionStorage":
			continue
		}
		t.Errorf("%q is used as shared state but never declared in app.js", name)
	}
}
