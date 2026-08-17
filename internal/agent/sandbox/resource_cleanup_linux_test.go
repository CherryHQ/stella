//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestCleanupDurableHostResourceKillsMarkedProcessTree(t *testing.T) {
	resourceID := pkgsandbox.NewSessionID()
	// The child deliberately drops the marker and leaves the root's process
	// group. Cleanup must follow the exact registered ancestry, not rediscover
	// it through environment or PGID scans.
	cmd := exec.Command("sh", "-c", "env -u "+pkgsandbox.EnvResourceID+" setsid sleep 30 & wait")
	cmd.Env = append(os.Environ(), pkgsandbox.EnvResourceID+"="+resourceID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var childPID int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		children, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", cmd.Process.Pid, cmd.Process.Pid))
		if err == nil {
			_, _ = fmt.Sscan(string(children), &childPID)
		}
		if childPID > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("detached child did not start")
	}
	identity, err := pkgsandbox.LinuxProcessIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = pkgsandbox.WithProcessIdentities(ctx, []pkgsandbox.ProcessIdentity{identity})
	if err := CleanupDurableResource(ctx, "local", resourceID); err != nil {
		t.Fatalf("cleanup marked process tree: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("marked process exited successfully, want recovery SIGKILL")
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait marked process: %v", err)
		}
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(childPID, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached markerless child %d survived cleanup", childPID)
		}
	}
}

func TestCleanupDurableHostResourceIgnoresUnrelatedProtectedProcess(t *testing.T) {
	unrelated := exec.Command("python3", "-c", "import ctypes,time; ctypes.CDLL(None).prctl(4,0); time.sleep(30)")
	unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })

	target := exec.Command("sleep", "30")
	target.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := target.Start(); err != nil {
		t.Fatal(err)
	}
	identity, err := pkgsandbox.LinuxProcessIdentity(target.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	ctx := pkgsandbox.WithProcessIdentities(t.Context(), []pkgsandbox.ProcessIdentity{identity})
	if err := CleanupDurableResource(ctx, "local", pkgsandbox.NewSessionID()); err != nil {
		t.Fatal(err)
	}
	_ = target.Wait()
	if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated protected process was affected: %v", err)
	}
}

func TestCleanupDurableHostResourceKillsProtectedTarget(t *testing.T) {
	cmd := exec.Command("python3", "-c", "import ctypes,time; ctypes.CDLL(None).prctl(4,0); time.sleep(30)")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	identity, err := pkgsandbox.LinuxProcessIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	ctx := pkgsandbox.WithProcessIdentities(t.Context(), []pkgsandbox.ProcessIdentity{identity})
	if err := CleanupDurableResource(ctx, "none", pkgsandbox.NewSessionID()); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}

func TestCleanupDurableHostResourceRejectsPIDReuseIdentity(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	identity, err := pkgsandbox.LinuxProcessIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	identity.StartTime++
	ctx := pkgsandbox.WithProcessIdentities(t.Context(), []pkgsandbox.ProcessIdentity{identity})
	if err := CleanupDurableResource(ctx, "local", pkgsandbox.NewSessionID()); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("reused PID was killed: %v", err)
	}
}

func TestCleanupDurableHostResourceProvesMissingResource(t *testing.T) {
	if err := CleanupDurableResource(t.Context(), "none", pkgsandbox.NewSessionID()); err != nil {
		t.Fatalf("cleanup absent host resource: %v", err)
	}
}
