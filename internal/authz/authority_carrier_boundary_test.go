package authz_test

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

const carrierImport = "github.com/CherryHQ/stella/internal/authz"

func TestAuthorityCarrierProductionBoundary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	allowedReads := map[string]bool{"internal/settings": true}
	allowedWrites := map[string]bool{"internal/agent/runtime": true}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "dist", ".agents":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		local := ""
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, "\"`") == carrierImport {
				if imp.Name != nil {
					local = imp.Name.Name
				} else {
					local = "authz"
				}
				break
			}
		}
		if local == "" {
			return nil
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok || x.Name != local {
				return true
			}
			switch sel.Sel.Name {
			case "AuthorityFromContext":
				if !allowedReads[dir] {
					t.Errorf("%s reads AuthorityFromContext outside Settings tool", rel)
				}
			case "WithAuthority", "ClearAuthority":
				if !allowedWrites[dir] {
					t.Errorf("%s writes Authority carrier outside runtime", rel)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
