package local

import (
	"testing"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
)

func TestCheckPathUsesWorkspaceRootBoundary(t *testing.T) {
	root := t.TempDir()
	policy := Policy{
		Relaxed: false,
		Filesystem: sandboxpkg.FilesystemPolicy{
			WorkspaceRoot: root,
			WorkingDir:    root + "/nested",
			AllowEscapes:  false,
		},
	}
	host := &localHost{session: newLocalSession(policy)}

	if err := host.checkPath(root + "/file.txt"); err != nil {
		t.Fatalf("checkPath(root child): %v", err)
	}
	if err := host.checkPath(root + "/nested/file.txt"); err != nil {
		t.Fatalf("checkPath(workdir child): %v", err)
	}
	if err := host.checkPath(root + "/../outside.txt"); err == nil {
		t.Fatal("expected outside-root path to be rejected")
	}
}
