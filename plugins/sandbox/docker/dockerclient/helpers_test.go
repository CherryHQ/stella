package dockerclient

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shimCase defines the behavior for a single subcommand match in the shim.
type shimCase struct {
	// match is a substring that must appear in the space-joined argv to trigger
	// this case. Checked in order; first match wins.
	match    string
	exitCode int
	stdout   string
	stderr   string
}

// writeShim writes a shell script to tmpdir/docker that handles argv dispatch
// based on the provided cases. It also appends all argv to tmpdir/docker.log.
// Each case.match is a simple word that must appear in $1 (the first arg).
// For multi-word matches use the full args string via grep.
// Returns the absolute path to the shim binary.
func writeShim(t *testing.T, tmpdir string, cases []shimCase) string {
	t.Helper()

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")

	// Log full argv to docker.log for assertion.
	logPath := filepath.Join(tmpdir, "docker.log")
	fmt.Fprintf(&sb, "echo \"$@\" >> %q\n", logPath)

	// Use grep against the full argv string for matching.
	// We echo argv to a temp var and grep for the match string.
	for i, sc := range cases {
		// Write stdout/stderr payloads to separate files so the shim can cat them
		// without any shell quoting or escape-sequence issues.
		if sc.stdout != "" {
			outFile := filepath.Join(tmpdir, fmt.Sprintf("case%d.stdout", i))
			_ = os.WriteFile(outFile, []byte(sc.stdout), 0o644)
		}
		if sc.stderr != "" {
			errFile := filepath.Join(tmpdir, fmt.Sprintf("case%d.stderr", i))
			_ = os.WriteFile(errFile, []byte(sc.stderr), 0o644)
		}

		// Use grep against the full argv string for matching.
		fmt.Fprintf(&sb, "if echo \" $@ \" | grep -qF %q; then\n", " "+sc.match+" ")
		if sc.stdout != "" {
			outFile := filepath.Join(tmpdir, fmt.Sprintf("case%d.stdout", i))
			fmt.Fprintf(&sb, "  cat %q\n", outFile)
		}
		if sc.stderr != "" {
			errFile := filepath.Join(tmpdir, fmt.Sprintf("case%d.stderr", i))
			fmt.Fprintf(&sb, "  cat %q >&2\n", errFile)
		}
		fmt.Fprintf(&sb, "  exit %d\n", sc.exitCode)
		fmt.Fprintf(&sb, "fi\n")
	}

	// Default: exit 0
	sb.WriteString("exit 0\n")

	shimPath := filepath.Join(tmpdir, "docker")
	if err := os.WriteFile(shimPath, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("writeShim: %v", err)
	}
	return shimPath
}

// readLog returns the content of docker.log in tmpdir.
func readLog(t *testing.T, tmpdir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tmpdir, "docker.log"))
	if err != nil {
		return ""
	}
	return string(data)
}
