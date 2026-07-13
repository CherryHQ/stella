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
	entries := map[string]map[string]bool{
		"Chat": {"beginSessionAccess": true}, "ChatForScheduler": {"beginSessionAccess": true}, "chatOnSession": {"beginSessionAccess": true},
		"ResolvePrivateChannelSession":       {"resolvePrivateChannelSession": true},
		"ResolvePrivateChannelSessionForUse": {"resolvePrivateChannelSession": true},
		"resolvePrivateChannelSession":       {"beginSessionAccess": true},
		"ResolveGroupChannelSession":         {"resolveGroupChannelSession": true},
		"ResolveGroupChannelSessionForUse":   {"resolveGroupChannelSession": true},
		"resolveGroupChannelSession":         {"beginSessionAccess": true},
		"NewSession":                         {"beginSessionAccess": true}, "MintTaskSession": {"beginSessionAccess": true},
		"Delegate": {"beginSessionAccess": true}, "ArchiveSession": {"beginSessionAccess": true},
		"ResolveMainSession":       {"resolveMainSession": true},
		"ResolveMainSessionForUse": {"resolveMainSession": true},
		"resolveMainSession":       {"beginSessionAccess": true},
	}
	seen := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if _, tracked := entries[fn.Name.Name]; !tracked {
			continue
		}
		seen[fn.Name.Name] = true
		callsRequiredGate := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if entries[fn.Name.Name][sel.Sel.Name] {
					callsRequiredGate = true
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
		if !callsRequiredGate {
			t.Errorf("%s does not call its trusted SessionAccess gate", fn.Name.Name)
		}
	}
	for name := range entries {
		if !seen[name] {
			t.Errorf("entry %s disappeared; update the non-HTTP session cutover inventory", name)
		}
	}
}
