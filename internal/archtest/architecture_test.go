// Package archtest holds fitness-function tests for the module's
// hexagonal layering: internal/domain implements pure business rules,
// internal/app orchestrates them against ports, and internal/adapter/*
// implements those ports against real infrastructure. Dependencies only
// ever point inward (adapter -> app/domain, app -> domain), never the
// other way — these tests fail the build the moment a file breaks that
// rule, rather than relying on a reviewer to notice a stray import.
package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePath = "cs2predictor"

// moduleRoot walks up from the current test's working directory (which `go
// test` sets to this package's own directory) until it finds go.mod, so the
// scan works regardless of where the repository is checked out.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod above " + dir)
		}
		dir = parent
	}
}

// importsUnder collects every distinct import path found in non-test .go
// files under root (relative to the module root), skipping the directory a
// forbidden-prefix violation would otherwise trivially match against
// itself.
func importsUnder(t *testing.T, moduleRoot, relDir string) map[string][]string {
	t.Helper()
	byImport := map[string][]string{}
	root := filepath.Join(moduleRoot, relDir)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			byImport[importPath] = append(byImport[importPath], rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return byImport
}

// assertNoForbiddenImports fails the test, listing every offending file,
// if any import under relDir starts with one of the forbidden prefixes.
func assertNoForbiddenImports(t *testing.T, relDir string, forbiddenPrefixes ...string) {
	t.Helper()
	root := moduleRoot(t)
	imports := importsUnder(t, root, relDir)

	var violations []string
	for importPath, files := range imports {
		for _, forbidden := range forbiddenPrefixes {
			if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
				for _, f := range files {
					violations = append(violations, f+" imports "+importPath)
				}
			}
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("%s must never import %v, but found:\n%s", relDir, forbiddenPrefixes, strings.Join(violations, "\n"))
	}
}

// TestDomainNeverImportsAdapterOrApp is the core hexagonal-architecture
// invariant: internal/domain holds business rules only. It must compile
// and be testable with no knowledge of Postgres, Telegram, or any other
// concrete infrastructure — those live behind ports domain declares (see
// e.g. chat.Repository, chat.MembershipGateway) and app/adapter satisfy.
func TestDomainNeverImportsAdapterOrApp(t *testing.T) {
	assertNoForbiddenImports(t, "internal/domain",
		modulePath+"/internal/adapter",
		modulePath+"/internal/app",
	)
}

// TestAppNeverImportsAdapter is the same rule one layer out: internal/app
// orchestrates domain services against ports (see internal/platform/common
// for the shared ones); it must not hard-code a dependency on a specific
// adapter implementation — those are wired together only in cmd/bot, the
// composition root.
func TestAppNeverImportsAdapter(t *testing.T) {
	assertNoForbiddenImports(t, "internal/app",
		modulePath+"/internal/adapter",
	)
}
