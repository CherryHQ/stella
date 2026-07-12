package server_test

// Architecture boundary tripwires for issue #706 (stack 1 of #703).
//
// These tests freeze the CURRENT transport-boundary debt of internal/server as
// exact allowlists and fail on any NEW instance. They are structural: they parse
// the package's own source with go/ast and match declarations/imports/selectors.
// They deliberately prove only what source structure can prove — they do not
// assert that any given check is semantically "correct authorization", only that
// no new symbol of a guarded shape appears outside its recorded allowlist.
//
// When a test fails it names the new forbidden symbol/file and the fix: route the
// capability through the composition root / a narrow port, or (if genuinely
// unavoidable) add a justified allowlist entry. Allowlists carry an unused-entry
// guard so they cannot rot once debt is paid down.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// serverFile is one parsed non-test .go file of the internal/server package.
type serverFile struct {
	rel  string
	file *ast.File
	fset *token.FileSet
}

// parseServerPackage parses every non-test .go file in the internal/server
// package directory (the test's working directory). Generated and build-ignored
// files are skipped.
func parseServerPackage(t *testing.T) []serverFile {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve package dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []serverFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(raw)
		if strings.Contains(src, "//go:build ignore") || strings.Contains(src, "// Code generated") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		out = append(out, serverFile{rel: name, file: f, fset: fset})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// isSelector reports whether e is the selector pkg.Name matched by the receiver
// identifier text. Used only for stdlib selectors (http.ResponseWriter,
// authz.Identity) whose package name is fixed by convention; internal-package
// selectors are matched via resolved local import names (see localImportName).
func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// localImportName returns the identifier a file uses for importPath, honoring an
// explicit alias; "" if the file does not import it. This defeats trivial alias
// evasion where a package is imported under a different name.
func localImportName(f *ast.File, importPath string) string {
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, "`\"") != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return importPath[strings.LastIndex(importPath, "/")+1:]
	}
	return ""
}

// ---------------------------------------------------------------------------
// Tripwire 1: no new Server wiring setters.
//
// Mutable-topology setters on *Server (Set*/Init* methods that are NOT OpenAPI
// operation handlers, i.e. take no http.ResponseWriter) are composition-time
// wiring. The target architecture replaces this post-construction mutation with
// constructor injection, so the set may only shrink. A new one fails here.
//
// Scope: *Server is the transport composition root and the unambiguous guarded
// type. pluginhost.Host has an adjacent service-injection setter surface
// (service_extensions.go), but that surface is governed by the separate
// plugin-capability contract phase of #703, not the transport boundary, so it is
// intentionally out of scope here rather than folded in with weaker signals.
// ---------------------------------------------------------------------------

// serverWiringSetterAllowlist is empty: issue #708 Section C replaced every
// post-construction *Server wiring setter with immutable constructor injection
// through server.Deps. A NEW Set*/Init* method (that is not an OpenAPI operation
// handler) must not reappear — inject it through server.New(ctx, Deps) instead.
var serverWiringSetterAllowlist = map[string]bool{}

func hasHTTPResponseWriterParam(ft *ast.FuncType) bool {
	if ft.Params == nil {
		return false
	}
	for _, p := range ft.Params.List {
		if isSelector(p.Type, "http", "ResponseWriter") {
			return true
		}
	}
	return false
}

func isServerReceiver(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return false
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Server"
}

func TestNoNewServerWiringSetters(t *testing.T) {
	unused := map[string]bool{}
	for k := range serverWiringSetterAllowlist {
		unused[k] = true
	}
	for _, sf := range parseServerPackage(t) {
		for _, decl := range sf.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !isServerReceiver(fd) {
				continue
			}
			name := fd.Name.Name
			if !strings.HasPrefix(name, "Set") && !strings.HasPrefix(name, "Init") {
				continue
			}
			if hasHTTPResponseWriterParam(fd.Type) {
				continue // OpenAPI operation handler, not a wiring setter
			}
			if serverWiringSetterAllowlist[name] {
				delete(unused, name)
				continue
			}
			pos := sf.fset.Position(fd.Pos())
			t.Errorf("%s:%d: new *Server wiring setter %q; inject it through server.New instead of a post-construction setter, or add a justified allowlist entry", sf.rel, pos.Line, name)
		}
	}
	for k := range unused {
		t.Errorf("stale wiring-setter allowlist entry %q is no longer present; remove it", k)
	}
}

// ---------------------------------------------------------------------------
// Tripwire 2: no new broad persistence capabilities in internal/server.
//
// Transports must reach persistence through narrow ports, not raw stores. Each
// file's set of broad-capability tokens (raw sqlc, raw pgxpool, config.Store,
// memory.SessionManager, raw auth stores, raw blob.Store) is frozen. A new
// (file, capability) pair fails.
// ---------------------------------------------------------------------------

const (
	capSQLC           = "sqlc"                  // import pkg/db/sqlc (raw query layer)
	capPgxpool        = "pgxpool"               // import pgx/v5/pgxpool (raw pool)
	capConfigStore    = "config.Store"          // raw config store handle
	capSessionManager = "memory.SessionManager" // raw memory session manager
	capAuthStore      = "auth.store"            // raw auth persistence stores
	capBlobStore      = "blob.Store"            // raw blob store handle
)

// serverPersistenceAllowlist records, per file, the broad persistence
// capabilities that file is permitted to reach directly today. server.go is the
// composition root that injects these into the Server struct.
var serverPersistenceAllowlist = map[string]map[string]bool{
	"server.go":          {capSQLC: true, capPgxpool: true, capConfigStore: true, capAuthStore: true},
	"agent_tools.go":     {capSQLC: true},
	"credential_wire.go": {capSQLC: true},
	"goals.go":           {capSQLC: true},
	"inbox.go":           {capSQLC: true},
	"groups.go":          {capSQLC: true},
	"oauth_wire.go":      {capSQLC: true},
	"projects.go":        {capSQLC: true},
	"profile.go":         {capSQLC: true},
	"shares.go":          {capSQLC: true},
	"workflows.go":       {capSQLC: true},
}

var authStoreTypes = map[string]bool{
	"AuthStore": true, "SessionStore": true, "CredentialStore": true,
	"UserStore": true, "LoginIdentityStore": true, "ChannelIdentityStore": true,
	"LinkCodeStore": true,
}

func fileCapabilities(sf serverFile) map[string]bool {
	caps := map[string]bool{}
	for _, imp := range sf.file.Imports {
		path := strings.Trim(imp.Path.Value, "`\"")
		switch path {
		case "github.com/CherryHQ/stella/pkg/db/sqlc":
			caps[capSQLC] = true
		case "github.com/jackc/pgx/v5/pgxpool":
			caps[capPgxpool] = true
		}
	}
	// Resolve local import names so an aliased import (e.g. cfg "…/internal/config")
	// cannot evade the selector match.
	configName := localImportName(sf.file, "github.com/CherryHQ/stella/internal/config")
	memoryName := localImportName(sf.file, "github.com/CherryHQ/stella/internal/memory")
	blobName := localImportName(sf.file, "github.com/CherryHQ/stella/internal/blob")
	authName := localImportName(sf.file, "github.com/CherryHQ/stella/internal/auth")
	ast.Inspect(sf.file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case configName != "" && id.Name == configName && sel.Sel.Name == "Store":
			caps[capConfigStore] = true
		case memoryName != "" && id.Name == memoryName && sel.Sel.Name == "SessionManager":
			caps[capSessionManager] = true
		case blobName != "" && id.Name == blobName && sel.Sel.Name == "Store":
			caps[capBlobStore] = true
		case authName != "" && id.Name == authName && authStoreTypes[sel.Sel.Name]:
			caps[capAuthStore] = true
		}
		return true
	})
	return caps
}

func TestNoNewServerPersistenceCapabilities(t *testing.T) {
	unused := map[string]map[string]bool{}
	for f, caps := range serverPersistenceAllowlist {
		unused[f] = map[string]bool{}
		for c := range caps {
			unused[f][c] = true
		}
	}
	for _, sf := range parseServerPackage(t) {
		for c := range fileCapabilities(sf) {
			if serverPersistenceAllowlist[sf.rel][c] {
				delete(unused[sf.rel], c)
				continue
			}
			t.Errorf("%s: new broad persistence capability %q in internal/server; use a narrow port or the composition root, or add a justified allowlist entry", sf.rel, c)
		}
	}
	for f, caps := range unused {
		for c := range caps {
			t.Errorf("stale persistence allowlist entry: %s no longer uses %q; remove it", f, c)
		}
	}
}

// ---------------------------------------------------------------------------
// Tripwire 3: workspace HTTP handlers stay transport-only.
//
// The workspace filesystem, sandbox path, and AssetStore semantics belong to
// internal/agent/session/access. These handlers may decode/encode HTTP and call typed
// sessionaccess use cases, but must not reach broad filesystem/config/sandbox
// capabilities directly.
// ---------------------------------------------------------------------------

func TestWorkspaceHandlersStayTransportOnly(t *testing.T) {
	var sessions serverFile
	for _, sf := range parseServerPackage(t) {
		if sf.rel == "sessions.go" {
			sessions = sf
			break
		}
	}
	if sessions.file == nil {
		t.Fatal("sessions.go not found")
	}

	workspaceHandlers := map[string]bool{
		"GetSessionWorkspace":        true,
		"CreateWorkspaceFile":        true,
		"DeleteWorkspaceFile":        true,
		"MoveWorkspaceFile":          true,
		"GetWorkspaceFileContent":    true,
		"UpdateWorkspaceFileContent": true,
		"UploadWorkspaceFile":        true,
	}
	forbiddenPkgs := map[string]map[string]bool{
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/agent"):         {"SetupUserWorkspace": true},
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/agent/sandbox"): {"UserDataViewFor": true, "WorkspaceViewFor": true},
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/config"):        {"StellaHome": true, "Store": true},
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/share"):         {"SafePath": true},
		"os":       {"Stat": true, "ReadFile": true, "MkdirAll": true, "Remove": true, "RemoveAll": true, "Rename": true, "WriteFile": true},
		"filepath": {"WalkDir": true, "Join": true, "Rel": true, "ToSlash": true, "Base": true, "Ext": true, "Clean": true},
	}
	delete(forbiddenPkgs, "")

	for _, decl := range sessions.file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || !workspaceHandlers[fd.Name.Name] {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && forbiddenPkgs[id.Name][sel.Sel.Name] {
				pos := sessions.fset.Position(sel.Pos())
				t.Errorf("%s:%d: workspace handler %s directly uses %s.%s; route it through internal/agent/session/access", sessions.rel, pos.Line, fd.Name.Name, id.Name, sel.Sel.Name)
			}
			if sel.Sel.Name == "assets" {
				pos := sessions.fset.Position(sel.Pos())
				t.Errorf("%s:%d: workspace handler %s directly uses Server.assets; route it through internal/agent/session/access", sessions.rel, pos.Line, fd.Name.Name)
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// Tripwire 4: transcript/context/summary HTTP handlers stay transport-only.
//
// These handlers may parse request params, call sessionaccess use cases, and map
// typed results to API DTOs. Raw sqlc/pgtype/query access belongs to
// internal/agent/session/access so the policy read and DB operations cannot drift.
// ---------------------------------------------------------------------------

func TestTranscriptHandlersStayTransportOnly(t *testing.T) {
	var sessions serverFile
	for _, sf := range parseServerPackage(t) {
		if sf.rel == "sessions.go" {
			sessions = sf
			break
		}
	}
	if sessions.file == nil {
		t.Fatal("sessions.go not found")
	}

	handlers := map[string]bool{
		"GetSessionMessages":     true,
		"GetSessionContextItems": true,
		"GetSessionSummary":      true,
	}
	for _, decl := range sessions.file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || !handlers[fd.Name.Name] {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok {
				switch {
				case id.Name == "sqlc" || id.Name == "pgtype":
					pos := sessions.fset.Position(sel.Pos())
					t.Errorf("%s:%d: %s directly uses %s.%s; route transcript persistence through internal/agent/session/access", sessions.rel, pos.Line, fd.Name.Name, id.Name, sel.Sel.Name)
				case id.Name == "s" && sel.Sel.Name == "q":
					pos := sessions.fset.Position(sel.Pos())
					t.Errorf("%s:%d: %s directly uses Server.q; route transcript persistence through internal/agent/session/access", sessions.rel, pos.Line, fd.Name.Name)
				}
			}
			return true
		})
	}
}

func TestSessionTransportHasNoBroadDomainCapabilities(t *testing.T) {
	var sessions serverFile
	for _, sf := range parseServerPackage(t) {
		if sf.rel == "sessions.go" {
			sessions = sf
			break
		}
	}
	if sessions.file == nil {
		t.Fatal("sessions.go not found")
	}
	forbiddenImports := map[string]bool{
		"github.com/CherryHQ/stella/pkg/db/sqlc":     true,
		"github.com/CherryHQ/stella/internal/memory": true,
		"github.com/CherryHQ/stella/internal/config": true,
		"github.com/CherryHQ/stella/internal/asset":  true,
	}
	for _, imp := range sessions.file.Imports {
		path := strings.Trim(imp.Path.Value, "`\"")
		if forbiddenImports[path] {
			t.Errorf("sessions.go imports broad domain capability %q; keep persistence and workspace behavior in internal/agent/session/access", path)
		}
	}
	ast.Inspect(sessions.file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && id.Name == "s" {
			switch sel.Sel.Name {
			case "q", "mem", "store", "assets", "poolManager", "checkSessionAccess":
				pos := sessions.fset.Position(sel.Pos())
				t.Errorf("sessions.go:%d uses Server.%s; route the use case through internal/agent/session/access", pos.Line, sel.Sel.Name)
			}
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// Tripwire 5: Send/SSE handlers stay transport-only.
//
// Live session orchestration belongs to internal/agent/session/access: durable lookup,
// runtime service resolution, send turn creation, attach subscription, and
// per-event reauthorization. These handlers may parse/encode HTTP and call the
// typed sessionaccess use cases only.
// ---------------------------------------------------------------------------

func TestSendAndStreamHandlersStayTransportOnly(t *testing.T) {
	var sessions serverFile
	for _, sf := range parseServerPackage(t) {
		if sf.rel == "sessions.go" {
			sessions = sf
			break
		}
	}
	if sessions.file == nil {
		t.Fatal("sessions.go not found")
	}

	handlers := map[string]bool{"SendSessionMessage": true, "StreamSessionEvents": true}
	memoryName := localImportName(sessions.file, "github.com/CherryHQ/stella/internal/memory")
	for _, decl := range sessions.file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || !handlers[fd.Name.Name] {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pos := sessions.fset.Position(sel.Pos())
			switch {
			case memoryName != "" && id.Name == memoryName && sel.Sel.Name == "SessionManager":
				t.Errorf("%s:%d: %s directly uses memory.SessionManager; route session lookup through internal/agent/session/access", sessions.rel, pos.Line, fd.Name.Name)
			case id.Name == "s" && (sel.Sel.Name == "mem" || sel.Sel.Name == "poolManager" || sel.Sel.Name == "checkSessionAccess"):
				t.Errorf("%s:%d: %s directly uses Server.%s; route live session orchestration through internal/agent/session/access", sessions.rel, pos.Line, fd.Name.Name, sel.Sel.Name)
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// Tripwire 6: system-prompt handler stays transport-only.
//
// The effective prompt is a session use case: session lookup, policy read,
// plugin prompt construction, skill prompt construction, and filesystem roots
// belong to internal/agent/session/access and its injected prompt collaborators.
// ---------------------------------------------------------------------------

func TestSystemPromptHandlerStaysTransportOnly(t *testing.T) {
	var sessions serverFile
	for _, sf := range parseServerPackage(t) {
		if sf.rel == "sessions.go" {
			sessions = sf
			break
		}
	}
	if sessions.file == nil {
		t.Fatal("sessions.go not found")
	}

	forbiddenImports := map[string]map[string]bool{
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/agent"):        {"SetupUserWorkspace": true},
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/agent/prompt"): {"BuildSystemPromptFromDB": true},
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/config"):       {"StellaHome": true, "Agent": true, "Store": true},
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/memory"):       {"SessionManager": true},
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/pluginhost"):   {"NewSkillStoreAdapter": true},
		localImportName(sessions.file, "github.com/CherryHQ/stella/internal/skills"):       {"BuildPromptSection": true},
		"os": {"UserHomeDir": true},
	}
	delete(forbiddenImports, "")

	for _, decl := range sessions.file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "GetSessionSystemPrompt" {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, _ := sel.X.(*ast.Ident)
			pos := sessions.fset.Position(sel.Pos())
			if id != nil && forbiddenImports[id.Name][sel.Sel.Name] {
				t.Errorf("%s:%d: system prompt handler directly uses %s.%s; route prompt construction through internal/agent/session/access", sessions.rel, pos.Line, id.Name, sel.Sel.Name)
			}
			if id != nil && id.Name == "s" {
				switch sel.Sel.Name {
				case "mem", "store", "pluginHost", "q", "skillStore", "projectRootForSession", "checkSessionAccess":
					t.Errorf("%s:%d: system prompt handler directly uses Server.%s; route prompt construction through internal/agent/session/access", sessions.rel, pos.Line, sel.Sel.Name)
				}
			}
			return true
		})
	}
}

// ---------------------------------------------------------------------------
// Tripwire 7: no new internal/server imports of platform plugin implementations.
//
// The transport layer must depend on plugin ports (pkg/plugins, pluginhost), not
// concrete plugins/* implementations. The current channel-registration debt is
// frozen per (file, plugin import path).
// ---------------------------------------------------------------------------

const pluginImplPrefix = "github.com/CherryHQ/stella/plugins/"

// serverPluginImportAllowlist is empty: issue #712 Item 4 removed every
// internal/server import of a platform plugin implementation. The webhook config
// decode now routes through pluginhost.DecodeWebhookRunConfig, and the WeChat
// QR/registration handlers route through the server.WeixinRegistrar port whose
// concrete adapter lives in the composition root. A NEW plugins/* import in any
// non-test internal/server file must not reappear — depend on the plugin port
// (pkg/plugins / pluginhost) or a narrow composition-root adapter instead.
var serverPluginImportAllowlist = map[string]map[string]bool{}

func TestNoServerPlatformPluginImports(t *testing.T) {
	unused := map[string]map[string]bool{}
	for f, paths := range serverPluginImportAllowlist {
		unused[f] = map[string]bool{}
		for p := range paths {
			unused[f][p] = true
		}
	}
	for _, sf := range parseServerPackage(t) {
		for _, imp := range sf.file.Imports {
			path := strings.Trim(imp.Path.Value, "`\"")
			if !strings.HasPrefix(path, pluginImplPrefix) {
				continue
			}
			if serverPluginImportAllowlist[sf.rel][path] {
				delete(unused[sf.rel], path)
				continue
			}
			pos := sf.fset.Position(imp.Pos())
			t.Errorf("%s:%d: internal/server imports platform plugin %q; depend on the plugin port (pkg/plugins / pluginhost), or add a justified allowlist entry", sf.rel, pos.Line, path)
		}
	}
	for f, paths := range unused {
		for p := range paths {
			t.Errorf("stale plugin-import allowlist entry: %s no longer imports %q; remove it", f, p)
		}
	}
}

// ---------------------------------------------------------------------------
// Tripwire 4: no new scattered resource-authorization identity constructors.
//
// The target authorization core centralizes request->authz.Identity mapping. The
// hand-rolled per-domain constructors (functions whose result type includes
// authz.Identity) are frozen. This proves only the structural "constructs an
// authz.Identity" shape, not that any check is semantically complete.
// ---------------------------------------------------------------------------

var authIdentityConstructorAllowlist = map[string]bool{}

func resultHasAuthzIdentity(ft *ast.FuncType) bool {
	if ft.Results == nil {
		return false
	}
	for _, r := range ft.Results.List {
		if isSelector(r.Type, "authz", "Identity") {
			return true
		}
	}
	return false
}

func TestNoNewResourceAuthIdentityConstructors(t *testing.T) {
	unused := map[string]bool{}
	for k := range authIdentityConstructorAllowlist {
		unused[k] = true
	}
	for _, sf := range parseServerPackage(t) {
		for _, decl := range sf.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !resultHasAuthzIdentity(fd.Type) {
				continue
			}
			name := fd.Name.Name
			if authIdentityConstructorAllowlist[name] {
				delete(unused, name)
				continue
			}
			pos := sf.fset.Position(fd.Pos())
			t.Errorf("%s:%d: new resource-authorization identity constructor %q returning authz.Identity; route authorization through the shared authorization core, or add a justified allowlist entry", sf.rel, pos.Line, name)
		}
	}
	for k := range unused {
		t.Errorf("stale authz-identity-constructor allowlist entry %q is no longer present; remove it", k)
	}
}

// ---------------------------------------------------------------------------
// Tripwire 5: no new scattered resource-authorization helpers.
//
// The target authorization core centralizes access decisions. The current
// scattered helpers are gate/ownership functions whose names follow the
// require*/authorize*/canAccess*/check*Access shapes. This freezes that exact set
// and fails on a new one (e.g. a `checkGoalAccess`). It complements — does not
// replace — the identity-constructor guard above, which catches a distinct shape
// (functions that build an authz.Identity, none of which match these name
// prefixes).
//
// Only UNEXPORTED helpers are considered: generated OpenAPI ServerInterface
// methods are exported (PascalCase) and so are excluded by construction, which is
// the explicit evidence for that exclusion. require*/authorize*/canAccess* names
// are resource/auth gates by convention; the one authentication-only helper
// (requireAuth) is kept in the allowlist rather than excluded, since freezing it
// is harmless and a new authn helper still warrants review.
// ---------------------------------------------------------------------------

// resourceAuthHelperAllowlist is the exact current set of scattered
// resource-authorization / gate helpers in internal/server. A new helper of the
// same shape must be added here (with justification) or, preferably, routed
// through the shared authorization core.
var resourceAuthHelperAllowlist = map[string]bool{
	"authorizeReadableDBSkills": true, // skills_scoped.go — routes DB-skill list reads through the skillaccess PEP
	"authorizeDBSkillRead":      true, // skills_scoped.go — routes a single DB-skill read through the skillaccess PEP
	"requireGroupOwner":         true, // groups.go — group ownership gate
	"requireAuth":               true, // middleware.go — authentication (not resource authz)
	"requireAdmin":              true, // middleware.go — admin gate
	"requireAgentAccess":        true, // skills_scoped.go — agent access gate
	"requireAgentUse":           true, // skills_scoped.go — agent execute gate
	"requireAgentManage":        true, // skills_scoped.go — agent manage gate
	"requireAgentDelete":        true, // skills_scoped.go — agent delete gate
	"requireAgentAction":        true, // skills_scoped.go — shared Agent PEP adapter
	"requireUserTarget":         true, // users.go — target-user admin gate
}

// isResourceAuthHelperName reports whether an unexported function name has one of
// the scattered-auth-helper shapes.
func isResourceAuthHelperName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false // exported (generated OpenAPI methods) or non-identifier
	}
	switch {
	case strings.HasPrefix(name, "require"):
		return true
	case strings.HasPrefix(name, "authorize"):
		return true
	case strings.HasPrefix(name, "canAccess"):
		return true
	case strings.HasPrefix(name, "check") && strings.HasSuffix(name, "Access"):
		return true
	}
	return false
}

func TestNoNewResourceAuthHelpers(t *testing.T) {
	unused := map[string]bool{}
	for k := range resourceAuthHelperAllowlist {
		unused[k] = true
	}
	for _, sf := range parseServerPackage(t) {
		for _, decl := range sf.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || !isResourceAuthHelperName(fd.Name.Name) {
				continue
			}
			name := fd.Name.Name
			if resourceAuthHelperAllowlist[name] {
				delete(unused, name)
				continue
			}
			pos := sf.fset.Position(fd.Pos())
			t.Errorf("%s:%d: new scattered resource-authorization helper %q; route the decision through the shared authorization core, or add a justified allowlist entry", sf.rel, pos.Line, name)
		}
	}
	for k := range unused {
		t.Errorf("stale resource-auth-helper allowlist entry %q is no longer present; remove it", k)
	}
}
