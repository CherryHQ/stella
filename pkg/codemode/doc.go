// Package codemode hosts the isolated QuickJS feasibility runtime for Code Mode.
//
// The runtime deliberately installs no module loader or ambient filesystem,
// network, process, environment, or WASI capability. Its only host boundary is
// tools.invoke, which returns a Promise and runs the Go host outside the VM.
//
// This spike pins modernc.org/quickjs v0.24.2 (Git revision
// 3282d3c5cb5c16ddd5d78e33441ee399a3cf3c62), embedding upstream QuickJS
// 2026-06-04. The binding is BSD-3-Clause and its embedded QuickJS is MIT.
// Any upgrade must re-run this package's no-CGO, race, interrupt, teardown,
// and release-target build acceptance checks.
package codemode
