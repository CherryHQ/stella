package access

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// invocationInventory is a deliberately exact AST inventory of production
// service selection and turn-entry calls outside internal/agent. It is not a
// grep: aliases and comments cannot satisfy it. Any new Agent invocation must
// be routed through an existing Agent access-authorized adapter first, then added
// here with its adapter rationale; deleting an entry is also reviewed.
var invocationInventory = map[string]map[string]int{
	"cmd/stellad/commands.go":              {"GetService": 2}, // goal worker + scheduler durable adapters
	"cmd/stellad/setup_pool.go":            {"GetService": 1}, // lazy composition adapter; no turn
	"internal/channel/coordinator.go":      {"Chat": 1},
	"internal/channel/group_dispatch.go":   {"GetService": 1},
	"internal/channel/group_dispatcher.go": {"Chat": 1},
	// Sole Web-group service selection: resolveWebGroupChat mints and re-checks the
	// group authority for both the Web group turn and the Web group `/new`.
	"internal/channel/group_new_session.go": {"GetService": 1},
	"internal/channel/resolved_chat.go":     {"GetService": 1, "Chat": 1},
	"internal/goal/session.go":              {"GetService": 1}, // session creation; execution is guarded in workerExecutor
	"internal/reflect/loop.go":              {"GetService": 1}, // session listing only; no turn
	"internal/server/webhook_ingress.go":    {"GetService": 1, "Chat": 1},
}

func TestAgentInvocationInventoryIsExact(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]int{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(filepath.ToSlash(path), "/internal/agent/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "cmd/stellad/") && !strings.HasPrefix(rel, "internal/channel/") && !strings.HasPrefix(rel, "internal/goal/") && !strings.HasPrefix(rel, "internal/reflect/") && !strings.HasPrefix(rel, "internal/server/") {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "GetService", "Chat", "Delegate":
				if got[rel] == nil {
					got[rel] = map[string]int{}
				}
				got[rel][sel.Sel.Name]++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var diffs []string
	for file, methods := range invocationInventory {
		for method, want := range methods {
			if got[file][method] != want {
				diffs = append(diffs, file+" "+method)
			}
			delete(got[file], method)
		}
		if len(got[file]) == 0 {
			delete(got, file)
		}
	}
	for file, methods := range got {
		for method := range methods {
			diffs = append(diffs, file+" "+method)
		}
	}
	sort.Strings(diffs)
	if len(diffs) != 0 {
		t.Fatalf("Agent invocation inventory changed: %s; route new turn entry through an Agent access-authorized adapter before changing this exact allowlist", strings.Join(diffs, ", "))
	}
}
