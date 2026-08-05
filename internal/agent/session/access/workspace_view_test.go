package access

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWorkspaceStaysOnTheFilesystemBoundary prevents a future convenience
// fallback from quietly reintroducing host paths or AssetStore semantics.
func TestWorkspaceStaysOnTheFilesystemBoundary(t *testing.T) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(thisFile), "workspace.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		path := imp.Path.Value
		for _, forbidden := range []string{"internal/asset", "internal/home", "\"os\"", "\"path/filepath\""} {
			if path == "\"github.com/CherryHQ/stella/"+forbidden+"\"" || path == forbidden {
				t.Errorf("workspace.go imports forbidden %s", path)
			}
		}
	}
	forbiddenSelectors := map[string]bool{"ResolvePath": true, "ResolveWritePath": true, "SafePath": true, "OpenSafeRoot": true}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if ok && forbiddenSelectors[selector.Sel.Name] {
			t.Errorf("workspace.go uses forbidden selector %s", selector.Sel.Name)
		}
		return true
	})
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			for _, forbidden := range []string{"canonicalizeAbsPath", "containedRel", "resolveExistingSymlinks", "mountScopeRel"} {
				if fn.Name.Name == forbidden {
					t.Errorf("workspace.go declares forbidden host canonicalization helper %s", forbidden)
				}
			}
		}
	}
}
