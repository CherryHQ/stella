//go:build !windows

package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecXbergRunnerKillsDescendantOnTimeout(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "xberg")
	script := `#!/bin/sh
sleep 30 &
child_pid=$!
printf '%s' "$child_pid" > "$0.child"
wait "$child_pid"
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	document := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(document, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}

	config := DefaultXbergParserConfig(binary)
	config.Timeout = 300 * time.Millisecond
	parser, err := NewXbergParser(config)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = parser.Parse(t.Context(), document, MediaTypeText)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Parse() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("process-tree cancellation took %s", elapsed)
	}

	pidBytes, err := os.ReadFile(binary + ".child")
	if err != nil {
		t.Fatalf("read descendant PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse descendant PID: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(2 * time.Second)
	for xbergProcessRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if xbergProcessRunning(pid) {
		t.Fatalf("descendant process %d survived parser cancellation", pid)
	}
}

func TestExecXbergRunnerAppliesOSResourceLimits(t *testing.T) {
	const (
		memoryBytes = int64(96 << 20)
		cpuTime     = 7 * time.Second
	)
	binary := filepath.Join(t.TempDir(), "xberg")
	t.Setenv("STELLA_VAULT_KEY", "must-not-reach-xberg")
	script := fmt.Sprintf(`#!/bin/sh
if [ "${STELLA_VAULT_KEY+x}" = "x" ]; then
  printf 'daemon secret reached Xberg' >&2
  exit 40
fi
if [ "$(ulimit -v)" != "%d" ]; then
  printf 'unexpected virtual memory limit: %%s' "$(ulimit -v)" >&2
  exit 41
fi
if [ "$(ulimit -t)" != "%d" ]; then
  printf 'unexpected CPU limit: %%s' "$(ulimit -t)" >&2
  exit 42
fi
printf '%%s' '%s'
`, memoryBytes/1024, int64(cpuTime/time.Second), validXbergPayload("bounded parser"))
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	document := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(document, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}

	config := DefaultXbergParserConfig(binary)
	config.MaxProcessMemoryBytes = memoryBytes
	config.MaxProcessCPUTime = cpuTime
	parser, err := NewXbergParser(config)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := parser.Parse(t.Context(), document, MediaTypeText)
	if err != nil {
		t.Fatalf("Parse() under constrained process: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Content != "bounded parser" {
		t.Fatalf("Parse() chunks = %#v", chunks)
	}
}

func xbergProcessRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	if err != nil {
		return true
	}
	if runtime.GOOS == "linux" {
		status, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if readErr == nil {
			// A killed child may briefly remain as a zombie until init reaps it;
			// it no longer consumes CPU or executes parser code.
			closeParen := strings.LastIndexByte(string(status), ')')
			if closeParen >= 0 && len(status) > closeParen+2 && status[closeParen+2] == 'Z' {
				return false
			}
		}
	}
	return true
}
