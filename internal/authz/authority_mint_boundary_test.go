package authz_test

// Architecture boundary tripwire for #707: Authority issuance.
//
// An authz.Authority is the trusted, immutable capability every policy decision
// is evaluated against. Its constructors (NewUserAuthority / NewChannelAuthority /
// NewAgentAuthority / NewGroupAgentAuthority / NewSystemAuthority) are the ONLY
// way to mint one, so whoever may call them is exactly whoever the system trusts
// to assert identity. That set must stay small and reviewed: a transport,
// request-payload, channel-message, or plugin package that could mint an
// Authority from attacker-influenced data would defeat the whole model.
//
// This guard freezes the exact set of production packages permitted to mint an
// Authority and fails on any new callsite outside it. Adding a package here is a
// deliberate, reviewed act — which is the point. It walks the whole module,
// resolves the internal/authz import's local name per file (so an alias cannot
// evade it), and references each constructor symbol (stale guard) so a rename
// fails loudly here instead of silently disabling the check.
//
// The frozen initial allowlist is the set of trusted identity adapters that mint
// an Authority today. As the migration lands the remaining trusted producers
// (a channel-ingress adapter, the durable-worker adapter), each new minting
// package is added here in the same change that introduces it — never silently.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

// Stale guards: if any constructor is renamed or removed, these references stop
// compiling and the boundary test fails loudly instead of going vacuous.
var (
	_ = authz.NewUserAuthority
	_ = authz.NewChannelAuthority
	_ = authz.NewAgentAuthority
	_ = authz.NewGroupAgentAuthority
	_ = authz.NewSystemAuthority
)

// authorityConstructors is the closed set of Authority-minting functions.
var authorityConstructors = map[string]bool{
	"NewUserAuthority":       true,
	"NewChannelAuthority":    true,
	"NewAgentAuthority":      true,
	"NewGroupAgentAuthority": true,
	"NewSystemAuthority":     true,
}

// authorityMintAllowset is the FROZEN initial allowlist: the exact production
// package directories permitted to mint an Authority. Each entry is a trusted
// identity/credential adapter; a transport or request-payload package must never
// appear here.
var authorityMintAllowset = map[string]string{
	"internal/authz":        "defines the constructors and the runtime Identity→Authority adapter (adapt.go)",
	"internal/auth":         "auth.Subject session → Authority adapter (authority.go)",
	"internal/credential":   "credential.Principal → Authority adapter (authority.go)",
	"internal/agent/access": "trusted worker/group authority adapter; durable webhook-capability reconstruction; transports receive only its Authority value",
}

const authzImportPath = "github.com/CherryHQ/stella/internal/authz"

// authzLocalName returns the identifier a file uses for the internal/authz
// import (honoring an explicit alias), or "" if it does not import it.
func authzLocalName(f *ast.File) string {
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, "`\"") != authzImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "authz"
	}
	return ""
}

// mintsAuthority reports whether a file calls an Authority constructor through
// its internal/authz import (honoring an alias). It is the shared predicate used
// by both the real-tree walk and the counterexample below.
func mintsAuthority(f *ast.File) bool {
	local := authzLocalName(f)
	if local == "" {
		return false
	}
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != local {
			return true
		}
		if authorityConstructors[sel.Sel.Name] {
			found = true
		}
		return true
	})
	return found
}

func TestAuthorityMintersRestrictedToTrustedAdapters(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}

	var offenders []string
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
		// Only production Go files. Tests are exempt: they legitimately construct
		// authorities to test the authorization boundary.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))
		if _, ok := authorityMintAllowset[dir]; ok {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, raw, 0)
		if err != nil {
			return err
		}
		if mintsAuthority(f) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	for _, file := range offenders {
		t.Errorf("%s: mints an authz.Authority outside the frozen trusted-adapter allowlist; "+
			"mint an Authority only in a trusted identity/credential adapter (auth/credential/channel/runtime/worker), "+
			"never in a transport or request-payload package", file)
	}
}

// TestAuthorityMintTripwireCatchesCounterexample proves the guard is not vacuous:
// a synthetic transport package that mints an Authority is both detected as a
// minter and rejected by the allowlist. If this ever stops flagging, the real
// walk above would silently pass anything.
func TestAuthorityMintTripwireCatchesCounterexample(t *testing.T) {
	const offendingSrc = `package server

import "github.com/CherryHQ/stella/internal/authz"

// Simulated abuse: a transport handler mints an Authority straight from a
// request-supplied user id. This must be caught.
func handle(userID string) {
	_, _ = authz.NewUserAuthority(authz.UserID(userID), false)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "internal/server/handler.go", offendingSrc, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	if !mintsAuthority(f) {
		t.Fatal("counterexample not detected as an Authority minter — the tripwire is vacuous")
	}
	if _, allowed := authorityMintAllowset["internal/server"]; allowed {
		t.Fatal("internal/server must not be on the Authority-mint allowlist")
	}

	// And an aliased import must not evade detection.
	const aliasedSrc = `package server

import az "github.com/CherryHQ/stella/internal/authz"

func handle2() { _, _ = az.NewSystemAuthority("x") }
`
	fa, err := parser.ParseFile(fset, "internal/server/handler2.go", aliasedSrc, 0)
	if err != nil {
		t.Fatalf("parse aliased source: %v", err)
	}
	if !mintsAuthority(fa) {
		t.Fatal("aliased Authority mint evaded detection")
	}
}
