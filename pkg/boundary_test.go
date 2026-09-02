package pkg

// Package-direction tripwire: pkg/** must never import internal/**.
//
// pkg/ is the contract surface plugins and external consumers compile against
// (channel, tools, hooks, providers, sandbox, plugins, db/sqlc). internal/ is
// the application. The dependency arrow points one way only: internal may use
// pkg, pkg may never reach back. Today that holds by discipline alone with zero
// violations; this guard is what keeps it true through refactors.
//
// This directory carries no production Go file on purpose — the guard walks the
// whole pkg/ tree from here, so it does not need to live in any one subpackage.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const internalImportPrefix = "github.com/CherryHQ/stella/internal/"

// importPaths returns every import path in a parsed file, unquoted.
func importPaths(f *ast.File) []string {
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, "`\""))
	}
	return out
}

// internalImports returns the internal/ import paths a file pulls in. Aliases
// are irrelevant here: the path itself is the violation.
func internalImports(f *ast.File) []string {
	var out []string
	for _, path := range importPaths(f) {
		if strings.HasPrefix(path, internalImportPrefix) {
			out = append(out, path)
		}
	}
	return out
}

// walkGoFiles parses every .go file under root (tests included — a pkg test that
// reaches into internal is the same coupling) and calls fn with the repo-relative
// path and parsed file.
func walkGoFiles(t *testing.T, root string, fn func(rel string, f *ast.File)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fn(filepath.ToSlash(rel), f)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func TestPkgBoundaryExcludesInternalImports(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve pkg dir: %v", err)
	}
	seen := 0
	walkGoFiles(t, root, func(rel string, f *ast.File) {
		seen++
		for _, path := range internalImports(f) {
			t.Errorf("pkg/%s imports %s; pkg/ is the plugin-facing contract surface and must never depend on internal/ — move the shared type into pkg/ or invert the dependency", rel, path)
		}
	})
	if seen == 0 {
		t.Fatal("no Go files found under pkg/ — the guard would pass vacuously")
	}
}

// TestPkgBoundaryTripwireCatchesCounterexample proves the guard is not vacuous:
// a synthetic pkg file importing internal/ must be detected.
func TestPkgBoundaryTripwireCatchesCounterexample(t *testing.T) {
	const src = `package channel

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent"
)

var _ = context.Background
var _ = agent.Service{}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "pkg/channel/offender.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	got := internalImports(f)
	if len(got) != 1 || got[0] != "github.com/CherryHQ/stella/internal/agent" {
		t.Fatalf("counterexample not detected, got %v — the tripwire is vacuous", got)
	}
}
