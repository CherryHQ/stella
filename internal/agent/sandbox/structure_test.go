package sandbox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestActiveFileToolsUseFilesystemBoundary is a structural guard: the active
// read/write/edit tools and their shared helpers must reach files only through
// the provider-neutral Filesystem. They may not resolve a host coordinate
// (ResolvePath/ResolveWritePath) or touch the host os filesystem directly
// (os.ReadFile/os.WriteFile). bash is deliberately excluded — it keeps the
// Session process capability.
func TestActiveFileToolsUseFilesystemBoundary(t *testing.T) {
	forbiddenSelectors := map[string]bool{"ResolvePath": true, "ResolveWritePath": true}
	forbiddenOS := map[string]bool{"ReadFile": true, "WriteFile": true}
	for _, file := range []string{"read.go", "write.go", "edit.go", "tools.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if forbiddenSelectors[sel.Sel.Name] {
				t.Errorf("%s references .%s: active file tools must not resolve host coordinates", file, sel.Sel.Name)
			}
			if base, ok := sel.X.(*ast.Ident); ok && base.Name == "os" && forbiddenOS[sel.Sel.Name] {
				t.Errorf("%s references os.%s: active file tools must use Filesystem, not host os.*", file, sel.Sel.Name)
			}
			return true
		})
	}
}
