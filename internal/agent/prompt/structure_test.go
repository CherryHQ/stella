package prompt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestPromptContextDoesNotResolveHostPaths is a structural guard on the active
// prompt-context path. The injected-Filesystem branch must address files by
// canonical path only, so the prompt code may not call a host path resolver
// (ResolvePath/ResolveWritePath). The explicit nil-host fallback still reads the
// host filesystem through os.*, so os usage is intentionally left allowed here.
func TestPromptContextDoesNotResolveHostPaths(t *testing.T) {
	forbidden := map[string]bool{"ResolvePath": true, "ResolveWritePath": true}
	for _, file := range []string{"host.go", "prompt.go"} {
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
			if forbidden[sel.Sel.Name] {
				t.Errorf("%s references .%s: active prompt context must not resolve host paths", file, sel.Sel.Name)
			}
			return true
		})
	}
}
