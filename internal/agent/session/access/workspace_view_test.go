package access

import (
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestCanonicalWorkspacePathAcceptsOnlyLogicalCoordinates(t *testing.T) {
	for _, tt := range []struct {
		input string
		scope WorkspaceScope
		want  string
	}{
		{"file.txt", WorkspaceScopeAgent, "file.txt"},
		{"$HOME/file.txt", WorkspaceScopeAgent, "file.txt"},
		{"$STELLA_ASSETS_DIR/202608/file.txt", WorkspaceScopeUser, "assets/202608/file.txt"},
		{pkgsandbox.MountUserData + "/assets/file.txt", WorkspaceScopeUser, "assets/file.txt"},
	} {
		scope, got, err := canonicalWorkspacePath(WorkspaceScopeAgent, tt.input, false)
		if err != nil || scope != tt.scope || got != tt.want {
			t.Fatalf("canonicalWorkspacePath(%q) = %q, %q, %v", tt.input, scope, got, err)
		}
	}
	for _, input := range []string{"/tmp/file", "../file", `C:\\file`, "$TMPDIR/file"} {
		if _, _, err := canonicalWorkspacePath(WorkspaceScopeAgent, input, false); err == nil {
			t.Fatalf("canonicalWorkspacePath(%q) succeeded", input)
		}
	}
}
