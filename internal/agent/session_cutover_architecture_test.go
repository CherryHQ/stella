package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestNonHTTPSessionEntryCutoverTripwire(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}
	entries := map[string]bool{
		"Chat": true, "ChatForScheduler": true, "chatOnSession": true,
		"ResolvePrivateChannelSession": true,
		"ResolveGroupChannelSession":   true, "NewSession": true,
		"MintTaskSession": true, "Delegate": true, "ArchiveSession": true,
		"ResolveMainSession": true,
	}
	seen := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !entries[fn.Name.Name] {
			continue
		}
		seen[fn.Name.Name] = true
		callsSessionAccess := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "beginSessionAccess" {
					callsSessionAccess = true
				}
				if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == "Sessions" {
					switch sel.Sel.Name {
					case "Ensure", "ResolveMain", "Archive", "Get":
						pos := fset.Position(sel.Pos())
						t.Errorf("%s calls s.Sessions.%s directly at %s; route lifecycle through SessionAccess", fn.Name.Name, sel.Sel.Name, pos)
					}
				}
			}
			return true
		})
		if !callsSessionAccess {
			t.Errorf("%s does not begin trusted SessionAccess", fn.Name.Name)
		}
	}
	for name := range entries {
		if !seen[name] {
			t.Errorf("entry %s disappeared; update the non-HTTP session cutover inventory", name)
		}
	}
}
