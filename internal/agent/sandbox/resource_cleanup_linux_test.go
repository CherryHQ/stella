//go:build linux

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestCleanupDurableHostResourceKillsMarkedProcessTree(t *testing.T) {
	resourceID := pkgsandbox.NewSessionID()
	cmd := exec.Command("sh", "-c", "sleep 30 & wait")
	cmd.Env = append(os.Environ(), pkgsandbox.EnvResourceID+"="+resourceID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
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
}

func TestCleanupDurableHostResourceProvesMissingResource(t *testing.T) {
	if err := CleanupDurableResource(t.Context(), "none", pkgsandbox.NewSessionID()); err != nil {
		t.Fatalf("cleanup absent host resource: %v", err)
	}
}
