package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectAndPoolHaveNoAssetDependency prevents the retired mutable asset
// mirror from returning through project setup or runtime startup.
func TestProjectAndPoolHaveNoAssetDependency(t *testing.T) {
	for _, name := range []string{"project_store.go", "pool_manager.go"} {
		path := filepath.Join(".", name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, "`\"") == "github.com/CherryHQ/stella/internal/asset" {
				t.Errorf("%s imports retired asset dependency", name)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok {
				return true
			}
			for _, fieldName := range field.Names {
				if fieldName.Name == "assets" || fieldName.Name == "assetStore" {
					pos := fset.Position(field.Pos())
					t.Errorf("%s:%d retains asset field %s", name, pos.Line, fieldName.Name)
				}
			}
			return true
		})
	}
}
