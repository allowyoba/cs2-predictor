package telegram

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The navigation tree's one structural rule: every button this package can
// render leads somewhere.
//
// A button whose callback data no route matches is a dead button. Tapping it
// produces "⚠️ не удалось выполнить действие" and nothing else — which is
// exactly how the chat notification settings were unreachable in production,
// and the kind of break no per-screen test notices, because each screen
// renders perfectly well on its own.
//
// Checked over the source rather than by walking the live tree: most screens
// need real tournaments, polls and people behind them, and a walk that cannot
// reach a screen proves nothing about the button on it.

// callbackDataInSource collects the callback data every button(), urlButton()
// and backButton() call in this package is given, as far as it can be read
// statically: a plain literal, or the literal a concatenation or format
// string starts with (which is what a prefix route matches on anyway).
func callbackDataInSource(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{} // data -> where it came from
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			index := -1
			switch ident.Name {
			case "button", "urlButton":
				index = 1
			case "backButton":
				index = 1 // h.backButton(locale, data) — the selector form is handled below
			}
			if index < 0 || index >= len(call.Args) {
				return true
			}
			if data, ok := leadingLiteral(call.Args[index]); ok && data != "" {
				found[data] = name
			}
			return true
		})
		// h.backButton(locale, data) is a selector call, not a plain ident.
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "backButton" || len(call.Args) < 2 {
				return true
			}
			if data, ok := leadingLiteral(call.Args[1]); ok && data != "" {
				found[data] = name
			}
			return true
		})
	}
	if len(found) < 20 {
		t.Fatalf("only found %d callbacks in the source — the scan is broken, not the tree", len(found))
	}
	return found
}

// leadingLiteral reads the constant head of an expression: "x", "x"+id, or
// fmt.Sprintf("x%s", id). Anything else is a value this cannot resolve, and
// is skipped rather than guessed at.
func leadingLiteral(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(e.Value)
		return value, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		return leadingLiteral(e.X)
	case *ast.CallExpr:
		// fmt.Sprintf("lv:who:%s:%d", ...) — the route matches its prefix.
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sprintf" || len(e.Args) == 0 {
			return "", false
		}
		return leadingLiteral(e.Args[0])
	}
	return "", false
}

func TestNavigationTree_EveryRenderedButtonIsRouted(t *testing.T) {
	for data, source := range callbackDataInSource(t) {
		if data == "noop" || strings.HasPrefix(data, "http") {
			continue // a deliberate non-button, or a link out
		}
		var routed bool
		for _, route := range callbackRoutes {
			if route.match(data) {
				routed = true
				break
			}
		}
		if !routed && !handledOutsideTheRouteTable(data) {
			t.Errorf("%s renders a button with callback data %q that no route matches — tapping it fails", source, data)
		}
	}
}

// handledOutsideTheRouteTable lists the callbacks answered by callbacks.go's
// own switch rather than by callbackRoutes. They are a second dispatch for
// the screens that belong to a person rather than to a chat; named here so
// the check above stays honest about what it is not looking at.
func handledOutsideTheRouteTable(data string) bool {
	for _, prefix := range []string{
		"pstats:", "hub:", "manage:", "notify:", "idea:", "team_matches:", "team_match_operators:",
		"miniapp:", "feedback:", "dm:", "tmatch:", "tmatch_admin:",
	} {
		if strings.HasPrefix(data, prefix) {
			return true
		}
	}
	return false
}

// The two hub-level screens were renamed from pstats:* to hub:*, because
// that is the level they sit at. Every keyboard the bot has already posted
// still carries the old spelling, and a callback is only ever as good as the
// message it is attached to — so both forms have to keep working for as long
// as those messages exist, which is forever.
func TestNavigationTree_TheOldHubCallbackSpellingsStillWork(t *testing.T) {
	// Asserting the screen, not merely that something rendered: an unknown
	// callback here falls through to the personal cabinet, so a lost route
	// looks like a working button that goes to the wrong place.
	cases := []struct {
		legacy string
		modern string
	}{
		{"pstats:settings", "hub:settings"},
		{"pstats:help", "hub:help"},
	}
	for _, c := range cases {
		want := renderPrivateCallback(t, c.modern)
		got := renderPrivateCallback(t, c.legacy)
		if got != want {
			t.Fatalf("%s no longer opens what %s does.\nold: %q\nnew: %q", c.legacy, c.modern, got, want)
		}
	}
}

// renderPrivateCallback taps one callback in a DM and returns the text it
// rendered.
func renderPrivateCallback(t *testing.T, data string) string {
	t.Helper()
	server, calls := newRecordingServer(t)
	defer server.Close()
	handler, _ := newTestHandler(t, server)

	cb := &CallbackQuery{
		ID: "cb", From: User{ID: 7, FirstName: "A"},
		Message: &Message{MessageID: 1, Chat: Chat{ID: 7, Type: "private"}}, Data: &data,
	}
	if err := handler.handlePrivateCallback(context.Background(), cb); err != nil {
		t.Fatalf("%s failed: %v", data, err)
	}
	var body strings.Builder
	for _, call := range *calls {
		if text, ok := call["text"].(string); ok {
			body.WriteString(text + "\n")
		}
	}
	if body.Len() == 0 {
		t.Fatalf("%s rendered nothing", data)
	}
	return body.String()
}
