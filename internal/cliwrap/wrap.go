// Package cliwrap provisions thin POSIX shell wrapper scripts for CLI tools
// (gh, lark-cli) under a user-owned bin directory. The wrappers rely on
// environment variables injected by the runner (ANNA_GH_BIN, ANNA_LARK_BIN)
// to locate the real binaries, avoiding infinite exec loops.
package cliwrap

import (
	"os"
	"path/filepath"
)

const (
	// ghWrapper is the shell script placed at binDir/gh.
	// It delegates to the real binary path exported by the runner as ANNA_GH_BIN.
	ghWrapper = "#!/bin/sh\nexec \"${ANNA_GH_BIN:-gh}\" \"$@\"\n"

	// larkWrapper is the shell script placed at binDir/lark-cli.
	// It delegates to the real binary path exported by the runner as ANNA_LARK_BIN.
	larkWrapper = "#!/bin/sh\nexec \"${ANNA_LARK_BIN:-lark-cli}\" \"$@\"\n"
)

// EnsureWrappers creates wrapper scripts for gh and lark-cli under binDir.
// binDir is typically UserRoot/.anna/bin/.
// The wrappers are re-written on every call so stale content is never left behind.
func EnsureWrappers(binDir string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := writeExecutable(filepath.Join(binDir, "gh"), ghWrapper); err != nil {
		return err
	}
	return writeExecutable(filepath.Join(binDir, "lark-cli"), larkWrapper)
}

// writeExecutable atomically writes content to path with executable permissions.
func writeExecutable(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o755)
}
