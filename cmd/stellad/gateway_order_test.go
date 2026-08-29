package main

// Issue #708 lifecycle contract: no late-bind / ingress window. This AST guard
// freezes the source ordering inside runServer so a future edit cannot move an
// ingress source (HTTP Serve, group-dispatch Run, managed channel startup) ahead
// of the static callbacks and River/scheduler/goal/embedding/media startup they
// need.

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

func TestObservabilityInitializesBeforeSetup(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "gateway.go", nil, 0)
	if err != nil {
		t.Fatalf("parse gateway.go: %v", err)
	}
	var initLine, setupLine int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "setup" && setupLine == 0 {
			setupLine = fset.Position(call.Pos()).Line
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "Init" {
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "observability" && initLine == 0 {
				initLine = fset.Position(call.Pos()).Line
			}
		}
		return true
	})
	if initLine == 0 || setupLine == 0 {
		t.Fatalf("observability.Init=%d setup=%d", initLine, setupLine)
	}
	if initLine >= setupLine {
		t.Fatalf("observability.Init line %d must precede setup line %d", initLine, setupLine)
	}
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
	nudgeBind := mustHave("Bind")
	nudgePeriodic := mustHave("StartPeriodic")
	riverStart := mustHave("riverClient.Start")
	goalTick := mustHave("StartDispatchTick")
	embedStart := mustHave("StartBackfill")
	mediaSweep := mustHave("StartOrphanSweep")

	// Ingress sources.
	groupRun := mustHave("Run")                        // groupDispatcher.Run (first Run in source)
	channels := mustHave("applyManagedChannelPlugins") // managed channel startup
	serve := mustHave("Serve")                         // httpSrv.Serve

	// 1. The scheduler OnJob handler and the notification auth directory must be
	//    wired before River starts (River may run a persisted job immediately).
	if wireSched >= riverStart {
		t.Errorf("wireSchedulerCallbacks (line %d) must precede riverClient.Start (line %d)", wireSched, riverStart)
	}
	if setAuth >= riverStart {
		t.Errorf("notifier.SetAuthService (line %d) must precede riverClient.Start (line %d)", setAuth, riverStart)
	}
	if nudgeBind >= riverStart || nudgePeriodic >= riverStart {
		t.Errorf("group nudge bind/periodic must precede riverClient.Start (bind=%d periodic=%d start=%d)", nudgeBind, nudgePeriodic, riverStart)
	}

	// 2. Every ingress source must come after every backend startup line.
	backendMax := maxInt(setAuth, wireSched, riverStart, goalTick, embedStart, mediaSweep)
	ingressMin := minInt(groupRun, channels, serve)
	if backendMax >= ingressMin {
		t.Errorf("ingress must not start before backends: last backend line %d, first ingress line %d (SetAuthService=%d wireSchedulerCallbacks=%d riverClient.Start=%d StartDispatchTick=%d StartBackfill=%d StartOrphanSweep=%d | Run=%d applyManagedChannelPlugins=%d Serve=%d)",
			backendMax, ingressMin, setAuth, wireSched, riverStart, goalTick, embedStart, mediaSweep, groupRun, channels, serve)
	}
}

func TestSharedRiverClientRegistersGroupNudgeWorker(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "commands.go", nil, 0)
	if err != nil {
		t.Fatalf("parse commands.go: %v", err)
	}
	registered := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "buildSharedRiverClient" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "RegisterGroupNudgeWorker" {
				registered = true
			}
			return true
		})
	}
	if !registered {
		t.Fatal("buildSharedRiverClient must register GroupNudgeWorker before constructing River")
	}
}

func TestSetupRecoversSessionInboxAfterAgentsStart(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "commands.go", nil, 0)
	if err != nil {
		t.Fatalf("parse commands.go: %v", err)
	}
	lines := map[string]int{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "setup" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && (selector.Sel.Name == "StartAll" || selector.Sel.Name == "Recover") {
				lines[selector.Sel.Name] = fset.Position(call.Pos()).Line
			}
			return true
		})
	}
	if lines["StartAll"] == 0 || lines["Recover"] == 0 {
		t.Fatalf("setup startup calls = %v, want StartAll and Recover", lines)
	}
	if lines["StartAll"] >= lines["Recover"] {
		t.Fatalf("PoolManager.StartAll line %d must precede Session inbox Recover line %d", lines["StartAll"], lines["Recover"])
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
