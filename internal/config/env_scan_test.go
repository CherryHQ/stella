package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nonLiteralRead is the allowlist key for an os.Getenv/os.LookupEnv call whose
// argument is not a plain string literal (a const name, a prefix concatenation,
// or a computed key). Such reads are dynamic prefix/selection scans that a
// centralized config cannot own without inventing structure it does not have.
const nonLiteralRead = "<non-literal>"

// envReadAllowlist is the approved set of direct environment reads that remain
// after issue #701 centralized the server's boot/setup variables onto
// ServerConfig. Every entry is a deliberate exception, justified by the comment
// on its group below. A new (file, variable) read not listed here fails this
// test: either route it through ServerConfig, or add it here with a comment
// explaining why it cannot be.
//
// Keys are module-relative file paths; values are the env names that file may
// read directly, or nonLiteralRead for a dynamic-argument read.
var envReadAllowlist = map[string]map[string]bool{
	// Logging is configured before ServerConfig is parsed, so main passes the raw
	// value to the (now pure) ParseLogLevel.
	"cmd/stellad/main.go": {"LOG_LEVEL": true},

	// Bootstrap: STELLA_HOME locates the home dir (and thus $STELLA_HOME/.env),
	// so it must be readable before and independent of ServerConfig.
	"internal/config/paths.go": {"STELLA_HOME": true},
	"internal/cli/dotenv.go":   {"STELLA_HOME": true, nonLiteralRead: true},

	// Per-call lenient selection: the sandbox backend is chosen per operation
	// from the live plugin set, not fixed at boot.
	"internal/config/sandbox_env.go": {nonLiteralRead: true},

	// Internal testing escape hatch (STELLA_POSTGRES_RUNTIME) for exercising a
	// Stella-built postgres runtime; not an operator-facing knob.
	"internal/db/pgruntime.go": {nonLiteralRead: true},

	// The multi-provider OAuth block (AUTH_OAUTH_*) and local password auth
	// (LOCAL_PASSWORD_/LOCAL_OIDC_) are now parsed through oidc.LoadLoginConfig at
	// the startup boundary (serverAction passes os.LookupEnv), so the oidc package
	// reads no environment of its own — no allowlist entry is needed here.

	// Standard OpenTelemetry SDK variables, owned by the OTEL spec/SDK, not by
	// stella; mirroring them onto ServerConfig would fork their semantics.
	"internal/observability/observability.go": {
		"OTEL_EXPORTER_OTLP_ENDPOINT": true,
		"OTEL_EXPORTER_OTLP_INSECURE": true,
		"OTEL_SERVICE_NAME":           true,
		"OTEL_SDK_DISABLED":           true,
		nonLiteralRead:                true,
	},
	"pkg/httpclient/httpclient.go": {
		"OTEL_EXPORTER_OTLP_ENDPOINT": true,
		"OTEL_SDK_DISABLED":           true,
	},

	// Per-request: trusted-proxy CIDRs are consulted on every inbound request to
	// resolve the client IP, not cached at boot.
	"internal/server/oidc.go": {"STELLA_TRUSTED_PROXIES": true},

	// Per-fetch: clawhub token/URL are read each skill fetch so an operator can
	// rotate them without a restart.
	"internal/skills/clawhub.go": {"CLAWHUB_TOKEN": true, "CLAWHUB_URL": true},

	// Dead-in-production loader: real email config is vault-scoped per user
	// (internal/email/service.go); LoadFromEnv has no production caller and must
	// keep its unset-vs-empty distinction, which the normalized ServerConfig
	// deliberately collapses.
	"internal/email/config.go": {"EMAIL_CONFIG": true},

	// Dynamic per-key reads over a computed key set.
	"internal/manifestplugins/mise_installer.go": {nonLiteralRead: true},
	"internal/store/dbstore.go":                  {nonLiteralRead: true},

	// Host environment passthrough for the sandbox: PATH and selected host vars
	// are forwarded into the sandbox, not stella configuration.
	"internal/agent/sandbox/bash.go": {"PATH": true},
	"pkg/sandbox/hostenv.go":         {"PATH": true, nonLiteralRead: true},

	// pkg/ must not import internal/config. These are per-call diagnostic/tuning
	// reads local to reusable packages.
	"pkg/agent/llm_dump.go": {"STELLA_HOME": true, nonLiteralRead: true},
	"pkg/tools/truncate.go": {"STELLA_TOOL_MAX_LINES": true, "STELLA_TOOL_MAX_BYTES": true},

	// Plugins do not import internal/config. Per-message render read (feishu) and
	// docker-sandbox host wiring stay local to their plugin.
	"plugins/channels/feishu/references.go": {"STELLA_BASE_URL": true},
	"plugins/sandbox/docker/dood.go": {
		"STELLA_HOME_HOST":          true,
		"STELLA_HOME_VOLUME":        true,
		"STELLA_SANDBOX_NETWORK":    true,
		"STELLA_SANDBOX_SERVER_URL": true,
		nonLiteralRead:              true,
	},
}

// TestNoUnapprovedEnvReads is the finish-line guard for issue #701: after the
// migration, a direct os.Getenv/os.LookupEnv in non-test code is a regression
// unless it is a documented exception. It walks every non-test, compiled .go
// file in the module and fails on any (file, variable) read not in
// envReadAllowlist.
//
// Known limits (deliberate; tighten if they ever bite): variable names are
// matched only for string-literal arguments — const or computed names collapse
// to <non-literal>, so a file allowlisted for one dynamic read is not re-checked
// for new dynamic reads; and the receiver is matched by identifier name ("os"),
// not import binding, so an aliased stdlib import would evade the scan.
func TestNoUnapprovedEnvReads(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	// unused tracks allowlist entries never hit, so the allowlist cannot rot with
	// stale exceptions once a read is removed or migrated.
	unused := map[string]map[string]bool{}
	for file, vars := range envReadAllowlist {
		unused[file] = map[string]bool{}
		for v := range vars {
			unused[file][v] = true
		}
	}

	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "dist": true, ".agents": true,
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		// Build-ignored generators (e.g. resources/binaries/gen.go) are not part
		// of the module build, so their env reads are irrelevant to the server.
		if strings.Contains(src, "//go:build ignore") {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isEnvRead(call.Fun) || len(call.Args) == 0 {
				return true
			}
			name := envArgName(call.Args[0])
			allowed := envReadAllowlist[rel]
			if allowed[name] {
				delete(unused[rel], name)
				return true
			}
			pos := fset.Position(call.Pos())
			if name == nonLiteralRead {
				t.Errorf("%s:%d: unapproved dynamic env read; route through ServerConfig or add a justified allowlist entry", rel, pos.Line)
			} else {
				t.Errorf("%s:%d: unapproved env read %q; route through ServerConfig or add a justified allowlist entry", rel, pos.Line, name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}

	for file, vars := range unused {
		for v := range vars {
			t.Errorf("stale env-read allowlist entry: %s reads %q is no longer present; remove it", file, v)
		}
	}
}

// isEnvRead reports whether fun is a call to os.Getenv or os.LookupEnv.
func isEnvRead(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "os" {
		return false
	}
	return sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv"
}

// envArgName returns the env variable name for a string-literal argument, or
// nonLiteralRead for any computed argument (const name, concatenation, ...).
func envArgName(arg ast.Expr) string {
	lit, ok := arg.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nonLiteralRead
	}
	return strings.Trim(lit.Value, "`\"")
}
