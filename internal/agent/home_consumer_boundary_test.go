package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestHomeConsumerBoundary(t *testing.T) {
	files := []string{
		"session/access/workspace.go", "project_store.go", "../channel/coordinator.go", "../share/service.go",
	}
	for _, name := range files {
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var selected string
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				selected = fun.Name
			case *ast.SelectorExpr:
				selected = fun.Sel.Name
			}
			switch selected {
			case "SetupUserWorkspace", "SetupGroupWorkspace", "UserHomeDir", "GroupHomeDir", "UserAgentDir", "GroupAgentDir", "UserDataDir":
				t.Errorf("%s calls forbidden Home path helper %s", name, selected)
			}
			return true
		})
	}
}
