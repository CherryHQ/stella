package access

import "testing"

func TestWorkspaceCanonicalRootsAreProviderCoordinates(t *testing.T) {
	if got := workspaceCanonicalRoot(WorkspaceScopeAgent); got != "/workspace" {
		t.Fatalf("agent root = %q", got)
	}
	if got := workspaceCanonicalRoot(WorkspaceScopeUser); got != "/user" {
		t.Fatalf("user root = %q", got)
	}
}
