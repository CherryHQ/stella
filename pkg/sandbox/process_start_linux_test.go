//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStartProcessRegisteredPersistsIdentityBeforeTargetExec(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	cmd := exec.Command("sh", "-c", "printf started > \"$1\"", "sh", marker)
	extra, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = extra.Close() }()
	cmd.ExtraFiles = []*os.File{extra} // proves the gate does not assume fd 3
	registered := false
	if err := StartProcessRegistered(t.Context(), cmd, func(_ context.Context, identity ProcessIdentity) error {
		registered = identity.PID > 0 && identity.StartTime > 0
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			return fmt.Errorf("target executed before durable registration: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Fatal("process identity was not registered")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "started" {
		t.Fatalf("target result = %q, %v", got, err)
	}
}
