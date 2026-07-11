package main

// Issue #708 lifecycle contract: no late-bind / ingress window. This AST guard
// freezes the source ordering inside runServer so a future edit cannot move an
// ingress source (HTTP Serve, group-dispatch Run, managed channel runtimes)
// ahead of the static callbacks and River/scheduler/goal/embedding startup they
// depend on.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// runServerCallLines parses gateway.go and returns, for each callee name of
// interest, the source line of its (first) call inside runServer.
func runServerCallLines(t *testing.T) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "gateway.go", nil, 0)
	if err != nil {
		t.Fatalf("parse gateway.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Name.Name == "runServer" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("func runServer not found")
	}

	lines := map[string]int{}
	record := func(name string, line int) {
		if _, seen := lines[name]; !seen {
			lines[name] = line
		}
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		line := fset.Position(call.Pos()).Line
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			record(fun.Name, line) // applyManagedChannelPlugins, wireSchedulerCallbacks
		case *ast.SelectorExpr:
			// Distinguish River's Start from the scheduler's by receiver.
			if fun.Sel.Name == "Start" {
				if inner, ok := fun.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "riverClient" {
					record("riverClient.Start", line)
				}
			}
			record(fun.Sel.Name, line) // SetAuthService, StartDispatchTick, StartBackfill, Run, Serve
		}
		return true
	})
	return lines
}

func TestRunServerStartsBackendsBeforeIngress(t *testing.T) {
	lines := runServerCallLines(t)

	mustHave := func(name string) int {
		line, ok := lines[name]
		if !ok {
			t.Fatalf("call %q not found in runServer", name)
		}
		return line
	}

	// Backend startup + static callbacks.
	setAuth := mustHave("SetAuthService")
	wireSched := mustHave("wireSchedulerCallbacks")
	riverStart := mustHave("riverClient.Start")
	goalTick := mustHave("StartDispatchTick")
	embedStart := mustHave("StartBackfill")

	// Ingress sources.
	groupRun := mustHave("Run") // groupDispatcher.Run
	channels := mustHave("applyManagedChannelPlugins")
	serve := mustHave("Serve") // httpSrv.Serve

	// 1. The scheduler OnJob handler and the notification auth directory must be
	//    wired before River starts (River may run a persisted job immediately).
	if wireSched >= riverStart {
		t.Errorf("wireSchedulerCallbacks (line %d) must precede riverClient.Start (line %d)", wireSched, riverStart)
	}
	if setAuth >= riverStart {
		t.Errorf("notifier.SetAuthService (line %d) must precede riverClient.Start (line %d)", setAuth, riverStart)
	}

	// 2. Every ingress source must come after every backend startup line.
	backendMax := maxInt(setAuth, wireSched, riverStart, goalTick, embedStart)
	ingressMin := minInt(groupRun, channels, serve)
	if backendMax >= ingressMin {
		t.Errorf("ingress must not start before backends: last backend line %d, first ingress line %d (SetAuthService=%d wireSchedulerCallbacks=%d riverClient.Start=%d StartDispatchTick=%d StartBackfill=%d | Run=%d applyManagedChannelPlugins=%d Serve=%d)",
			backendMax, ingressMin, setAuth, wireSched, riverStart, goalTick, embedStart, groupRun, channels, serve)
	}
}

func maxInt(xs ...int) int {
	m := xs[0]
	for _, x := range xs[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

func minInt(xs ...int) int {
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return m
}
