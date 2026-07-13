package main

// Architecture boundary tripwire for issue #706 (stack 1 of #703).
//
// cmd/stellad is the composition root: it wires services, but must not grow new
// application/business logic. The clearest structural signal is a direct reach
// into the raw sqlc query layer (pkg/db/sqlc). Pure wiring closures that carry no
// query are out of structural scope by design — a test cannot prove "wiring vs
// business" for an arbitrary closure, so this guard does not claim to.
//
// This freezes the EXACT number of sqlc package references per allowlisted file
// (resolved through each file's own import name, so an aliased import cannot
// evade the guard). Adding another query reference to commands.go/gateway.go —
// or introducing sqlc in a new file — fails, forcing the query behind a service
// the root merely wires.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sqlcImportPath = "github.com/CherryHQ/stella/pkg/db/sqlc"

// cmdSQLCRefCounts is empty: issue #708 Section B relocated every application
// query and business fallback closure out of cmd/stellad into named
// owning-domain adapters/services (the *ForPool constructors, agent.ProjectStore
// / agent.ToolOverrideStore, workflow.Service.LatestRunState,
// channel.NewDBGroupMemberLister). No composition-root file may reference the raw
// sqlc query layer; a new sqlc.<Symbol> reference in any cmd/stellad file fails
// here — route the query behind a service the root merely wires.
var cmdSQLCRefCounts = map[string]int{}

// localImportName returns the identifier a file uses for importPath, honoring an
// explicit alias; "" if the file does not import it. This defeats trivial alias
// evasion (import foo "…/sqlc" is still counted).
func localImportName(f *ast.File, importPath string) string {
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, "`\"") != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name // explicit alias (including "." / "_")
		}
		seg := importPath[strings.LastIndex(importPath, "/")+1:]
		return seg
	}
	return ""
}

// countSelectorRefs counts SelectorExpr nodes whose receiver identifier is local
// (e.g. sqlc.New, sqlc.GetProjectParams) — every reference to the package.
func countSelectorRefs(f *ast.File, local string) int {
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
			n++
		}
		return true
	})
	return n
}

func TestNoNewCmdApplicationQueries(t *testing.T) {
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve package dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	seen := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		local := localImportName(f, sqlcImportPath)
		if local == "" {
			continue
		}
		seen[name] = countSelectorRefs(f, local)
	}

	for name, got := range seen {
		want, ok := cmdSQLCRefCounts[name]
		if !ok {
			t.Errorf("%s: cmd/stellad reaches the raw sqlc query layer (%d ref(s)); move this query behind a service the composition root wires, or add a justified allowlist entry", name, got)
			continue
		}
		if got > want {
			t.Errorf("%s: %d sqlc references, allowlist permits %d; do not add new raw queries to the composition root — relocate them behind a service", name, got, want)
		}
	}
	for name, want := range cmdSQLCRefCounts {
		got, ok := seen[name]
		if !ok {
			t.Errorf("stale cmd sqlc allowlist entry %q no longer imports sqlc; remove it", name)
			continue
		}
		if got < want {
			t.Errorf("%s: only %d sqlc references remain (allowlist expects %d); lower the count so regressions are caught", name, got, want)
		}
	}
}
