package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shimCase defines the behavior for a single subcommand match in the shim.
type shimCase struct {
	match    string
	exitCode int
	stdout   string
	stderr   string
}

// writeShim writes a shell script to tmpdir/docker that dispatches based on argv.
// Returns the absolute path to the shim binary.
func writeShim(t *testing.T, tmpdir string, cases []shimCase) string {
	t.Helper()

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")

	logPath := filepath.Join(tmpdir, "docker.log")
	fmt.Fprintf(&sb, "echo \"$@\" >> %q\n", logPath)

	for i, sc := range cases {
		if sc.stdout != "" {
			outFile := filepath.Join(tmpdir, fmt.Sprintf("case%d.stdout", i))
			_ = os.WriteFile(outFile, []byte(sc.stdout), 0o644)
		}
		if sc.stderr != "" {
			errFile := filepath.Join(tmpdir, fmt.Sprintf("case%d.stderr", i))
			_ = os.WriteFile(errFile, []byte(sc.stderr), 0o644)
		}

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
