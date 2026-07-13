package blob_test

// Architecture boundary tripwire for issue #706 (stack 1 of #703).
//
// blob.Default()/blob.SetDefault() are the process-global raw blob store. The
// target architecture injects raw blob storage only into a single asset/workspace
// persistence service; transports and plugins receive narrow ports instead.
// Removing the global is a later stack, so this guard freezes the CURRENT set of
// production call sites by (file -> call count) and fails on any new call — even
// inside an already-listed file, since the count is exact.
//
// This walks the whole module (call sites live outside internal/blob) and matches
// blob.Default / blob.SetDefault by the "blob" import identifier (an aliased
// import would evade it — an accepted limit shared with env_scan_test).

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

// blobDefaultCallAllowlist records the exact number of blob.Default/SetDefault
// production call sites per module-relative file. These are existing debt to be
// replaced by an injected asset/workspace persistence service; the counts may
// only go down (pay off debt, then lower the number), never up.
var blobDefaultCallAllowlist = map[string]int{
	"cmd/stellad/commands.go":     1, // SetDefault: installs the process-global store at boot
	"internal/server/sessions.go": 5, // asset upload/download/list/delete handlers
	"internal/agent/hydrate.go":   1, // cold-pod workspace hydration
	"internal/agent/workspace.go": 1, // workspace materialization
	"internal/share/service.go":   1, // shared-asset read
}

const blobImportPath = "github.com/CherryHQ/stella/internal/blob"

// blobLocalName returns the identifier a file uses for the internal/blob import
// (honoring an explicit alias), or "" if it does not import it. Resolving the
// local name means an aliased import cannot evade the call counter.
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
	return sel.Sel.Name == "Default" || sel.Sel.Name == "SetDefault"
}

func TestNoNewBlobDefaultCallers(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	counts := map[string]int{}
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
				counts[rel]++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}

	for file, got := range counts {
		want, ok := blobDefaultCallAllowlist[file]
		if !ok {
			t.Errorf("%s: new production blob.Default/SetDefault caller (%d call(s)); inject an asset/workspace persistence port instead of the process-global store", file, got)
			continue
		}
		if got > want {
			t.Errorf("%s: %d blob.Default/SetDefault call(s), allowlist permits %d; do not add new callers of the process-global blob store", file, got, want)
		}
	}
	for file, want := range blobDefaultCallAllowlist {
		got := counts[file]
		if got < want {
			t.Errorf("%s: only %d blob.Default/SetDefault call(s) remain (allowlist expects %d); lower the allowlist so regressions are caught", file, got, want)
		}
	}
}
