package core

// Admission tripwire for internal/core: the leaf kernel.
//
// internal/core holds the types every other internal package needs and that
// need almost nothing back — tool metadata, agent context keys, sentinel
// errors, capability/access checks, provider credentials. Keeping them here
// (rather than as leaf subpackages of internal/agent) is what removes the
// top-level agent ↔ {memory,vault,connections,observability} bidirectional
// edges: those packages depended on agent's leaves, never on agent itself.
//
// A package earns a place in core only if it can live under this rule:
// internal/core/** may import stdlib, third-party modules, github.com/CherryHQ/stella/pkg/**,
// other internal/core/**, internal/authz, and internal/platform/config — nothing else
// inside the repo. Third-party modules are unconstrained; the rule is about the
// direction of intra-repo dependencies, not about vendoring.
//
// If a candidate needs anything beyond that whitelist it is not a kernel, it is
// a domain package with a runtime dependency, and it stays where it is
// (internal/agent/settingspolicy is the worked example: its Available() takes a
// runtime.RunnerParams).

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

// coreAllowedRepoImports is the closed whitelist of in-repo import paths (and
// path prefixes) internal/core may depend on. Adding an entry is a deliberate,
// reviewed act — it widens what "kernel" means for the whole repo.
var coreAllowedRepoImports = []string{
	"pkg/",           // plugin-facing contract surface; the layer below core
	"internal/core/", // sibling kernels
	"internal/authz", // stable leaf: capability model, fan-in 25
	"internal/platform/config",
}

// forbiddenRepoImports returns the in-repo imports of a file that fall outside
// the core whitelist. Imports outside the module (stdlib, third-party) are not
// constrained by this rule.
func forbiddenRepoImports(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, "`\"")
		if !strings.HasPrefix(path, stellaModulePrefix) {
			continue
		}
		rel := strings.TrimPrefix(path, stellaModulePrefix)
		allowed := false
		for _, prefix := range coreAllowedRepoImports {
			if rel == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(rel, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			out = append(out, path)
		}
	}
	return out
}

func TestCoreBoundaryImportsStayInsideKernelWhitelist(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve core dir: %v", err)
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
		for _, bad := range forbiddenRepoImports(f) {
			t.Errorf("internal/core/%s imports %s, outside the kernel whitelist %v; either the dependency is wrong or the package does not belong in core", filepath.ToSlash(rel), bad, coreAllowedRepoImports)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk internal/core: %v", err)
	}
}

// TestCoreBoundaryTripwireCatchesCounterexample proves the guard is not vacuous
// while core is still small: a synthetic kernel file reaching into a domain
// package must be flagged, and a legitimate one must not.
func TestCoreBoundaryTripwireCatchesCounterexample(t *testing.T) {
	const offending = `package toolmeta

import (
	"context"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/tools"
)

var _ = context.Background
var _ = memory.Provider(nil)
var _ = tools.Tool{}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "internal/core/toolmeta/offender.go", offending, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	got := forbiddenRepoImports(f)
	if len(got) != 1 || got[0] != "github.com/CherryHQ/stella/internal/memory" {
		t.Fatalf("counterexample not detected, got %v — the tripwire is vacuous", got)
	}

	const allowed = `package access

import (
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/core/providercred"
	"github.com/CherryHQ/stella/pkg/channel"
)

var (
	_ = authz.UserID("")
	_ = config.Store(nil)
	_ = providercred.Service{}
	_ = channel.Message{}
)
`
	fa, err := parser.ParseFile(fset, "internal/core/access/service.go", allowed, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse allowed source: %v", err)
	}
	if bad := forbiddenRepoImports(fa); len(bad) != 0 {
		t.Fatalf("whitelisted imports rejected: %v", bad)
	}
}
