package blob_test

// Architecture boundary tripwire for #709 (stack 4 of #703).
//
// The blob package exposes no process-global store: blob.Default / blob.SetDefault
// / blob.ResetDefaultForTest remain deleted. Raw blob storage is injected into
// immutable domain services such as session media and library raw content;
// mutable workspace and user-data consumers use Home rooted POSIX capabilities.
//
// This guard keeps the global deleted: it walks the whole module and fails on any
// production call to blob.Default / blob.SetDefault. The symbols are gone, so such
// a call would not compile — but the scan documents the invariant and catches a
// re-introduction attempt (e.g. a new package-level default helper) at review time
// rather than in production. It resolves the internal/blob import's local name per
// file so an aliased import cannot evade it (an accepted limit shared with
// env_scan_test: a fully dynamic/reflection call is out of scope).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const blobImportPath = "github.com/CherryHQ/stella/internal/blob"

// blobLocalName returns the identifier a file uses for the internal/blob import
// (honoring an explicit alias), or "" if it does not import it.
func blobLocalName(f *ast.File) string {
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, "`\"") != blobImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "blob"
	}
	return ""
}

func isBlobDefaultCall(call *ast.CallExpr, local string) bool {
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
	return sel.Sel.Name == "Default" || sel.Sel.Name == "SetDefault" || sel.Sel.Name == "ResetDefaultForTest"
}

func TestNoBlobProcessGlobal(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
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
		// Production files only: tests may reference the removed names in comments.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		if strings.Contains(src, "//go:build ignore") || strings.Contains(src, "// Code generated") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		local := blobLocalName(f)
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isBlobDefaultCall(call, local) {
				t.Errorf("%s: production code calls a blob process-global (Default/SetDefault/ResetDefaultForTest); "+
					"inject the blob store into the owning immutable domain instead", rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
}
