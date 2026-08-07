package sandbox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHostPathBoundary(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	pkgDir := filepath.Join(root, "pkg", "sandbox")
	for _, dir := range []string{pkgDir, filepath.Join(root, "internal", "agent"), filepath.Join(root, "internal", "server")} {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			forbiddenImport := map[string]bool{}
			for _, imp := range file.Imports {
				if strings.Trim(imp.Path.Value, "\"") == "github.com/CherryHQ/stella/pkg/sandbox" {
					name := filepath.Base(strings.Trim(imp.Path.Value, "\""))
					if imp.Name != nil {
						name = imp.Name.Name
					}
					forbiddenImport[name] = true
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && dir == pkgDir {
					if isLegacyHostPathContract(id.Name) {
						t.Errorf("%s exposes %q", path, id.Name)
					}
				}
				if sel, ok := n.(*ast.SelectorExpr); ok && dir != pkgDir {
					base := sel.X
					for {
						if nested, ok := base.(*ast.SelectorExpr); ok {
							base = nested.X
						} else {
							break
						}
					}
					root, _ := base.(*ast.Ident)
					if root == nil || !forbiddenImport[root.Name] {
						return true
					}
					if isLegacyHostPathContract(sel.Sel.Name) {
						t.Errorf("%s uses %q", path, sel.Sel.Name)
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
}

func isLegacyHostPathContract(name string) bool {
	switch name {
	case "HostPath", "SandboxPath", "WorkspaceRoot", "WorkspaceRootOrDefault", "Mounts",
		"Mount", "MountAccess", "MountReadOnly", "MountReadWrite",
		"PathResolver", "PathResolverConfig", "ResolvedPath", "NewPathResolver",
		"ResolvePath", "ResolveWritePath":
		return true
	default:
		return false
	}
}
