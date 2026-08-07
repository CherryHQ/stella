package vision

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

const (
	// xbergTimeout bounds local extraction for canonical baseline fallback.
	xbergTimeout = 60 * time.Second
	// xbergMaxStdoutBytes is intentionally far above the durable 12k-rune
	// baseline ceiling while bounding untrusted child-process output in memory.
	xbergMaxStdoutBytes = 256 * 1024
)

// xbergDrainWait bounds cleanup after the supervisor's process group has been
// cancelled. Tests shorten it to exercise the escaped-descendant boundary.
var xbergDrainWait = 5 * time.Second

// extractBytesWithXberg stages already-validated, service-owned image bytes for
// the one daemon-side Xberg invocation. The staging directory and its fixed
// filename prevent callers from selecting either the input path or Xberg cwd.
func extractBytesWithXberg(ctx context.Context, data []byte, mime string) (string, error) {
	if err := xbergFallbackSupported(); err != nil {
		return "", fmt.Errorf("xberg fallback: %w", err)
	}
	dir, err := os.MkdirTemp("", "stella-vision-")
	if err != nil {
		return "", fmt.Errorf("stage image for xberg: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "image"+extensionForMime(mime))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("stage image for xberg: %w", err)
	}
	if err := writeAndClose(file, data); err != nil {
		return "", fmt.Errorf("stage image for xberg: %w", err)
	}
	return runXberg(ctx, dir, path)
}

func writeAndClose(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			_ = file.Close()
			return err
		}
		if n == 0 {
			_ = file.Close()
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return file.Close()
}

// runXberg is deliberately private: its path and cwd originate exclusively
// from extractBytesWithXberg's daemon-owned staging directory.
func runXberg(ctx context.Context, stagingDir, stagedPath string) (string, error) {
	// Reconciliation installs the Xberg shim under STELLA_HOME; the daemon's
	// own PATH need not contain sandbox-only tool directories.
	stellaHome := config.StellaHome()
	bin := filepath.Join(pkgsandbox.MiseShimsDir(stellaHome), "xberg")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	managedShim := true
	if _, err := os.Stat(bin); err != nil {
		managedShim = false
		bin, err = exec.LookPath("xberg")
		if err != nil {
			return "", fmt.Errorf("xberg not available: %w", err)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, xbergTimeout)
	defer cancel()
	// The supervisor remains the process-group leader until we decide to reap
	// it. Its private completion pipe tells us that Xberg itself exited even
	// when a descendant keeps stdout open; that lets us kill the group before
	// Cmd.Wait can wait for a descendant-held pipe. This is deliberately a
	// trusted-Xberg process-group boundary, not arbitrary POSIX tree control:
	// a descendant that calls setsid cannot be killed. If it retains a daemon
	// pipe, the bounded drain below fails closed; one that fully detaches is
	// outside this daemon fallback's supported process contract.
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("create xberg supervisor pipe: %w", err)
	}
	defer func() { _ = controlRead.Close() }()
	holdRead, holdWrite, err := os.Pipe()
	if err != nil {
		_ = controlRead.Close()
		_ = controlWrite.Close()
		return "", fmt.Errorf("create xberg supervisor hold pipe: %w", err)
	}
	defer func() { _ = holdWrite.Close() }()
	cmd := manifestplugins.ManagedCommandContext(cctx, "/bin/sh", "-c", `"$@" 3>&- 4>&- & child=$!; wait "$child"; status=$?; printf '%s\n' "$status" >&3; read -r _ <&4`, "xberg-supervisor", bin, "extract", stagedPath)
	cmd.Dir = stagingDir
	cmd.ExtraFiles = []*os.File{controlWrite, holdRead}
	if managedShim {
		miseEnv := manifestplugins.RuntimeMiseEnv(stellaHome, "", "")
		// RuntimeMiseEnv uses the sandbox's /tmp by default; this command runs in
		// the daemon process, so use the host platform's temporary directory.
		miseEnv["MISE_STATE_DIR"] = filepath.Join(os.TempDir(), "stella-mise-state")
		cmd.Env = withEnvOverrides(os.Environ(), miseEnv)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = controlWrite.Close()
		_ = holdRead.Close()
		return "", fmt.Errorf("start xberg stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = controlWrite.Close()
		_ = holdRead.Close()
		return "", err
	}
	if err := controlWrite.Close(); err != nil {
		_ = holdRead.Close()
		_ = holdWrite.Close()
		_ = terminateXbergProcessTree(cmd)
		_ = stdout.Close()
		_ = cmd.Wait()
		return "", fmt.Errorf("close xberg supervisor pipe: %w", err)
	}
	if err := holdRead.Close(); err != nil {
		_ = holdWrite.Close()
		_ = terminateXbergProcessTree(cmd)
		_ = stdout.Close()
		_ = cmd.Wait()
		return "", fmt.Errorf("close xberg supervisor hold pipe: %w", err)
	}

	type readResult struct {
		data []byte
		err  error
	}
	stdoutDone := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(stdout, xbergMaxStdoutBytes+1))
		stdoutDone <- readResult{data: data, err: err}
	}()
	completionDone := make(chan readResult, 1)
	go func() {
		data, err := bufio.NewReader(controlRead).ReadBytes('\n')
		completionDone <- readResult{data: data, err: err}
	}()

	var (
		out            []byte
		stdoutRead     bool
		completion     readResult
		completionRead bool
		primaryErr     error
	)
	for !completionRead && primaryErr == nil {
		select {
		case result := <-stdoutDone:
			out, stdoutRead = result.data, true
			if result.err != nil {
				primaryErr = fmt.Errorf("read xberg output: %w", result.err)
			} else if len(out) > xbergMaxStdoutBytes {
				primaryErr = fmt.Errorf("xberg output exceeds %d bytes", xbergMaxStdoutBytes)
			}
		case completion = <-completionDone:
			completionRead = true
			primaryErr = xbergCompletionError(completion.data, completion.err)
		case <-cctx.Done():
			primaryErr = cctx.Err()
		}
	}

	// Keep the leader unreaped while cancelling its group. This pins the group
	// identity, avoiding both Cmd.Wait's descendant-pipe delay and a recycled
	// numeric PGID.
	cleanupErr := terminateXbergProcessTree(cmd)
	_ = holdWrite.Close()
	drainTimer := time.NewTimer(xbergDrainWait)
	defer drainTimer.Stop()
	for !stdoutRead || !completionRead {
		select {
		case result := <-stdoutDone:
			out, stdoutRead = result.data, true
			if primaryErr == nil && result.err != nil {
				primaryErr = fmt.Errorf("read xberg output: %w", result.err)
			}
		case completion = <-completionDone:
			completionRead = true
		case <-drainTimer.C:
			// Closing the daemon-owned read ends unblocks both bounded reader
			// goroutines even when an escaped descendant retained their writes.
			_ = stdout.Close()
			_ = controlRead.Close()
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("xberg process output did not drain within %s", xbergDrainWait))
			if !stdoutRead {
				result := <-stdoutDone
				out, stdoutRead = result.data, true
			}
			if !completionRead {
				completion = <-completionDone
				completionRead = true
			}
		}
	}
	if primaryErr == nil {
		primaryErr = xbergCompletionError(completion.data, completion.err)
	}
	_ = cmd.Wait() // cleanup deliberately SIGKILLs the still-held supervisor.
	cleanupErr = errors.Join(cleanupErr, confirmXbergProcessGroupGone(cmd.Process.Pid))
	if cctx.Err() != nil {
		primaryErr = cctx.Err()
	}
	if primaryErr != nil {
		if cleanupErr == nil {
			return "", primaryErr
		}
		return "", errors.Join(primaryErr, cleanupErr)
	}
	if cleanupErr != nil {
		return "", cleanupErr
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("xberg returned no text")
	}
	return text, nil
}

func xbergCompletionError(data []byte, readErr error) error {
	if readErr != nil {
		return fmt.Errorf("read xberg completion: %w", readErr)
	}
	status, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("read xberg completion: %w", err)
	}
	if status != 0 {
		return fmt.Errorf("xberg exited with status %d", status)
	}
	return nil
}

// withEnvOverrides replaces environment values while retaining the process
// environment required by external tools (for example, PATH and certificates).
func withEnvOverrides(env []string, overrides map[string]string) []string {
	out := append([]string(nil), env...)
	for key, value := range overrides {
		prefix := key + "="
		found := false
		for i, entry := range out {
			if strings.HasPrefix(entry, prefix) {
				out[i] = prefix + value
				found = true
				break
			}
		}
		if !found {
			out = append(out, prefix+value)
		}
	}
	return out
}
