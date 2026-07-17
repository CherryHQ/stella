package server

// Section C (issue #708) boundary tests for the immutable, validated server
// dependency set.
//
//   - TestServerNewReadsNoEnvConstructsNoService (AST): Server.New must only wire
//     injected Deps — it reads no environment and constructs no service. It flags
//     BOTH selector calls (connections.NewService) and same-package identifier
//     calls whose name is constructor-shaped (New*/new*), except a small allowlist
//     of language/runtime constructors New legitimately needs (a query handle over
//     the injected pool, a mux, the readiness probe, etc.).
//   - TestDepsBroadCapabilityFieldsFrozen is terminal: server.Deps cannot carry
//     a broad persistence capability or hide one through nesting. Persistence
//     belongs behind an application service; `DBPinger` is the sole narrow probe
//     port.

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"maps"
	"sort"
	"strings"
	"testing"
)

// parseServerGo parses internal/server/server.go (the file that declares New,
// Deps, and OIDCDeps).
func parseServerGo(t *testing.T) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	return f, fset
}

// ---------------------------------------------------------------------------
// New reads no env, constructs no service.
// ---------------------------------------------------------------------------

// envReadsForbiddenInNew are always rejected inside New regardless of shape.
var envReadsForbiddenInNew = map[string]bool{
	"os.Getenv":    true,
	"os.LookupEnv": true,
}

// constructorsAllowedInNew is the exact set of constructor-shaped calls New may
// make: language/runtime primitives and same-package presentation/wiring
// helpers that build no domain service. Anything else whose callee name starts
// with New/new (a selector like connections.NewService, or a same-package
// identifier constructor) fails — those instances must be built by the
// composition root and injected.
var constructorsAllowedInNew = map[string]bool{
	"auth.NewRateLimiter":           true, // in-memory rate limiter, not a domain service
	"newWebhookLimiter":             true, // in-memory limiter, same package
	"http.NewServeMux":              true, // runtime mux
	"newReadiness":                  true, // readiness probe over the injected pool
	"newRecallyHandlersWithService": true, // presentation handler over the INJECTED recally service
}

// calleeKey returns "pkg.Name" for a selector call, "Name" for a same-package
// identifier call, or "" for anything else (method calls, etc.).
func calleeKey(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name + "." + e.Sel.Name
		}
	}
	return ""
}

// isConstructorShaped reports whether the final name segment looks like a
// constructor (New… or new…), the shape used to build a value.
func isConstructorShaped(key string) bool {
	last := key
	if i := strings.LastIndex(key, "."); i >= 0 {
		last = key[i+1:]
	}
	return strings.HasPrefix(last, "New") || strings.HasPrefix(last, "new")
}

func TestServerNewReadsNoEnvConstructsNoService(t *testing.T) {
	f, fset := parseServerGo(t)
	var newFn *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Name.Name == "New" {
			newFn = fd
			break
		}
	}
	if newFn == nil {
		t.Fatal("func New not found in server.go")
	}
	ast.Inspect(newFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		key := calleeKey(call.Fun)
		if key == "" {
			return true
		}
		pos := fset.Position(call.Pos()).Line
		if envReadsForbiddenInNew[key] {
			t.Errorf("%d: server.New reads the environment via %q; read config at the startup boundary and inject it", pos, key)
			return true
		}
		if isConstructorShaped(key) && !constructorsAllowedInNew[key] {
			t.Errorf("%d: server.New constructs %q; build shared services in the composition root and inject them via Deps (allowed constructors: %v)", pos, key, sortedKeys(constructorsAllowedInNew))
		}
		return true
	})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Deps broad-capability field freeze.
// ---------------------------------------------------------------------------

// depsBroadMarkers are broad-capability type markers forbidden on server.Deps.
// The presence of any marker in a field's source-rendered type makes it broad.
var depsBroadMarkers = []string{
	"pgxpool.Pool",
	"config.Store",
	"memory.Provider",
	"auth.AuthStore",
	"auth.UserStore",
	"auth.SessionStore",
	"auth.CredentialStore",
	"auth.LoginIdentityStore",
	"auth.ChannelIdentityStore",
}

// depsForbiddenForever are broad capabilities that must NEVER appear on Deps,
// even as new debt: a raw query set, a blob store, or the memory session
// manager. Adding one fails immediately.
var depsForbiddenForever = []string{
	"sqlc.Queries",
	"blob.Store",
	"memory.SessionManager",
}

// currentBroadDeps is empty by design. A new broad field fails immediately;
// there is no remaining server persistence debt. Deps.Pinger is a narrow
// DBPinger liveness port, not a broad capability.
var currentBroadDeps = map[string]string{}

func renderType(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return ""
	}
	return buf.String()
}

// collectStructDecls returns every top-level struct type declared in the file,
// keyed by type name.
func collectStructDecls(f *ast.File) map[string]*ast.StructType {
	out := map[string]*ast.StructType{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				out[ts.Name.Name] = st
			}
		}
	}
	return out
}

// broadSignature returns the sorted comma-joined broad markers present in a
// rendered type string, "" if none.
func broadSignature(typeStr string) string {
	var found []string
	for _, m := range depsBroadMarkers {
		if strings.Contains(typeStr, m) {
			found = append(found, m)
		}
	}
	sort.Strings(found)
	return strings.Join(found, ",")
}

func TestDepsBroadCapabilityFieldsFrozen(t *testing.T) {
	f, fset := parseServerGo(t)
	structs := collectStructDecls(f)
	deps, ok := structs["Deps"]
	if !ok {
		t.Fatal("type Deps not found in server.go")
	}

	got := map[string]string{}
	pgxpoolFields := 0

	var walk func(st *ast.StructType, prefix string)
	walk = func(st *ast.StructType, prefix string) {
		for _, field := range st.Fields.List {
			// Recurse into a locally-declared nested struct (e.g. OIDCDeps) so new
			// broad fields cannot hide behind a grouping struct.
			if id, ok := field.Type.(*ast.Ident); ok {
				if nested, ok := structs[id.Name]; ok {
					for _, name := range field.Names {
						walk(nested, prefix+name.Name+".")
					}
					continue
				}
			}
			typeStr := renderType(fset, field.Type)
			for _, forbidden := range depsForbiddenForever {
				if strings.Contains(typeStr, forbidden) {
					for _, name := range field.Names {
						t.Errorf("server.Deps field %s%s has forbidden broad type %q; this capability may never be a Deps field — route it through a narrow domain port", prefix, name.Name, forbidden)
					}
				}
			}
			sig := broadSignature(typeStr)
			if sig == "" {
				continue
			}
			if strings.Contains(typeStr, "pgxpool.Pool") {
				pgxpoolFields++
			}
			for _, name := range field.Names {
				got[prefix+name.Name] = sig
			}
		}
	}
	walk(deps, "")

	if pgxpoolFields > 1 {
		t.Errorf("server.Deps carries %d *pgxpool.Pool fields; there must be at most one shared pool", pgxpoolFields)
	}

	remaining := map[string]string{}
	maps.Copy(remaining, currentBroadDeps)
	for path, sig := range got {
		want, ok := currentBroadDeps[path]
		if !ok {
			t.Errorf("server.Deps has a NEW broad-capability field %q (%s); broad persistence must go through a narrow port, not a new Deps field", path, sig)
			continue
		}
		if want != sig {
			t.Errorf("server.Deps field %q broad signature changed to %q, frozen as %q; do not widen documented debt", path, sig, want)
		}
		delete(remaining, path)
	}
	for path := range remaining {
		t.Errorf("stale broad-Deps entry %q; remove it: server.Deps admits no broad persistence capability", path)
	}
}
