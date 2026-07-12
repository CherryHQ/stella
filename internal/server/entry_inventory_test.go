package server_test

// Non-OpenAPI authorization-entry inventory for issue #706 (stack 1 of #703).
//
// The /api surface is enumerable via the router (authorization_coverage_test.go).
// The remaining entry families are not all exposed through one registry, so this
// test uses two disciplines:
//
//   - ENUMERABLE registries (River durable workers, the builtin-tool list) are
//     mechanically walked; the live set must equal a frozen inventory, so a newly
//     registered worker/tool fails until classified.
//   - NON-ENUMERABLE grant points (the inbound webhook route; the channel
//     DM/group/dedicated grant decisions, which are plugin-fed and have no single
//     registry) are frozen as concrete (file, symbol) rows and AST-checked to
//     still exist; a rename/move fails so the inventory cannot silently rot.
//
// Every row carries the same target classification as the /api matrix (actor,
// action, resource, visibility, stack). Discovery ceiling: channel entries are
// enumerated by frozen source symbol, NOT by live registry, because concrete
// channels are provided by out-of-tree plugins and their per-message grant is
// decided in code, not a table.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/server"
)

type entryRow struct {
	Actor, Action, Resource, Visibility, Stack, Gate string
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	return root
}

func parseGoFile(t *testing.T, path string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f, fset
}

// funcDeclNames returns the set of declared function/method names in a file
// (methods keyed by "Recv.Name").
func funcDeclNames(f *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			t := fd.Recv.List[0].Type
			if star, ok := t.(*ast.StarExpr); ok {
				t = star.X
			}
			if id, ok := t.(*ast.Ident); ok {
				name = id.Name + "." + name
			}
		}
		names[name] = true
	}
	return names
}

// ---------------------------------------------------------------------------
// 1. Inbound webhook (POST /webhooks/{id}) — single hand-registered route.
// ---------------------------------------------------------------------------

var webhookEntry = entryRow{
	Actor: "SystemActor(webhook-ingress)", Action: "execute", Resource: "webhook_trigger",
	Visibility: "private(secret-authenticated)", Stack: "transports",
	Gate: "webhook id/secret + per-instance rate limit (not session/PAT)",
}

func TestWebhookEntryRegistered(t *testing.T) {
	// #712 Item 4 moved the webhook ingress mount out of internal/server's route
	// table and onto the HTTP root in the composition root, so it bypasses the
	// admin middleware chain and does its own PAT auth. The frozen entry inventory
	// tracks the route where it is now registered: gateway.go's outer mux
	// (rootMux.Handle("POST /webhooks/{id}", ...)).
	path := filepath.Join(moduleRoot(t), "cmd/stellad/gateway.go")
	f, _ := parseGoFile(t, path)
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok {
			if v := lit.Value; len(v) >= 2 && v[1:len(v)-1] == "POST /webhooks/{id}" {
				found = true
			}
		}
		return true
	})
	if !found {
		t.Errorf("webhook ingress route \"POST /webhooks/{id}\" is no longer registered in cmd/stellad/gateway.go; the non-HTTP entry inventory is stale — re-classify (%+v)", webhookEntry)
	}
}

// ---------------------------------------------------------------------------
// 2. River durable workers — ENUMERABLE registry.
// ---------------------------------------------------------------------------

var riverWorkerFiles = []string{
	"internal/embedding/river.go",
	"internal/scheduler/river.go",
	"internal/goal/river.go",
}

// riverWorkerInventory freezes every durable worker entry family and its target
// classification. A new river.AddWorker(...) not listed here fails.
var riverWorkerInventory = map[string]entryRow{
	"backfillWorker":    {Actor: "SystemActor(embedding-backfill)", Action: "execute", Resource: "embedding_job", Visibility: "private", Stack: "authz-core", Gate: "River queue insert authority (no per-request auth)"},
	"schedJobWorker":    {Actor: "AgentActor(scheduled)", Action: "execute", Resource: "scheduler_job", Visibility: "private", Stack: "authz-core", Gate: "reconstruct owner AgentActor from persisted job row"},
	"goalAttemptWorker": {Actor: "AgentActor(goal)", Action: "execute", Resource: "goal_attempt", Visibility: "private", Stack: "authz-core", Gate: "reconstruct owner/executor from persisted goal"},
	"goalTickWorker":    {Actor: "SystemActor(goal-tick)", Action: "execute", Resource: "goal_tick", Visibility: "private", Stack: "authz-core", Gate: "River queue insert authority (no per-request auth)"},
}

func collectRiverWorkers(t *testing.T) map[string]bool {
	got := map[string]bool{}
	for _, rel := range riverWorkerFiles {
		f, _ := parseGoFile(t, filepath.Join(moduleRoot(t), rel))
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "AddWorker" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "river" {
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			arg := call.Args[1]
			if u, ok := arg.(*ast.UnaryExpr); ok {
				arg = u.X
			}
			if clit, ok := arg.(*ast.CompositeLit); ok {
				if id, ok := clit.Type.(*ast.Ident); ok {
					got[id.Name] = true
				}
			}
			return true
		})
	}
	return got
}

func TestDurableWorkerInventory(t *testing.T) {
	got := collectRiverWorkers(t)
	if len(got) == 0 {
		t.Fatal("no river.AddWorker registrations found; the worker enumerator is not working")
	}
	for name := range got {
		if _, ok := riverWorkerInventory[name]; !ok {
			t.Errorf("new durable worker %q registered via river.AddWorker; classify it (target Actor/action/resource/visibility/stack) in riverWorkerInventory", name)
		}
	}
	for name := range riverWorkerInventory {
		if !got[name] {
			t.Errorf("stale worker-inventory entry %q no longer registered; remove it", name)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Builtin agent tools — ENUMERABLE list in the composition root.
//
// Ceiling: this covers the static builtin-tool list wired in commands.go. Tools
// injected at runtime via PoolManager.AddBuiltinTool (the plugin-tool path) are a
// separate entry family governed by the plugin-capability contract and are NOT
// enumerated here — documented, not hidden.
// ---------------------------------------------------------------------------

// builtinToolInventory freezes every agent builtin-tool entry family (keyed by
// its constructor descriptor: "pkg.Func" for a call, or the local ident for a
// pre-built tool). A new tool added to the list fails until classified.
var builtinToolInventory = map[string]entryRow{
	"memory.BuildTool":    {Actor: "AgentActor", Action: "write", Resource: "memory", Visibility: "private", Stack: "authz-core", Gate: "runtime authz.Identity (session-scoped writes)"},
	"notifyTool":          {Actor: "AgentActor", Action: "execute", Resource: "notify", Visibility: "private", Stack: "authz-core", Gate: "runtime authz.Identity"},
	"goal.NewTool":        {Actor: "AgentActor", Action: "execute", Resource: "goal", Visibility: "private", Stack: "authz-core", Gate: "goal.Service Authority PEP (#710)"},
	"scheduler.NewTool":   {Actor: "AgentActor", Action: "execute", Resource: "scheduler", Visibility: "private", Stack: "authz-core", Gate: "scheduler.Service Authority PEP (#710)"},
	"workflowpkg.NewTool": {Actor: "AgentActor", Action: "execute", Resource: "workflow", Visibility: "private", Stack: "authz-core", Gate: "workflow.Service Authority PEP (#710)"},
	"connections.NewTool": {Actor: "AgentActor", Action: "execute", Resource: "connections", Visibility: "private", Stack: "authz-core", Gate: "oauthToolAvailable gate"},
	"email.NewTool":       {Actor: "AgentActor", Action: "execute", Resource: "email", Visibility: "private", Stack: "authz-core", Gate: "emailToolAvailable gate"},
	"sharepkg.NewTool":    {Actor: "AgentActor", Action: "execute", Resource: "share", Visibility: "private", Stack: "authz-core", Gate: "runtime authz.Identity"},
	"recally.NewTool":     {Actor: "AgentActor", Action: "execute", Resource: "recally", Visibility: "private", Stack: "authz-core", Gate: "runtime authz.Identity"},
	"vault.NewTool":       {Actor: "AgentActor", Action: "execute", Resource: "vault", Visibility: "private", Stack: "authz-core", Gate: "runtime authz.Identity (vault capability resolver)"},
}

// toolValueDescriptor returns the frozen-inventory key for a BuiltinTool "Tool:"
// value: "pkg.Func" for a constructor call, or the ident name for a prebuilt var.
func toolValueDescriptor(v ast.Expr) string {
	if call, ok := v.(*ast.CallExpr); ok {
		v = call.Fun
	}
	switch e := v.(type) {
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name + "." + e.Sel.Name
		}
	case *ast.Ident:
		return e.Name
	}
	return ""
}

func toolDescriptorsFromLit(clit *ast.CompositeLit, out map[string]bool) {
	for _, elt := range clit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Tool" {
			continue
		}
		if d := toolValueDescriptor(kv.Value); d != "" {
			out[d] = true
		}
	}
}

func isBuiltinToolType(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "agent" && sel.Sel.Name == "BuiltinTool"
}

func TestBuiltinToolInventory(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "cmd/stellad/commands.go")
	f, _ := parseGoFile(t, path)
	got := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		clit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch typ := clit.Type.(type) {
		case *ast.SelectorExpr: // agent.BuiltinTool{Tool: ...}
			if isBuiltinToolType(typ) {
				toolDescriptorsFromLit(clit, got)
			}
		case *ast.ArrayType: // []agent.BuiltinTool{ {Tool: ...}, ... }
			if isBuiltinToolType(typ.Elt) {
				for _, elt := range clit.Elts {
					if inner, ok := elt.(*ast.CompositeLit); ok {
						toolDescriptorsFromLit(inner, got)
					}
				}
			}
		}
		return true
	})
	if len(got) == 0 {
		t.Fatal("no agent.BuiltinTool literals found; the builtin-tool enumerator is not working")
	}
	for d := range got {
		if _, ok := builtinToolInventory[d]; !ok {
			t.Errorf("new builtin agent tool %q registered in commands.go; classify it (target Actor/action/resource/visibility/stack) in builtinToolInventory", d)
		}
	}
	for d := range builtinToolInventory {
		if !got[d] {
			t.Errorf("stale builtin-tool inventory entry %q no longer registered; remove it", d)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Channel DM/group/dedicated grant — frozen source symbols (no live registry).
// ---------------------------------------------------------------------------

type channelSymbol struct {
	file, symbol string
	class        entryRow
}

var channelGrantInventory = []channelSymbol{
	{"internal/channel/coordinator.go", "Coordinator.handleResolvedIncoming", entryRow{
		Actor: "UserActor(channel-DM)/AgentActor", Action: "execute", Resource: "channel_dm_turn",
		Visibility: "private", Stack: "authz-core", Gate: "resolved chat identity + channel binding",
	}},
	{"internal/channel/agent_switch.go", "filterDedicatedAgents", entryRow{
		Actor: "UserActor", Action: "execute", Resource: "channel_dedicated_grant",
		Visibility: "private", Stack: "authz-core", Gate: "dedicated-channel/agent binding enforcement",
	}},
	{"internal/channel/agent_switch.go", "agentDedicatedToOtherChannel", entryRow{
		Actor: "UserActor", Action: "execute", Resource: "channel_dedicated_grant",
		Visibility: "private", Stack: "authz-core", Gate: "dedicated-channel/agent binding enforcement",
	}},
	{"internal/channel/group_dispatcher.go", "GroupDispatcher.Run", entryRow{
		Actor: "GroupIngressActor", Action: "execute", Resource: "group_turn",
		Visibility: "private", Stack: "authz-core", Gate: "durable group outbox lease + membership",
	}},
}

// TestEntryInventoryCatalogMapping proves every non-HTTP entry family's target
// actor/action/resource resolves onto the closed authz catalog (issue #707
// subphase A), using the same mapping bridge the /api route mapping uses. A new
// worker/tool/channel-grant/webhook whose target names an action/resource/actor
// outside the catalog fails here until the catalog is extended.
func TestEntryInventoryCatalogMapping(t *testing.T) {
	rows := []entryRow{webhookEntry}
	for _, r := range riverWorkerInventory {
		rows = append(rows, r)
	}
	for _, r := range builtinToolInventory {
		rows = append(rows, r)
	}
	for _, c := range channelGrantInventory {
		rows = append(rows, c.class)
	}
	for _, r := range rows {
		if act, ok := server.CatalogActionFor(r.Action); !ok || !act.Valid() {
			t.Errorf("entry actor=%q action %q has no valid authz.Action catalog member", r.Actor, r.Action)
		}
		if res, ok := server.CatalogResourceFor(r.Resource); !ok || !res.Valid() {
			t.Errorf("entry actor=%q resource %q has no valid authz.ResourceType catalog member", r.Actor, r.Resource)
		}
		kinds, ok := server.CatalogActorKindsFor(r.Actor)
		if !ok {
			t.Errorf("entry actor %q has no valid authz.ActorKind mapping", r.Actor)
			continue
		}
		for _, k := range kinds {
			if !k.Valid() {
				t.Errorf("entry actor %q mapped to invalid kind %v", r.Actor, k)
			}
		}
	}
}

func TestChannelGrantInventory(t *testing.T) {
	byFile := map[string]map[string]bool{}
	for _, sym := range channelGrantInventory {
		if _, ok := byFile[sym.file]; !ok {
			f, _ := parseGoFile(t, filepath.Join(moduleRoot(t), sym.file))
			byFile[sym.file] = funcDeclNames(f)
		}
		if !byFile[sym.file][sym.symbol] {
			t.Errorf("channel grant symbol %s in %s no longer exists; the frozen channel-entry inventory is stale — re-locate and re-classify (%+v)", sym.symbol, sym.file, sym.class)
		}
	}
}
