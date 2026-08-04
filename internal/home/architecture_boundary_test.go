package home

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestNonDestructiveBoundariesDoNotReachHomeDeletion(t *testing.T) {
	repo := repositoryRoot(t)
	for _, target := range []struct {
		path     string
		receiver string
		method   string
	}{
		{path: "internal/agent/access/management.go", receiver: "Management", method: "RemoveUser"},
		{path: "internal/channel/group_service.go", receiver: "GroupAccess", method: "RemoveMember"},
		{path: "internal/agent/session/access/service.go", receiver: "Access", method: "Archive"},
	} {
		t.Run(target.path+":"+target.method, func(t *testing.T) {
			body := methodBody(t, filepath.Join(repo, target.path), target.receiver, target.method)
			forbiddenCall(t, body, target.path, target.method)
		})
	}
}

func TestHelmTemplatesDoNotExposeDestructiveHomeControls(t *testing.T) {
	repo := repositoryRoot(t)
	paths, err := filepath.Glob(filepath.Join(repo, "deploy", "helm", "stella", "templates", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"storage_home", "retry-purge", "OwnerDeletion", "DeleteUser", "DeleteGroup", "DeleteAgent", "Purge", "Tombstone"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s exposes destructive Home control %q", filepath.ToSlash(path), forbidden)
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate boundary test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func methodBody(t *testing.T, path, receiver, method string) *ast.BlockStmt {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != method || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		recv := receiverName(fn.Recv.List[0].Type)
		if recv == receiver {
			return fn.Body
		}
	}
	t.Fatalf("%s.%s not found in %s", receiver, method, path)
	return nil
}

func receiverName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.StarExpr:
		return receiverName(expr.X)
	default:
		return ""
	}
}

func forbiddenCall(t *testing.T, body *ast.BlockStmt, path, method string) {
	t.Helper()
	forbidden := []string{"OwnerDeletion", "DeleteUser", "DeleteGroup", "DeleteAgent", "Purge", "Tombstone"}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calledName(call.Fun)
		if slices.Contains(forbidden, name) {
			t.Errorf("%s %s calls forbidden destructive boundary %s", path, method, name)
		}
		return true
	})
}

func calledName(expr ast.Expr) string {
	switch expr := expr.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		return expr.Sel.Name
	default:
		return ""
	}
}
