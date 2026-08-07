package skills

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// TestRuntimeToolLoadDoesNotReintroduceMaterializationCache guards the narrow
// runtime boundary. Home catalog revisions are provider-owned and mounted;
// load must never rebuild a host-side skill cache.
func TestRuntimeToolLoadDoesNotReintroduceMaterializationCache(t *testing.T) {
	source, err := os.ReadFile("tool.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "tool.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	retired := map[string]bool{
		"materializeDBSkill": false,
		"SkillDiskLayout":    false,
		"safeSkillDiskPath":  false,
		"readDiskFile":       false,
	}
	var load *ast.FuncDecl
	ast.Inspect(file, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok {
			if _, tracked := retired[ident.Name]; tracked {
				retired[ident.Name] = true
			}
		}
		if fn, ok := node.(*ast.FuncDecl); ok && fn.Name.Name == "load" {
			load = fn
		}
		return true
	})
	for symbol, found := range retired {
		if found {
			t.Errorf("runtime tool retains retired materialization symbol %s", symbol)
		}
	}
	if load == nil {
		t.Fatal("runtime Tool.load is missing")
	}
	forbiddenCalls := map[string]bool{"os.MkdirAll": false, "os.WriteFile": false, "filepath.WalkDir": false}
	ast.Inspect(load.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, tracked := forbiddenCalls[architectureCallName(call.Fun)]; tracked {
			forbiddenCalls[architectureCallName(call.Fun)] = true
		}
		return true
	})
	for call, found := range forbiddenCalls {
		if found {
			t.Errorf("runtime Tool.load must not materialize a cache via %s", call)
		}
	}
}

func architectureCallName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return architectureCallName(value.X) + "." + value.Sel.Name
	default:
		return ""
	}
}
