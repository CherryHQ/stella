package platform

// Admission tripwire for internal/platform: the infrastructure layer.
//
// internal/platform holds what the rest of the repo stands on and what knows
// nothing about the rest: config, the STELLA_HOME layout, blob storage,
// observability, CLI output plumbing, diagnostics, build version, the bundled
// xberg CLI. The layering is one-way by construction — domain packages import
// platform, platform never imports a domain package — and this test is what
// makes that machine-checked instead of a convention.
//
// A package earns a place in platform only if it can live under this rule:
// internal/platform/** may import stdlib, third-party modules,
// github.com/CherryHQ/stella/pkg/** and other internal/platform/** — nothing
// else inside the repo. _test.go files may additionally import
// internal/db/dbtest (the embedded-PostgreSQL harness): a test-only edge to a
// test-only package creates no production dependency.
//
// If a candidate needs anything beyond that it is a domain package with
// infrastructure flavour and it stays where it is. internal/db is the worked
// example: internal/db/authstore.go implements internal/auth's store types, so
// db depends on the auth domain.

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

const stellaModulePrefix = "github.com/CherryHQ/stella/"

// platformAllowedRepoImports is the closed whitelist of in-repo import paths
// (and path prefixes) internal/platform may depend on. Adding an entry is a
// deliberate, reviewed act — it widens what "infrastructure" means for the
// whole repo.
var platformAllowedRepoImports = []string{
	"pkg/",               // plugin-facing contract surface
	"internal/platform/", // sibling infrastructure packages
}

// platformAllowedTestOnlyRepoImports may appear in _test.go files only.
var platformAllowedTestOnlyRepoImports = []string{
	"internal/db/dbtest", // embedded-PostgreSQL harness; test-only, no production edge
}

// forbiddenRepoImports returns the in-repo imports of a file that fall outside
// the platform whitelist. Imports outside the module (stdlib, third-party) are
// not constrained by this rule. isTest widens the whitelist by the test-only
// entries.
func forbiddenRepoImports(f *ast.File, isTest bool) []string {
	allowed := platformAllowedRepoImports
	if isTest {
		allowed = append(append([]string{}, allowed...), platformAllowedTestOnlyRepoImports...)
	}
	var out []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, "`\"")
		if !strings.HasPrefix(path, stellaModulePrefix) {
			continue
		}
		rel := strings.TrimPrefix(path, stellaModulePrefix)
		ok := false
		for _, prefix := range allowed {
			if rel == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(rel, prefix) {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, path)
		}
	}
	return out
}

func TestPlatformBoundaryImportsStayInsideInfrastructureWhitelist(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve platform dir: %v", err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		for _, bad := range forbiddenRepoImports(f, strings.HasSuffix(path, "_test.go")) {
			t.Errorf("internal/platform/%s imports %s, outside the infrastructure whitelist %v; either the dependency is wrong or the package does not belong in platform", filepath.ToSlash(rel), bad, platformAllowedRepoImports)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk internal/platform: %v", err)
	}
}

// TestPlatformBoundaryTripwireCatchesCounterexample proves the guard is not
// vacuous: a synthetic infrastructure file reaching into a domain package must
// be flagged, a legitimate one must not, and the dbtest carve-out must stay
// test-only.
func TestPlatformBoundaryTripwireCatchesCounterexample(t *testing.T) {
	const offending = `package home

import (
	"context"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/pkg/tools"
)

var _ = context.Background
var _ = agent.Service{}
var _ = tools.Tool{}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "internal/platform/home/offender.go", offending, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	got := forbiddenRepoImports(f, false)
	if len(got) != 1 || got[0] != "github.com/CherryHQ/stella/internal/agent" {
		t.Fatalf("counterexample not detected, got %v — the tripwire is vacuous", got)
	}

	const allowed = `package observability

import (
	"github.com/CherryHQ/stella/internal/platform/cli"
	"github.com/CherryHQ/stella/internal/platform/version"
	"github.com/CherryHQ/stella/pkg/ai"
)

var (
	_ = cli.Env{}
	_ = version.Version
	_ = ai.Message{}
)
`
	fa, err := parser.ParseFile(fset, "internal/platform/observability/observability.go", allowed, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse allowed source: %v", err)
	}
	if bad := forbiddenRepoImports(fa, false); len(bad) != 0 {
		t.Fatalf("whitelisted imports rejected: %v", bad)
	}

	const testOnly = `package home_test

import "github.com/CherryHQ/stella/internal/db/dbtest"

var _ = dbtest.Main
`
	ft, err := parser.ParseFile(fset, "internal/platform/home/root_test.go", testOnly, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse test-only source: %v", err)
	}
	if bad := forbiddenRepoImports(ft, true); len(bad) != 0 {
		t.Fatalf("dbtest rejected in a _test.go file: %v", bad)
	}
	if bad := forbiddenRepoImports(ft, false); len(bad) != 1 {
		t.Fatalf("dbtest must stay test-only, got %v in a non-test file", bad)
	}
}
