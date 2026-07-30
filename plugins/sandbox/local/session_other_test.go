//go:build !linux && !darwin

package local

import (
	"os"
	"path/filepath"
	"testing"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestOtherPlatformTmpMountsAreSessionOwnedIdentityViews(t *testing.T) {
	workspace := t.TempDir()
	mounts, err := createSessionTmpMounts()
	if err != nil {
		t.Fatalf("createSessionTmpMounts: %v", err)
	}
	if len(mounts) != 1 {
		t.Fatalf("tmp mounts = %#v, want one identity mount", mounts)
	}
	mount := mounts[0]
	if mount.sandboxPath == "" || mount.sandboxPath != mount.realPath || !mount.owned {
		t.Fatalf("tmp mount = %#v, want owned identity mount", mount)
	}
	if got := filesystemTempDir(mounts); got != mount.realPath {
		t.Errorf("filesystemTempDir = %q, want %q", got, mount.realPath)
	}

	session := &localSession{
		realRoot:    workspace,
		sandboxRoot: workspace,
		tmpMounts:   mounts,
		policy: sandboxpkg.Policy{Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    workspace,
		}},
	}
	wantPath := filepath.Join(mount.realPath, "tmp-file")
	if got, err := session.ResolveWritePath(wantPath); err != nil || got != wantPath {
		t.Errorf("ResolveWritePath(%q) = %q, %v; want %q, nil", wantPath, got, err, wantPath)
	}
	cleanupOwnedTmpMounts(mounts)
	if _, err := os.Stat(mount.realPath); !os.IsNotExist(err) {
		t.Errorf("owned temp survives cleanup: %v", err)
	}
}
