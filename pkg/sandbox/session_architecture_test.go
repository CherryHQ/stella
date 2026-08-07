package sandbox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSessionDoesNotExposeHostPathResolution keeps physical host coordinates
// below the provider boundary. The method names are deliberately literals here:
// this is the architecture guard that forbids them.
func TestSessionDoesNotExposeHostPathResolution(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
	forbidden := map[string]bool{"ResolvePath": true, "ResolveWritePath": true}

	fset := token.NewFileSet()
	sessionFile, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(thisFile), "session.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range sessionFile.Decls {
		typeDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range typeDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "Session" {
				continue
			}
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				t.Fatal("Session is not an interface")
			}
			for _, method := range iface.Methods.List {
				for _, name := range method.Names {
					if forbidden[name.Name] {
						t.Errorf("Session exposes forbidden host path method %s", name.Name)
					}
				}
			}
		}
	}

	err = filepath.WalkDir(filepath.Join(root, "internal", "agent"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && forbidden[selector.Sel.Name] {
				t.Errorf("%s calls forbidden host path method %s", path, selector.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
