// Package-direction guards for the repo's layered trees. The rule text lives
// in each guarded package or tree (pkg is the extension contract;
// plugins/providers and plugins/sandbox are replaceable adapters; internal/core
// is the leaf kernel; internal/platform is infrastructure) and in
// web/content/docs/development/rules/go-patterns.md; this file only enforces it.
package internal_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const modulePrefix = "github.com/CherryHQ/stella/"

// boundary is one guarded tree. allowed lists the in-repo import paths (exact,
// or prefixes ending in "/") files under root may use; testOnly widens that for
// _test.go files. Standard library and third-party imports are unconstrained:
// the rule is about the direction of intra-repo dependencies.
type boundary struct {
	root     string
	allowed  []string
	testOnly []string
}

var boundaries = []boundary{
	{root: "pkg", allowed: []string{"pkg/"}},
	{root: "plugins/providers", allowed: []string{"pkg/", "plugins/providers/"}},
	{root: "plugins/sandbox", allowed: []string{"pkg/", "plugins/sandbox/"}, testOnly: []string{"internal/agent/prompt"}},
	{root: "internal/core", allowed: []string{"pkg/", "internal/core/", "internal/authz", "internal/platform/config"}},
	{root: "internal/platform", allowed: []string{"pkg/", "internal/platform/"}, testOnly: []string{"internal/db/dbtest"}},
}

// forbidden returns the in-repo imports of f that fall outside b's whitelist.
func (b boundary) forbidden(f *ast.File, isTest bool) []string {
	allowed := b.allowed
	if isTest {
		allowed = append(append([]string{}, allowed...), b.testOnly...)
	}
	var out []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, "`\"")
		rel, ok := strings.CutPrefix(path, modulePrefix)
		if !ok {
			continue
		}
		permitted := false
		for _, prefix := range allowed {
			if rel == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(rel, prefix) {
				permitted = true
				break
			}
		}
		if !permitted {
			out = append(out, path)
		}
	}
	return out
}

func TestPackageBoundaries(t *testing.T) {
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range boundaries {
		t.Run(b.root, func(t *testing.T) {
			root := filepath.Join(repo, filepath.FromSlash(b.root))
			seen := 0
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					if d.Name() == "testdata" || d.Name() == "node_modules" {
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.HasSuffix(path, ".go") {
					return nil
				}
				seen++
				f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(repo, path)
				for _, bad := range b.forbidden(f, strings.HasSuffix(path, "_test.go")) {
					t.Errorf("%s imports %s, outside the %s whitelist %v; either the dependency is wrong or the package does not belong there", filepath.ToSlash(rel), bad, b.root, b.allowed)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk %s: %v", b.root, err)
			}
			if seen == 0 {
				t.Fatalf("no Go files under %s; the guard would pass vacuously", b.root)
			}
		})
	}
}

// TestPackageBoundariesTripwire proves each guard bites: a synthetic file
// reaching into a domain package is flagged, a whitelisted one is not, and a
// test-only carve-out stays test-only.
func TestPackageBoundariesTripwire(t *testing.T) {
	parse := func(src string) *ast.File {
		t.Helper()
		f, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	const offender = modulePrefix + "internal/agent"
	for _, b := range boundaries {
		t.Run(b.root, func(t *testing.T) {
			bad := parse("package x\nimport (\n\t\"context\"\n\t\"" + offender + "\"\n\t\"" + modulePrefix + b.allowed[0] + "ai\"\n)\n")
			if got := b.forbidden(bad, false); len(got) != 1 || got[0] != offender {
				t.Fatalf("counterexample not detected, got %v; the guard is vacuous", got)
			}
			for _, extra := range b.testOnly {
				f := parse("package x\nimport _ \"" + modulePrefix + extra + "\"\n")
				if got := b.forbidden(f, true); len(got) != 0 {
					t.Fatalf("test-only import %s rejected in a _test.go file: %v", extra, got)
				}
				if got := b.forbidden(f, false); len(got) != 1 {
					t.Fatalf("test-only import %s must stay test-only, got %v in a non-test file", extra, got)
				}
			}
		})
	}
}
