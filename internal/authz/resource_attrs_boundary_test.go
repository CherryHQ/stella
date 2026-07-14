package authz_test

// Architecture boundary tripwire for the static policy bridge.
//
// authz.NewResourceWithAttrs stamps a resource with domain-derived facts. The
// static evaluator is their only consumer, so only internal/authz/policy may
// construct attributed resources. Transport code must not supply facts directly.
//
// It walks the whole module and resolves the internal/authz import's local name
// per file, so an aliased import cannot evade it. It also references the symbol
// (stale guard) so a rename/removal fails loudly here instead of silently
// disabling the check.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// Stale guard: if NewResourceWithAttrs is renamed or removed, this reference
// stops compiling and the boundary test fails loudly.
var _ = authz.NewResourceWithAttrs

const authzImportPath = "github.com/CherryHQ/stella/internal/authz"

// authzLocalName returns the identifier a file uses for the internal/authz
// import (honoring an explicit alias), or "" if it does not import it.
func authzLocalName(f *ast.File) string {
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, "`\"") != authzImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "authz"
	}
	return ""
}

func isNewResourceWithAttrsCall(call *ast.CallExpr, local string) bool {
	if local == "" {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != local {
		return false
	}
	return sel.Sel.Name == "NewResourceWithAttrs"
}

func TestNewResourceWithAttrsCallersRestrictedToPolicy(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	// Only internal/authz/policy may construct attributed resources in production.
	exemptPrefix := filepath.ToSlash(filepath.Join("internal", "authz", "policy")) + "/"

	var offenders []string
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "dist": true, ".agents": true,
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, exemptPrefix) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, raw, 0)
		if err != nil {
			return err
		}
		local := authzLocalName(f)
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isNewResourceWithAttrsCall(call, local) {
				offenders = append(offenders, rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	for _, file := range offenders {
		t.Errorf("%s: calls authz.NewResourceWithAttrs outside internal/authz/policy; build attributed resources through the static policy fact builders instead", file)
	}
}
