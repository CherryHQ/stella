// Package platform groups the infrastructure packages that do not know agents
// exist: process/CLI plumbing, configuration, the STELLA_HOME layout, blob
// storage, observability, the bundled xberg CLI, build version, diagnostics.
//
// Admission rule, enforced by boundary_test.go in this directory:
// internal/platform/** may import the standard library, third-party modules,
// github.com/CherryHQ/stella/pkg/**, and other internal/platform/** — nothing
// else inside the repo. Third-party modules are unconstrained; the rule is
// about the direction of intra-repo dependencies, not about vendoring.
//
// The one carve-out is in _test.go files, which may also import
// internal/db/dbtest, the embedded-PostgreSQL test harness. A test harness
// creates no production edge and is unusable outside tests, so it cannot
// invert the layering; anything beyond it is a real dependency and disqualifies
// the package.
//
// A package that needs more than that belongs to a domain and stays where it
// lives. internal/db is the worked example: it implements internal/auth's
// stores, so it depends on the auth domain and cannot sit under platform.
//
// This directory holds no code of its own; each package is a subpackage keeping
// its original package name, so moving a package here changes import lines and
// nothing else.
package platform
